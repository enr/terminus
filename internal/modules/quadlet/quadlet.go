// Package quadlet is an optional module about the quadlet files of root and of the users: what
// they declare, what the quadlet generator of the machine really produces (dry run with the
// installed binary), the state of the generated units and the volumes podman really uses.
//
//	[modules.quadlet]
//	enabled = true
//	users = "auto"
//	rootful = true
//	binary = ""        # default: /usr/libexec/podman/quadlet or /usr/lib/podman/quadlet
package quadlet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
const Name = "quadlet"

const commandTimeout = 30 * time.Second

// binaries are the usual locations of the quadlet generator.
var binaries = []string{"/usr/libexec/podman/quadlet", "/usr/lib/podman/quadlet"}

// kinds are the quadlet file extensions and the suffix of the service they generate.
var kinds = map[string]string{
	".container": "",
	".volume":    "-volume",
	".network":   "-network",
	".pod":       "-pod",
	".kube":      "",
	".image":     "-image",
	".build":     "-build",
}

// Facts about quadlet.
type Facts struct {
	Binary string  `json:"binary,omitempty"`
	Scopes []Scope `json:"scopes"`
}

// Scope holds the quadlet files of root or of a user.
type Scope struct {
	Name     string   `json:"name"`
	Rootless bool     `json:"rootless"`
	Skipped  string   `json:"skipped,omitempty"`
	Dirs     []string `json:"dirs"`
	Files    []File   `json:"files"`
	// DryRun tells whether the dry run ran and succeeded; Diagnostics are its messages that do not
	// belong to a file.
	DryRun      string   `json:"dry_run"` // ok, failed, not-run
	Diagnostics []string `json:"diagnostics,omitempty"`
	Errors      []string `json:"errors,omitempty"`
	// Volumes are the volumes podman really has, by name.
	Volumes map[string]VolumeState `json:"volumes,omitempty"`
}

// VolumeState is a podman volume as it exists.
type VolumeState struct {
	Mountpoint   string `json:"mountpoint"`
	OwnerUID     *int64 `json:"owner_uid_in_container,omitempty"`
	OwnerGID     *int64 `json:"owner_gid_in_container,omitempty"`
	HostOwnerUID uint32 `json:"owner_uid"`
}

// File is a quadlet file.
type File struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	ModTime time.Time `json:"mod_time"`
	// Service is the unit the file generates.
	Service     string   `json:"service"`
	Diagnostics []string `json:"diagnostics,omitempty"`

	// .container
	Image    string   `json:"image,omitempty"`
	Volumes  []string `json:"volumes,omitempty"`
	Networks []string `json:"networks,omitempty"`
	// .volume / .network: the name podman gives to the object.
	ObjectName string `json:"object_name,omitempty"`
	User       string `json:"user,omitempty"`
	Group      string `json:"group,omitempty"`
	Device     string `json:"device,omitempty"`

	Generated *Generated `json:"generated,omitempty"`
	State     *UnitState `json:"state,omitempty"`
}

// UnitState is the state of the generated unit in systemd.
type UnitState struct {
	Load        string    `json:"load"`
	Active      string    `json:"active"`
	Sub         string    `json:"sub"`
	ActiveSince time.Time `json:"active_since,omitzero"`
}

// Module collects the quadlet facts.
type Module struct {
	fs       hostfs.FS
	users    users.Spec
	rootful  bool
	binary   string
	resolver users.Resolver
}

// New returns the quadlet module.
func New() *Module {
	return &Module{fs: hostfs.Host, users: users.AutoSpec, rootful: true, resolver: users.Host()}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "quadlet files: what the generator really produces, generated unit states, volumes and networks really used"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Users whose quadlet files are inspected: "auto", [] or ["apps"] (as in [modules.systemd]).
users = "auto"
# Inspect the rootful quadlet files too.
rootful = true
# Quadlet generator; empty: /usr/libexec/podman/quadlet or /usr/lib/podman/quadlet.
binary = ""`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Users   *users.Spec `toml:"users"`
		Rootful *bool       `toml:"rootful"`
		Binary  string      `toml:"binary"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Users != nil {
		if err := m.resolver.Validate(*c.Users); err != nil {
			return fmt.Errorf("modules.quadlet.users: %w", err)
		}
		m.users = *c.Users
	}
	if c.Rootful != nil {
		m.rootful = *c.Rootful
	}
	m.binary = c.Binary
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

func (m *Module) findBinary() string {
	if m.binary != "" {
		return m.binary
	}
	for _, b := range binaries {
		if m.fs.Exists(b) {
			return b
		}
	}
	return ""
}

// Detect implements module.Detector.
func (m *Module) Detect(context.Context, *module.Env) module.Detection {
	bin := m.findBinary()
	if bin == "" {
		return module.Detection{Reason: "quadlet generator not found"}
	}
	scopes, _ := m.resolver.Scopes(m.users, m.rootful)
	n := 0
	for _, sc := range scopes {
		for _, d := range dirsOf(sc) {
			files, _ := m.listFiles(d)
			n += len(files)
		}
	}
	if n == 0 {
		return module.Detection{Reason: bin + " found, but no quadlet file"}
	}
	return module.Detection{Found: true, Reason: fmt.Sprintf("%d quadlet files", n), Config: "enabled = true"}
}

// dirsOf returns the quadlet directories of a scope, as the generator searches them.
func dirsOf(sc users.Scope) []string {
	if !sc.Rootless {
		return []string{"/etc/containers/systemd", "/usr/share/containers/systemd"}
	}
	dirs := []string{}
	if sc.Account.Home != "" && sc.Account.Home != "/" {
		dirs = append(dirs, sc.Account.Home+"/.config/containers/systemd")
	}
	return append(dirs,
		fmt.Sprintf("/etc/containers/systemd/users/%d", sc.Account.UID),
		"/etc/containers/systemd/users",
	)
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	bin := m.findBinary()
	if bin == "" {
		return nil, module.Skip("quadlet generator not found (podman < 4.4?)")
	}
	scopes, err := m.resolver.Scopes(m.users, m.rootful)
	f := &Facts{Binary: bin, Scopes: make([]Scope, len(scopes))}
	var wg sync.WaitGroup
	for i, sc := range scopes {
		f.Scopes[i] = Scope{Name: sc.Name, Rootless: sc.Rootless, Skipped: sc.Skipped, Files: []File{}, DryRun: "not-run"}
		if sc.Skipped != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.collectScope(ctx, env.Runner, bin, sc, &f.Scopes[i])
		}()
	}
	wg.Wait()
	return f, err
}

func (m *Module) collectScope(ctx context.Context, r runner.Runner, bin string, sc users.Scope, out *Scope) {
	fail := func(err error) { out.Errors = append(out.Errors, err.Error()) }
	out.Dirs = []string{}
	for _, d := range dirsOf(sc) {
		files, err := m.listFiles(d)
		if err != nil {
			fail(err)
			continue
		}
		if len(files) > 0 {
			out.Dirs = append(out.Dirs, d)
		}
		out.Files = append(out.Files, files...)
	}
	if len(out.Files) == 0 {
		return
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })

	// Dry run with the generator of the machine, on the directories of the scope.
	args := []string{"-dryrun"}
	if sc.Rootless {
		args = append(args, "-user")
	}
	res, err := r.Run(ctx, runner.Cmd{
		Name: bin, Args: args, User: sc.User, Timeout: commandTimeout,
		Env: []string{"QUADLET_UNIT_DIRS=" + strings.Join(out.Dirs, ":")},
	})
	if err != nil {
		fail(fmt.Errorf("quadlet -dryrun: %w", err))
	} else {
		out.DryRun = "ok"
		if res.ExitCode != 0 {
			out.DryRun = "failed"
		}
		generated := parseDryRun(hostfs.SplitLines(res.Stdout))
		byFile, general := parseDiagnostics(string(res.Stderr))
		out.Diagnostics = general
		for i := range out.Files {
			fl := &out.Files[i]
			fl.Diagnostics = byFile[fl.Name]
			fl.Generated = generated[fl.Service]
		}
	}

	m.unitStates(ctx, r, sc, out)
	m.volumes(ctx, r, sc, out)
}

// listFiles parses the quadlet files of a directory and its subdirectories.
func (m *Module) listFiles(dir string) ([]File, error) {
	var out []File
	root := m.fs.Path(dir)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			// The users directories belong to the rootless scopes, users/<uid> to one user.
			parent := filepath.Dir(p)
			switch {
			case p == root:
			case dir == "/etc/containers/systemd" && parent == root && e.Name() == "users":
				return filepath.SkipDir
			case dir == "/etc/containers/systemd/users" && parent == root && isNumber(e.Name()):
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(e.Name())
		suffix, ok := kinds[ext]
		if !ok {
			return nil
		}
		lines, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		info, _ := e.Info()
		base := strings.TrimSuffix(e.Name(), ext)
		rel, _ := filepath.Rel(root, p)
		fl := File{
			Name:    e.Name(),
			Path:    filepath.Join(dir, rel),
			Kind:    strings.TrimPrefix(ext, "."),
			Service: base + suffix + ".service",
		}
		if info != nil {
			fl.ModTime = info.ModTime().UTC()
		}
		describe(&fl, ParseUnit(hostfs.SplitLines(lines)), base)
		out = append(out, fl)
		return nil
	})
	return out, err
}

func isNumber(s string) bool {
	_, err := strconv.ParseUint(s, 10, 32)
	return err == nil
}

// describe reads the keys that matter from a quadlet file.
func describe(fl *File, u UnitFile, base string) {
	switch fl.Kind {
	case "container":
		fl.Image = u.Value("Container", "Image")
		fl.Volumes = u.Values("Container", "Volume")
		fl.Networks = u.Values("Container", "Network")
	case "volume":
		fl.ObjectName = u.Value("Volume", "VolumeName")
		if fl.ObjectName == "" {
			fl.ObjectName = "systemd-" + base
		}
		fl.User, fl.Group, fl.Device = u.Value("Volume", "User"), u.Value("Volume", "Group"), u.Value("Volume", "Device")
	case "network":
		fl.ObjectName = u.Value("Network", "NetworkName")
		if fl.ObjectName == "" {
			fl.ObjectName = "systemd-" + base
		}
	}
}

// unitStates reads the state of the generated units from systemd.
func (m *Module) unitStates(ctx context.Context, r runner.Runner, sc users.Scope, out *Scope) {
	if !m.fs.Exists("/run/systemd/system") {
		return
	}
	if sc.Rootless && !m.resolver.ManagerRunning(sc.Account) {
		out.Errors = append(out.Errors, "user manager not running: the states of the generated units are unknown")
		return
	}
	args := []string{}
	if sc.Rootless {
		args = append(args, "--user")
	}
	args = append(args, "show", "--no-pager", "--timestamp=unix", "-p", "Id,LoadState,ActiveState,SubState,ActiveEnterTimestamp")
	for _, fl := range out.Files {
		args = append(args, fl.Service)
	}
	res, err := r.Run(ctx, runner.Cmd{Name: "systemctl", Args: args, User: sc.User, Timeout: commandTimeout})
	if err != nil || res.ExitCode != 0 {
		out.Errors = append(out.Errors, fmt.Sprintf("systemctl show: %v %s", err, strings.TrimSpace(string(res.Stderr))))
		return
	}
	states := map[string]*UnitState{}
	for _, block := range splitBlocks(hostfs.SplitLines(res.Stdout)) {
		st := &UnitState{Load: block["LoadState"], Active: block["ActiveState"], Sub: block["SubState"]}
		st.ActiveSince = parseTimestamp(block["ActiveEnterTimestamp"])
		states[block["Id"]] = st
	}
	for i := range out.Files {
		out.Files[i].State = states[out.Files[i].Service]
	}
}

func splitBlocks(lines []string) []map[string]string {
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

// parseTimestamp reads "@1790000000" (--timestamp=unix, systemd 248+) or the default format.
func parseTimestamp(v string) time.Time {
	if s, ok := strings.CutPrefix(v, "@"); ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0).UTC()
		}
		return time.Time{}
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// volumes lists the volumes podman really has, with the owner of their mountpoint.
func (m *Module) volumes(ctx context.Context, r runner.Runner, sc users.Scope, out *Scope) {
	if _, err := r.LookPath("podman"); err != nil {
		return
	}
	res, err := r.Run(ctx, runner.Cmd{Name: "podman", Args: []string{"volume", "ls", "--format", "json"}, User: sc.User, Timeout: commandTimeout})
	if err != nil || res.ExitCode != 0 {
		out.Errors = append(out.Errors, fmt.Sprintf("podman volume ls: %v %s", err, strings.TrimSpace(string(res.Stderr))))
		return
	}
	var raw []struct{ Name, Mountpoint string }
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		out.Errors = append(out.Errors, "podman volume ls: "+err.Error())
		return
	}
	idm := users.Identity()
	if sc.Rootless {
		idm = users.NewIDMap(m.fs, sc.Account)
	}
	out.Volumes = map[string]VolumeState{}
	for _, v := range raw {
		vs := VolumeState{Mountpoint: v.Mountpoint}
		if uid, gid, err := m.fs.Owner(v.Mountpoint); err == nil {
			cu, cg := idm.UID(uid), idm.GID(gid)
			vs.HostOwnerUID, vs.OwnerUID, vs.OwnerGID = uid, &cu, &cg
		}
		out.Volumes[v.Name] = vs
	}
}
