// Package updates is an optional module about pending package updates (apt, dnf, yum, apk), the
// security ones, the age of the package index they are computed from, and pending reboots.
//
// Nothing is downloaded: the counts come from the index the machine already has, so its age is
// part of the facts.
package updates

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "updates"

// managers are the package managers supported, in order of preference.
var managers = []string{"apt-get", "dnf", "yum", "apk"}

// indexDirs hold the package index of each manager: the newest file tells when it was updated.
var indexDirs = map[string][]string{
	"apt-get": {"/var/lib/apt/lists"},
	"dnf":     {"/var/cache/dnf", "/var/cache/libdnf5"},
	"yum":     {"/var/cache/yum"},
	"apk":     {"/var/cache/apk"},
}

// Facts about the updates.
type Facts struct {
	// Manager is the package manager: apt-get, dnf, yum, apk.
	Manager       string    `json:"manager"`
	PendingCount  int       `json:"pending_count"`
	SecurityCount int       `json:"security_count"`
	Pending       []Package `json:"pending"`
	// SecurityKnown tells whether the manager says which updates are security ones (apk does not).
	SecurityKnown bool `json:"security_known"`
	// IndexUpdated is when the package index was last refreshed (apt update, dnf makecache).
	IndexUpdated    time.Time `json:"index_updated,omitzero"`
	IndexAgeSeconds float64   `json:"index_age_seconds,omitempty"`
	RebootRequired  bool      `json:"reboot_required"`
	RebootReasons   []string  `json:"reboot_reasons,omitempty"`
	RunningKernel   string    `json:"running_kernel,omitempty"`
	// InstalledKernels are the kernels with modules in /lib/modules, oldest first.
	InstalledKernels []string `json:"installed_kernels,omitempty"`
}

// Package is a pending update.
type Package struct {
	Name      string `json:"name"`
	Installed string `json:"installed,omitempty"`
	Available string `json:"available"`
	// Origin is the repository or suite of the new version.
	Origin   string `json:"origin,omitempty"`
	Security bool   `json:"security"`
}

// Module collects the update facts.
type Module struct {
	fs  hostfs.FS
	now func() time.Time
}

// New returns the updates module.
func New() *Module { return &Module{fs: hostfs.Host, now: time.Now} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "pending package updates (apt, dnf, yum, apk), security ones, pending reboot"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

func manager(r runner.Runner) string {
	for _, m := range managers {
		if _, err := r.LookPath(m); err == nil {
			return m
		}
	}
	return ""
}

// Detect implements module.Detector.
func (*Module) Detect(_ context.Context, env *module.Env) module.Detection {
	if env == nil || env.Runner == nil {
		return module.Detection{Reason: "no command runner"}
	}
	m := manager(env.Runner)
	if m == "" {
		return module.Detection{Reason: "no supported package manager (" + strings.Join(managers, ", ") + ")"}
	}
	return module.Detection{Found: true, Reason: m + " found", Config: "enabled = true"}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	r := env.Runner
	f := &Facts{Manager: manager(r), Pending: []Package{}}
	if f.Manager == "" {
		return nil, module.Skip("no supported package manager (%s)", strings.Join(managers, ", "))
	}
	var errs []error
	var err error
	switch f.Manager {
	case "apt-get":
		f.SecurityKnown = true
		f.Pending, err = m.apt(ctx, r)
	case "dnf", "yum":
		f.SecurityKnown = true
		f.Pending, err = dnf(ctx, r, f.Manager)
	case "apk":
		f.Pending, err = apk(ctx, r)
	}
	if err != nil {
		errs = append(errs, err)
	}
	sort.Slice(f.Pending, func(i, j int) bool { return f.Pending[i].Name < f.Pending[j].Name })
	f.PendingCount = len(f.Pending)
	for _, p := range f.Pending {
		if p.Security {
			f.SecurityCount++
		}
	}
	if t := m.newestFile(indexDirs[f.Manager]); !t.IsZero() {
		f.IndexUpdated = t.UTC()
		f.IndexAgeSeconds = m.now().Sub(t).Seconds()
	}
	if err := m.reboot(ctx, r, f); err != nil {
		errs = append(errs, err)
	}
	return f, errors.Join(errs...)
}

// aptInst matches a line of apt-get -s: "Inst name [installed] (available origin [arch])"; the
// installed version is missing for new packages.
var aptInst = regexp.MustCompile(`^Inst (\S+) (?:\[(\S+)\] )?\((\S+) (.*?)(?: \[[^\]]+\])?\)`)

func (m *Module) apt(ctx context.Context, r runner.Runner) ([]Package, error) {
	res, err := r.Run(ctx, runner.Cmd{
		Name:    "apt-get",
		Args:    []string{"-s", "-o", "Debug::NoLocking=1", "dist-upgrade"},
		Env:     []string{"LANG=C", "LC_ALL=C"},
		Timeout: runner.NoTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("apt-get: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("apt-get -s dist-upgrade: exit code %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	return parseApt(hostfs.SplitLines(res.Stdout)), nil
}

func parseApt(lines []string) []Package {
	out := []Package{}
	for _, l := range lines {
		g := aptInst.FindStringSubmatch(l)
		if g == nil {
			continue
		}
		out = append(out, Package{
			Name:      g[1],
			Installed: g[2],
			Available: g[3],
			Origin:    g[4],
			Security:  strings.Contains(strings.ToLower(g[4]), "-security"),
		})
	}
	return out
}

func dnf(ctx context.Context, r runner.Runner, bin string) ([]Package, error) {
	// --cacheonly: the index the machine has, no download (and it works as another user than root).
	res, err := r.Run(ctx, runner.Cmd{Name: bin, Args: []string{"-q", "--cacheonly", "check-update"}, Env: []string{"LANG=C"}, Timeout: runner.NoTimeout})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bin, err)
	}
	// 100: updates available, 0: none.
	if res.ExitCode != 0 && res.ExitCode != 100 {
		return nil, fmt.Errorf("%s check-update: exit code %d: %s", bin, res.ExitCode, firstLine(res.Stderr))
	}
	pkgs := parseCheckUpdate(hostfs.SplitLines(res.Stdout))
	res, err = r.Run(ctx, runner.Cmd{Name: bin, Args: []string{"-q", "--cacheonly", "updateinfo", "list", "--security"}, Env: []string{"LANG=C"}, Timeout: runner.NoTimeout})
	if err != nil || res.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("exit code %d: %s", res.ExitCode, firstLine(res.Stderr))
		}
		return pkgs, fmt.Errorf("%s updateinfo: %w", bin, err)
	}
	sec := parseUpdateinfo(hostfs.SplitLines(res.Stdout))
	for i, p := range pkgs {
		pkgs[i].Security = sec[p.Name]
	}
	return pkgs, nil
}

// parseCheckUpdate parses `dnf check-update`: "name.arch  version  repository"; the list ends at
// "Obsoleting Packages".
func parseCheckUpdate(lines []string) []Package {
	out := []Package{}
	for _, l := range lines {
		if strings.HasPrefix(l, "Obsoleting") || strings.HasPrefix(l, "Security:") {
			break
		}
		f := strings.Fields(l)
		if len(f) != 3 || !strings.Contains(f[0], ".") || strings.HasPrefix(l, " ") {
			continue
		}
		out = append(out, Package{Name: f[0], Available: f[1], Origin: f[2]})
	}
	return out
}

// parseUpdateinfo parses `dnf updateinfo list --security` into the set of name.arch with a
// security advisory. dnf 4: "ADVISORY TYPE PACKAGE-NEVRA"; dnf 5: "Name Type Severity Package
// Issued" with a header.
func parseUpdateinfo(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		f := strings.Fields(l)
		var nevra string
		switch {
		case len(f) == 3:
			nevra = f[2]
		case len(f) >= 5 && f[0] != "Name":
			nevra = f[3]
		default:
			continue
		}
		if n := nameArch(nevra); n != "" {
			out[n] = true
		}
	}
	return out
}

// nameArch turns name-[epoch:]version-release.arch into name.arch.
func nameArch(nevra string) string {
	dot := strings.LastIndex(nevra, ".")
	if dot < 0 {
		return ""
	}
	name, arch := nevra[:dot], nevra[dot+1:]
	for range 2 { // drop release and version
		i := strings.LastIndex(name, "-")
		if i < 0 {
			return ""
		}
		name = name[:i]
	}
	return name + "." + arch
}

func apk(ctx context.Context, r runner.Runner) ([]Package, error) {
	res, err := r.Run(ctx, runner.Cmd{Name: "apk", Args: []string{"version", "-l", "<"}, Timeout: runner.NoTimeout})
	if err != nil {
		return nil, fmt.Errorf("apk: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("apk version: exit code %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	return parseApk(hostfs.SplitLines(res.Stdout)), nil
}

// parseApk parses `apk version -l '<'`: "name-version-rN < new-version-rN" after a header.
func parseApk(lines []string) []Package {
	out := []Package{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 || f[1] != "<" {
			continue
		}
		name, installed := splitApkName(f[0])
		out = append(out, Package{Name: name, Installed: installed, Available: f[2]})
	}
	return out
}

// splitApkName splits name-version-rN: the version starts at the second-last dash.
func splitApkName(s string) (string, string) {
	i := strings.LastIndex(s, "-")
	if i <= 0 {
		return s, ""
	}
	j := strings.LastIndex(s[:i], "-")
	if j <= 0 {
		return s, ""
	}
	return s[:j], s[j+1:]
}

// newestFile returns the modification time of the newest file under the directories (three
// levels down at most, as dnf keeps repodata/repomd.xml; downloaded packages are left out).
func (m *Module) newestFile(dirs []string) time.Time {
	var newest time.Time
	for _, d := range dirs {
		root := m.fs.Path(d)
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if e.IsDir() {
				if rel != "." && (strings.Count(rel, string(os.PathSeparator)) >= 2 || e.Name() == "partial" || e.Name() == "packages") {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Name() == "lock" {
				return nil
			}
			if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
	}
	return newest
}

// reboot tells whether a reboot is pending: the Debian marker, needs-restarting on Red Hat
// systems, and a running kernel older than the newest installed or whose modules are gone.
func (m *Module) reboot(ctx context.Context, r runner.Runner, f *Facts) error {
	if m.fs.Exists("/run/reboot-required") || m.fs.Exists("/var/run/reboot-required") {
		reason := "/run/reboot-required"
		if pkgs, err := m.fs.Lines("/run/reboot-required.pkgs"); err == nil && len(pkgs) > 0 {
			reason += " (" + strings.Join(uniq(pkgs), ", ") + ")"
		}
		f.RebootReasons = append(f.RebootReasons, reason)
	}
	var errs []error
	if _, err := r.LookPath("needs-restarting"); err == nil {
		res, err := r.Run(ctx, runner.Cmd{Name: "needs-restarting", Args: []string{"-r"}, Env: []string{"LANG=C"}})
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("needs-restarting: %w", err))
		case res.ExitCode == 1:
			f.RebootReasons = append(f.RebootReasons, "needs-restarting -r: core libraries or services updated")
		}
	}

	if rel, err := m.fs.ReadString("/proc/sys/kernel/osrelease"); err == nil {
		f.RunningKernel = rel
	}
	for _, d := range []string{"/lib/modules", "/usr/lib/modules"} {
		entries, err := m.fs.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// A directory with only leftovers (no modules.dep) is a removed kernel.
			if e.IsDir() && m.fs.Exists(filepath.Join(d, e.Name(), "modules.dep")) {
				f.InstalledKernels = append(f.InstalledKernels, e.Name())
			}
		}
		if len(f.InstalledKernels) > 0 {
			break
		}
	}
	sort.Slice(f.InstalledKernels, func(i, j int) bool { return compareVersions(f.InstalledKernels[i], f.InstalledKernels[j]) < 0 })
	if n := len(f.InstalledKernels); n > 0 && f.RunningKernel != "" {
		switch newest := f.InstalledKernels[n-1]; {
		case !contains(f.InstalledKernels, f.RunningKernel):
			f.RebootReasons = append(f.RebootReasons, "the modules of the running kernel "+f.RunningKernel+" are not installed any more")
		case compareVersions(newest, f.RunningKernel) > 0:
			f.RebootReasons = append(f.RebootReasons, "kernel "+newest+" installed, "+f.RunningKernel+" running")
		}
	}
	f.RebootRequired = len(f.RebootReasons) > 0
	return errors.Join(errs...)
}

// compareVersions compares versions by their numeric and textual parts: 6.1.0-21 > 6.1.0-9.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, errA := strconv.Atoi(pa[i])
		nb, errB := strconv.Atoi(pb[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				return na - nb
			}
		case pa[i] != pb[i]:
			return strings.Compare(pa[i], pb[i])
		}
	}
	return len(pa) - len(pb)
}

var versionPart = regexp.MustCompile(`\d+|[a-zA-Z]+`)

func versionParts(s string) []string { return versionPart.FindAllString(s, -1) }

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
