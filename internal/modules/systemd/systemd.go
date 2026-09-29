// Package systemd is the core module with the units of the system manager and of the user
// managers (systemctl --user): state, restarts, memory and task limits and peaks, OOM kills,
// lingering users.
//
//	[modules.systemd]
//	users = "auto"          # or [] or ["apps", "1001"]
//	units = ["*.service"]   # units shown in detail
//	history = "7d"          # window for OOM kills, exits and restarts in the journal
package systemd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// Name of the module.
const Name = "systemd"

const (
	defaultHistory = 7 * 24 * time.Hour
	commandTimeout = 20 * time.Second
)

// Facts about systemd.
type Facts struct {
	Version int `json:"version"`
	// CgroupV2 tells whether the unified hierarchy is mounted: per-unit resource files need it.
	CgroupV2 bool   `json:"cgroup_v2"`
	Users    []User `json:"users"`
	// Managers are the system manager ("system") and the user managers ("user:apps").
	Managers []Manager `json:"managers"`
	// HistoryWindow is the journal window of the unit histories.
	HistoryWindow string `json:"history_window"`
	HistoryError  string `json:"history_error,omitempty"`
	// MemTotalBytes is the RAM of the machine, to compare with the sum of the limits.
	MemTotalBytes uint64 `json:"mem_total_bytes"`
}

// User is a user whose manager is inspected.
type User struct {
	Name           string `json:"name"`
	UID            uint32 `json:"uid"`
	Linger         bool   `json:"linger"`
	ManagerRunning bool   `json:"manager_running"`
	// EnabledUnits counts the units enabled in ~/.config/systemd/user and the quadlet files: the
	// user expects services to run.
	EnabledUnits int `json:"enabled_units"`
}

// Manager is a systemd instance.
type Manager struct {
	Name    string   `json:"name"`
	User    string   `json:"user,omitempty"`
	Skipped string   `json:"skipped,omitempty"`
	Error   string   `json:"error,omitempty"`
	Failed  []string `json:"failed"`
	Units   []Unit   `json:"units"`
}

// Unit is a unit with its state and resources.
type Unit struct {
	Name           string `json:"name"`
	Load           string `json:"load"`
	Active         string `json:"active"`
	Sub            string `json:"sub"`
	Result         string `json:"result,omitempty"`
	Restarts       *int   `json:"restarts,omitempty"`
	MainExitCode   string `json:"main_exit_code,omitempty"`
	MainExitStatus int    `json:"main_exit_status"`
	ActiveSince    string `json:"active_since,omitempty"`

	MemoryCurrentBytes *uint64  `json:"memory_current_bytes,omitempty"`
	MemoryPeakBytes    *uint64  `json:"memory_peak_bytes,omitempty"`
	MemoryMaxBytes     *uint64  `json:"memory_max_bytes,omitempty"`
	MemoryHighBytes    *uint64  `json:"memory_high_bytes,omitempty"`
	TasksCurrent       *uint64  `json:"tasks_current,omitempty"`
	TasksMax           *uint64  `json:"tasks_max,omitempty"`
	CPUUsageSeconds    *float64 `json:"cpu_usage_seconds,omitempty"`
	// OOMKills counts the OOM kills in the current cgroup (memory.events): it restarts from zero
	// when the unit restarts, the journal history covers the earlier ones.
	OOMKills *uint64 `json:"oom_kills,omitempty"`

	ControlGroup string   `json:"control_group,omitempty"`
	FragmentPath string   `json:"fragment_path,omitempty"`
	SourcePath   string   `json:"source_path,omitempty"`
	History      *History `json:"history,omitempty"`
}

// Module collects the systemd facts.
type Module struct {
	fs      hostfs.FS
	users   users.Spec
	units   []string
	history time.Duration
	lookup  func(string) (runner.Account, error)
	euid    func() int
}

// New returns the systemd module reading the running machine.
func New() *Module {
	return &Module{
		fs:      hostfs.Host,
		users:   users.AutoSpec,
		units:   []string{"*.service"},
		history: defaultHistory,
		lookup:  runner.LookupAccount,
		euid:    os.Geteuid,
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "units of the system and user managers: state, restarts, memory limits and peaks, OOM kills, linger"
}

// Tables implements module.Tabular.
func (*Module) Tables() map[string]module.Table {
	return map[string]module.Table{
		"managers.units": {Columns: []string{"name", "active", "sub", "result", "restarts", "memory=memory_current_bytes", "peak=memory_peak_bytes", "max=memory_max_bytes", "since=active_since"}},
	}
}

// Core implements module.Module.
func (*Module) Core() bool { return true }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Users whose "systemctl --user" manager is inspected: "auto" (users with linger or a
# running manager), [] (system manager only) or names/UIDs such as ["apps"].
users = "auto"
# Units shown in detail (glob patterns).
units = ["*.service"]
# Journal window for OOM kills, exits and restarts.
history = "7d"`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Users   *users.Spec `toml:"users"`
		Units   []string    `toml:"units"`
		History string      `toml:"history"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Users != nil {
		if err := m.setUsers(*c.Users); err != nil {
			return fmt.Errorf("modules.systemd.users: %w", err)
		}
	}
	if c.Units != nil {
		for _, p := range c.Units {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("modules.systemd.units: invalid pattern %q", p)
			}
		}
		m.units = c.Units
	}
	if c.History != "" {
		d, err := config.ParseDuration(c.History)
		if err != nil {
			return fmt.Errorf("modules.systemd.history: %w", err)
		}
		m.history = d
	}
	return nil
}

// SetUsers overrides the configured users (--users flag).
func (m *Module) SetUsers(names []string) error {
	return m.setUsers(users.Spec{Names: names})
}

func (m *Module) setUsers(s users.Spec) error {
	if err := m.resolver().Validate(s); err != nil {
		return err
	}
	m.users = s
	return nil
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if !m.fs.Exists("/run/systemd/system") {
		return nil, module.Skip("systemd is not the init system")
	}
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	r := env.Runner
	f := &Facts{CgroupV2: m.fs.Exists("/sys/fs/cgroup/cgroup.controllers"), HistoryWindow: m.history.String()}
	var errs []error
	if res, err := r.Run(ctx, runner.Cmd{Name: "systemctl", Args: []string{"--version"}}); err == nil {
		f.Version = parseVersion(string(res.Stdout))
	} else {
		errs = append(errs, err)
	}
	if lines, err := m.fs.Lines("/proc/meminfo"); err == nil && len(lines) > 0 {
		f.MemTotalBytes = memTotal(lines)
	}

	selected, err := m.selectUsers()
	if err != nil {
		errs = append(errs, err)
	}
	f.Users = selected

	managers := []Manager{{Name: "system"}}
	for _, u := range selected {
		mg := Manager{Name: "user:" + u.Name, User: u.Name}
		switch {
		case !u.ManagerRunning:
			mg.Skipped = "user manager not running (no linger and no session)"
		case uint32(m.euid()) != u.UID && m.euid() != 0:
			mg.Skipped = "requires root"
		}
		managers = append(managers, mg)
	}

	var wg sync.WaitGroup
	for i := range managers {
		if managers[i].Skipped != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.collectManager(ctx, r, &managers[i], f.CgroupV2)
		}()
	}
	wg.Wait()
	f.Managers = managers

	if err := m.attachHistory(ctx, r, f); err != nil {
		f.HistoryError = err.Error()
	}
	return f, errors.Join(errs...)
}

// collectManager lists the units of a manager and reads the details of the interesting ones.
func (m *Module) collectManager(ctx context.Context, r runner.Runner, mg *Manager, cgroupV2 bool) {
	mg.Failed, mg.Units = []string{}, []Unit{}
	base := runner.Cmd{Name: "systemctl", User: mg.User, Timeout: commandTimeout}
	scope := []string{}
	if mg.User != "" {
		scope = []string{"--user"}
	}

	list := base
	list.Args = append(append([]string{}, scope...), "list-units", "--all", "--plain", "--no-legend", "--no-pager", "--full")
	res, err := r.Run(ctx, list)
	if err != nil || res.ExitCode != 0 {
		mg.Error = commandError("list-units", res, err)
		return
	}
	var detail []string
	for _, u := range parseListUnits(hostfs.SplitLines(res.Stdout)) {
		if u.Active == "failed" {
			mg.Failed = append(mg.Failed, u.Name)
		}
		// Failed units always; the others when they match the patterns and are not stopped.
		if u.Active == "failed" || (u.Load == "loaded" && u.Active != "inactive" && m.wanted(u.Name)) {
			detail = append(detail, u.Name)
		}
	}
	sort.Strings(mg.Failed)
	if len(detail) == 0 {
		return
	}

	show := base
	show.Args = append(append(append([]string{}, scope...), "show", "--no-pager", "-p", strings.Join(showProperties, ",")), detail...)
	res, err = r.Run(ctx, show)
	if err != nil || res.ExitCode != 0 {
		mg.Error = commandError("show", res, err)
		return
	}
	for _, p := range parseShow(hostfs.SplitLines(res.Stdout)) {
		u := unitFromShow(p)
		if cgroupV2 && u.ControlGroup != "" {
			readCgroup(m.fs, &u)
		}
		mg.Units = append(mg.Units, u)
	}
	sort.Slice(mg.Units, func(i, j int) bool { return mg.Units[i].Name < mg.Units[j].Name })
}

// wanted tells whether a unit matches the configured patterns.
func (m *Module) wanted(name string) bool {
	for _, p := range m.units {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func commandError(what string, res runner.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("systemctl %s: %v", what, err)
	}
	return fmt.Sprintf("systemctl %s: exit code %d: %s", what, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
}

func memTotal(lines []string) uint64 {
	for _, l := range lines {
		var kb uint64
		if n, _ := fmt.Sscanf(l, "MemTotal: %d kB", &kb); n == 1 {
			return kb * 1024
		}
	}
	return 0
}
