package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
)

const mountinfo = `22 1 252:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw,errors=remount-ro
23 22 0:22 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
24 22 0:24 / /run rw,nosuid,nodev shared:6 - tmpfs tmpfs rw,size=400000k,mode=755
25 22 252:2 / /srv/My\040Data rw,relatime shared:7 - xfs /dev/vda2 ro,attr2
26 22 252:1 /var/lib/app /mnt/bind rw,relatime shared:1 - ext4 /dev/vda1 rw
27 22 7:0 / /snap/core/1 ro,nodev,relatime shared:8 - squashfs /dev/loop0 ro
28 22 0:50 / /home/apps/.local/share/containers/storage/overlay/abc/merged rw - overlay overlay rw,lowerdir=x
29 22 0:51 / /var/lib/containers/storage/overlay-containers/abc/userdata/shm rw - tmpfs shm rw
30 22 0:52 / /mnt/nfs rw - nfs4 server:/export rw
31 22 0:60 / /dev/shm rw - tmpfs shm rw
32 22 0:61 / /dev/shm rw - tmpfs shm rw
malformed line
`

const fstab = `# /etc/fstab
UUID=abc  /                ext4  errors=remount-ro  0 1
/dev/vda2 /srv/My\040Data  xfs   defaults           0 2
/dev/vda3 none             swap  sw                 0 0
`

// statfsFixture returns fixed usage for each mount point, and hangs on NFS.
func statfsFixture(block chan struct{}) func(string) (unix.Statfs_t, error) {
	return func(p string) (unix.Statfs_t, error) {
		switch {
		case strings.HasSuffix(p, "/mnt/nfs"):
			<-block
			return unix.Statfs_t{}, nil
		case strings.HasSuffix(p, "/dev/shm"):
			return unix.Statfs_t{Bsize: 4096, Blocks: 100, Bfree: 1, Bavail: 1, Files: 10, Ffree: 9}, nil
		case strings.HasSuffix(p, "/run"):
			return unix.Statfs_t{}, errors.New("permission denied")
		case strings.Contains(p, "My Data"):
			return unix.Statfs_t{Bsize: 4096, Blocks: 1000, Bfree: 10, Bavail: 10, Files: 100, Ffree: 1}, nil
		default:
			// 90% used (df semantics: reserved blocks excluded), 50% inodes.
			return unix.Statfs_t{Bsize: 4096, Blocks: 1100, Bfree: 200, Bavail: 100, Files: 1000, Ffree: 500}, nil
		}
	}
}

func TestParseMountinfo(t *testing.T) {
	ms, err := parseMountinfo(strings.Split(strings.TrimSpace(mountinfo), "\n"))
	if err == nil {
		t.Error("malformed line not reported")
	}
	if len(ms) != 11 {
		t.Fatalf("mounts: %d", len(ms))
	}
	data := ms[3]
	if data.MountPoint != "/srv/My Data" || data.FSType != "xfs" || !hasOption(data.Options, "ro") || data.DeviceID != "252:2" {
		t.Errorf("escaped/ro mount: %+v", data)
	}
}

func TestCollectAndCheck(t *testing.T) {
	old := statfsTimeout
	statfsTimeout = 50 * time.Millisecond
	defer func() { statfsTimeout = old }()
	block := make(chan struct{})
	defer close(block)

	fs := hostfstest.New(t, map[string]string{
		"/proc/self/mountinfo":            mountinfo,
		"/etc/fstab":                      fstab,
		"/proc/swaps":                     "Filename Type Size Used Priority\n/dev/vda3 partition 1048572 512 -2\n",
		"/sys/block/vda/size":             "20971520\n",
		"/sys/block/vda/ro":               "0\n",
		"/sys/block/vda/queue/rotational": "0\n",
		"/sys/block/vda/device/vendor":    "0x1af4\n",
		"/sys/block/vda/stat":             "9067 3045 495386 25835 5393 54083 3165768 24980 0 5836 51496 0 0 0 0 37 680\n",
		"/sys/block/vda/vda1/partition":   "1\n",
		"/sys/block/dm-0/size":            "2048\n",
		"/sys/block/dm-0/dm/name":         "vg-root\n",
		"/sys/block/loop0/size":           "8\n",
	})
	m := &Module{fs: fs, statfs: statfsFixture(block)}
	got, err := m.Collect(context.Background(), nil)
	if err == nil {
		t.Error("malformed mountinfo line not reported")
	}
	f := got.(*Facts)

	var points []string
	for _, x := range f.Filesystems {
		points = append(points, x.MountPoint)
	}
	if strings.Join(points, ",") != "/,/run,/srv/My Data,/mnt/bind,/snap/core/1,/mnt/nfs,/dev/shm" {
		t.Fatalf("filesystems: %v", points)
	}
	root := f.Filesystems[0]
	if root.SizeBytes != 1100*4096 || root.UsedBytes != 900*4096 || root.UsedRatio != 0.9 || root.InodesUsedRatio != 0.5 {
		t.Errorf("root usage: %+v", root)
	}
	if f.Filesystems[6].DeviceID != "0:61" {
		t.Errorf("hidden /dev/shm mount kept: %+v", f.Filesystems[6])
	}
	if f.Filesystems[1].Error == "" || !strings.Contains(f.Filesystems[5].Error, "did not answer") {
		t.Errorf("errors: %q %q", f.Filesystems[1].Error, f.Filesystems[5].Error)
	}

	if len(f.BlockDevices) != 2 {
		t.Fatalf("block devices: %+v", f.BlockDevices)
	}
	dm, vda := f.BlockDevices[0], f.BlockDevices[1]
	if dm.DMName != "vg-root" || vda.SizeBytes != 20971520*512 || vda.Rotational || vda.Vendor != "0x1af4" || len(vda.Partitions) != 1 {
		t.Errorf("block devices: %+v %+v", dm, vda)
	}
	if vda.Stats == nil || vda.Stats.Reads != 9067 || vda.Stats.WrittenBytes != 3165768*512 || vda.Stats.IOTimeMs != 5836 {
		t.Errorf("17-field stat not parsed: %+v", vda.Stats)
	}
	if len(f.Swaps) != 1 || f.Swaps[0].SizeBytes != 1048572*1024 || f.Swaps[0].Priority != -2 {
		t.Errorf("swaps: %+v", f.Swaps)
	}

	findings := (&Module{}).Check(nil, f)
	got2 := map[string]model.Severity{}
	for _, x := range findings {
		got2[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"disk.usage /":               model.SeverityWarn,
		"disk.inodes /":              model.SeverityOK,
		"disk.unreachable /run":      model.SeverityFail,
		"disk.unreachable /mnt/nfs":  model.SeverityFail,
		"disk.readonly /srv/My Data": model.SeverityFail,
		// tmpfs: the full usage is reported, the fine inodes are not.
		"disk.usage /dev/shm": model.SeverityFail,
	}
	if len(got2) != len(want) {
		t.Errorf("findings: %v", got2)
	}
	for k, v := range want {
		if got2[k] != v {
			t.Errorf("%s: %s, want %s (all: %v)", k, got2[k], v, got2)
		}
	}
}

func TestUnescapeAndFstab(t *testing.T) {
	if unescape(`/a\040b\134c\04`) != `/a b\c\04` {
		t.Errorf("unescape: %q", unescape(`/a\040b\134c\04`))
	}
	fst := parseFstab(strings.Split(fstab, "\n"))
	if len(fst) != 2 || fst["/srv/My Data"][0] != "defaults" {
		t.Errorf("fstab: %v", fst)
	}
}
