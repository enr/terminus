package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Fake is a Runner that returns canned results, for tests.
// Results are keyed by Key: the command line, prefixed by "user|" when the command runs as a user.
type Fake struct {
	Results map[string]Result
	Errors  map[string]error
	Paths   map[string]string

	mu    sync.Mutex
	Calls []Cmd
}

// Run returns the canned result for the command, or an error if there is none.
func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, c)
	f.mu.Unlock()
	key := Key(c)
	if err, ok := f.Errors[key]; ok {
		return Result{}, err
	}
	if r, ok := f.Results[key]; ok {
		return r, nil
	}
	return Result{}, fmt.Errorf("fake runner: no result for %q", key)
}

// Stream passes the lines of the canned output to fn.
func (f *Fake) Stream(ctx context.Context, c Cmd, fn func([]byte) error) (Result, error) {
	res, err := f.Run(ctx, c)
	if err != nil {
		return res, err
	}
	for _, l := range bytes.Split(bytes.TrimSuffix(res.Stdout, []byte("\n")), []byte("\n")) {
		if len(res.Stdout) == 0 {
			break
		}
		if err := fn(l); err != nil {
			return Result{}, err
		}
	}
	res.Stdout = nil
	return res, nil
}

// Key returns the key of a command in Results, Errors and Calls: the user, when set, followed by
// "|", then the command line.
func Key(c Cmd) string {
	k := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if c.User != "" {
		k = c.User + "|" + k
	}
	return k
}

// LookPath returns the canned path for the executable.
func (f *Fake) LookPath(name string) (string, error) {
	if p, ok := f.Paths[name]; ok {
		return p, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
