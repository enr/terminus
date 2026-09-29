// Package storage is the core module with filesystems (usage, inodes, read-only state),
// block devices and swap areas.
package storage

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "storage"

// statfsTimeout bounds each statfs call: a dead network filesystem must not hang the run.
var statfsTimeout = 3 * time.Second

// Facts about storage.
type Facts struct {
	Filesystems  []Filesystem  `json:"filesystems"`
	BlockDevices []BlockDevice `json:"block_devices"`
	Swaps        []Swap        `json:"swaps,omitempty"`
}

// Filesystem is a mounted filesystem with its usage.
type Filesystem struct {
	MountPoint string   `json:"mount_point"`
	Source     string   `json:"source"`
	FSType     string   `json:"fs_type"`
	Options    []string `json:"options"`
	ReadOnly   bool     `json:"read_only"`
	// DeviceID is major:minor: bind mounts of the same filesystem share it.
	DeviceID string `json:"device_id"`
	// FstabOptions are the options in /etc/fstab, when the mount point is listed there.
	FstabOptions    []string `json:"fstab_options,omitempty"`
	SizeBytes       uint64   `json:"size_bytes"`
	UsedBytes       uint64   `json:"used_bytes"`
	AvailableBytes  uint64   `json:"available_bytes"`
	UsedRatio       float64  `json:"used_ratio"`
	InodesTotal     uint64   `json:"inodes_total"`
	InodesUsed      uint64   `json:"inodes_used"`
	InodesUsedRatio float64  `json:"inodes_used_ratio"`
	Error           string   `json:"error,omitempty"`
}

// Swap is an active swap area (/proc/swaps).
type Swap struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	UsedBytes uint64 `json:"used_bytes"`
	Priority  int    `json:"priority"`
}

// Module collects the storage facts.
type Module struct {
	fs     hostfs.FS
	statfs func(path string) (unix.Statfs_t, error)
}

// New returns the storage module reading the running machine.
func New() *Module {
	return &Module{fs: hostfs.Host, statfs: func(p string) (unix.Statfs_t, error) {
		var st unix.Statfs_t
		err := unix.Statfs(p, &st)
		return st, err
	}}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "filesystems (usage, inodes, read-only), block devices, swap areas"
}

// Tables implements module.Tabular.
func (*Module) Tables() map[string][]string {
	return map[string][]string{
		"filesystems":   {"mount=mount_point", "type=fs_type", "size=size_bytes", "used=used_ratio", "inodes=inodes_used_ratio", "ro=read_only", "source", "error"},
		"block_devices": {"name", "size=size_bytes", "model", "rotational", "ro=read_only", "partitions"},
	}
}

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, _ *module.Env) (any, error) {
	var errs []error
	f := &Facts{}

	lines, err := m.fs.Lines("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	mounts, err := parseMountinfo(lines)
	if err != nil {
		errs = append(errs, err)
	}
	fstab := map[string][]string{}
	if lines, err := m.fs.Lines("/etc/fstab"); err == nil {
		fstab = parseFstab(lines)
	} else if !hostfs.IsIgnorable(err) {
		errs = append(errs, err)
	}
	// A mount hides the earlier ones on the same mount point: only the last one is visible.
	visible := map[string]int{}
	for i, mt := range mounts {
		visible[mt.MountPoint] = i
	}
	for i, mt := range mounts {
		if !relevant(mt) || visible[mt.MountPoint] != i {
			continue
		}
		fs := Filesystem{
			MountPoint:   mt.MountPoint,
			Source:       mt.Source,
			FSType:       mt.FSType,
			Options:      mt.Options,
			ReadOnly:     hasOption(mt.Options, "ro"),
			DeviceID:     mt.DeviceID,
			FstabOptions: fstab[mt.MountPoint],
		}
		f.Filesystems = append(f.Filesystems, fs)
	}
	m.fillUsage(ctx, f.Filesystems)

	if f.BlockDevices, err = readBlockDevices(m.fs); err != nil {
		errs = append(errs, err)
	}
	if lines, err := m.fs.Lines("/proc/swaps"); err == nil {
		f.Swaps = parseSwaps(lines)
	} else if !hostfs.IsIgnorable(err) {
		errs = append(errs, err)
	}
	return f, errors.Join(errs...)
}

// fillUsage runs statfs on every filesystem in parallel, each with its own timeout.
func (m *Module) fillUsage(ctx context.Context, fss []Filesystem) {
	var wg sync.WaitGroup
	for i := range fss {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fs := &fss[i]
			type result struct {
				st  unix.Statfs_t
				err error
			}
			ch := make(chan result, 1)
			go func() {
				st, err := m.statfs(m.fs.Path(fs.MountPoint))
				ch <- result{st, err}
			}()
			ctx, cancel := context.WithTimeout(ctx, statfsTimeout)
			defer cancel()
			select {
			case r := <-ch:
				if r.err != nil {
					fs.Error = r.err.Error()
					return
				}
				setUsage(fs, r.st)
			case <-ctx.Done():
				fs.Error = fmt.Sprintf("statfs did not answer in %s (unreachable network filesystem?)", statfsTimeout)
			}
		}()
	}
	wg.Wait()
}

// setUsage computes the usage the way df does: used / (used + available to unprivileged users).
func setUsage(fs *Filesystem, st unix.Statfs_t) {
	bsize := uint64(st.Bsize)
	if st.Frsize > 0 {
		bsize = uint64(st.Frsize)
	}
	fs.SizeBytes = st.Blocks * bsize
	fs.UsedBytes = (st.Blocks - st.Bfree) * bsize
	fs.AvailableBytes = st.Bavail * bsize
	if d := fs.UsedBytes + fs.AvailableBytes; d > 0 {
		fs.UsedRatio = float64(fs.UsedBytes) / float64(d)
	}
	fs.InodesTotal = st.Files
	if st.Files > 0 {
		fs.InodesUsed = st.Files - st.Ffree
		fs.InodesUsedRatio = float64(fs.InodesUsed) / float64(st.Files)
	}
}

// pseudoFS are the kernel filesystems that hold no user data.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true,
	"securityfs": true, "pstore": true, "bpf": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true,
	"rpc_pipefs": true, "nsfs": true, "efivarfs": true, "selinuxfs": true, "ramfs": true,
	// Container image layers: one per running container, the host filesystems already count them.
	"overlay": true, "fuse.fuse-overlayfs": true,
}

// relevant tells whether a mount belongs to the facts: real filesystems, not kernel ones and not
// the private mounts of container storage.
func relevant(m Mount) bool {
	if pseudoFS[m.FSType] {
		return false
	}
	for _, p := range []string{"/containers/storage/", "/overlay-containers/", "/var/lib/docker/", "/run/netns/"} {
		if strings.Contains(m.MountPoint+"/", p) {
			return false
		}
	}
	return true
}

func hasOption(opts []string, o string) bool {
	for _, x := range opts {
		if x == o {
			return true
		}
	}
	return false
}

// Mount is a line of /proc/self/mountinfo.
type Mount struct {
	DeviceID   string
	Root       string
	MountPoint string
	Options    []string
	FSType     string
	Source     string
}

// parseMountinfo parses /proc/self/mountinfo (proc(5)). Mount options include the
// superblock ones, so that "ro" is reported whatever level set it.
func parseMountinfo(lines []string) ([]Mount, error) {
	var mounts []Mount
	var errs []error
	for _, l := range lines {
		fields := strings.Fields(l)
		sep := -1
		for i, f := range fields {
			if f == "-" && i >= 6 {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+3 {
			errs = append(errs, fmt.Errorf("mountinfo: malformed line %q", l))
			continue
		}
		opts := strings.Split(fields[5], ",")
		if len(fields) > sep+3 {
			for _, o := range strings.Split(fields[sep+3], ",") {
				if !hasOption(opts, o) {
					opts = append(opts, o)
				}
			}
		}
		mounts = append(mounts, Mount{
			DeviceID:   fields[2],
			Root:       unescape(fields[3]),
			MountPoint: unescape(fields[4]),
			Options:    opts,
			FSType:     fields[sep+1],
			Source:     unescape(fields[sep+2]),
		})
	}
	return mounts, errors.Join(errs...)
}

// unescape decodes the octal escapes (\040 for space) used in mountinfo and fstab.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseFstab returns the mount options of each mount point listed in /etc/fstab.
func parseFstab(lines []string) map[string][]string {
	m := map[string][]string{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) < 4 || fields[1] == "none" || fields[2] == "swap" {
			continue
		}
		m[path.Clean(unescape(fields[1]))] = strings.Split(fields[3], ",")
	}
	return m
}

func parseSwaps(lines []string) []Swap {
	var swaps []Swap
	for i, l := range lines {
		fields := strings.Fields(l)
		if i == 0 || len(fields) < 5 {
			continue // header
		}
		size, _ := strconv.ParseUint(fields[2], 10, 64)
		used, _ := strconv.ParseUint(fields[3], 10, 64)
		prio, _ := strconv.Atoi(fields[4])
		swaps = append(swaps, Swap{Name: unescape(fields[0]), Type: fields[1], SizeBytes: size * 1024, UsedBytes: used * 1024, Priority: prio})
	}
	return swaps
}
