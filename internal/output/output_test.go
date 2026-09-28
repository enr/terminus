package output

import (
	"bytes"
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
	if _, err := ForFormat("yaml"); err == nil || !strings.Contains(err.Error(), "text, json") {
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
