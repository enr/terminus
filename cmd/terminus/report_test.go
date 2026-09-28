package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
)

// failingModule writes an external module that reports a failure.
func failingModule(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\necho '{\"facts\":{\"v\":1},\"findings\":[{\"id\":\"rsync.age\",\"severity\":\"fail\",\"message\":\"no sync <3 days> | x\"}]}'\n"
	if err := os.WriteFile(filepath.Join(dir, "rsync.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestReportFormatsAndOutputFile(t *testing.T) {
	dir := t.TempDir()
	mods := filepath.Join(dir, "mods")
	os.Mkdir(mods, 0o755)
	failingModule(t, mods)

	for format, marker := range map[string]string{
		"markdown":   "## Findings",
		"html":       "<!doctype html>",
		"jsonl":      `"id":"rsync.age"`,
		"prometheus": `terminus_finding_severity{id="rsync.age",module="rsync",subject=""} 3`,
		"json":       `"schema_version": 1`,
	} {
		out := filepath.Join(dir, "report."+format)
		code, stdout, errOut := runCLI(t, "--modules-dir", mods, "--only", "rsync,memory", "report", "-o", format, "--output-file", out)
		if code != model.ExitFail || stdout != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", format, code, stdout, errOut)
		}
		b, err := os.ReadFile(out)
		if err != nil || !strings.Contains(string(b), marker) {
			t.Errorf("%s: %v\n%s", format, err, b)
		}
		if info, _ := os.Stat(out); info.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v", format, info.Mode())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "report.markdown"))
	if !strings.Contains(string(b), "## Facts") || !strings.Contains(string(b), "no sync &lt;3 days&gt; \\| x") {
		t.Errorf("markdown report:\n%s", b)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "report.html"))
	if strings.Contains(string(b), "<3 days>") {
		t.Error("html not escaped")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}

	// check keeps text by default; facts accepts --output-file too.
	if code, out, _ := runCLI(t, "--modules-dir", mods, "--only", "rsync", "check", "--color", "never"); code != model.ExitFail || !strings.Contains(out, "rsync.age") {
		t.Errorf("check: %d %s", code, out)
	}
	factsOut := filepath.Join(dir, "facts.json")
	if code, _, errOut := runCLI(t, "--only", "memory", "facts", "-o", "json", "--output-file", factsOut); code != 0 {
		t.Errorf("facts: %d %s", code, errOut)
	}
	if b, _ := os.ReadFile(factsOut); !json.Valid(b) {
		t.Errorf("facts file: %s", b)
	}
	if code, _, errOut := runCLI(t, "report", "--output-file", filepath.Join(dir, "missing", "x.md")); code != model.ExitError || errOut == "" {
		t.Errorf("unwritable output file: %d %s", code, errOut)
	}
}

func TestDiffCommand(t *testing.T) {
	dir := t.TempDir()
	mods := filepath.Join(dir, "mods")
	os.Mkdir(mods, 0o755)
	before, after := filepath.Join(dir, "before.json"), filepath.Join(dir, "after.json")
	if code, _, errOut := runCLI(t, "--modules-dir", mods, "--only", "memory,system", "report", "-o", "json", "--output-file", before); code != 0 {
		t.Fatalf("before: %d %s", code, errOut)
	}
	failingModule(t, mods)
	runCLI(t, "--modules-dir", mods, "--only", "memory,system,rsync", "report", "-o", "json", "--output-file", after)

	code, out, errOut := runCLI(t, "diff", before, after, "--facts")
	if code != model.ExitFail || !strings.Contains(out, "+ new fail") || !strings.Contains(out, "rsync.v = 1") {
		t.Fatalf("diff: %d %s %s", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "diff", after, before, "-o", "json"); code != 0 || !strings.Contains(out, `"kind": "resolved"`) {
		t.Errorf("reverse diff: %d %s", code, out)
	}
	if code, _, errOut := runCLI(t, "diff", before, filepath.Join(dir, "nope.json")); code != model.ExitError || errOut == "" {
		t.Errorf("missing file: %d", code)
	}
}
