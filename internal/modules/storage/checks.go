package storage

import (
	"fmt"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Default thresholds on used space and used inodes.
var (
	usageThreshold  = module.Threshold{Warn: 0.85, Fail: 0.95, Unit: "ratio"}
	inodesThreshold = module.Threshold{Warn: 0.85, Fail: 0.95, Unit: "ratio"}
)

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "disk.usage", Description: "used space of each filesystem (as df)", Threshold: &usageThreshold},
		{ID: "disk.inodes", Description: "used inodes of each filesystem", Threshold: &inodesThreshold},
		{ID: "disk.readonly", Description: "filesystem read-only while /etc/fstab mounts it read-write"},
		{ID: "disk.unreachable", Description: "filesystem whose usage cannot be read (statfs error or timeout)"},
	}
}

// volatileFS are the memory-backed filesystems.
var volatileFS = map[string]bool{"tmpfs": true, "devtmpfs": true}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	seen := map[string]bool{}
	for _, fs := range f.Filesystems {
		if fs.Error != "" {
			out = append(out, model.Finding{
				ID:       "disk.unreachable",
				Subject:  fs.MountPoint,
				Severity: model.SeverityFail,
				Message:  "cannot read the filesystem usage: " + fs.Error,
				Evidence: map[string]any{"source": fs.Source, "fs_type": fs.FSType},
				Hint:     "check the device or the network share behind the mount",
			})
			continue
		}
		if ro := readOnlyFinding(fs); ro != nil {
			out = append(out, *ro)
		}
		// Read-only filesystems (images, snaps) are full by design; bind mounts of the same
		// filesystem are checked once.
		if fs.ReadOnly || fs.SizeBytes == 0 || seen[fs.DeviceID] {
			continue
		}
		seen[fs.DeviceID] = true
		// Memory-backed filesystems (/run, /dev/shm) are reported only when they fill up.
		volatile := volatileFS[fs.FSType]
		if u := usageFinding(fs, env.Threshold("disk.usage", usageThreshold)); !volatile || u.Severity >= model.SeverityWarn {
			out = append(out, u)
		}
		if fs.InodesTotal == 0 {
			continue
		}
		if in := inodesFinding(fs, env.Threshold("disk.inodes", inodesThreshold)); !volatile || in.Severity >= model.SeverityWarn {
			out = append(out, in)
		}
	}
	return out
}

func usageFinding(fs Filesystem, t module.Threshold) model.Finding {
	f := model.Finding{
		ID:       "disk.usage",
		Subject:  fs.MountPoint,
		Severity: t.Grade(fs.UsedRatio),
		Message:  fmt.Sprintf("%.0f%% used", fs.UsedRatio*100),
		Evidence: map[string]any{
			"used_bytes":      fs.UsedBytes,
			"available_bytes": fs.AvailableBytes,
			"size_bytes":      fs.SizeBytes,
			"source":          fs.Source,
		},
	}
	if f.Severity >= model.SeverityWarn {
		f.Hint = "find what grows with du -xh --max-depth=2; common culprits: logs (journalctl --disk-usage), container images and volumes"
	}
	return f
}

func inodesFinding(fs Filesystem, t module.Threshold) model.Finding {
	f := model.Finding{
		ID:       "disk.inodes",
		Subject:  fs.MountPoint,
		Severity: t.Grade(fs.InodesUsedRatio),
		Message:  fmt.Sprintf("%.0f%% of inodes used", fs.InodesUsedRatio*100),
		Evidence: map[string]any{"inodes_used": fs.InodesUsed, "inodes_total": fs.InodesTotal},
	}
	if f.Severity >= model.SeverityWarn {
		f.Hint = "many small files: look for caches, sessions or mail queues (find -xdev | cut -d/ -f2-3 | sort | uniq -c)"
	}
	return f
}

// readOnlyFinding reports a filesystem that /etc/fstab mounts read-write but is read-only now:
// usually the kernel remounted it after I/O or filesystem errors.
func readOnlyFinding(fs Filesystem) *model.Finding {
	if !fs.ReadOnly || fs.FstabOptions == nil || hasOption(fs.FstabOptions, "ro") {
		return nil
	}
	return &model.Finding{
		ID:       "disk.readonly",
		Subject:  fs.MountPoint,
		Severity: model.SeverityFail,
		Message:  "mounted read-only, but /etc/fstab mounts it read-write",
		Evidence: map[string]any{"options": fs.Options, "fstab_options": fs.FstabOptions, "source": fs.Source},
		Hint:     "the kernel remounts a filesystem read-only after errors: check dmesg / journalctl -k",
	}
}
