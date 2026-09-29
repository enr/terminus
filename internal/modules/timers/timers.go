// Package timers is an optional module about the systemd timers of the system and user managers:
// schedule, last and next run, and the result of the last run of the unit they trigger.
//
//	[modules.timers]
//	enabled = true
//	users = "auto"
//	timers = ["*.timer"]
package timers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// Name of the module.
const Name = "timers"

const commandTimeout = 20 * time.Second

var (
	timerProperties   = []string{"Id", "LoadState", "ActiveState", "UnitFileState", "Unit", "TimersCalendar", "TimersMonotonic", "Persistent", "LastTriggerUSec", "NextElapseUSecRealtime", "ActiveEnterTimestamp"}
	serviceProperties = []string{"Id", "LoadState", "ActiveState", "Result", "ExecMainStatus", "InactiveEnterTimestamp"}
)

// Facts about the timers.
type Facts struct {
	// Managers are the system manager ("system") and the user managers ("user:apps").
	Managers []Manager `json:"managers"`
}

// Manager is a systemd instance with its timers.
type Manager struct {
	Name    string  `json:"name"`
	User    string  `json:"user,omitempty"`
	Skipped string  `json:"skipped,omitempty"`
	Error   string  `json:"error,omitempty"`
	Timers  []Timer `json:"timers"`
}

// Timer is a timer unit and the unit it triggers.
type Timer struct {
	Name          string `json:"name"`
	Active        string `json:"active"`
	UnitFileState string `json:"unit_file_state,omitempty"`
	// Schedule lists the triggers: "OnCalendar=*-*-* 03:00:00", "OnUnitActiveSec=1d".
	Schedule   []string  `json:"schedule,omitempty"`
	Persistent bool      `json:"persistent"`
	LastRun    time.Time `json:"last_run,omitzero"`
	NextRun    time.Time `json:"next_run,omitzero"`
	// ActiveSince is when the timer was started.
	ActiveSince time.Time `json:"active_since,omitzero"`
	Unit        Triggered `json:"unit"`
}

// Triggered is the unit a timer starts.
type Triggered struct {
	Name       string `json:"name"`
	Load       string `json:"load,omitempty"`
	Active     string `json:"active,omitempty"`
	Result     string `json:"result,omitempty"`
	ExitStatus int    `json:"exit_status"`
	// LastExit is when the unit last stopped.
	LastExit time.Time `json:"last_exit,omitzero"`
}

// Module collects the timers.
type Module struct {
	fs     hostfs.FS
	users  users.Spec
	timers []string
	lookup func(string) (runner.Account, error)
	euid   func() int
	now    func() time.Time
}

// New returns the timers module.
func New() *Module {
	return &Module{
		fs:     hostfs.Host,
		users:  users.AutoSpec,
		timers: []string{"*.timer"},
		lookup: runner.LookupAccount,
		euid:   os.Geteuid,
		now:    time.Now,
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "systemd timers of the system and user managers: schedule, last and next run, result"
}

// Tables implements module.Tabular.
func (*Module) Tables() map[string]module.Table {
	return map[string]module.Table{
		"managers.timers": {Columns: []string{"name", "active", "schedule", "last_run", "next_run", "unit=unit.name", "result=unit.result"}},
	}
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Users whose timers are inspected, as in [modules.systemd].
users = "auto"
# Timers inspected (glob patterns).
timers = ["*.timer"]`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Users  *users.Spec `toml:"users"`
		Timers []string    `toml:"timers"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Users != nil {
		if err := m.setUsers(*c.Users); err != nil {
			return fmt.Errorf("modules.timers.users: %w", err)
		}
	}
	if c.Timers != nil {
		for _, p := range c.Timers {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("modules.timers.timers: invalid pattern %q", p)
			}
		}
		m.timers = c.Timers
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

func (m *Module) resolver() users.Resolver {
	return users.Resolver{FS: m.fs, Lookup: m.lookup, Euid: m.euid}
}

// Detect implements module.Detector.
func (m *Module) Detect(context.Context, *module.Env) module.Detection {
	if !m.fs.Exists("/run/systemd/system") {
		return module.Detection{Reason: "systemd is not the init system"}
	}
	return module.Detection{Found: true, Reason: "systemd is the init system", Config: "enabled = true"}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if !m.fs.Exists("/run/systemd/system") {
		return nil, module.Skip("systemd is not the init system")
	}
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	r := m.resolver()
	accounts, err := r.Select(m.users)
	managers := []Manager{{Name: "system"}}
	for _, a := range accounts {
		mg := Manager{Name: "user:" + a.Name, User: a.Name}
		switch {
		case !r.ManagerRunning(a):
			mg.Skipped = "user manager not running (no linger and no session)"
		case !r.CanRunAs(a):
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
			m.collectManager(ctx, env.Runner, &managers[i])
		}()
	}
	wg.Wait()
	errs := []error{err}
	for _, mg := range managers {
		if mg.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", mg.Name, mg.Error))
		}
	}
	return &Facts{Managers: managers}, errors.Join(errs...)
}

func (m *Module) collectManager(ctx context.Context, r runner.Runner, mg *Manager) {
	mg.Timers = []Timer{}
	base := runner.Cmd{Name: "systemctl", User: mg.User, Timeout: commandTimeout}
	var scope []string
	if mg.User != "" {
		scope = []string{"--user"}
	}
	run := func(args ...string) ([]string, error) {
		c := base
		c.Args = append(append([]string{}, scope...), args...)
		res, err := r.Run(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("systemctl %s: %w", args[0], err)
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("systemctl %s: exit code %d: %s", args[0], res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
		return hostfs.SplitLines(res.Stdout), nil
	}

	lines, err := run("list-units", "--type=timer", "--all", "--plain", "--no-legend", "--no-pager", "--full")
	if err != nil {
		mg.Error = err.Error()
		return
	}
	var names []string
	for _, l := range lines {
		f := strings.Fields(strings.TrimLeft(l, "●* "))
		if len(f) >= 2 && f[1] != "not-found" && m.wanted(f[0]) {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return
	}
	lines, err = run(append([]string{"show", "--no-pager", "-p", strings.Join(timerProperties, ",")}, names...)...)
	if err != nil {
		mg.Error = err.Error()
		return
	}
	var units []string
	for _, p := range parseShow(lines) {
		t := Timer{
			Name:          p["Id"],
			Active:        p["ActiveState"],
			UnitFileState: p["UnitFileState"],
			Persistent:    p["Persistent"] == "yes",
			LastRun:       parseTimestamp(p["LastTriggerUSec"]),
			NextRun:       parseTimestamp(p["NextElapseUSecRealtime"]),
			ActiveSince:   parseTimestamp(p["ActiveEnterTimestamp"]),
			Unit:          Triggered{Name: p["Unit"]},
		}
		t.Schedule = append(parseTriggers(p["TimersCalendar"]), parseTriggers(p["TimersMonotonic"])...)
		mg.Timers = append(mg.Timers, t)
		if t.Unit.Name != "" {
			units = append(units, t.Unit.Name)
		}
	}
	sort.Slice(mg.Timers, func(i, j int) bool { return mg.Timers[i].Name < mg.Timers[j].Name })
	if len(units) == 0 {
		return
	}
	lines, err = run(append([]string{"show", "--no-pager", "-p", strings.Join(serviceProperties, ",")}, units...)...)
	if err != nil {
		mg.Error = err.Error()
		return
	}
	byName := map[string]Triggered{}
	for _, p := range parseShow(lines) {
		u := Triggered{
			Name:     p["Id"],
			Load:     p["LoadState"],
			Active:   p["ActiveState"],
			Result:   p["Result"],
			LastExit: parseTimestamp(p["InactiveEnterTimestamp"]),
		}
		u.ExitStatus, _ = strconv.Atoi(p["ExecMainStatus"])
		byName[u.Name] = u
	}
	for i, t := range mg.Timers {
		if u, ok := byName[t.Unit.Name]; ok {
			mg.Timers[i].Unit = u
		}
	}
}

func (m *Module) wanted(name string) bool {
	for _, p := range m.timers {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// parseShow parses `systemctl show` output for several units: blocks of Key=Value separated by
// blank lines.
func parseShow(lines []string) []map[string]string {
	var out []map[string]string
	cur := map[string]string{}
	for _, l := range append(lines, "") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = map[string]string{}
			}
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			cur[k] = v
		}
	}
	return out
}

// trigger matches an entry of TimersCalendar/TimersMonotonic:
// "{ OnCalendar=*-*-* 03:00:00 ; next_elapse=... }", "{ OnUnitActiveUSec=1d ; ... }".
var trigger = regexp.MustCompile(`\{ (\w+)=([^;]*?) ;`)

func parseTriggers(v string) []string {
	var out []string
	for _, g := range trigger.FindAllStringSubmatch(v, -1) {
		out = append(out, strings.Replace(g[1], "USec", "Sec", 1)+"="+g[2])
	}
	return out
}

// parseTimestamp parses a systemctl show timestamp: "Mon 2026-09-28 03:00:12 CEST" in the local
// time zone of the machine (the abbreviation resolves against it). Empty and "n/a" are zero.
func parseTimestamp(v string) time.Time {
	if v == "" || v == "n/a" || v == "0" {
		return time.Time{}
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}
