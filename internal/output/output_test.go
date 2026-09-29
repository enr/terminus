package output

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
)

var update = flag.Bool("update", false, "update golden files")

func sampleReport() *model.Report {
	r := model.NewReport()
	r.Meta = model.Meta{
		Tool:       "terminus",
		Version:    "0.2.0-test",
		Hostname:   "srv-01",
		Timestamp:  time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
		DurationMs: 123,
	}
	r.Modules["system"] = model.ModuleResult{
		Name:       "system",
		Status:     model.StatusOK,
		DurationMs: 12,
		Facts: map[string]any{
			"hostname":       "srv-01",
			"memory":         map[string]any{"total_bytes": uint64(4 << 30), "available_ratio": 0.0732},
			"uptime_seconds": 187200.5,
			"flags":          []string{"fpu", "sse"},
			"long":           []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
			"disks":          []map[string]string{{"device": "sda"}},
		},
	}
	r.Modules["external"] = model.ModuleResult{Name: "external", Status: model.StatusSkipped, SkipReason: "directory /etc/terminus/facts.d does not exist"}
	r.Modules["podman"] = model.ModuleResult{Name: "podman", Status: model.StatusPartial, DurationMs: 1500, Facts: map[string]any{}, Errors: []string{"volume inspect: permission denied"}}
	r.AddFindings(
		model.Finding{ID: "mem.available-low", Module: "system", Severity: model.SeverityWarn, Subject: "memory",
			Message: "only 7.3% of memory available", Hint: "check memory usage",
			Evidence: map[string]any{"available_bytes": uint64(300 << 20), "available_ratio": 0.0732}},
		model.Finding{ID: "unit.failed", Module: "systemd", Severity: model.SeverityFail, Subject: "backup.service", Message: "unit is failed"},
		model.Finding{ID: "disk.usage", Module: "system", Severity: model.SeverityOK, Subject: "/", Message: "42% used"},
		model.Finding{ID: "ext.escape", Module: "ext", Severity: model.SeverityInfo, Subject: `a|b <x> "q" \ z`, Message: "line1\nline2 <b>&</b>"},
	)
	return r
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create golden files)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func render(t *testing.T, format string, o Options) []byte {
	t.Helper()
	r, err := ForFormat(format)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.Render(&buf, sampleReport(), o); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGolden(t *testing.T) {
	golden(t, "report.json", render(t, "json", Options{}))
	golden(t, "report.txt", render(t, "text", Options{Findings: true}))
	golden(t, "report-verbose-facts.txt", render(t, "text", Options{Findings: true, Verbose: true, Facts: true}))
	golden(t, "facts.txt", render(t, "text", Options{Facts: true}))
	golden(t, "report.md", render(t, "markdown", Options{Findings: true, Facts: true}))
	golden(t, "report.html", render(t, "html", Options{Findings: true, Facts: true}))
	golden(t, "report.jsonl", render(t, "jsonl", Options{Findings: true}))
	golden(t, "report.prom", render(t, "prometheus", Options{Findings: true}))
	golden(t, "report-problems.jsonl", render(t, "jsonl", Options{Findings: true, ProblemsOnly: true}))
	golden(t, "report-problems.txt", render(t, "text", Options{Findings: true, ProblemsOnly: true}))
}

func TestTextColor(t *testing.T) {
	plain := render(t, "text", Options{Findings: true})
	if bytes.Contains(plain, []byte("\x1b[")) {
		t.Error("ANSI escapes without color")
	}
	colored := render(t, "text", Options{Findings: true, Color: true})
	if !bytes.Contains(colored, []byte("\x1b[")) {
		t.Error("no ANSI escapes with color")
	}
}

func TestForFormat(t *testing.T) {
	if _, err := ForFormat("yaml"); err == nil || !strings.Contains(err.Error(), "text, json, jsonl, markdown, html, prometheus") {
		t.Fatalf("err = %v", err)
	}
}

func TestUseColor(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for mode, want := range map[ColorMode]bool{ColorAlways: true, ColorNever: false, ColorAuto: false} {
		got, err := UseColor(mode, f)
		if err != nil || got != want {
			t.Errorf("%s: %v %v", mode, got, err)
		}
	}
	if _, err := UseColor("sometimes", f); err == nil {
		t.Error("invalid mode accepted")
	}
}

func TestHumanize(t *testing.T) {
	bytesCases := map[uint64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 4080218931: "3.8 GiB"}
	for n, want := range bytesCases {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %s, want %s", n, got, want)
		}
	}
	durCases := map[time.Duration]string{
		850 * time.Millisecond:         "850ms",
		12300 * time.Millisecond:       "12.3s",
		5*time.Minute + 10*time.Second: "5m10s",
		3*time.Hour + 4*time.Minute:    "3h4m",
		52 * time.Hour:                 "2d4h",
	}
	for d, want := range durCases {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%s) = %s, want %s", d, got, want)
		}
	}
}

// tablesReport has lists of records: declared columns, nested lists, records without a name.
func tablesReport() *model.Report {
	r := model.NewReport()
	r.Meta = model.Meta{Hostname: "srv-01", Timestamp: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
	r.Modules["network"] = model.ModuleResult{Name: "network", Status: model.StatusOK, Facts: map[string]any{
		"interfaces": []map[string]any{
			{"name": "lo", "mtu": 65536, "addresses": []map[string]any{{"cidr": "127.0.0.1/8"}, {"cidr": "::1/128"}},
				"stats": map[string]any{"rx_bytes": 2048}},
			{"name": "eth0", "mtu": 1500, "addresses": []map[string]any{{"cidr": "10.0.0.2/24"}},
				"stats": map[string]any{"rx_bytes": uint64(3 << 30)}, "error": ""},
		},
		"routes": []map[string]any{{"gateway": "10.0.0.1", "metric": 0, "flags": []string{"up", "gw"}, "stats": map[string]any{"uses": 1}}},
	}}
	r.Modules["systemd"] = model.ModuleResult{Name: "systemd", Status: model.StatusOK, Facts: map[string]any{
		"managers": []map[string]any{{"name": "system", "units": []map[string]any{
			{"name": "sshd.service", "active": "active", "restarts": 0, "io_ms": 1500, "load": 0.04},
			{"name": "backup.service", "active": "failed", "restarts": 3, "io_ms": 12, "load": 2800.0},
		}}},
	}}
	return r
}

var tablesColumns = map[string][]string{
	"network.interfaces": {"name", "addresses=addresses.cidr", "mtu", "rx=stats.rx_bytes", "error"},
}

func TestTextTables(t *testing.T) {
	render := func(o Options) []byte {
		var buf bytes.Buffer
		if err := (Text{}).Render(&buf, tablesReport(), o); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	golden(t, "facts-tables.txt", render(Options{Facts: true, Tables: tablesColumns}))
	golden(t, "facts-tables-narrow.txt", render(Options{Facts: true, Tables: tablesColumns, Width: 30}))
	golden(t, "facts-tables-verbose.txt", render(Options{Facts: true, Tables: tablesColumns, Verbose: true}))
}

func TestRenderFactsValue(t *testing.T) {
	var buf bytes.Buffer
	v := []any{map[string]any{"cidr": "10.0.0.2/24", "prefix": json.Number("24")}}
	if err := RenderFactsValue(&buf, "network.interfaces.eth0.addresses", "network.interfaces.addresses", v, Options{}); err != nil {
		t.Fatal(err)
	}
	want := "CIDR         PREFIX\n10.0.0.2/24      24\n"
	if buf.String() != want {
		t.Errorf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestHumanValue(t *testing.T) {
	cases := []struct {
		key  string
		v    any
		want string
	}{
		{"mhz", 2799.998, "2799.998"},
		{"mhz", 2800.0, "2800"},
		{"avg", 0.0425, "0.043"},
		{"avg", json.Number("16.83"), "16.83"},
		{"io_time_ms", json.Number("3804"), "3.8s"},
		{"total_us", json.Number("12864225"), "12.9s"},
		{"size_bytes", uint64(2048), "2.0 KiB (2048)"},
	}
	for _, c := range cases {
		if got := humanValue(c.key, c.v); got != c.want {
			t.Errorf("humanValue(%s, %v) = %s, want %s", c.key, c.v, got, c.want)
		}
	}
}
