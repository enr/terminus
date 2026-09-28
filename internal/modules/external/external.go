// Package external is the core module that loads custom facts from a directory: executables
// printing JSON and static .json files (see docs/custom-facts.md).
package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "external"

// DefaultDir is the default external facts directory.
const DefaultDir = "/etc/terminus/facts.d"

// execTimeout bounds each executable fact.
const execTimeout = 30 * time.Second

// Module loads the external facts.
type Module struct {
	dir string
}

// New returns the external facts module reading from dir.
func New(dir string) *Module { return &Module{dir: dir} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module. Facts are keyed by file name without extension: docker.json
// and docker.sh both provide the "docker" fact.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	entries, err := os.ReadDir(m.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, module.Skip("directory %s does not exist", m.dir)
	}
	if err != nil {
		return nil, err
	}
	var r runner.Runner = runner.Exec{}
	if env != nil && env.Runner != nil {
		r = env.Runner
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		facts = map[string]any{}
		errs  []error
	)
	add := func(name string, v any, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return
		}
		key := strings.TrimSuffix(name, filepath.Ext(name))
		if _, dup := facts[key]; dup {
			errs = append(errs, fmt.Errorf("%s: fact %q already provided by another file", name, key))
			return
		}
		facts[key] = v
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			add(name, nil, err)
			continue
		}
		path := filepath.Join(m.dir, name)
		switch {
		case info.Mode()&0o111 != 0:
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := fromExec(ctx, r, path)
				add(name, v, err)
			}()
		case strings.HasSuffix(name, ".json"):
			v, err := fromFile(path)
			add(name, v, err)
		}
	}
	wg.Wait()
	return facts, errors.Join(errs...)
}

func fromFile(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(b)
}

func fromExec(ctx context.Context, r runner.Runner, path string) (any, error) {
	res, err := r.Run(ctx, runner.Cmd{Name: path, Timeout: execTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return decode(res.Stdout)
}

func decode(b []byte) (any, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return v, nil
}
