package runner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Fake is a Runner that returns canned results, for tests.
// Results are keyed by the command line: name and arguments joined by spaces.
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
	key := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if err, ok := f.Errors[key]; ok {
		return Result{}, err
	}
	if r, ok := f.Results[key]; ok {
		return r, nil
	}
	return Result{}, fmt.Errorf("fake runner: no result for %q", key)
}

// LookPath returns the canned path for the executable.
func (f *Fake) LookPath(name string) (string, error) {
	if p, ok := f.Paths[name]; ok {
		return p, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
