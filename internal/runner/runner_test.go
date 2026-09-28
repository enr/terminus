package runner

import (
	"context"
	"errors"
	"os"
	"strconv"
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

func TestExecRunKillsProcessGroup(t *testing.T) {
	// A backgrounded grandchild must die with the timeout, not keep the pipes open until it
	// finishes on its own.
	start := time.Now()
	_, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & wait"}, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s: the backgrounded child was not killed", elapsed)
	}
}

func TestExecRunErrors(t *testing.T) {
	if _, err := (Exec{}).Run(context.Background(), Cmd{Name: "terminus-no-such-command"}); err == nil {
		t.Fatal("expected error for missing command")
	}
	if _, err := (Exec{}).Run(context.Background(), Cmd{Name: "true", User: "terminus-no-such-user"}); err == nil {
		t.Fatal("unknown user accepted")
	}
}

func TestExecRunAsUser(t *testing.T) {
	cur, err := LookupAccount(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	// The current user: no privilege needed, only the session environment.
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `printf %s "$XDG_RUNTIME_DIR"`}, User: cur.Name})
	if err != nil || string(res.Stdout) != cur.RuntimeDir() {
		t.Fatalf("current user: %q %v", res.Stdout, err)
	}

	res, err = Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `id -u; printf %s "$DBUS_SESSION_BUS_ADDRESS"`}, User: "nobody"})
	if os.Geteuid() != 0 {
		if !errors.Is(err, ErrNeedRoot) {
			t.Fatalf("non-root: err = %v, want ErrNeedRoot", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "65534\nunix:path=/run/user/65534/bus" {
		t.Fatalf("as nobody: %q", res.Stdout)
	}
}

func TestLookupAccount(t *testing.T) {
	a, err := LookupAccount("0")
	if err != nil || a.Name != "root" || a.UID != 0 {
		t.Fatalf("uid 0: %+v %v", a, err)
	}
	a, err = LookupAccount("4242")
	if err != nil || a.UID != 4242 {
		t.Fatalf("uid without passwd entry: %+v %v", a, err)
	}
}

func TestExecStream(t *testing.T) {
	var lines []string
	res, err := Exec{}.Stream(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "printf 'a\\nb\\nc'; exit 2"}},
		func(l []byte) error { lines = append(lines, string(l)); return nil })
	if err != nil || res.ExitCode != 2 || strings.Join(lines, ",") != "a,b,c" {
		t.Fatalf("stream: %v %v %v", lines, res, err)
	}

	stop := errors.New("enough")
	start := time.Now()
	n := 0
	_, err = Exec{}.Stream(context.Background(), Cmd{Name: "yes"}, func([]byte) error {
		n++
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || time.Since(start) > 3*time.Second {
		t.Fatalf("stopped stream: %v after %s", err, time.Since(start))
	}

	f := &Fake{Results: map[string]Result{"u|cmd x": {Stdout: []byte("1\n2\n")}}}
	lines = nil
	if _, err := f.Stream(context.Background(), Cmd{Name: "cmd", Args: []string{"x"}, User: "u"}, func(l []byte) error {
		lines = append(lines, string(l))
		return nil
	}); err != nil || strings.Join(lines, ",") != "1,2" {
		t.Fatalf("fake stream: %v %v", lines, err)
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
