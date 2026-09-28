// Package backup is an optional module about backups made with restic, borg or pgBackRest: the
// latest backup of each configured repository, its age, and the result of the job that makes it.
//
//	[modules.backup]
//	enabled = true
//
//	[[modules.backup.repositories]]
//	name = "home"
//	type = "restic"
//	repository = "sftp:backup@nas:/srv/restic"
//	password_file = "/etc/restic/password"
//	unit = "restic-backup.service"
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/redact"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "backup"

// Repository types.
const (
	Restic     = "restic"
	Borg       = "borg"
	PgBackRest = "pgbackrest"
)

var tools = []string{Restic, Borg, PgBackRest}

// Facts about the backups.
type Facts struct {
	Repositories []Repository `json:"repositories"`
}

// Repository is a configured repository with its latest backup.
type Repository struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Repository string `json:"repository"`
	User       string `json:"user,omitempty"`
	// Latest is the most recent backup; nil when there is none or the repository was not read.
	Latest     *Backup `json:"latest,omitempty"`
	AgeSeconds float64 `json:"age_seconds,omitempty"`
	// MaxAgeSeconds is the max_age of the repository, when configured.
	MaxAgeSeconds float64 `json:"max_age_seconds,omitempty"`
	Error         string  `json:"error,omitempty"`
	// Job is the systemd unit that makes the backups, when configured.
	Job *Job `json:"job,omitempty"`
}

// Backup is a snapshot (restic), an archive (borg) or a backup (pgBackRest).
type Backup struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"host,omitempty"`
	Paths []string  `json:"paths,omitempty"`
	// Kind is the pgBackRest backup type: full, diff, incr.
	Kind string `json:"kind,omitempty"`
	// SizeBytes is the size of the data backed up, when the tool reports it.
	SizeBytes *uint64 `json:"size_bytes,omitempty"`
	// Failed is set by pgBackRest on backups that ended with an error.
	Failed bool `json:"failed,omitempty"`
}

// Job is the state of the systemd unit that runs the backup.
type Job struct {
	Unit       string `json:"unit"`
	Active     string `json:"active,omitempty"`
	Result     string `json:"result,omitempty"`
	ExitStatus int    `json:"exit_status"`
	// LastRun is when the unit last finished (InactiveEnterTimestamp).
	LastRun string `json:"last_run,omitempty"`
	Error   string `json:"error,omitempty"`
}

type repoConfig struct {
	Name         string `toml:"name"`
	Type         string `toml:"type"`
	Repository   string `toml:"repository"`
	PasswordFile string `toml:"password_file"`
	EnvFile      string `toml:"env_file"`
	Host         string `toml:"host"`
	User         string `toml:"user"`
	Unit         string `toml:"unit"`
	MaxAge       string `toml:"max_age"`

	maxAge time.Duration
}

// Module collects the backup facts.
type Module struct {
	fs    hostfs.FS
	repos []repoConfig
	now   func() time.Time
}

// New returns the backup module, without repositories until configured.
func New() *Module { return &Module{fs: hostfs.Host, now: time.Now} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "backups (restic, borg, pgBackRest): latest backup, age, result of the backup job"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# One table per repository; type is restic, borg or pgbackrest.
[[modules.backup.repositories]]
name = "home"
type = "restic"
repository = "/srv/backup/restic"   # restic/borg repository; pgbackrest: the stanza
password_file = ""                  # restic and borg
env_file = ""                       # KEY=value lines for the tool (S3/B2 credentials, BORG_RSH, ...)
host = ""                           # restic: only the snapshots of this host
user = ""                           # run the tool as this user (pgbackrest: postgres)
unit = ""                           # systemd unit that makes the backups
max_age = ""                        # default: the backup.age thresholds`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Repositories []repoConfig `toml:"repositories"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	var errs []error
	names := map[string]bool{}
	for i := range c.Repositories {
		r := &c.Repositories[i]
		where := fmt.Sprintf("modules.backup.repositories[%d]", i)
		switch r.Type {
		case Restic, Borg, PgBackRest:
		default:
			errs = append(errs, fmt.Errorf("%s: type must be one of %s, not %q", where, strings.Join(tools, ", "), r.Type))
		}
		if r.Repository == "" {
			errs = append(errs, fmt.Errorf("%s: repository missing", where))
		}
		if r.Name == "" {
			// The repository can embed credentials (restic's rest: backend, an S3 URL): never
			// default the name, which becomes the subject of every finding, to the raw value.
			r.Name = redact.URL(r.Repository)
		}
		if names[r.Name] {
			errs = append(errs, fmt.Errorf("%s: name %q used twice", where, r.Name))
		}
		names[r.Name] = true
		if r.Host != "" && r.Type != Restic {
			errs = append(errs, fmt.Errorf("%s: host applies only to restic", where))
		}
		if r.PasswordFile != "" && r.Type == PgBackRest {
			errs = append(errs, fmt.Errorf("%s: password_file does not apply to pgbackrest", where))
		}
		if r.MaxAge != "" {
			d, err := config.ParseDuration(r.MaxAge)
			if err != nil || d <= 0 {
				errs = append(errs, fmt.Errorf("%s: invalid max_age %q", where, r.MaxAge))
			}
			r.maxAge = d
		}
	}
	m.repos = c.Repositories
	return errors.Join(errs...)
}

// Detect implements module.Detector: the tools are found, the repositories must be configured.
func (m *Module) Detect(_ context.Context, env *module.Env) module.Detection {
	if env == nil || env.Runner == nil {
		return module.Detection{Reason: "no command runner"}
	}
	var found []string
	for _, t := range tools {
		if _, err := env.Runner.LookPath(t); err == nil {
			found = append(found, t)
		}
	}
	if len(found) == 0 {
		return module.Detection{Reason: "none of " + strings.Join(tools, ", ") + " is installed"}
	}
	return module.Detection{
		Found:  true,
		Reason: strings.Join(found, ", ") + " installed: list the repositories in the configuration",
		Config: "enabled = true\n\n" + strings.Replace(m.ConfigExample(), `type = "restic"`, fmt.Sprintf("type = %q", found[0]), 1),
	}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if len(m.repos) == 0 {
		return nil, module.Skip("no repositories configured ([[modules.backup.repositories]])")
	}
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	f := &Facts{Repositories: make([]Repository, len(m.repos))}
	var wg sync.WaitGroup
	for i, rc := range m.repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Repositories[i] = m.collect(ctx, env.Runner, rc)
		}()
	}
	wg.Wait()
	var errs []error
	for _, r := range f.Repositories {
		if r.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", r.Name, r.Error))
		}
	}
	return f, errors.Join(errs...)
}

func (m *Module) collect(ctx context.Context, r runner.Runner, rc repoConfig) Repository {
	// Repository is shown as-is in facts and evidence: never the raw configured value, which can
	// embed credentials (rc.Repository is still used below to actually reach the repository).
	repo := Repository{Name: rc.Name, Type: rc.Type, Repository: redact.URL(rc.Repository), User: rc.User, MaxAgeSeconds: rc.maxAge.Seconds()}
	if rc.Unit != "" {
		repo.Job = unitState(ctx, r, rc.Unit)
	}
	if _, err := r.LookPath(rc.Type); err != nil {
		repo.Error = rc.Type + " not found"
		return repo
	}
	cmd := runner.Cmd{Name: rc.Type, User: rc.User, Timeout: runner.NoTimeout}
	if rc.EnvFile != "" {
		lines, err := m.fs.Lines(rc.EnvFile)
		if err != nil {
			repo.Error = "env_file: " + err.Error()
			return repo
		}
		for i, l := range lines {
			lines[i] = strings.TrimPrefix(strings.TrimSpace(l), "export ")
		}
		for k, v := range hostfs.ParseKeyValue(lines) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		sort.Strings(cmd.Env)
	}
	var parse func([]byte) (*Backup, error)
	switch rc.Type {
	case Restic:
		cmd.Env = append(cmd.Env, "RESTIC_REPOSITORY="+rc.Repository)
		if rc.PasswordFile != "" {
			cmd.Env = append(cmd.Env, "RESTIC_PASSWORD_FILE="+rc.PasswordFile)
		}
		cmd.Args = []string{"snapshots", "--json", "--no-lock", "--latest", "1"}
		if rc.Host != "" {
			cmd.Args = append(cmd.Args, "--host", rc.Host)
		}
		parse = parseRestic
	case Borg:
		// No prompt may block the run: unknown or moved repositories are errors.
		cmd.Env = append(cmd.Env, "BORG_REPO="+rc.Repository,
			"BORG_RELOCATED_REPO_ACCESS_IS_OK=no", "BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=no")
		if rc.PasswordFile != "" {
			cmd.Env = append(cmd.Env, "BORG_PASSCOMMAND=cat "+rc.PasswordFile)
		}
		cmd.Args = []string{"list", "--json", "--last", "1", "--bypass-lock"}
		parse = parseBorg
	case PgBackRest:
		cmd.Args = []string{"info", "--output=json", "--stanza=" + rc.Repository}
		parse = parsePgBackRest
	}
	res, err := r.Run(ctx, cmd)
	switch {
	case err != nil:
		repo.Error = err.Error()
		return repo
	case res.ExitCode != 0:
		repo.Error = fmt.Sprintf("%s exited with code %d: %s", rc.Type, res.ExitCode, lastLine(res.Stderr))
		return repo
	}
	b, err := parse(res.Stdout)
	if err != nil {
		repo.Error = err.Error()
		return repo
	}
	repo.Latest = b
	if b != nil {
		repo.AgeSeconds = m.now().Sub(b.Time).Seconds()
	}
	return repo
}

// lastLine returns the last non-empty line of an error output: the tools print the error last.
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// parseRestic parses `restic snapshots --json --latest 1`: the latest snapshot of each host and
// set of paths.
func parseRestic(b []byte) (*Backup, error) {
	var snaps []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Paths    []string  `json:"paths"`
		Summary  *struct {
			TotalBytesProcessed uint64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(b, &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	var latest *Backup
	for _, s := range snaps {
		if latest != nil && !s.Time.After(latest.Time) {
			continue
		}
		latest = &Backup{ID: s.ShortID, Time: s.Time, Host: s.Hostname, Paths: s.Paths}
		if latest.ID == "" && len(s.ID) >= 8 {
			latest.ID = s.ID[:8]
		}
		if s.Summary != nil {
			n := s.Summary.TotalBytesProcessed
			latest.SizeBytes = &n
		}
	}
	return latest, nil
}

// parseBorg parses `borg list --json --last 1`. Borg prints local times without a zone.
func parseBorg(b []byte) (*Backup, error) {
	var out struct {
		Archives []struct {
			Name  string `json:"name"`
			Start string `json:"start"`
			Time  string `json:"time"`
		} `json:"archives"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("borg list: %w", err)
	}
	var latest *Backup
	for _, a := range out.Archives {
		ts := a.Start
		if ts == "" {
			ts = a.Time
		}
		t, err := time.ParseInLocation("2006-01-02T15:04:05.999999", ts, time.Local)
		if err != nil {
			return nil, fmt.Errorf("borg list: archive %s: %w", a.Name, err)
		}
		if latest == nil || t.After(latest.Time) {
			latest = &Backup{ID: a.Name, Time: t}
		}
	}
	return latest, nil
}

// parsePgBackRest parses `pgbackrest info --output=json --stanza=<stanza>`.
func parsePgBackRest(b []byte) (*Backup, error) {
	var stanzas []struct {
		Name   string `json:"name"`
		Status struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
		Backup []struct {
			Label     string `json:"label"`
			Type      string `json:"type"`
			Error     bool   `json:"error"`
			Timestamp struct {
				Start int64 `json:"start"`
				Stop  int64 `json:"stop"`
			} `json:"timestamp"`
			Info struct {
				Size uint64 `json:"size"`
			} `json:"info"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(b, &stanzas); err != nil {
		return nil, fmt.Errorf("pgbackrest info: %w", err)
	}
	if len(stanzas) == 0 {
		return nil, errors.New("pgbackrest info: stanza not found")
	}
	s := stanzas[0]
	var latest *Backup
	for _, bk := range s.Backup {
		t := time.Unix(bk.Timestamp.Stop, 0).UTC()
		if latest != nil && !t.After(latest.Time) {
			continue
		}
		size := bk.Info.Size
		latest = &Backup{ID: bk.Label, Time: t, Kind: bk.Type, SizeBytes: &size, Failed: bk.Error}
	}
	// Code 2 is "no valid backups": reported as no backup, not as an error.
	if s.Status.Code != 0 && s.Status.Code != 2 {
		return latest, fmt.Errorf("pgbackrest: stanza %s: %s (status %d)", s.Name, s.Status.Message, s.Status.Code)
	}
	return latest, nil
}

// unitState reads the state of the unit that makes the backups.
func unitState(ctx context.Context, r runner.Runner, unit string) *Job {
	j := &Job{Unit: unit}
	res, err := r.Run(ctx, runner.Cmd{Name: "systemctl", Args: []string{
		"show", "--property=LoadState,ActiveState,Result,ExecMainStatus,InactiveEnterTimestamp", "--", unit,
	}})
	switch {
	case err != nil:
		j.Error = err.Error()
		return j
	case res.ExitCode != 0:
		j.Error = "systemctl show: " + lastLine(res.Stderr)
		return j
	}
	p := hostfs.ParseKeyValue(hostfs.SplitLines(res.Stdout))
	if p["LoadState"] == "not-found" {
		j.Error = "unit not found"
		return j
	}
	j.Active, j.Result, j.LastRun = p["ActiveState"], p["Result"], p["InactiveEnterTimestamp"]
	j.ExitStatus, _ = strconv.Atoi(p["ExecMainStatus"])
	return j
}
