package storage

import (
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
)

// sectorSize is the unit of the sizes and counters in /sys/block, whatever the disk sector size.
const sectorSize = 512

// BlockDevice is a disk (or a device-mapper/md device) from /sys/block.
type BlockDevice struct {
	Name string `json:"name"`
	// DMName is the device-mapper name (LVM volumes, LUKS), when the device is one.
	DMName     string      `json:"dm_name,omitempty"`
	SizeBytes  uint64      `json:"size_bytes"`
	ReadOnly   bool        `json:"read_only"`
	Removable  bool        `json:"removable"`
	Rotational bool        `json:"rotational"`
	Vendor     string      `json:"vendor,omitempty"`
	Model      string      `json:"model,omitempty"`
	Serial     string      `json:"serial,omitempty"`
	Partitions []string    `json:"partitions,omitempty"`
	Stats      *BlockStats `json:"stats,omitempty"`
}

// BlockStats are the I/O counters since boot (Documentation/block/stat.rst).
type BlockStats struct {
	Reads        uint64 `json:"reads"`
	ReadBytes    uint64 `json:"read_bytes"`
	Writes       uint64 `json:"writes"`
	WrittenBytes uint64 `json:"written_bytes"`
	InFlight     uint64 `json:"in_flight"`
	IOTimeMs     uint64 `json:"io_time_ms"`
}

// readBlockDevices lists /sys/block, leaving out loop and ram devices.
func readBlockDevices(fs hostfs.FS) ([]BlockDevice, error) {
	entries, err := fs.ReadDir("/sys/block")
	if err != nil {
		if hostfs.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var devs []BlockDevice
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		dir := "/sys/block/" + name + "/"
		str := func(p string) string {
			v, _ := fs.ReadString(dir + p)
			return v
		}
		flag := func(p string) bool { return str(p) == "1" }

		d := BlockDevice{
			Name:       name,
			DMName:     str("dm/name"),
			ReadOnly:   flag("ro"),
			Removable:  flag("removable"),
			Rotational: flag("queue/rotational"),
			Vendor:     str("device/vendor"),
			Model:      str("device/model"),
			Serial:     str("device/serial"),
		}
		if sectors, err := fs.ReadUint(dir + "size"); err == nil {
			d.SizeBytes = sectors * sectorSize
		}
		if s, err := fs.ReadString(dir + "stat"); err == nil {
			d.Stats = parseBlockStat(s)
		}
		if parts, err := fs.ReadDir(dir); err == nil {
			for _, p := range parts {
				if strings.HasPrefix(p.Name(), name) && fs.Exists(dir+p.Name()+"/partition") {
					d.Partitions = append(d.Partitions, p.Name())
				}
			}
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// parseBlockStat parses /sys/block/<dev>/stat, which has 11, 15 or 17 fields depending on the
// kernel version (v1 accepted only 11 and reported zeros on recent kernels).
func parseBlockStat(s string) *BlockStats {
	fields := strings.Fields(s)
	if len(fields) < 11 {
		return nil
	}
	n := make([]uint64, 11)
	for i := range n {
		n[i], _ = strconv.ParseUint(fields[i], 10, 64)
	}
	return &BlockStats{
		Reads:        n[0],
		ReadBytes:    n[2] * sectorSize,
		Writes:       n[4],
		WrittenBytes: n[6] * sectorSize,
		InFlight:     n[8],
		IOTimeMs:     n[9],
	}
}
