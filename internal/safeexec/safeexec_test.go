package safeexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.sh")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Check(good, dir); err != nil {
		t.Errorf("owned, not group/other writable: %v", err)
	}

	writable := filepath.Join(dir, "writable.sh")
	if err := os.WriteFile(writable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil { // WriteFile applies umask; force the mode
		t.Fatal(err)
	}
	if err := Check(writable, dir); err == nil {
		t.Error("world-writable file accepted")
	}

	target := filepath.Join(dir, "target.sh")
	os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755)
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Check(link, dir); err != nil {
		t.Errorf("symlink to a safe target: %v", err)
	}

	os.Chmod(target, 0o777)
	if err := Check(link, dir); err == nil {
		t.Error("symlink to a world-writable target accepted")
	}

	writableDir := t.TempDir()
	os.Chmod(writableDir, 0o777)
	inWritableDir := filepath.Join(writableDir, "ok.sh")
	os.WriteFile(inWritableDir, []byte("#!/bin/sh\n"), 0o755)
	if err := Check(inWritableDir, writableDir); err == nil {
		t.Error("file in a world-writable directory accepted")
	}
}
