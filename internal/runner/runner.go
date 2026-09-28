// Package runner executes external commands with timeouts, so that modules never hang the tool.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultTimeout applies to commands that do not set their own timeout.
const DefaultTimeout = 10 * time.Second

// Cmd describes a command to run.
type Cmd struct {
	Name    string
	Args    []string
	Env     []string // added to the current environment
	Timeout time.Duration
	// User to run the command as. Not implemented yet: a non-empty value makes Run fail.
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
	LookPath(name string) (string, error)
}

// ErrUserNotSupported is returned when a command asks to run as another user.
var ErrUserNotSupported = errors.New("running commands as another user is not supported yet")

// Exec runs commands on the local machine.
type Exec struct{}

// Run executes the command and waits for it, killing it when the timeout expires.
func (Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.User != "" {
		return Result{}, ErrUserNotSupported
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%s: %w", c.Name, ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

// LookPath searches for an executable in PATH.
func (Exec) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}
