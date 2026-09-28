package system

import (
	"context"
	"testing"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func newTestModule(fs hostfs.FS) *Module {
	return &Module{
		fs: fs,
		uname: func() (Kernel, string, string, string, error) {
			return Kernel{Name: "Linux", Release: "6.8.0", Version: "#1 SMP"}, "x86_64", "srv-01", "(none)", nil
		},
		now: func() time.Time { return now },
	}
}

func TestCollect(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{
		"/etc/os-release": `NAME="Debian GNU/Linux"
ID=debian
ID_LIKE="ubuntu rhel"
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
`,
		"/etc/machine-id":                 "abc123\n",
		"/proc/sys/kernel/random/boot_id": "b-o-o-t\n",
		"/proc/uptime":                    "3600.50 7000.00\n",
		"/etc/localtime":                  hostfstest.Symlink + "../usr/share/zoneinfo/Europe/Rome",
		"/sys/class/dmi/id/sys_vendor":    "QEMU\n",
		"/sys/class/dmi/id/product_name":  "Standard PC (Q35 + ICH9, 2009)\n",
		// product_serial missing (root only): the other DMI fields must still be there.
		"/sys/class/dmi/id/bios_version": "1.16.3\n",
	})
	env := &module.Env{Runner: &runner.Fake{}}
	got, err := newTestModule(fs).Collect(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.Hostname != "srv-01" || f.Domainname != "" || f.Architecture != "x86_64" || f.Kernel.Release != "6.8.0" {
		t.Errorf("uname facts: %+v", f)
	}
	if f.OS.ID != "debian" || f.OS.VersionID != "12" || f.OS.VersionCodename != "bookworm" || len(f.OS.IDLike) != 2 {
		t.Errorf("os: %+v", f.OS)
	}
	if f.MachineID != "abc123" || f.BootID != "b-o-o-t" {
		t.Errorf("ids: %q %q", f.MachineID, f.BootID)
	}
	if f.UptimeSeconds != 3600.5 || !f.BootTime.Equal(now.Add(-3600*time.Second-500*time.Millisecond).Truncate(time.Second)) {
		t.Errorf("uptime %v boot %v", f.UptimeSeconds, f.BootTime)
	}
	if f.Time.Timezone != "Europe/Rome" || f.Time.Abbreviation != "UTC" || f.Time.UTCOffset != "+00:00" {
		t.Errorf("time: %+v", f.Time)
	}
	if f.DMI == nil || f.DMI.SysVendor != "QEMU" || f.DMI.BIOSVersion != "1.16.3" || f.DMI.ProductSerial != "" {
		t.Errorf("dmi: %+v", f.DMI)
	}
	if f.Virtualization != (Virtualization{Type: "vm", Name: "qemu", Source: "files"}) {
		t.Errorf("virtualization: %+v", f.Virtualization)
	}
}

func TestCollectPartial(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{"/usr/lib/os-release": "ID=alpine\n"})
	got, err := newTestModule(fs).Collect(context.Background(), &module.Env{})
	if err == nil {
		t.Fatal("expected an error for the missing /proc/uptime")
	}
	f := got.(*Facts)
	if f.OS.ID != "alpine" || f.Hostname != "srv-01" || f.DMI != nil {
		t.Errorf("facts lost on partial collection: %+v", f)
	}
}

func TestVirtualization(t *testing.T) {
	sdv := func(container, vm string) *runner.Fake {
		res := func(out string) runner.Result {
			code := 0
			if out == "none" {
				code = 1
			}
			return runner.Result{Stdout: []byte(out + "\n"), ExitCode: code}
		}
		return &runner.Fake{
			Paths: map[string]string{"systemd-detect-virt": "/usr/bin/systemd-detect-virt"},
			Results: map[string]runner.Result{
				"systemd-detect-virt --container": res(container),
				"systemd-detect-virt --vm":        res(vm),
			},
		}
	}
	empty := hostfstest.New(t, nil)
	cases := []struct {
		name string
		r    runner.Runner
		fs   hostfs.FS
		dmi  *DMI
		want Virtualization
	}{
		{"sdv container", sdv("podman", "kvm"), empty, nil, Virtualization{"container", "podman", "systemd-detect-virt"}},
		{"sdv vm", sdv("none", "kvm"), empty, nil, Virtualization{"vm", "kvm", "systemd-detect-virt"}},
		{"sdv none", sdv("none", "none"), empty, nil, Virtualization{"none", "", "systemd-detect-virt"}},
		{"containerenv", nil, hostfstest.New(t, map[string]string{"/run/.containerenv": ""}), nil, Virtualization{"container", "podman", "files"}},
		{"environ", nil, hostfstest.New(t, map[string]string{"/proc/1/environ": "PATH=/bin\x00container=lxc\x00"}), nil, Virtualization{"container", "lxc", "files"}},
		{"dmi", &runner.Fake{}, empty, &DMI{SysVendor: "VMware, Inc."}, Virtualization{"vm", "vmware", "files"}},
		{"cpu flag", nil, hostfstest.New(t, map[string]string{"/proc/cpuinfo": "flags\t\t: fpu vme hypervisor lahf_lm\n"}), &DMI{}, Virtualization{"vm", "unknown", "files"}},
		{"bare metal", nil, empty, &DMI{SysVendor: "Dell Inc."}, Virtualization{"none", "", "files"}},
	}
	for _, c := range cases {
		if got := detectVirtualization(context.Background(), c.r, c.fs, c.dmi); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestCollectLive(t *testing.T) {
	got, err := New().Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	f := got.(*Facts)
	if f.Kernel.Name != "Linux" || f.Hostname == "" || f.UptimeSeconds <= 0 {
		t.Fatalf("live facts: %+v (err %v)", f, err)
	}
}
