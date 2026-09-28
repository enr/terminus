package model

import (
	"encoding/json"
	"testing"
)

func TestSeverityJSON(t *testing.T) {
	b, err := json.Marshal(Finding{ID: "x", Severity: SeverityWarn})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["severity"] != "warn" {
		t.Fatalf("severity = %v, want warn", got["severity"])
	}
	var f Finding
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Severity != SeverityWarn {
		t.Fatalf("round trip severity = %v", f.Severity)
	}
	if err := json.Unmarshal([]byte(`{"severity":"bad"}`), &f); err == nil {
		t.Fatal("expected error for invalid severity")
	}
}

func TestAddFindingsSortsAndSummarizes(t *testing.T) {
	r := NewReport()
	r.AddFindings(
		Finding{ID: "b", Severity: SeverityOK},
		Finding{ID: "z", Severity: SeverityWarn},
		Finding{ID: "a", Severity: SeverityFail},
		Finding{ID: "a", Severity: SeverityWarn, Subject: "2"},
		Finding{ID: "a", Severity: SeverityWarn, Subject: "1"},
		Finding{ID: "i", Severity: SeverityInfo},
	)
	want := []string{"a", "a/1", "a/2", "z", "i", "b"}
	for i, f := range r.Findings {
		key := f.ID
		if f.Subject != "" {
			key += "/" + f.Subject
		}
		if key != want[i] {
			t.Fatalf("finding %d = %s, want %s", i, key, want[i])
		}
	}
	if (r.Summary != Summary{OK: 1, Info: 1, Warn: 3, Fail: 1}) {
		t.Fatalf("summary = %+v", r.Summary)
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		sev  []Severity
		want int
	}{
		{nil, ExitOK},
		{[]Severity{SeverityOK, SeverityInfo}, ExitOK},
		{[]Severity{SeverityOK, SeverityWarn}, ExitWarn},
		{[]Severity{SeverityWarn, SeverityFail}, ExitFail},
	}
	for _, c := range cases {
		r := NewReport()
		for _, s := range c.sev {
			r.AddFindings(Finding{ID: "x", Severity: s})
		}
		if got := r.ExitCode(); got != c.want {
			t.Errorf("%v: exit code = %d, want %d", c.sev, got, c.want)
		}
	}
}

func TestExitCodeModuleProblem(t *testing.T) {
	cases := []struct {
		name   string
		status ModuleStatus
		want   int
	}{
		{"ok", StatusOK, ExitOK},
		{"skipped", StatusSkipped, ExitOK},
		{"partial", StatusPartial, ExitWarn},
		{"error", StatusError, ExitWarn},
	}
	for _, c := range cases {
		r := NewReport()
		r.Modules["m"] = ModuleResult{Name: "m", Status: c.status}
		r.AddFindings(Finding{ID: "x", Severity: SeverityOK})
		if got := r.ExitCode(); got != c.want {
			t.Errorf("%s: exit code = %d, want %d", c.name, got, c.want)
		}
	}
	// A fail finding still wins over a merely partial module.
	r := NewReport()
	r.Modules["m"] = ModuleResult{Name: "m", Status: StatusPartial}
	r.AddFindings(Finding{ID: "x", Severity: SeverityFail})
	if got := r.ExitCode(); got != ExitFail {
		t.Errorf("exit code = %d, want %d", got, ExitFail)
	}
}

func TestGrade(t *testing.T) {
	if Grade(1, 2, 3) != SeverityOK || Grade(2, 2, 3) != SeverityWarn || Grade(5, 2, 3) != SeverityFail {
		t.Error("Grade")
	}
	if GradeBelow(0.5, 0.1, 0.05) != SeverityOK || GradeBelow(0.07, 0.1, 0.05) != SeverityWarn || GradeBelow(0.01, 0.1, 0.05) != SeverityFail {
		t.Error("GradeBelow")
	}
}
