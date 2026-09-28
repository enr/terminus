// Package module defines the contract every terminus module implements.
//
// A module collects the facts of one domain (system, systemd, podman, ...) and can optionally
// evaluate them, producing findings.
package module

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

// Env is what modules get from the engine.
type Env struct {
	Runner runner.Runner
	Debug  bool
	Log    *slog.Logger
	// Checks holds the check settings of the configuration; nil means defaults.
	Checks *CheckSettings
}

// Threshold returns the threshold of a check: the configured one or def.
// It is safe on a nil Env.
func (e *Env) Threshold(id string, def Threshold) Threshold {
	if e == nil {
		return def
	}
	return e.Checks.Threshold(id, def)
}

// Module collects the facts of a domain.
type Module interface {
	// Name is the unique, lowercase module name: it is the key of the module in reports and queries.
	Name() string
	// Description says in one line what the module covers.
	Description() string
	// Core modules are enabled by default; the others must be enabled in the configuration.
	Core() bool
	// Collect returns the facts. A non-nil error together with non-nil facts means a partial
	// collection: the facts are kept and the error is reported.
	Collect(ctx context.Context, env *Env) (any, error)
}

// Checker is implemented by modules that evaluate their own facts.
type Checker interface {
	// Checks describes the checks of the module, with their default thresholds.
	Checks() []CheckInfo
	// Check receives the facts returned by Collect.
	Check(env *Env, facts any) []model.Finding
}

// Carrier is implemented by values that Collect returns to pass more than the facts to Check:
// the report shows ReportFacts(), Check receives the carrier itself.
type Carrier interface {
	ReportFacts() any
}

// Decoder decodes the configuration section of a module into v (a pointer to a struct).
// Keys that v does not know are reported as errors by the configuration loader.
type Decoder func(v any) error

// Configurable is implemented by modules that read settings from their [modules.<name>] section.
type Configurable interface {
	Configure(decode Decoder) error
	// ConfigExample returns the module settings at their defaults, as the body of the section
	// (without the header and the enabled key); nested tables are allowed.
	ConfigExample() string
}

// Detection is the outcome of Detector.Detect.
type Detection struct {
	// Found tells whether the machine runs what the module covers.
	Found bool
	// Reason explains the outcome (binary found, socket missing, ...).
	Reason string
	// Config is a suggested [modules.<name>] body for terminus.toml (without the header).
	Config string
}

// Detector is implemented by optional modules that can tell whether they are useful here.
type Detector interface {
	Detect(ctx context.Context, env *Env) Detection
}

// External is implemented by the modules loaded from the external modules directory.
type External interface {
	External() bool
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
