// Package module defines the contract every terminus module implements.
//
// A module collects the facts of one domain (system, systemd, podman, ...) and can optionally
// evaluate them, producing findings.
package module

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

// Env is what modules get from the engine.
type Env struct {
	Runner runner.Runner
	Debug  bool
	Log    *slog.Logger
}

// Module collects the facts of a domain.
type Module interface {
	// Name is the unique, lowercase module name: it is the key of the module in reports and queries.
	Name() string
	// Core modules are always enabled; the others must be enabled explicitly.
	Core() bool
	// Collect returns the facts. A non-nil error together with non-nil facts means a partial
	// collection: the facts are kept and the error is reported.
	Collect(ctx context.Context, env *Env) (any, error)
}

// Checker is implemented by modules that evaluate their own facts.
type Checker interface {
	// Check receives the facts returned by Collect.
	Check(env *Env, facts any) []model.Finding
}

// Registry holds the known modules.
type Registry struct {
	modules map[string]Module
}

// NewRegistry returns a registry with the given modules.
func NewRegistry(mods ...Module) (*Registry, error) {
	r := &Registry{modules: map[string]Module{}}
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a module; names must be unique.
func (r *Registry) Register(m Module) error {
	if _, ok := r.modules[m.Name()]; ok {
		return fmt.Errorf("module %q already registered", m.Name())
	}
	r.modules[m.Name()] = m
	return nil
}

// Get returns the module with the given name.
func (r *Registry) Get(name string) (Module, bool) {
	m, ok := r.modules[name]
	return m, ok
}

// All returns the modules sorted by name.
func (r *Registry) All() []Module {
	mods := make([]Module, 0, len(r.modules))
	for _, m := range r.modules {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Name() < mods[j].Name() })
	return mods
}

// Select returns the named modules, or the core ones when no name is given.
func (r *Registry) Select(names []string) ([]Module, error) {
	if len(names) == 0 {
		var core []Module
		for _, m := range r.All() {
			if m.Core() {
				core = append(core, m)
			}
		}
		return core, nil
	}
	seen := map[string]bool{}
	var mods []Module
	for _, n := range names {
		m, ok := r.modules[n]
		if !ok {
			return nil, fmt.Errorf("unknown module %q", n)
		}
		if !seen[n] {
			seen[n] = true
			mods = append(mods, m)
		}
	}
	return mods, nil
}

// SkipError is returned by modules that cannot run on this machine (missing binary, socket, ...).
type SkipError struct {
	Reason string
}

func (e *SkipError) Error() string { return "skipped: " + e.Reason }

// Skip returns a SkipError.
func Skip(format string, args ...any) error {
	return &SkipError{Reason: fmt.Sprintf(format, args...)}
}
