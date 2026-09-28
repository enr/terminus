package diff

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
)

func report(ts time.Time, facts map[string]any, fs ...model.Finding) *model.Report {
	r := model.NewReport()
	r.Meta = model.Meta{Tool: "terminus", Hostname: "srv-01", Timestamp: ts}
	for name, f := range facts {
		r.Modules[name] = model.ModuleResult{Name: name, Status: model.StatusOK, Facts: f}
	}
	r.AddFindings(fs...)
	return r
}

func f(id, subject string, sev model.Severity, msg string) model.Finding {
	return model.Finding{ID: id, Module: strings.Split(id, ".")[0], Subject: subject, Severity: sev, Message: msg}
}

func pair() (*model.Report, *model.Report) {
	t0 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	before := report(t0, map[string]any{
		"system": map[string]any{"kernel": map[string]any{"release": "6.8.0"}, "uptime_seconds": 100, "time": map[string]any{"now": "a"}},
		"network": map[string]any{"interfaces": []any{
			map[string]any{"name": "lo", "mtu": 65536},
			map[string]any{"name": "eth0", "mtu": 1500, "stats": map[string]any{"rx_bytes": 1}},
		}},
		"podman": map[string]any{"containers": []any{map[string]any{"name": "old"}}},
	},
		f("disk.usage", "/", model.SeverityWarn, "88% used"),
		f("mem.available", "memory", model.SeverityOK, "90% available"),
		f("systemd.failed", "system:backup.service", model.SeverityFail, "unit is failed"),
		f("unit.restarts", "user:apps:db.service", model.SeverityOK, "1 restart"),
	)
	after := report(t0.Add(time.Hour), map[string]any{
		"system": map[string]any{"kernel": map[string]any{"release": "6.8.1"}, "uptime_seconds": 200, "time": map[string]any{"now": "b"}},
		"network": map[string]any{"interfaces": []any{
			map[string]any{"name": "wg0", "mtu": 1420},
			map[string]any{"name": "lo", "mtu": 65536},
			map[string]any{"name": "eth0", "mtu": 9000, "stats": map[string]any{"rx_bytes": 2}},
		}},
		"podman": map[string]any{"containers": []any{}},
	},
		f("disk.usage", "/", model.SeverityFail, "96% used"),
		f("mem.available", "memory", model.SeverityOK, "85% available"),
		f("unit.restarts", "user:apps:db.service", model.SeverityOK, "2 restarts"),
		f("unit.oom-kills", "user:apps:db.service", model.SeverityFail, "1 OOM kill"),
		f("net.public-listeners", "tcp", model.SeverityInfo, "3 ports"),
	)
	return before, after
}

func TestCompareFindings(t *testing.T) {
	before, after := pair()
	r := Compare(before, after, false, nil)
	var got []string
	for _, c := range r.Findings {
		got = append(got, c.Kind+" "+c.ID+" "+c.Subject)
	}
	want := []string{
		"changed disk.usage /",
		"new unit.oom-kills user:apps:db.service",
		"new net.public-listeners tcp",
		"resolved systemd.failed system:backup.service",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.ExitCode() != model.ExitFail || r.Facts != nil {
		t.Errorf("exit %d facts %v", r.ExitCode(), r.Facts)
	}
	// Reversed: the failed unit comes back, a regression; the disk improves, not a regression.
	back := Compare(after, before, false, nil)
	if back.ExitCode() != model.ExitFail || back.Findings[0].ID != "systemd.failed" || back.Findings[1].Worse() {
		t.Errorf("reverse exit %d: %+v", back.ExitCode(), back.Findings)
	}
	// Only improvements: exit 0.
	fixed := report(after.Meta.Timestamp, nil)
	if c := Compare(before, fixed, false, nil); c.ExitCode() != model.ExitOK || len(c.Findings) != 2 {
		t.Errorf("improvements: %d %+v", c.ExitCode(), c.Findings)
	}
}

func TestCompareFacts(t *testing.T) {
	before, after := pair()
	r := Compare(before, after, true, DefaultIgnore)
	var got []string
	for _, c := range r.Facts {
		got = append(got, c.Kind+" "+c.Path)
	}
	want := []string{
		"changed network.interfaces.eth0.mtu",
		"added network.interfaces.wg0.mtu",
		"added network.interfaces.wg0.name",
		"removed podman.containers.old.name",
		"changed system.kernel.release",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("facts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	all := Compare(before, after, true, nil)
	if len(all.Facts) <= len(r.Facts) {
		t.Error("ignore patterns had no effect")
	}
}

func TestMatch(t *testing.T) {
	cases := map[[2]string]bool{
		{"*.time.*", "system.time.now"}:        true,
		{"*.time.*", "system.timezone"}:        false,
		{"*uptime*", "system.uptime_seconds"}:  true,
		{"a*b*c", "axxbyyc"}:                   true,
		{"a*b*c", "axxbyy"}:                    false,
		{"exact", "exact"}:                     true,
		{"*_ratio", "memory.available_ratio"}:  true,
		{"*_ratio", "memory.available_ratio2"}: false,
	}
	for c, want := range cases {
		if Match(c[0], c[1]) != want {
			t.Errorf("Match(%q, %q) != %v", c[0], c[1], want)
		}
	}
}

func TestRenderAndLoad(t *testing.T) {
	before, after := pair()
	r := Compare(before, after, true, DefaultIgnore)
	for _, format := range []string{"text", "json", "markdown"} {
		var b bytes.Buffer
		if err := Render(&b, r, format); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "unit.oom-kills") {
			t.Errorf("%s output:\n%s", format, b.String())
		}
	}
	var b bytes.Buffer
	_ = Render(&b, r, "text")
	if !strings.Contains(b.String(), "! warn → fail") || !strings.Contains(b.String(), "~ system.kernel.release: 6.8.0 → 6.8.1") {
		t.Errorf("text:\n%s", b.String())
	}
	if err := Render(&b, r, "yaml"); err == nil {
		t.Error("unknown format accepted")
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	j, _ := json.Marshal(before)
	os.WriteFile(p, j, 0o644)
	if got, err := Load(p); err != nil || got.Meta.Hostname != "srv-01" || len(got.Findings) != 4 {
		t.Fatalf("load: %v %v", got, err)
	}
	os.WriteFile(p, []byte(`{"meta":{}}`), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("report without schema accepted")
	}
	os.WriteFile(p, []byte(`{"schema_version": 99}`), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("future schema: %v", err)
	}
}
