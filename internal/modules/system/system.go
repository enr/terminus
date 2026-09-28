// Package system is the core module describing the machine itself: host name, kernel, OS
// release, identifiers, uptime, time zone, hardware (DMI) and virtualization.
package system

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "system"

// Facts about the machine.
type Facts struct {
	Hostname       string         `json:"hostname"`
	Domainname     string         `json:"domainname,omitempty"`
	Architecture   string         `json:"architecture"`
	Kernel         Kernel         `json:"kernel"`
	OS             OSRelease      `json:"os"`
	MachineID      string         `json:"machine_id,omitempty"`
	BootID         string         `json:"boot_id,omitempty"`
	UptimeSeconds  float64        `json:"uptime_seconds"`
	BootTime       time.Time      `json:"boot_time"`
	Time           Time           `json:"time"`
	DMI            *DMI           `json:"dmi,omitempty"`
	Virtualization Virtualization `json:"virtualization"`
}

// Kernel identification (uname).
type Kernel struct {
	Name    string `json:"name"`
	Release string `json:"release"`
	Version string `json:"version"`
}

// OSRelease holds the main fields of os-release(5).
type OSRelease struct {
	ID              string   `json:"id"`
	IDLike          []string `json:"id_like,omitempty"`
	Name            string   `json:"name"`
	PrettyName      string   `json:"pretty_name"`
	Version         string   `json:"version,omitempty"`
	VersionID       string   `json:"version_id,omitempty"`
	VersionCodename string   `json:"version_codename,omitempty"`
	Variant         string   `json:"variant,omitempty"`
}

// Time is the clock and time zone of the machine.
type Time struct {
	Now          time.Time `json:"now"`
	Timezone     string    `json:"timezone,omitempty"`
	Abbreviation string    `json:"abbreviation"`
	// UTCOffset is the offset from UTC, as in RFC 3339 (+02:00).
	UTCOffset string `json:"utc_offset"`
}

// DMI holds hardware information from /sys/class/dmi/id. Serial numbers are readable by root only.
type DMI struct {
	SysVendor       string `json:"sys_vendor,omitempty"`
	ProductName     string `json:"product_name,omitempty"`
	ProductVersion  string `json:"product_version,omitempty"`
	ProductSerial   string `json:"product_serial,omitempty"`
	ProductUUID     string `json:"product_uuid,omitempty"`
	BoardVendor     string `json:"board_vendor,omitempty"`
	BoardName       string `json:"board_name,omitempty"`
	BIOSVendor      string `json:"bios_vendor,omitempty"`
	BIOSVersion     string `json:"bios_version,omitempty"`
	BIOSDate        string `json:"bios_date,omitempty"`
	ChassisVendor   string `json:"chassis_vendor,omitempty"`
	ChassisType     string `json:"chassis_type,omitempty"`
	ChassisSerial   string `json:"chassis_serial,omitempty"`
	ChassisAssetTag string `json:"chassis_asset_tag,omitempty"`
}

// Virtualization tells whether the machine is a VM or a container.
type Virtualization struct {
	// Type is "none", "vm" or "container".
	Type string `json:"type"`
	// Name of the technology (kvm, vmware, podman, docker, ...), as systemd-detect-virt names it.
	Name string `json:"name,omitempty"`
	// Source is how it was detected.
	Source string `json:"source"`
}

// Module collects the system facts.
type Module struct {
	fs    hostfs.FS
	uname func() (Kernel, string, string, string, error)
	now   func() time.Time
}

// New returns the system module reading the running machine.
func New() *Module {
	return &Module{fs: hostfs.Host, uname: uname, now: time.Now}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	f := &Facts{}
	var errs []error

	kernel, arch, host, domain, err := m.uname()
	if err != nil {
		errs = append(errs, fmt.Errorf("uname: %w", err))
	}
	f.Kernel, f.Architecture, f.Hostname = kernel, arch, host
	if domain != "(none)" {
		f.Domainname = domain
	}

	if f.OS, err = readOSRelease(m.fs); err != nil {
		errs = append(errs, err)
	}
	if f.MachineID, err = m.fs.ReadString("/etc/machine-id"); err != nil && !hostfs.IsIgnorable(err) {
		errs = append(errs, err)
	}
	if f.BootID, err = m.fs.ReadString("/proc/sys/kernel/random/boot_id"); err != nil && !hostfs.IsIgnorable(err) {
		errs = append(errs, err)
	}

	now := m.now()
	if up, err := readUptime(m.fs); err != nil {
		errs = append(errs, err)
	} else {
		f.UptimeSeconds = up
		f.BootTime = now.Add(-time.Duration(up * float64(time.Second))).UTC().Truncate(time.Second)
	}
	f.Time = readTime(m.fs, now)
	f.DMI = readDMI(m.fs)

	var r runner.Runner
	if env != nil {
		r = env.Runner
	}
	f.Virtualization = detectVirtualization(ctx, r, m.fs, f.DMI)

	return f, errors.Join(errs...)
}

func readOSRelease(fs hostfs.FS) (OSRelease, error) {
	lines, err := fs.Lines("/etc/os-release")
	if hostfs.IsNotExist(err) {
		lines, err = fs.Lines("/usr/lib/os-release")
	}
	if err != nil {
		return OSRelease{}, fmt.Errorf("os-release: %w", err)
	}
	kv := hostfs.ParseKeyValue(lines)
	o := OSRelease{
		ID:              kv["ID"],
		Name:            kv["NAME"],
		PrettyName:      kv["PRETTY_NAME"],
		Version:         kv["VERSION"],
		VersionID:       kv["VERSION_ID"],
		VersionCodename: kv["VERSION_CODENAME"],
		Variant:         kv["VARIANT"],
	}
	if like := strings.Fields(kv["ID_LIKE"]); len(like) > 0 {
		o.IDLike = like
	}
	return o, nil
}

func readUptime(fs hostfs.FS) (float64, error) {
	s, err := fs.ReadString("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, fmt.Errorf("/proc/uptime: empty")
	}
	up, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("/proc/uptime: %w", err)
	}
	return up, nil
}

func readTime(fs hostfs.FS, now time.Time) Time {
	abbr, _ := now.Zone()
	t := Time{Now: now.UTC().Truncate(time.Second), Abbreviation: abbr, UTCOffset: now.Format("-07:00")}
	if tz, err := fs.ReadString("/etc/timezone"); err == nil && tz != "" {
		t.Timezone = tz
	} else if link, err := fs.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(link, "zoneinfo/"); ok {
			t.Timezone = name
		}
	}
	return t
}

func readDMI(fs hostfs.FS) *DMI {
	const dir = "/sys/class/dmi/id/"
	if !fs.Exists(dir) {
		return nil
	}
	// Each file is optional: a missing or unreadable one must not hide the others.
	read := func(name string) string {
		v, _ := fs.ReadString(dir + name)
		return v
	}
	return &DMI{
		SysVendor:       read("sys_vendor"),
		ProductName:     read("product_name"),
		ProductVersion:  read("product_version"),
		ProductSerial:   read("product_serial"),
		ProductUUID:     read("product_uuid"),
		BoardVendor:     read("board_vendor"),
		BoardName:       read("board_name"),
		BIOSVendor:      read("bios_vendor"),
		BIOSVersion:     read("bios_version"),
		BIOSDate:        read("bios_date"),
		ChassisVendor:   read("chassis_vendor"),
		ChassisType:     read("chassis_type"),
		ChassisSerial:   read("chassis_serial"),
		ChassisAssetTag: read("chassis_asset_tag"),
	}
}
