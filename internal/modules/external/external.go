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
	"github.com/enr/terminus/internal/safeexec"
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

// Configure implements module.Configurable:
//
//	[modules.external]
//	dir = "/etc/terminus/facts.d"
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Dir string `toml:"dir"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Dir != "" {
		m.dir = c.Dir
	}
	return nil
}

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string { return `dir = "` + DefaultDir + `"` }

// SetDir changes the directory (the --external-facts-dir flag wins over the configuration).
func (m *Module) SetDir(dir string) { m.dir = dir }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string { return "custom facts from executables and JSON files" }

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
		path := filepath.Join(m.dir, name)
		// os.Stat follows symlinks: what is classified and checked below is what actually runs
		// or is read, not the symlink itself (always rwxrwxrwx, so e.Info(), which does not
		// follow it, would see every symlink as executable).
		info, err := os.Stat(path)
		if err != nil {
			add(name, nil, err)
			continue
		}
		exec := info.Mode()&0o111 != 0
		if !exec && !strings.HasSuffix(name, ".json") {
			continue
		}
		// A custom fact, executable or not, is trusted input: a file only root or the terminus
		// user could have placed there, and that no one else can write to or replace.
		if err := safeexec.Check(path, m.dir); err != nil {
			add(name, nil, fmt.Errorf("not trusted: %w", err))
			continue
		}
		if exec {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := fromExec(ctx, r, path)
				add(name, v, err)
			}()
			continue
		}
		v, err := fromFile(path)
		add(name, v, err)
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
