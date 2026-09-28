package external

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

func write(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker.json", `{"ServerAPIVersion":"1.16"}`, 0o644)
	write(t, dir, "date.sh", "#!/bin/sh\necho '{\"Now\": \"today\"}'\n", 0o755)
	write(t, dir, "broken.json", `{`, 0o644)
	write(t, dir, "fails", "#!/bin/sh\necho oops >&2\nexit 2\n", 0o755)
	write(t, dir, "notes.txt", "ignored", 0o644)
	write(t, dir, ".hidden.json", `{}`, 0o644)

	got, err := New(dir).Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	facts := got.(map[string]any)
	if len(facts) != 2 {
		t.Fatalf("facts: %v", facts)
	}
	if facts["docker"].(map[string]any)["ServerAPIVersion"] != "1.16" || facts["date"].(map[string]any)["Now"] != "today" {
		t.Errorf("facts: %v", facts)
	}
	if err == nil || !strings.Contains(err.Error(), "broken.json: invalid JSON") || !strings.Contains(err.Error(), "fails: exit code 2: oops") {
		t.Errorf("errors: %v", err)
	}
}

func TestDuplicateAndSkip(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", `1`, 0o644)
	write(t, dir, "a.sh", "#!/bin/sh\necho 2\n", 0o755)
	_, err := New(dir).Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "already provided") {
		t.Errorf("duplicate not reported: %v", err)
	}

	_, err = New(filepath.Join(dir, "none")).Collect(context.Background(), nil)
	if _, ok := err.(*module.SkipError); !ok {
		t.Errorf("missing directory: %v", err)
	}
}

func TestCollectFollowsSymlinksForClassification(t *testing.T) {
	dir := t.TempDir()
	// A symlink's own mode is always rwxrwxrwx: classifying it by DirEntry.Info() (which does
	// not follow it) would try to execute a symlinked, non-executable JSON file.
	write(t, dir, "real.json", `{"a":1}`, 0o644)
	if err := os.Symlink(filepath.Join(dir, "real.json"), filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}

	got, err := New(dir).Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	if err != nil {
		t.Fatal(err)
	}
	facts := got.(map[string]any)
	if facts["linked"] == nil || facts["linked"].(map[string]any)["a"] != float64(1) {
		t.Fatalf("facts: %v", facts)
	}
}

func TestCollectRejectsUntrustedPermissions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "shared.json", `{"a":1}`, 0o644)
	if err := os.Chmod(filepath.Join(dir, "shared.json"), 0o666); err != nil { // bypass umask
		t.Fatal(err)
	}
	got, err := New(dir).Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("group/world-writable fact accepted: %v", err)
	}
	if facts := got.(map[string]any); facts["shared"] != nil {
		t.Errorf("group/world-writable fact was loaded: %v", facts)
	}
}
