// Package system is the core module with the facts about the machine itself: OS, kernel,
// hardware, memory, network interfaces, filesystems.
//
// For now it wraps the collectors in lib/facts; they will be ported here (phase 2).
package system

import (
	"context"
	"fmt"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/lib/facts"
)

// Name of the module.
const Name = "system"

// Thresholds on available memory, as a fraction of the total.
const (
	memAvailableWarn = 0.10
	memAvailableFail = 0.05
)

// Module collects the system facts.
type Module struct{}

// New returns the system module.
func New() *Module { return &Module{} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (*Module) Collect(_ context.Context, _ *module.Env) (any, error) {
	return facts.System(), nil
}

// Check implements module.Checker.
func (*Module) Check(_ *module.Env, f any) []model.Finding {
	sf, ok := f.(*facts.SystemFacts)
	if !ok {
		return nil
	}
	return checkMemory(sf.Memory)
}

func checkMemory(m facts.Memory) []model.Finding {
	if m.Total == 0 || m.Available == 0 {
		return nil
	}
	ratio := float64(m.Available) / float64(m.Total)
	f := model.Finding{
		ID:      "mem.available-low",
		Subject: "memory",
		Evidence: map[string]any{
			"available_bytes": m.Available,
			"total_bytes":     m.Total,
			"available_ratio": ratio,
		},
	}
	switch {
	case ratio < memAvailableFail:
		f.Severity = model.SeverityFail
	case ratio < memAvailableWarn:
		f.Severity = model.SeverityWarn
	default:
		f.Severity = model.SeverityOK
		f.Message = fmt.Sprintf("%.0f%% of memory available", ratio*100)
		return []model.Finding{f}
	}
	f.Message = fmt.Sprintf("only %.1f%% of memory available", ratio*100)
	f.Hint = "check which processes or containers use the memory (ps, podman stats) and their limits"
	return []model.Finding{f}
}
