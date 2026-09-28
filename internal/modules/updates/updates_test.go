package updates

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

var now = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

const aptOut = `Reading package lists...
Building dependency tree...
Calculating upgrade...
The following packages will be upgraded:
  coreutils libssl3 openssl
Inst coreutils [9.4-3ubuntu6.1] (9.4-3ubuntu6.2 Ubuntu:24.04/noble-updates [amd64])
Inst libssl3t64 [3.0.13-0ubuntu3.4] (3.0.13-0ubuntu3.5 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64]) []
Inst openssl [3.0.13-0ubuntu3.4] (3.0.13-0ubuntu3.5 Debian-Security:12/stable-security [amd64])
Inst linux-image-6.8.0-60-generic (6.8.0-60.63 Ubuntu:24.04/noble-updates [amd64])
Conf coreutils (9.4-3ubuntu6.2 Ubuntu:24.04/noble-updates [amd64])
`

// touch sets the modification time of a fixture file.
func touch(t *testing.T, fs hostfs.FS, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(fs.Path(p), at, at); err != nil {
		t.Fatal(err)
	}
}

func TestApt(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{
		"/var/lib/apt/lists/deb.debian.org_dists_bookworm_InRelease": "x",
		"/var/lib/apt/lists/lock":                                    "",
		"/var/lib/apt/lists/partial/new":                             "x",
		"/run/reboot-required":                                       "*** System restart required ***\n",
		"/run/reboot-required.pkgs":                                  "linux-base\nlibssl3t64\nlinux-base\n",
		"/proc/sys/kernel/osrelease":                                 "6.8.0-51-generic\n",
		"/lib/modules/6.8.0-51-generic/modules.dep":                  "",
		"/lib/modules/6.8.0-9-generic/modules.dep":                   "",
		"/lib/modules/6.8.0-60-generic/modules.dep":                  "",
		"/lib/modules/6.8.0-1-generic/leftover":                      "", // removed kernel
	})
	touch(t, fs, "/var/lib/apt/lists/deb.debian.org_dists_bookworm_InRelease", now.Add(-10*24*time.Hour))
	touch(t, fs, "/var/lib/apt/lists/lock", now)
	touch(t, fs, "/var/lib/apt/lists/partial/new", now)
	r := &runner.Fake{
		Paths:   map[string]string{"apt-get": "/usr/bin/apt-get", "dnf": "/usr/bin/dnf"},
		Results: map[string]runner.Result{"apt-get -s -o Debug::NoLocking=1 dist-upgrade": {Stdout: []byte(aptOut)}},
	}
	m := New()
	m.fs, m.now = fs, func() time.Time { return now }
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.Manager != "apt-get" || f.PendingCount != 4 || f.SecurityCount != 2 || !f.SecurityKnown {
		t.Fatalf("facts: %+v", f)
	}
	img := f.Pending[2]
	if img.Name != "linux-image-6.8.0-60-generic" || img.Installed != "" || img.Available != "6.8.0-60.63" || img.Origin != "Ubuntu:24.04/noble-updates" {
		t.Errorf("new package: %+v", img)
	}
	if ssl := f.Pending[0]; ssl.Name != "coreutils" || ssl.Installed != "9.4-3ubuntu6.1" || ssl.Security {
		t.Errorf("coreutils: %+v", ssl)
	}
	if f.IndexAgeSeconds != 10*86400 {
		t.Errorf("index age: %v (%v)", f.IndexAgeSeconds, f.IndexUpdated)
	}
	if strings.Join(f.InstalledKernels, ",") != "6.8.0-9-generic,6.8.0-51-generic,6.8.0-60-generic" || !f.RebootRequired ||
		len(f.RebootReasons) != 2 || f.RebootReasons[0] != "/run/reboot-required (linux-base, libssl3t64)" ||
		f.RebootReasons[1] != "kernel 6.8.0-60-generic installed, 6.8.0-51-generic running" {
		t.Errorf("reboot: %+v %q", f.InstalledKernels, f.RebootReasons)
	}

	sev := map[string]model.Severity{}
	for _, x := range m.Check(nil, f) {
		sev[x.ID] = x.Severity
	}
	want := map[string]model.Severity{
		"updates.security":  model.SeverityWarn,
		"updates.pending":   model.SeverityInfo,
		"updates.reboot":    model.SeverityWarn,
		"updates.index-age": model.SeverityWarn,
	}
	for k, w := range want {
		if sev[k] != w {
			t.Errorf("%s: %v, want %v", k, sev[k], w)
		}
	}
}

const dnfCheckUpdate = `
kernel.x86_64                       6.8.9-300.fc40                 updates
openssl-libs.x86_64                 1:3.2.1-2.fc40                 updates
vim-minimal.x86_64                  2:9.1.393-1.fc40               updates
Obsoleting Packages
grub2-tools.x86_64                  1:2.06-121.fc40                updates
    grub2-tools.x86_64              1:2.06-120.fc40                @updates
`

const dnf4Updateinfo = `FEDORA-2024-1a2b3c4d5e security     openssl-libs-1:3.2.1-2.fc40.x86_64
FEDORA-2024-1a2b3c4d5e security     openssl-3.2.1-2.fc40.x86_64
RHSA-2024:1234 Important/Sec. kernel-6.8.9-300.fc40.x86_64
`

const dnf5Updateinfo = `Name                 Type     Severity  Package                                  Issued
FEDORA-2024-1a2b3c4d security  Moderate openssl-libs-1:3.2.1-2.fc40.x86_64      2024-05-01 00:00:00
`

func TestDnf(t *testing.T) {
	for _, tc := range []struct {
		name, updateinfo string
		security         int
	}{{"dnf4", dnf4Updateinfo, 2}, {"dnf5", dnf5Updateinfo, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			fs := hostfstest.New(t, map[string]string{
				"/var/cache/dnf/updates-abc/repodata/repomd.xml": "x",
				"/var/cache/dnf/updates-abc/packages/big.rpm":    "x",
				"/proc/sys/kernel/osrelease":                     "6.8.9-300.fc40.x86_64",
				"/lib/modules/6.8.9-300.fc40.x86_64/modules.dep": "",
			})
			touch(t, fs, "/var/cache/dnf/updates-abc/repodata/repomd.xml", now.Add(-12*time.Hour))
			touch(t, fs, "/var/cache/dnf/updates-abc/packages/big.rpm", now)
			r := &runner.Fake{
				Paths: map[string]string{"dnf": "/usr/bin/dnf", "needs-restarting": "/usr/bin/needs-restarting"},
				Results: map[string]runner.Result{
					"dnf -q --cacheonly check-update":               {ExitCode: 100, Stdout: []byte(dnfCheckUpdate)},
					"dnf -q --cacheonly updateinfo list --security": {Stdout: []byte(tc.updateinfo)},
					"needs-restarting -r":                           {ExitCode: 1},
				},
			}
			m := New()
			m.fs, m.now = fs, func() time.Time { return now }
			got, err := m.Collect(context.Background(), &module.Env{Runner: r})
			if err != nil {
				t.Fatal(err)
			}
			f := got.(*Facts)
			if f.PendingCount != 3 || f.SecurityCount != tc.security || f.Pending[1].Name != "openssl-libs.x86_64" || !f.Pending[1].Security || f.Pending[2].Security {
				t.Errorf("pending: %+v", f.Pending)
			}
			if f.IndexAgeSeconds != 12*3600 {
				t.Errorf("index age: %v", f.IndexAgeSeconds)
			}
			if !f.RebootRequired || len(f.RebootReasons) != 1 || !strings.HasPrefix(f.RebootReasons[0], "needs-restarting") {
				t.Errorf("reboot: %q", f.RebootReasons)
			}
		})
	}

	// Errors are reported, the updates already read are kept.
	r := &runner.Fake{
		Paths: map[string]string{"yum": "/usr/bin/yum"},
		Results: map[string]runner.Result{
			"yum -q --cacheonly check-update":               {ExitCode: 100, Stdout: []byte(dnfCheckUpdate)},
			"yum -q --cacheonly updateinfo list --security": {ExitCode: 1, Stderr: []byte("Error: Cache-only enabled but no cache\n")},
		},
	}
	m := New()
	m.fs = hostfstest.New(t, map[string]string{})
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err == nil || !strings.Contains(err.Error(), "no cache") || got.(*Facts).PendingCount != 3 {
		t.Errorf("yum: %+v %v", got, err)
	}
	r.Results["yum -q --cacheonly check-update"] = runner.Result{ExitCode: 1, Stderr: []byte("Error: Cache-only enabled but no cache for 'fedora'\n")}
	if _, err := m.Collect(context.Background(), &module.Env{Runner: r}); err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("check-update error: %v", err)
	}
}

func TestApk(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{
		"/var/cache/apk/APKINDEX.abc.tar.gz":    "x",
		"/proc/sys/kernel/osrelease":            "6.6.30-0-lts",
		"/lib/modules/6.6.31-0-lts/modules.dep": "",
	})
	touch(t, fs, "/var/cache/apk/APKINDEX.abc.tar.gz", now.Add(-40*24*time.Hour))
	r := &runner.Fake{
		Paths: map[string]string{"apk": "/sbin/apk"},
		Results: map[string]runner.Result{"apk version -l <": {Stdout: []byte(
			"Installed:                                Available:\nbusybox-1.36.1-r28                      < 1.36.1-r29\nlibcrypto3-3.3.1-r0                     < 3.3.2-r0\n")}},
	}
	m := New()
	m.fs, m.now = fs, func() time.Time { return now }
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.PendingCount != 2 || f.Pending[0].Name != "busybox" || f.Pending[0].Installed != "1.36.1-r28" || f.SecurityKnown {
		t.Errorf("pending: %+v", f)
	}
	if !f.RebootRequired || !strings.Contains(f.RebootReasons[0], "not installed any more") {
		t.Errorf("reboot: %q", f.RebootReasons)
	}
	for _, x := range m.Check(nil, f) {
		switch x.ID {
		case "updates.security":
			t.Error("apk does not know security updates")
		case "updates.index-age":
			if x.Severity != model.SeverityFail {
				t.Errorf("index age: %+v", x)
			}
		}
	}
}

func TestUpToDate(t *testing.T) {
	f := &Facts{Manager: "apt-get", SecurityKnown: true, Pending: []Package{}}
	for _, x := range New().Check(nil, f) {
		if x.Severity != model.SeverityOK {
			t.Errorf("%+v", x)
		}
	}
	if _, err := New().Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("no manager: %v", err)
	}
	if d := New().Detect(context.Background(), &module.Env{Runner: &runner.Fake{Paths: map[string]string{"apk": "/sbin/apk"}}}); !d.Found {
		t.Errorf("detect: %+v", d)
	}
}

func TestHelpers(t *testing.T) {
	for _, c := range []struct {
		a, b string
		sign int
	}{
		{"6.1.0-21-amd64", "6.1.0-9-amd64", 1},
		{"6.8.9-300.fc40.x86_64", "6.8.10-100.fc40.x86_64", -1},
		{"5.15.0-100-generic", "5.15.0-100-generic", 0},
		{"6.6.30-0-lts", "6.6.30-0-virt", -1},
	} {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.sign > 0) || (got < 0) != (c.sign < 0) {
			t.Errorf("compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
	for in, want := range map[string]string{
		"openssl-libs-1:3.2.1-2.fc40.x86_64": "openssl-libs.x86_64",
		"kernel-6.8.9-300.fc40.x86_64":       "kernel.x86_64",
		"nodots":                             "",
	} {
		if got := nameArch(in); got != want {
			t.Errorf("nameArch(%s) = %q", in, got)
		}
	}
	if n, v := splitApkName("py3-cryptography-42.0.5-r0"); n != "py3-cryptography" || v != "42.0.5-r0" {
		t.Errorf("apk name: %s %s", n, v)
	}
	names := strings.Split("a b c d e f g h i j", " ")
	if list(names) != "a, b, c, d, e, f, g, h and 2 more" {
		t.Errorf("list: %s", list(names))
	}
}
