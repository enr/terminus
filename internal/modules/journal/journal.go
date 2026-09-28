// Package journal is an optional module about the systemd journal: disk usage and log volume per
// unit. Counting the lines reads the whole window, so it is not a core module.
//
//	[modules.journal]
//	enabled = true
//	window = "24h"
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "journal"

// topUnits is how many units the facts list.
const topUnits = 20

// journalDirs hold the persistent and the volatile journals.
var journalDirs = []string{"/var/log/journal", "/run/log/journal"}

// Default thresholds.
var (
	diskThreshold  = module.Threshold{Warn: 0.10, Fail: 0.20, Unit: "ratio"}
	noisyThreshold = module.Threshold{Warn: 50000, Fail: 500000, Unit: "lines per day"}
)

// Facts about the journal.
type Facts struct {
	DiskUsageBytes      uint64  `json:"disk_usage_bytes"`
	Directory           string  `json:"directory"`
	FilesystemSizeBytes uint64  `json:"filesystem_size_bytes"`
	DiskUsageRatio      float64 `json:"disk_usage_ratio"`
	Window              string  `json:"window"`
	TotalLines          int     `json:"total_lines"`
	// Units are the units that logged the most in the window.
	Units []UnitLines `json:"units"`
}

// UnitLines is the log volume of a unit.
type UnitLines struct {
	// Name is "system:<unit>", "user:<name>:<unit>", or "other" for entries without a unit
	// (kernel, processes outside units).
	Name        string  `json:"name"`
	Lines       int     `json:"lines"`
	LinesPerDay float64 `json:"lines_per_day"`
}

// Module collects the journal facts.
type Module struct {
	fs     hostfs.FS
	window time.Duration
	statfs func(string) (unix.Statfs_t, error)
	lookup func(string) (runner.Account, error)
}

// New returns the journal module.
func New() *Module {
	return &Module{
		fs:     hostfs.Host,
		window: 24 * time.Hour,
		statfs: func(p string) (unix.Statfs_t, error) {
			var st unix.Statfs_t
			err := unix.Statfs(p, &st)
			return st, err
		},
		lookup: runner.LookupAccount,
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string { return "systemd journal: disk usage, log lines per unit" }

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Window for the lines per unit (the whole window is read).
window = "24h"`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Window string `toml:"window"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Window != "" {
		d, err := config.ParseDuration(c.Window)
		if err != nil || d <= 0 {
			return fmt.Errorf("modules.journal.window: invalid duration %q", c.Window)
		}
		m.window = d
	}
	return nil
}

// Detect implements module.Detector.
func (m *Module) Detect(_ context.Context, env *module.Env) module.Detection {
	if env == nil || env.Runner == nil {
		return module.Detection{Reason: "no command runner"}
	}
	if _, err := env.Runner.LookPath("journalctl"); err != nil {
		return module.Detection{Reason: "journalctl not found"}
	}
	for _, d := range journalDirs {
		if m.fs.Exists(d) {
			return module.Detection{Found: true, Reason: "journal in " + d, Config: "enabled = true\n" + m.ConfigExample()}
		}
	}
	return module.Detection{Reason: "no journal directory"}
}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "journal.disk-usage", Description: "journal files over the size of their filesystem", Threshold: &diskThreshold},
		{ID: "journal.noisy-unit", Description: "log lines per day of a unit", Threshold: &noisyThreshold},
	}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	if _, err := env.Runner.LookPath("journalctl"); err != nil {
		return nil, module.Skip("journalctl not found")
	}
	f := &Facts{Window: m.window.String(), Units: []UnitLines{}}
	var errs []error
	if err := m.diskUsage(f); err != nil {
		errs = append(errs, err)
	}
	if err := m.countLines(ctx, env.Runner, f); err != nil {
		errs = append(errs, err)
	}
	return f, errors.Join(errs...)
}

// diskUsage sums the journal files, as journalctl --disk-usage does, and compares them with the
// filesystem that holds the persistent journal.
func (m *Module) diskUsage(f *Facts) error {
	for _, d := range journalDirs {
		root := m.fs.Path(d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable directories (other users' journals) are left out
			}
			if !e.IsDir() && strings.Contains(e.Name(), ".journal") {
				if info, err := e.Info(); err == nil {
					f.DiskUsageBytes += uint64(info.Size())
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if f.Directory == "" && m.fs.Exists(d) {
			f.Directory = d
		}
	}
	if f.Directory == "" {
		return nil
	}
	st, err := m.statfs(m.fs.Path(f.Directory))
	if err != nil {
		return fmt.Errorf("statfs %s: %w", f.Directory, err)
	}
	bsize := uint64(st.Bsize)
	if st.Frsize > 0 {
		bsize = uint64(st.Frsize)
	}
	f.FilesystemSizeBytes = st.Blocks * bsize
	if f.FilesystemSizeBytes > 0 {
		f.DiskUsageRatio = float64(f.DiskUsageBytes) / float64(f.FilesystemSizeBytes)
	}
	return nil
}

type entry struct {
	Unit     string `json:"_SYSTEMD_UNIT"`
	UserUnit string `json:"_SYSTEMD_USER_UNIT"`
	UID      string `json:"_UID"`
}

// countLines reads the window once and counts the entries by unit.
func (m *Module) countLines(ctx context.Context, r runner.Runner, f *Facts) error {
	counts := map[string]int{}
	users := map[string]string{}
	userName := func(uid string) string {
		if n, ok := users[uid]; ok {
			return n
		}
		n := uid
		if a, err := m.lookup(uid); err == nil {
			n = a.Name
		}
		users[uid] = n
		return n
	}
	args := []string{
		"--no-pager", "-o", "json",
		fmt.Sprintf("--since=-%ds", int(m.window.Seconds())),
		"--output-fields=_SYSTEMD_UNIT,_SYSTEMD_USER_UNIT,_UID",
	}
	res, err := r.Stream(ctx, runner.Cmd{Name: "journalctl", Args: args, Timeout: runner.NoTimeout}, func(line []byte) error {
		var e entry
		if json.Unmarshal(line, &e) != nil {
			return nil
		}
		f.TotalLines++
		switch {
		case e.UserUnit != "":
			counts["user:"+userName(e.UID)+":"+e.UserUnit]++
		case e.Unit != "":
			counts["system:"+e.Unit]++
		default:
			counts["other"]++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	if res.ExitCode != 0 && len(strings.TrimSpace(string(res.Stderr))) > 0 {
		return fmt.Errorf("journalctl: exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	days := m.window.Hours() / 24
	for name, n := range counts {
		f.Units = append(f.Units, UnitLines{Name: name, Lines: n, LinesPerDay: float64(n) / days})
	}
	sort.Slice(f.Units, func(i, j int) bool {
		if f.Units[i].Lines != f.Units[j].Lines {
			return f.Units[i].Lines > f.Units[j].Lines
		}
		return f.Units[i].Name < f.Units[j].Name
	})
	if len(f.Units) > topUnits {
		f.Units = f.Units[:topUnits]
	}
	return nil
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	if f.FilesystemSizeBytes > 0 {
		d := model.Finding{
			ID:       "journal.disk-usage",
			Subject:  f.Directory,
			Severity: env.Threshold("journal.disk-usage", diskThreshold).Grade(f.DiskUsageRatio),
			Message:  fmt.Sprintf("the journal takes %.1f%% of its filesystem", f.DiskUsageRatio*100),
			Evidence: map[string]any{"disk_usage_bytes": f.DiskUsageBytes, "filesystem_size_bytes": f.FilesystemSizeBytes},
		}
		if d.Severity >= model.SeverityWarn {
			d.Hint = "cap it with SystemMaxUse= in journald.conf, or journalctl --vacuum-size="
		}
		out = append(out, d)
	}
	t := env.Threshold("journal.noisy-unit", noisyThreshold)
	noisy := false
	for _, u := range f.Units {
		if u.Name == "other" {
			continue
		}
		sev := t.Grade(u.LinesPerDay)
		if sev < model.SeverityWarn {
			continue
		}
		noisy = true
		out = append(out, model.Finding{
			ID:       "journal.noisy-unit",
			Subject:  u.Name,
			Severity: sev,
			Message:  fmt.Sprintf("%.0f lines per day", u.LinesPerDay),
			Evidence: map[string]any{"lines": u.Lines, "window": f.Window},
			Hint:     "verbose logging left on (SQL queries, debug)? it costs disk and hides the useful lines",
		})
	}
	if !noisy && len(f.Units) > 0 {
		top := f.Units[0]
		out = append(out, model.Finding{
			ID:       "journal.noisy-unit",
			Subject:  "units",
			Severity: model.SeverityOK,
			Message:  fmt.Sprintf("the busiest is %s with %.0f lines per day", top.Name, top.LinesPerDay),
		})
	}
	return out
}
