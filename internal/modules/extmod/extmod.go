// Package extmod loads external modules: executables in the modules directory that print
//
//	{
//	  "facts": { ... any JSON ... },
//	  "findings": [
//	    {"id": "rsync.age", "severity": "warn", "subject": "nas",
//	     "message": "last snapshot 3 days ago", "hint": "...", "evidence": {"age_hours": 72}}
//	  ]
//	}
//
// Each executable is a module named after the file without extension. External modules are
// enabled by default (putting them in the directory is the opt-in); [modules.<name>] enabled =
// false disables one.
package extmod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// validName are the module names accepted from file names.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Module is an external module.
type Module struct {
	name string
	path string
}

// Load returns the modules found in dir: the executable files. A missing directory yields none.
func Load(dir string) ([]*Module, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var mods []*Module
	var errs []error
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !validName.MatchString(name) {
			errs = append(errs, fmt.Errorf("external module %s: name %q must match %s", e.Name(), name, validName))
			continue
		}
		mods = append(mods, &Module{name: name, path: filepath.Join(dir, e.Name())})
	}
	return mods, errors.Join(errs...)
}

// Name implements module.Module.
func (m *Module) Name() string { return m.name }

// Description implements module.Module.
func (m *Module) Description() string { return "external module " + m.path }

// Core implements module.Module: external modules are enabled unless disabled.
func (m *Module) Core() bool { return true }

// External implements module.External.
func (m *Module) External() bool { return true }

// Checks implements module.Checker: external checks are not known in advance.
func (m *Module) Checks() []module.CheckInfo { return nil }

// output is what the executable prints.
type output struct {
	Facts    json.RawMessage `json:"facts"`
	Findings []model.Finding `json:"findings"`
}

// result carries the findings from Collect to Check; the report shows only the facts.
type result struct {
	facts    any
	findings []model.Finding
}

// ReportFacts implements module.Carrier.
func (r *result) ReportFacts() any { return r.facts }

// Collect implements module.Module. The engine timeout bounds the execution.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	var r runner.Runner = runner.Exec{}
	if env != nil && env.Runner != nil {
		r = env.Runner
	}
	res, err := r.Run(ctx, runner.Cmd{Name: m.path, Timeout: runner.NoTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	out, err := parse(m.name, res.Stdout)
	if out == nil {
		return nil, err // no typed nil in the interface
	}
	return out, err
}

func parse(name string, stdout []byte) (*result, error) {
	var out output
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid output: %w", err)
	}
	r := &result{}
	if len(out.Facts) > 0 {
		if err := json.Unmarshal(out.Facts, &r.facts); err != nil {
			return nil, fmt.Errorf("invalid facts: %w", err)
		}
	}
	var errs []error
	for i, f := range out.Findings {
		switch {
		case f.ID == "":
			errs = append(errs, fmt.Errorf("finding %d: missing id", i))
		case !strings.HasPrefix(f.ID, name+"."):
			errs = append(errs, fmt.Errorf("finding %d: id %q must start with %q", i, f.ID, name+"."))
		case f.Message == "":
			errs = append(errs, fmt.Errorf("finding %d (%s): missing message", i, f.ID))
		default:
			r.findings = append(r.findings, f)
		}
	}
	return r, errors.Join(errs...)
}

// Check implements module.Checker: it returns the findings printed by the executable.
func (m *Module) Check(_ *module.Env, facts any) []model.Finding {
	r, ok := facts.(*result)
	if !ok {
		return nil
	}
	return r.findings
}
