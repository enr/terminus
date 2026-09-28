package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
)

func TestConfigSelectsModulesAndThresholds(t *testing.T) {
	cfg := writeConfig(t, `
[modules.network]
enabled = false

[checks]
disable = ["mem.oom-kills"]

[checks.thresholds."mem.available"]
warn = 1.01
fail = 1.0
`)
	code, out, errOut := runCLI(t, "--config", cfg, "check", "-o", "json", "--only", "memory,network")
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	// --only wins over the configuration.
	if _, ok := r.Modules["network"]; !ok {
		t.Error("--only did not win over the configuration")
	}
	for _, f := range r.Findings {
		if f.ID == "mem.oom-kills" {
			t.Error("disabled check reported")
		}
		if f.ID == "mem.available" && f.Severity != model.SeverityFail {
			t.Errorf("configured threshold not applied: %+v", f)
		}
	}
	if code != model.ExitFail {
		t.Errorf("exit code %d", code)
	}

	_, out, _ = runCLI(t, "--config", cfg, "check", "-o", "json")
	r = model.Report{}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Modules["network"]; ok {
		t.Error("module disabled in the configuration ran")
	}
	if _, ok := r.Modules["memory"]; !ok {
		t.Error("core module did not run")
	}
}

func TestInvalidConfig(t *testing.T) {
	cfg := writeConfig(t, "[modules.htpp]\nenabled = true\n\n[checks]\ndisable = [\"dsk.*\"]\n")
	code, _, errOut := runCLI(t, "--config", cfg, "check")
	if code != model.ExitError || !strings.Contains(errOut, `unknown module "htpp"`) || !strings.Contains(errOut, `"dsk.*"`) {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	code, _, errOut = runCLI(t, "--config", filepath.Join(t.TempDir(), "missing.toml"), "config", "validate")
	if code != model.ExitError || !strings.Contains(errOut, "no such file") {
		t.Fatalf("explicit missing config: code %d, stderr %q", code, errOut)
	}
	code, _, errOut = runCLI(t, "--modules", "nope", "check")
	if code != model.ExitError || !strings.Contains(errOut, `"nope" (--modules)`) {
		t.Fatalf("unknown module flag: code %d, stderr %q", code, errOut)
	}
}

func TestModulesListAndOptionalModule(t *testing.T) {
	_, out, _ := runCLI(t, "modules", "list", "-o", "json")
	var rows []struct {
		Name    string
		Kind    string
		Enabled bool
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	state := map[string]string{}
	for _, r := range rows {
		state[r.Name] = r.Kind + "/" + map[bool]string{true: "on", false: "off"}[r.Enabled]
	}
	if state["http"] != "optional/off" || state["memory"] != "core/on" {
		t.Fatalf("modules: %v", state)
	}

	// Enabled from the command line without endpoints: skipped, not failed.
	_, out, _ = runCLI(t, "--modules", "http", "--no-modules", "network", "check", "-o", "json")
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Modules["http"].Status != model.StatusSkipped {
		t.Errorf("http: %+v", r.Modules["http"])
	}
	if _, ok := r.Modules["network"]; ok {
		t.Error("--no-modules ignored")
	}
}

func TestExternalModule(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"facts": {"repo": "s3"}, "findings": [{"id": "rsync.age", "severity": "fail", "message": "no sync"}]}'
`
	if err := os.WriteFile(filepath.Join(dir, "rsync.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "--modules-dir", dir, "--only", "rsync", "check", "-o", "json")
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	if code != model.ExitFail || len(r.Findings) != 1 || r.Findings[0].Module != "rsync" {
		t.Fatalf("code %d findings %+v", code, r.Findings)
	}
	if facts, _ := r.Modules["rsync"].Facts.(map[string]any); facts["repo"] != "s3" {
		t.Errorf("facts: %#v", r.Modules["rsync"].Facts)
	}

	cfg := writeConfig(t, "[checks]\ndisable = [\"rsync.*\"]\n")
	if code, _, errOut := runCLI(t, "--config", cfg, "--modules-dir", dir, "--only", "rsync", "check"); code != model.ExitOK {
		t.Errorf("disabled external check: code %d %s", code, errOut)
	}
}

func TestChecksList(t *testing.T) {
	cfg := writeConfig(t, "[checks.thresholds.\"disk.usage\"]\nwarn = 0.5\nfail = 0.6\n")
	_, out, errOut := runCLI(t, "--config", cfg, "checks", "list", "-o", "json")
	var rows []struct {
		ID         string
		Threshold  *struct{ Warn, Fail float64 }
		Configured bool `json:"threshold_configured"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	found := false
	for _, r := range rows {
		if r.ID == "disk.usage" {
			found = r.Configured && r.Threshold.Warn == 0.5
		}
	}
	if !found || len(rows) < 10 {
		t.Errorf("checks: %+v", rows)
	}
}

// uncomment turns the example into a configuration with every setting at its default.
var commented = regexp.MustCompile(`(?m)^#(\[|[a-z_]+ = |"|\[\[)`)

func TestConfigExampleIsValid(t *testing.T) {
	code, example, errOut := runCLI(t, "config", "example")
	if code != 0 {
		t.Fatalf("example: %d %s", code, errOut)
	}
	for _, content := range []string{example, commented.ReplaceAllString(example, "$1")} {
		cfg := writeConfig(t, content)
		if code, out, errOut := runCLI(t, "--config", cfg, "config", "validate"); code != 0 || !strings.Contains(out, "is valid") {
			t.Errorf("example not valid: %d %s %s\n%s", code, out, errOut, content)
		}
	}
}

func TestUsersFlagAndSystemdConfig(t *testing.T) {
	code, _, errOut := runCLI(t, "--users", "terminus-no-such-user", "check")
	if code != model.ExitError || !strings.Contains(errOut, `unknown user "terminus-no-such-user"`) {
		t.Fatalf("--users: code %d, stderr %q", code, errOut)
	}
	cfg := writeConfig(t, "[modules.systemd]\nusers = \"everyone\"\n")
	code, _, errOut = runCLI(t, "--config", cfg, "check")
	if code != model.ExitError || !strings.Contains(errOut, "modules.systemd.users") {
		t.Fatalf("users setting: code %d, stderr %q", code, errOut)
	}
	cfg = writeConfig(t, "[modules.systemd]\nusers = [\"root\"]\nhistory = \"3d\"\n\n[modules.journal]\nenabled = true\nwindow = \"1h\"\n")
	if code, out, errOut := runCLI(t, "--config", cfg, "config", "validate"); code != 0 || !strings.Contains(out, "journal") {
		t.Fatalf("valid systemd/journal settings: %d %s %s", code, out, errOut)
	}
}
