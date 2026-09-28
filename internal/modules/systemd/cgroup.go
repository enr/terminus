package systemd

import (
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
)

// readCgroup completes a unit with the files of its cgroup (v2): the values systemd did not
// report (MemoryPeak before systemd 253) and the OOM kills of the current cgroup.
func readCgroup(fs hostfs.FS, u *Unit) {
	dir := "/sys/fs/cgroup" + u.ControlGroup + "/"
	if u.MemoryPeakBytes == nil {
		if n, err := fs.ReadUint(dir + "memory.peak"); err == nil {
			u.MemoryPeakBytes = &n
		}
	}
	if u.MemoryCurrentBytes == nil {
		if n, err := fs.ReadUint(dir + "memory.current"); err == nil {
			u.MemoryCurrentBytes = &n
		}
	}
	if u.MemoryMaxBytes == nil {
		// "max" means unlimited; a number is a limit set by a parent or outside systemd.
		if n, err := fs.ReadUint(dir + "memory.max"); err == nil {
			u.MemoryMaxBytes = &n
		}
	}
	if lines, err := fs.Lines(dir + "memory.events"); err == nil {
		for _, l := range lines {
			if k, v, ok := strings.Cut(l, " "); ok && k == "oom_kill" {
				if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
					u.OOMKills = &n
				}
			}
		}
	}
}
