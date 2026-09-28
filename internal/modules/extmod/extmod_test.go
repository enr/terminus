package extmod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

func script(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndRun(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "rsync.sh", `cat <<'JSON'
{"facts": {"last_snapshot": "2026-09-27"},
 "findings": [
   {"id": "rsync.age", "severity": "warn", "subject": "nas", "message": "last sync 30h ago", "evidence": {"age_hours": 30}},
   {"id": "other.x", "severity": "ok", "message": "wrong prefix"},
   {"id": "rsync.nomsg", "severity": "ok"}
 ]}
JSON
`, 0o755)
	script(t, dir, "Bad Name.sh", "echo {}", 0o755)
	script(t, dir, "notexec.sh", "echo {}", 0o644)
	script(t, dir, "fails", "echo boom >&2; exit 4", 0o755)
	script(t, dir, "garbage", "echo not json", 0o755)

	mods, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "Bad Name") {
		t.Errorf("invalid name not reported: %v", err)
	}
	byName := map[string]*Module{}
	for _, m := range mods {
		byName[m.Name()] = m
	}
	if len(byName) != 3 || byName["rsync"] == nil || byName["fails"] == nil || byName["garbage"] == nil {
		t.Fatalf("modules: %v", byName)
	}
	env := &module.Env{Runner: runner.Exec{}}

	m := byName["rsync"]
	got, err := m.Collect(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), `must start with "rsync."`) || !strings.Contains(err.Error(), "missing message") {
		t.Errorf("invalid findings not reported: %v", err)
	}
	c, ok := got.(module.Carrier)
	if !ok || c.ReportFacts().(map[string]any)["last_snapshot"] != "2026-09-27" {
		t.Fatalf("facts: %#v", got)
	}
	fs := m.Check(env, got)
	if len(fs) != 1 || fs[0].ID != "rsync.age" || fs[0].Severity != model.SeverityWarn || fs[0].Subject != "nas" {
		t.Errorf("findings: %+v", fs)
	}
	if !m.External() || !m.Core() || m.Checks() != nil {
		t.Error("external module flags")
	}

	if got, err := byName["fails"].Collect(context.Background(), env); got != nil || err == nil || !strings.Contains(err.Error(), "exit code 4: boom") {
		t.Errorf("failing module: %v %v", got, err)
	}
	if got, err := byName["garbage"].Collect(context.Background(), env); got != nil || err == nil {
		t.Errorf("garbage output: %#v %v", got, err)
	}
	if mods, err := Load(filepath.Join(dir, "none")); mods != nil || err != nil {
		t.Errorf("missing dir: %v %v", mods, err)
	}
}

func TestParseRejectsUnknownFieldsAndSeverities(t *testing.T) {
	if _, err := parse("m", []byte(`{"facts": {}, "extra": 1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := parse("m", []byte(`{"findings": [{"id": "m.a", "severity": "critical", "message": "x"}]}`)); err == nil {
		t.Error("unknown severity accepted")
	}
}
