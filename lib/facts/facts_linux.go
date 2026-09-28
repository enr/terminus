//go:build linux

package facts

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Constants
const (
	// linuxSysinfoLoadsScale has been described elsewhere as a "magic" number.
	// It reverts the calculation of "load << (SI_LOAD_SHIFT - FSHIFT)" done in the original load calculation.
	linuxSysinfoLoadsScale float64 = 65536.0
)

func (f *SystemFacts) getSysInfo(wg *sync.WaitGroup) {
	defer wg.Done()

	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// sysinfo reports memory in multiples of Unit bytes.
	unit := uint64(info.Unit)
	if unit == 0 {
		unit = 1
	}
	f.Memory.Total = uint64(info.Totalram) * unit
	f.Memory.Free = uint64(info.Freeram) * unit
	f.Memory.Shared = uint64(info.Sharedram) * unit
	f.Memory.Buffered = uint64(info.Bufferram) * unit
	if available, err := memAvailable("/proc/meminfo"); err == nil {
		f.Memory.Available = available
	} else if c.Debug {
		log.Println(err.Error())
	}

	f.Swap.Total = uint64(info.Totalswap) * unit
	f.Swap.Free = uint64(info.Freeswap) * unit

	f.Uptime = info.Uptime

	f.LoadAverage.One = fmt.Sprintf("%.2f", float64(info.Loads[0])/linuxSysinfoLoadsScale)
	f.LoadAverage.Five = fmt.Sprintf("%.2f", float64(info.Loads[1])/linuxSysinfoLoadsScale)
	f.LoadAverage.Ten = fmt.Sprintf("%.2f", float64(info.Loads[2])/linuxSysinfoLoadsScale)

	return
}

// memAvailable returns the MemAvailable value of /proc/meminfo, in bytes: the memory that can be
// used by new workloads without swapping, page cache included.
func memAvailable(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kb * 1024, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemAvailable not found in %s", path)
}

func (f *SystemFacts) getOSRelease(wg *sync.WaitGroup) {
	defer wg.Done()
	osReleaseFile, err := os.Open("/etc/os-release")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}
	defer osReleaseFile.Close()

	f.mu.Lock()
	defer f.mu.Unlock()
	scanner := bufio.NewScanner(osReleaseFile)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), "=")
		if len(columns) > 1 {
			key := columns[0]
			value := strings.Trim(strings.TrimSpace(columns[1]), `"`)
			switch key {
			case "NAME":
				f.OSRelease.Name = value
			case "ID":
				f.OSRelease.ID = value
			case "PRETTY_NAME":
				f.OSRelease.PrettyName = value
			case "VERSION":
				f.OSRelease.Version = value
			case "VERSION_ID":
				f.OSRelease.VersionID = value
			}
		}
	}

	lsbFile, err := os.Open("/etc/lsb-release")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}
	defer lsbFile.Close()

	scanner = bufio.NewScanner(lsbFile)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), "=")
		if len(columns) > 1 {
			key := columns[0]
			value := strings.Trim(strings.TrimSpace(columns[1]), `"`)
			switch key {
			case "DISTRIB_CODENAME":
				f.OSRelease.CodeName = value
			}
		}
	}

	return
}

func (f *SystemFacts) getUname(wg *sync.WaitGroup) {
	defer wg.Done()

	var buf unix.Utsname
	err := unix.Uname(&buf)
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.Domainname = charsToString(buf.Domainname)
	f.Architecture = charsToString(buf.Machine)
	f.Hostname = charsToString(buf.Nodename)
	f.Kernel.Name = charsToString(buf.Sysname)
	f.Kernel.Release = charsToString(buf.Release)
	f.Kernel.Version = charsToString(buf.Version)
	return
}
