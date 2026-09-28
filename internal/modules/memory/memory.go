// Package memory is the core module with RAM, swap, OOM kills and memory pressure.
package memory

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
const Name = "memory"

// Default thresholds.
var (
	// availableThreshold is on MemAvailable as a fraction of MemTotal.
	availableThreshold = module.Threshold{Warn: 0.10, Fail: 0.05, Below: true, Unit: "ratio"}
	// swapThreshold is on used swap as a fraction of the swap space.
	swapThreshold = module.Threshold{Warn: 0.50, Fail: 0.80, Unit: "ratio"}
	// pressureThreshold is on the share of time all tasks stalled on memory (5 minutes average).
	pressureThreshold = module.Threshold{Warn: 5, Fail: 20, Unit: "percent"}
)

// Facts about memory. Sizes are in bytes.
type Facts struct {
	TotalBytes     uint64  `json:"total_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	FreeBytes      uint64  `json:"free_bytes"`
	BuffersBytes   uint64  `json:"buffers_bytes"`
	CachedBytes    uint64  `json:"cached_bytes"`
	SharedBytes    uint64  `json:"shared_bytes"`
	DirtyBytes     uint64  `json:"dirty_bytes"`
	AvailableRatio float64 `json:"available_ratio"`
	SwapTotalBytes uint64  `json:"swap_total_bytes"`
	SwapFreeBytes  uint64  `json:"swap_free_bytes"`
	SwapUsedRatio  float64 `json:"swap_used_ratio"`
	// OOMKills counts the processes killed by the OOM killer since boot, cgroup limits included
	// (kernel >= 4.13).
	OOMKills *uint64     `json:"oom_kills,omitempty"`
	PSI      *hostfs.PSI `json:"pressure,omitempty"`
	// Meminfo is /proc/meminfo as is: kB values converted to bytes, the others (HugePages_*) counts.
	Meminfo map[string]uint64 `json:"meminfo"`
}

// Module collects the memory facts.
type Module struct {
	fs hostfs.FS
}

// New returns the memory module reading the running machine.
func New() *Module { return &Module{fs: hostfs.Host} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string { return "RAM, swap, OOM kills, memory pressure" }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "mem.available", Description: "available memory (MemAvailable) over total", Threshold: &availableThreshold},
		{ID: "mem.swap-used", Description: "used swap over swap space", Threshold: &swapThreshold},
		{ID: "mem.oom-kills", Description: "processes killed by the OOM killer since boot, cgroup limits included"},
		{ID: "mem.pressure", Description: "share of time all tasks stalled on memory (PSI full, 5 min)", Threshold: &pressureThreshold},
	}
}

// Collect implements module.Module.
func (m *Module) Collect(_ context.Context, _ *module.Env) (any, error) {
	lines, err := m.fs.Lines("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	mi, err := parseMeminfo(lines)
	if err != nil {
		return nil, err
	}
	f := &Facts{
		TotalBytes:     mi["MemTotal"],
		AvailableBytes: mi["MemAvailable"],
		FreeBytes:      mi["MemFree"],
		BuffersBytes:   mi["Buffers"],
		CachedBytes:    mi["Cached"],
		SharedBytes:    mi["Shmem"],
		DirtyBytes:     mi["Dirty"],
		SwapTotalBytes: mi["SwapTotal"],
		SwapFreeBytes:  mi["SwapFree"],
		Meminfo:        mi,
	}
	if f.TotalBytes > 0 {
		f.AvailableRatio = float64(f.AvailableBytes) / float64(f.TotalBytes)
	}
	if f.SwapTotalBytes > 0 {
		f.SwapUsedRatio = float64(f.SwapTotalBytes-f.SwapFreeBytes) / float64(f.SwapTotalBytes)
	}

	var errs []error
	if lines, err := m.fs.Lines("/proc/vmstat"); err != nil {
		errs = append(errs, err)
	} else {
		f.OOMKills = vmstatValue(lines, "oom_kill")
	}
	if f.PSI, err = m.fs.ReadPSI("memory"); err != nil {
		errs = append(errs, err)
	}
	return f, errors.Join(errs...)
}

func parseMeminfo(lines []string) (map[string]uint64, error) {
	m := make(map[string]uint64, len(lines))
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("/proc/meminfo %s: %w", k, err)
		}
		if len(fields) > 1 && fields[1] == "kB" {
			n *= 1024
		}
		m[k] = n
	}
	if _, ok := m["MemTotal"]; !ok {
		return nil, errors.New("/proc/meminfo: MemTotal missing")
	}
	return m, nil
}

func vmstatValue(lines []string, key string) *uint64 {
	for _, l := range lines {
		k, v, ok := strings.Cut(l, " ")
		if ok && k == key {
			if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
				return &n
			}
		}
	}
	return nil
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok || f.TotalBytes == 0 {
		return nil
	}
	var out []model.Finding

	avail := model.Finding{
		ID:       "mem.available",
		Subject:  "memory",
		Severity: env.Threshold("mem.available", availableThreshold).Grade(f.AvailableRatio),
		Message:  fmt.Sprintf("%.1f%% of memory available", f.AvailableRatio*100),
		Evidence: map[string]any{
			"available_bytes": f.AvailableBytes,
			"total_bytes":     f.TotalBytes,
			"available_ratio": f.AvailableRatio,
		},
	}
	if avail.Severity >= model.SeverityWarn {
		avail.Hint = "check which processes or containers use the memory (ps, podman stats) and their limits"
	}
	out = append(out, avail)

	if f.SwapTotalBytes > 0 {
		swap := model.Finding{
			ID:       "mem.swap-used",
			Subject:  "swap",
			Severity: env.Threshold("mem.swap-used", swapThreshold).Grade(f.SwapUsedRatio),
			Message:  fmt.Sprintf("%.1f%% of swap used", f.SwapUsedRatio*100),
			Evidence: map[string]any{
				"swap_used_bytes":  f.SwapTotalBytes - f.SwapFreeBytes,
				"swap_total_bytes": f.SwapTotalBytes,
			},
		}
		if swap.Severity >= model.SeverityWarn {
			swap.Hint = "the machine has been short of memory: see mem.available and mem.pressure"
		}
		out = append(out, swap)
	}

	if f.OOMKills != nil {
		oom := model.Finding{
			ID:       "mem.oom-kills",
			Subject:  "oom killer",
			Severity: model.SeverityOK,
			Message:  "no process killed by the OOM killer since boot",
			Evidence: map[string]any{"oom_kills": *f.OOMKills},
		}
		if *f.OOMKills > 0 {
			oom.Severity = model.SeverityWarn
			oom.Message = fmt.Sprintf("%d processes killed by the OOM killer since boot", *f.OOMKills)
			oom.Hint = "journalctl -k | grep -i 'out of memory' shows which ones; cgroup limits (MemoryMax) count too"
		}
		out = append(out, oom)
	}

	if f.PSI != nil && f.PSI.Full != nil {
		p := f.PSI.Full.Avg300
		pr := model.Finding{
			ID:       "mem.pressure",
			Subject:  "memory",
			Severity: env.Threshold("mem.pressure", pressureThreshold).Grade(p),
			Message:  fmt.Sprintf("all tasks stalled on memory %.1f%% of the time (5 min)", p),
			Evidence: map[string]any{"psi_full_avg300": p, "psi_full_avg60": f.PSI.Full.Avg60},
		}
		if pr.Severity >= model.SeverityWarn {
			pr.Hint = "the machine is reclaiming or swapping: reduce memory use or add RAM"
		}
		out = append(out, pr)
	}
	return out
}
