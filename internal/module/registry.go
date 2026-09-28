package module

import (
	"fmt"
	"sort"
	"strings"
)

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

// Checks returns the checks of all the modules, keyed by ID.
func (r *Registry) Checks() map[string]CheckInfo {
	out := map[string]CheckInfo{}
	for _, m := range r.modules {
		if c, ok := m.(Checker); ok {
			for _, ci := range c.Checks() {
				out[ci.ID] = ci
			}
		}
	}
	return out
}

// Selection decides which modules run. Precedence: Only, then the command line flags
// (Enable, Disable), then the configuration (Config), then the module default (core or not).
type Selection struct {
	// Only runs exactly these modules (--only).
	Only []string
	// Enable and Disable come from --modules and --no-modules.
	Enable, Disable []string
	// Config holds the "enabled" keys of the configuration, by module.
	Config map[string]bool
}

// State tells whether a module runs and why.
type State struct {
	Module  Module
	Enabled bool
	Reason  string
}

// Resolve applies a selection to all the modules. Unknown module names are errors.
func (r *Registry) Resolve(sel Selection) ([]State, error) {
	var unknown []string
	check := func(names []string, what string) {
		for _, n := range names {
			if _, ok := r.modules[n]; !ok {
				unknown = append(unknown, fmt.Sprintf("%q (%s)", n, what))
			}
		}
	}
	check(sel.Only, "--only")
	check(sel.Enable, "--modules")
	check(sel.Disable, "--no-modules")
	var configured []string
	for n := range sel.Config {
		configured = append(configured, n)
	}
	sort.Strings(configured)
	check(configured, "configuration")
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown modules: %s (known: %s)", strings.Join(unknown, ", "), strings.Join(r.names(), ", "))
	}

	in := func(names []string, n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	var states []State
	for _, m := range r.All() {
		n := m.Name()
		s := State{Module: m}
		enabledInConfig, inConfig := sel.Config[n]
		switch {
		case len(sel.Only) > 0:
			s.Enabled, s.Reason = in(sel.Only, n), "--only"
		case in(sel.Disable, n):
			s.Enabled, s.Reason = false, "--no-modules"
		case in(sel.Enable, n):
			s.Enabled, s.Reason = true, "--modules"
		case inConfig && enabledInConfig:
			s.Enabled, s.Reason = true, "enabled in configuration"
		case inConfig:
			s.Enabled, s.Reason = false, "disabled in configuration"
		case isExternal(m):
			s.Enabled, s.Reason = true, "external module, enabled by default"
		case m.Core():
			s.Enabled, s.Reason = true, "core module"
		default:
			s.Enabled, s.Reason = false, "optional module, disabled by default"
		}
		states = append(states, s)
	}
	return states, nil
}

// Enabled returns the modules that run under a selection.
func (r *Registry) Enabled(sel Selection) ([]Module, error) {
	states, err := r.Resolve(sel)
	if err != nil {
		return nil, err
	}
	var mods []Module
	for _, s := range states {
		if s.Enabled {
			mods = append(mods, s.Module)
		}
	}
	return mods, nil
}

func (r *Registry) names() []string {
	var n []string
	for _, m := range r.All() {
		n = append(n, m.Name())
	}
	return n
}

func isExternal(m Module) bool {
	e, ok := m.(External)
	return ok && e.External()
}
