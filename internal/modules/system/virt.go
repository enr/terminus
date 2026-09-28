package system

import (
	"context"
	"strings"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/runner"
)

// detectVirtualization asks systemd-detect-virt when available and falls back to the usual
// markers otherwise.
func detectVirtualization(ctx context.Context, r runner.Runner, fs hostfs.FS, dmi *DMI) Virtualization {
	if r != nil {
		if _, err := r.LookPath("systemd-detect-virt"); err == nil {
			if v, ok := systemdDetectVirt(ctx, r); ok {
				return v
			}
		}
	}
	return detectFromFiles(fs, dmi)
}

func systemdDetectVirt(ctx context.Context, r runner.Runner) (Virtualization, bool) {
	// The container check comes first: a container running in a VM is reported as a container.
	for _, kind := range []struct{ flag, typ string }{{"--container", "container"}, {"--vm", "vm"}} {
		res, err := r.Run(ctx, runner.Cmd{Name: "systemd-detect-virt", Args: []string{kind.flag}, Timeout: 5 * time.Second})
		if err != nil {
			return Virtualization{}, false
		}
		name := strings.TrimSpace(string(res.Stdout))
		if res.ExitCode == 0 && name != "" && name != "none" {
			return Virtualization{Type: kind.typ, Name: name, Source: "systemd-detect-virt"}, true
		}
	}
	return Virtualization{Type: "none", Source: "systemd-detect-virt"}, true
}

// dmiVendors maps DMI vendor/product substrings to systemd-detect-virt names.
var dmiVendors = []struct{ match, name string }{
	{"KVM", "kvm"},
	{"QEMU", "qemu"},
	{"VMware", "vmware"},
	{"VirtualBox", "oracle"},
	{"innotek", "oracle"},
	{"Xen", "xen"},
	{"Microsoft Corporation", "microsoft"},
	{"Amazon EC2", "amazon"},
	{"Google Compute Engine", "google"},
	{"Parallels", "parallels"},
	{"BHYVE", "bhyve"},
}

func detectFromFiles(fs hostfs.FS, dmi *DMI) Virtualization {
	src := "files"
	switch {
	case fs.Exists("/run/.containerenv"):
		return Virtualization{Type: "container", Name: "podman", Source: src}
	case fs.Exists("/.dockerenv"):
		return Virtualization{Type: "container", Name: "docker", Source: src}
	}
	if env, err := fs.ReadFile("/proc/1/environ"); err == nil {
		for _, kv := range strings.Split(string(env), "\x00") {
			if name, ok := strings.CutPrefix(kv, "container="); ok && name != "" {
				return Virtualization{Type: "container", Name: name, Source: src}
			}
		}
	}
	if dmi != nil {
		for _, v := range dmiVendors {
			if strings.Contains(dmi.SysVendor, v.match) || strings.Contains(dmi.ProductName, v.match) || strings.Contains(dmi.BIOSVendor, v.match) {
				return Virtualization{Type: "vm", Name: v.name, Source: src}
			}
		}
	}
	if cpuinfo, err := fs.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range hostfs.SplitLines(cpuinfo) {
			if strings.HasPrefix(l, "flags") && strings.Contains(l, " hypervisor") {
				return Virtualization{Type: "vm", Name: "unknown", Source: src}
			}
		}
	}
	return Virtualization{Type: "none", Source: src}
}
