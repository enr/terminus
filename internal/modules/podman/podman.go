// Package podman is an optional module with the containers of every selected user (rootless)
// and of root (rootful): state, health, restarts, published ports, mounts, resource use,
// volumes with their real owner, networks and storage.
//
//	[modules.podman]
//	enabled = true
//	users = "auto"        # shared semantics with [modules.systemd]
//	rootful = true
//	volume_sizes = true
package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// Name of the module.
const Name = "podman"

const commandTimeout = 20 * time.Second

// Facts about podman.
type Facts struct {
	Scopes []Scope `json:"scopes"`
}

// Scope holds the podman objects of root (rootful) or of a user (rootless).
type Scope struct {
	Name       string         `json:"name"`
	Rootless   bool           `json:"rootless"`
	Skipped    string         `json:"skipped,omitempty"`
	Errors     []string       `json:"errors,omitempty"`
	Version    string         `json:"version,omitempty"`
	Containers []Container    `json:"containers"`
	Volumes    []Volume       `json:"volumes"`
	Networks   []Network      `json:"networks"`
	Storage    []StorageUsage `json:"storage,omitempty"`
}

// Container is a container with its state and configuration.
type Container struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	ImageDigest   string `json:"image_digest,omitempty"`
	State         string `json:"state"`
	ExitCode      int    `json:"exit_code"`
	OOMKilled     bool   `json:"oom_killed"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	Pod           string `json:"pod,omitempty"`
	SystemdUnit   string `json:"systemd_unit,omitempty"`
	RestartPolicy string `json:"restart_policy,omitempty"`
	Restarts      int    `json:"restarts"`
	// Health is healthy, unhealthy, starting, or empty without a healthcheck.
	Health              string   `json:"health,omitempty"`
	HasHealthcheck      bool     `json:"has_healthcheck"`
	HealthFailingStreak int      `json:"health_failing_streak,omitempty"`
	HealthLastOutput    string   `json:"health_last_output,omitempty"`
	Ports               []string `json:"ports,omitempty"`
	Networks            []string `json:"networks,omitempty"`
	Mounts              []Mount  `json:"mounts,omitempty"`
	Stats               *Stats   `json:"stats,omitempty"`
}

// Mount is a volume or bind mount of a container.
type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadWrite   bool   `json:"read_write"`
}

// Stats are the resources used by a running container (podman stats).
type Stats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsageBytes uint64  `json:"mem_usage_bytes"`
	MemLimitBytes uint64  `json:"mem_limit_bytes"`
	PIDs          int     `json:"pids"`
}

// Volume is a named volume.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels,omitempty"`
	// UsedBy lists the containers that mount the volume.
	UsedBy []string `json:"used_by"`
	// OwnerUID/GID are the owner of the mountpoint on the host; ContainerUID/GID the same owner as
	// seen inside a rootless container (user namespace, /etc/subuid), -1 when not mapped.
	OwnerUID     *uint32 `json:"owner_uid,omitempty"`
	OwnerGID     *uint32 `json:"owner_gid,omitempty"`
	ContainerUID *int64  `json:"container_uid,omitempty"`
	ContainerGID *int64  `json:"container_gid,omitempty"`
	SizeBytes    *uint64 `json:"size_bytes,omitempty"`
	Files        *uint64 `json:"files,omitempty"`
	// SizePartial is true when the size walk hit its time budget.
	SizePartial bool `json:"size_partial,omitempty"`
}

// Network is a podman network.
type Network struct {
	Name     string   `json:"name"`
	Driver   string   `json:"driver"`
	Subnets  []string `json:"subnets,omitempty"`
	Internal bool     `json:"internal"`
	DNS      bool     `json:"dns_enabled"`
}

// StorageUsage is a line of podman system df.
type StorageUsage struct {
	Type             string `json:"type"`
	Total            int    `json:"total"`
	Active           int    `json:"active"`
	SizeBytes        uint64 `json:"size_bytes"`
	ReclaimableBytes uint64 `json:"reclaimable_bytes"`
}

// Module collects the podman facts.
type Module struct {
	fs          hostfs.FS
	users       users.Spec
	rootful     bool
	volumeSizes bool
	sizeBudget  time.Duration
	resolver    users.Resolver
}

// New returns the podman module.
func New() *Module {
	return &Module{
		fs:          hostfs.Host,
		users:       users.AutoSpec,
		rootful:     true,
		volumeSizes: true,
		sizeBudget:  5 * time.Second,
		resolver:    users.Host(),
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "podman containers of root and of the users: state, health, restarts, ports, volumes and their owner"
}

// Tables implements module.Tabular.
func (*Module) Tables() map[string]module.Table {
	return map[string]module.Table{
		"scopes.containers": {Columns: []string{"name", "state", "health", "restarts", "memory=stats.mem_usage_bytes", "cpu=stats.cpu_percent", "image", "ports"}},
		"scopes.volumes":    {Columns: []string{"name", "driver", "size=size_bytes", "owner=owner_uid", "used_by"}},
	}
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Users whose rootless containers are inspected: "auto", [] or ["apps"] (as in [modules.systemd]).
users = "auto"
# Inspect the rootful containers too (requires root).
rootful = true
# Measure size and files of each volume (bounded to a few seconds).
volume_sizes = true`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Users       *users.Spec `toml:"users"`
		Rootful     *bool       `toml:"rootful"`
		VolumeSizes *bool       `toml:"volume_sizes"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Users != nil {
		if err := m.resolver.Validate(*c.Users); err != nil {
			return fmt.Errorf("modules.podman.users: %w", err)
		}
		m.users = *c.Users
	}
	if c.Rootful != nil {
		m.rootful = *c.Rootful
	}
	if c.VolumeSizes != nil {
		m.volumeSizes = *c.VolumeSizes
	}
	return nil
}

// SetUsers overrides the configured users (--users flag).
func (m *Module) SetUsers(names []string) error {
	s := users.Spec{Names: names}
	if err := m.resolver.Validate(s); err != nil {
		return err
	}
	m.users = s
	return nil
}

// Detect implements module.Detector.
func (m *Module) Detect(_ context.Context, env *module.Env) module.Detection {
	if env == nil || env.Runner == nil {
		return module.Detection{Reason: "no command runner"}
	}
	p, err := env.Runner.LookPath("podman")
	if err != nil {
		return module.Detection{Reason: "podman not found"}
	}
	return module.Detection{Found: true, Reason: "podman at " + p, Config: "enabled = true"}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	if _, err := env.Runner.LookPath("podman"); err != nil {
		return nil, module.Skip("podman not found")
	}
	scopes, err := m.resolver.Scopes(m.users, m.rootful)
	f := &Facts{Scopes: make([]Scope, len(scopes))}
	var wg sync.WaitGroup
	for i, sc := range scopes {
		f.Scopes[i] = Scope{Name: sc.Name, Rootless: sc.Rootless, Skipped: sc.Skipped, Containers: []Container{}, Volumes: []Volume{}, Networks: []Network{}}
		if sc.Skipped != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			idm := users.Identity()
			if sc.Rootless {
				idm = users.NewIDMap(m.fs, sc.Account)
			}
			m.collectScope(ctx, env.Runner, sc, &f.Scopes[i], idm)
		}()
	}
	wg.Wait()
	return f, err
}

func (m *Module) collectScope(ctx context.Context, r runner.Runner, sc users.Scope, out *Scope, idm users.IDMap) {
	run := func(args ...string) ([]byte, error) {
		res, err := r.Run(ctx, runner.Cmd{Name: "podman", Args: args, User: sc.User, Timeout: commandTimeout})
		if err != nil {
			return nil, fmt.Errorf("podman %s: %w", args[0], err)
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("podman %s: exit code %d: %s", strings.Join(args[:min(2, len(args))], " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
		return res.Stdout, nil
	}
	fail := func(err error) { out.Errors = append(out.Errors, err.Error()) }

	if b, err := run("version", "--format", "json"); err != nil {
		fail(err)
		return // podman does not work for this scope: nothing else will
	} else {
		var v struct{ Client struct{ Version string } }
		if json.Unmarshal(b, &v) == nil {
			out.Version = v.Client.Version
		}
	}

	var ids []string
	if b, err := run("ps", "-a", "--format", "json"); err != nil {
		fail(err)
	} else if ids, err = containerIDs(b); err != nil {
		fail(err)
	}
	if len(ids) > 0 {
		if b, err := run(append([]string{"inspect", "--type", "container"}, ids...)...); err != nil {
			fail(err)
		} else if out.Containers, err = parseInspect(b); err != nil {
			fail(err)
		}
	}
	running := 0
	for _, c := range out.Containers {
		if c.State == "running" {
			running++
		}
	}
	if running > 0 {
		if b, err := run("stats", "--no-stream", "--format", "json"); err != nil {
			fail(err) // rootless on cgroup v1 has no stats
		} else if err := attachStats(b, out.Containers); err != nil {
			fail(err)
		}
	}

	if b, err := run("volume", "ls", "--format", "json"); err != nil {
		fail(err)
	} else if out.Volumes, err = parseVolumes(b); err != nil {
		fail(err)
	}
	usedBy(out)
	deadline := time.Now().Add(m.sizeBudget)
	for i := range out.Volumes {
		m.volumeOwner(&out.Volumes[i], idm)
		if m.volumeSizes {
			volumeSize(m.fs, &out.Volumes[i], deadline)
		}
	}

	if b, err := run("network", "ls", "--format", "json"); err != nil {
		fail(err)
	} else if out.Networks, err = parseNetworks(b); err != nil {
		fail(err)
	}
	if b, err := run("system", "df", "--format", "json"); err != nil {
		fail(err)
	} else if out.Storage, err = parseDF(b); err != nil {
		fail(err)
	}
}

// usedBy records which containers mount each volume.
func usedBy(s *Scope) {
	idx := map[string]int{}
	for i, v := range s.Volumes {
		idx[v.Name] = i
		s.Volumes[i].UsedBy = []string{}
	}
	for _, c := range s.Containers {
		for _, mt := range c.Mounts {
			if i, ok := idx[mt.Name]; ok && mt.Type == "volume" {
				s.Volumes[i].UsedBy = append(s.Volumes[i].UsedBy, c.Name)
			}
		}
	}
	for i := range s.Volumes {
		sort.Strings(s.Volumes[i].UsedBy)
	}
}
