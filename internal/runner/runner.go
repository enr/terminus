// Package runner executes external commands with timeouts, so that modules never hang the tool.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// waitDelay bounds how long Wait waits after the process group was killed: a grandchild that
// inherited the stdout/stderr pipes (a backgrounded job, an ssh ControlMaster) would otherwise
// keep them open forever, and Wait with them.
const waitDelay = 5 * time.Second

// DefaultTimeout applies to commands that do not set their own timeout.
const DefaultTimeout = 10 * time.Second

// NoTimeout makes a command bounded only by its context.
const NoTimeout time.Duration = -1

// maxLine bounds the lines read by Stream.
const maxLine = 4 << 20

// Cmd describes a command to run.
type Cmd struct {
	Name    string
	Args    []string
	Env     []string      // added to the current environment
	Timeout time.Duration // 0: DefaultTimeout; NoTimeout: only the context
	// Stdin is the standard input of the command; nil means none.
	Stdin io.Reader
	// User runs the command as this user (name or numeric UID), with the environment of its
	// session (HOME, XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS). Another user than the current
	// one requires root.
	User string
}

// Result of a command. A non-zero exit code is not an error: callers decide what it means.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner runs commands.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
	// Stream runs the command passing each line of its standard output to fn, without keeping
	// the output in memory. The returned Result has no Stdout. An error from fn stops the command.
	Stream(ctx context.Context, cmd Cmd, fn func(line []byte) error) (Result, error)
	LookPath(name string) (string, error)
}

// Exec runs commands on the local machine.
type Exec struct{}

// Run executes the command and waits for it, killing it when the timeout expires.
func (e Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	var stdout bytes.Buffer
	res, err := e.run(ctx, c, &stdout, nil)
	res.Stdout = stdout.Bytes()
	return res, err
}

// Stream implements Runner.
func (e Exec) Stream(ctx context.Context, c Cmd, fn func([]byte) error) (Result, error) {
	return e.run(ctx, c, nil, fn)
}

func (Exec) run(ctx context.Context, c Cmd, stdout io.Writer, fn func([]byte) error) (Result, error) {
	switch timeout := c.Timeout; {
	case timeout == NoTimeout:
	case timeout <= 0:
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	default:
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if c.User != "" {
		if err := asUser(cmd, c.User); err != nil {
			return Result{}, err
		}
	}
	// Run in its own process group so that the timeout or a cancellation kills the whole tree
	// (a shell's background jobs, a pipeline), not just the direct child.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
	if len(c.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, c.Env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = c.Stdin

	var err error
	if fn == nil {
		cmd.Stdout = stdout
		err = cmd.Run()
	} else {
		err = stream(cmd, fn, cancel)
	}
	res := Result{Stderr: stderr.Bytes()}
	// Timeout, cancellation or an error from fn: the exit status of a killed command means nothing.
	if cause := context.Cause(ctx); cause != nil {
		return res, fmt.Errorf("%s: %w", c.Name, cause)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

// stream feeds the lines of the command output to fn; an error from fn cancels the command.
func stream(cmd *exec.Cmd, fn func([]byte) error, cancel context.CancelCauseFunc) error {
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s := bufio.NewScanner(out)
	s.Buffer(make([]byte, 64*1024), maxLine)
	for s.Scan() {
		if err := fn(s.Bytes()); err != nil {
			cancel(err)
			break
		}
	}
	scanErr := s.Err()
	if scanErr != nil {
		cancel(scanErr)
	}
	// Drain what is left so that the command is not blocked on a full pipe before being killed.
	_, _ = io.Copy(io.Discard, out)
	return cmd.Wait()
}

// LookPath searches for an executable in PATH.
func (Exec) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}
