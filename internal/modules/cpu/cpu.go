// Package cpu is the core module with the processors, the load and the CPU pressure.
package cpu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "cpu"

// Thresholds on the 15 minutes load average per logical CPU.
const (
	loadWarn = 1.0
	loadFail = 2.0
)

// Thresholds on the share of time (percent, 5 minutes average) some task waited for a CPU.
const (
	pressureWarn = 20.0
	pressureFail = 50.0
)

// Facts about the processors.
type Facts struct {
	Model   string `json:"model"`
	Vendor  string `json:"vendor,omitempty"`
	Logical int    `json:"logical"`
	// Online lists the online CPUs as the kernel prints them (0-3,6).
	Online  string      `json:"online,omitempty"`
	Cores   int         `json:"cores,omitempty"`
	Sockets int         `json:"sockets,omitempty"`
	MHz     float64     `json:"mhz,omitempty"`
	Flags   []string    `json:"flags,omitempty"`
	Load    Load        `json:"load"`
	PSI     *hostfs.PSI `json:"pressure,omitempty"`
}

// Load is /proc/loadavg.
type Load struct {
	Avg1          float64 `json:"avg1"`
	Avg5          float64 `json:"avg5"`
	Avg15         float64 `json:"avg15"`
	RunnableTasks int     `json:"runnable_tasks"`
	TotalTasks    int     `json:"total_tasks"`
	Avg15PerCPU   float64 `json:"avg15_per_cpu"`
}

// Module collects the CPU facts.
type Module struct {
	fs hostfs.FS
}

// New returns the cpu module reading the running machine.
func New() *Module { return &Module{fs: hostfs.Host} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (m *Module) Collect(_ context.Context, _ *module.Env) (any, error) {
	var errs []error
	f := &Facts{}
	if lines, err := m.fs.Lines("/proc/cpuinfo"); err != nil {
		errs = append(errs, err)
	} else {
		parseCPUInfo(lines, f)
	}
	f.Online, _ = m.fs.ReadString("/sys/devices/system/cpu/online")

	if s, err := m.fs.ReadString("/proc/loadavg"); err != nil {
		errs = append(errs, err)
	} else if f.Load, err = parseLoadAvg(s); err != nil {
		errs = append(errs, err)
	}
	if f.Logical > 0 {
		f.Load.Avg15PerCPU = f.Load.Avg15 / float64(f.Logical)
	}

	psi, err := m.fs.ReadPSI("cpu")
	if err != nil {
		errs = append(errs, err)
	}
	f.PSI = psi
	return f, errors.Join(errs...)
}

// parseCPUInfo fills the processor facts from /proc/cpuinfo (x86 and ARM layouts).
func parseCPUInfo(lines []string, f *Facts) {
	cores := map[string]bool{}
	sockets := map[string]bool{}
	var physicalID string
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			f.Logical++
			physicalID = ""
		case "model name", "cpu model", "Processor":
			if f.Model == "" {
				f.Model = v
			}
		case "vendor_id", "CPU implementer":
			if f.Vendor == "" {
				f.Vendor = v
			}
		case "cpu MHz":
			if f.MHz == 0 {
				f.MHz, _ = strconv.ParseFloat(v, 64)
			}
		case "flags", "Features":
			if f.Flags == nil {
				f.Flags = strings.Fields(v)
			}
		case "physical id":
			physicalID = v
			sockets[v] = true
		case "core id":
			cores[physicalID+"/"+v] = true
		}
	}
	f.Cores, f.Sockets = len(cores), len(sockets)
}

func parseLoadAvg(s string) (Load, error) {
	var l Load
	fields := strings.Fields(s)
	if len(fields) < 4 {
		return l, fmt.Errorf("/proc/loadavg: unexpected content %q", s)
	}
	var err error
	for i, p := range []*float64{&l.Avg1, &l.Avg5, &l.Avg15} {
		if *p, err = strconv.ParseFloat(fields[i], 64); err != nil {
			return l, fmt.Errorf("/proc/loadavg: %w", err)
		}
	}
	if running, total, ok := strings.Cut(fields[3], "/"); ok {
		l.RunnableTasks, _ = strconv.Atoi(running)
		l.TotalTasks, _ = strconv.Atoi(total)
	}
	return l, nil
}

// Check implements module.Checker.
func (*Module) Check(_ *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	if f.Logical > 0 {
		load := model.Finding{
			ID:      "cpu.load",
			Subject: "load average",
			Evidence: map[string]any{
				"load15":         f.Load.Avg15,
				"logical_cpus":   f.Logical,
				"load15_per_cpu": f.Load.Avg15PerCPU,
			},
			Severity: model.Grade(f.Load.Avg15PerCPU, loadWarn, loadFail),
		}
		load.Message = fmt.Sprintf("15 min load %.2f on %d CPUs (%.2f per CPU)", f.Load.Avg15, f.Logical, f.Load.Avg15PerCPU)
		if load.Severity >= model.SeverityWarn {
			load.Hint = "more runnable or blocked (I/O) tasks than CPUs: check top/pidstat and the I/O wait"
		}
		out = append(out, load)
	}
	if f.PSI != nil && f.PSI.Some != nil {
		p := f.PSI.Some.Avg300
		fnd := model.Finding{
			ID:       "cpu.pressure",
			Subject:  "cpu",
			Severity: model.Grade(p, pressureWarn, pressureFail),
			Message:  fmt.Sprintf("tasks waited for a CPU %.1f%% of the time (5 min)", p),
			Evidence: map[string]any{"psi_some_avg300": p, "psi_some_avg60": f.PSI.Some.Avg60},
		}
		if fnd.Severity >= model.SeverityWarn {
			fnd.Hint = "the CPUs are saturated: find the busiest processes/containers or add CPUs"
		}
		out = append(out, fnd)
	}
	return out
}
