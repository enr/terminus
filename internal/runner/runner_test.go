package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecRun(t *testing.T) {
	r := Exec{}
	ctx := context.Background()

	res, err := r.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "out" || strings.TrimSpace(string(res.Stderr)) != "err" {
		t.Fatalf("unexpected output: %+v", res)
	}

	res, err = r.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}

	res, err = r.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `printf %s "$FOO"`}, Env: []string{"FOO=bar"}})
	if err != nil || string(res.Stdout) != "bar" {
		t.Fatalf("env not passed: %q %v", res.Stdout, err)
	}
}

func TestExecRunTimeout(t *testing.T) {
	start := time.Now()
	_, err := Exec{}.Run(context.Background(), Cmd{Name: "sleep", Args: []string{"5"}, Timeout: 100 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("command was not killed on timeout")
	}
}

func TestExecRunErrors(t *testing.T) {
	if _, err := (Exec{}).Run(context.Background(), Cmd{Name: "terminus-no-such-command"}); err == nil {
		t.Fatal("expected error for missing command")
	}
	if _, err := (Exec{}).Run(context.Background(), Cmd{Name: "true", User: "nobody"}); !errors.Is(err, ErrUserNotSupported) {
		t.Fatalf("err = %v, want ErrUserNotSupported", err)
	}
}

func TestFake(t *testing.T) {
	f := &Fake{
		Results: map[string]Result{"podman version": {Stdout: []byte("5.0")}},
		Paths:   map[string]string{"podman": "/usr/bin/podman"},
	}
	res, err := f.Run(context.Background(), Cmd{Name: "podman", Args: []string{"version"}})
	if err != nil || string(res.Stdout) != "5.0" {
		t.Fatalf("got %q %v", res.Stdout, err)
	}
	if _, err := f.Run(context.Background(), Cmd{Name: "other"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
	if _, err := f.LookPath("caddy"); err == nil {
		t.Fatal("expected not found")
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %d", len(f.Calls))
	}
}
