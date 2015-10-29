// +build darwin

package facts

import (
	"bufio"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

var osxNames = map[string]string{
	"10.1":  "Panther",
	"10.2":  "Jaguar",
	"10.3":  "Panther",
	"10.4":  "Tiger",
	"10.5":  "Leopard",
	"10.6":  "Snow Leopard",
	"10.7":  "Lion",
	"10.8":  "Mountain Lion",
	"10.9":  "Mavericks",
	"10.10": "Yosemite",
	"10.11": "El Capitan",
}

func (f *SystemFacts) getSysInfo(wg *sync.WaitGroup) {
	defer wg.Done()
	// TODO: Check if alternative to SysInfo exists
	/*var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		log.Println(err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.Memory.Total = info.Totalram
	f.Memory.Free = info.Freeram
	f.Memory.Shared = info.Sharedram
	f.Memory.Buffered = info.Bufferram

	f.Swap.Total = info.Totalswap
	f.Swap.Free = info.Freeswap

	f.Uptime = info.Uptime

	f.LoadAverage.One = info.Loads[0]
	f.LoadAverage.Five = info.Loads[1]
	f.LoadAverage.Ten = info.Loads[2]
	*/
	return
}

func (f *SystemFacts) getUname(wg *sync.WaitGroup) {
	defer wg.Done()
	cmd := exec.Command("uname", "-a")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Println(err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		log.Println(err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), " ")
		if len(columns) > 14 {
			fullhost := strings.Split(columns[1], ".")
			f.Hostname = fullhost[0]
			f.Domainname = fullhost[1]
			f.Architecture = columns[14]
			f.Kernel.Name = columns[3]
			f.Kernel.Version = columns[2]
			f.Kernel.Release = columns[13]
		}
	}
	return
}

func (f *SystemFacts) getOSRelease(wg *sync.WaitGroup) {
	defer wg.Done()
	cmd := exec.Command("sw_vers")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Println(err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		log.Println(err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), ":")
		if len(columns) > 1 {
			key := columns[0]
			value := strings.Trim(strings.TrimSpace(columns[1]), `"`)
			switch key {
			case "ProductName":
				f.OSRelease.Name = value
			case "ID":
				f.OSRelease.ID = value
			case "ProductVersion":
				f.OSRelease.Version = value
				f.OSRelease.PrettyName = getOSXName(value)
			case "BuildVersion":
				f.OSRelease.VersionID = value
			}
		}
	}
	return
}

func getOSXName(version string) string {
	reg, err := regexp.Compile(`(10.[0-9]{1,2})(.\d)?`)
	if err != nil {
		return ""
	}
	vers := reg.FindStringSubmatch(version)
	return osxNames[vers[1]]
}
