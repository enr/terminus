// +build windows

package facts

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/enr/runcmd"
)

func (f *SystemFacts) getSysInfo(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getSysInfo NOT fully implemented")

	f.mu.Lock()
	defer f.mu.Unlock()

	var err error
	f.Memory.Total, err = getWmicInfoUint("TotalVisibleMemorySize")
	if err != nil {
		log.Println(err.Error())
		return
	}
	f.Memory.Free, err = getWmicInfoUint("FreePhysicalMemory")
	if err != nil {
		log.Println(err.Error())
		return
	}
	f.Memory.Shared = 0
	f.Memory.Buffered = 0

	f.Swap.Total = 0
	f.Swap.Free = 0

	f.Uptime = 0

	f.LoadAverage.One = ""
	f.LoadAverage.Five = ""
	f.LoadAverage.Ten = ""

	return
}

func (f *SystemFacts) getOSRelease(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getOSRelease NOT fully implemented")

	f.mu.Lock()
	defer f.mu.Unlock()

	dll := syscall.MustLoadDLL("kernel32.dll")
	p, err := dll.FindProc("GetVersion")
	if err != nil {
		log.Println(err.Error())
		return
	}
	// The returned error is always non-nil
	v, _, err := p.Call()
	if v == 0 && err != nil {
		log.Println(err.Error())
		return
	}
	major := int(byte(v))
	minor := int(uint8(v >> 8))
	build := int(uint16(v >> 16))

	f.OSRelease.Name = "windows"
	f.OSRelease.ID = "windows"
	f.OSRelease.PrettyName = fmt.Sprintf("Windows version %d.%d (Build %d)", major, minor, build)
	f.OSRelease.Version = fmt.Sprintf("%d", major)
	f.OSRelease.VersionID = fmt.Sprintf("%d.%d.%d", major, minor, build)
	f.OSRelease.CodeName = ""
	return
}

func (f *SystemFacts) getUname(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println("getUname NOT fully implemented")

	f.mu.Lock()
	defer f.mu.Unlock()

	hostname, err := os.Hostname()
	if err != nil {
		log.Println(err.Error())
		return
	}

	f.Domainname = ""
	f.Architecture = runtime.GOARCH
	f.Hostname = hostname
	f.Kernel.Name = ""
	f.Kernel.Release = ""
	f.Kernel.Version = ""
	return
}

func isExecutable(fi os.FileInfo) bool {
	if strings.HasSuffix(fi.Name(), ".json") {
		return false
	}
	m := fi.Mode()
	return !m.IsDir()
}

/*
c:\windows>wmic OS get FreePhysicalMemory /Value
FreePhysicalMemory=12324404

c:\windows>wmic OS get TotalVisibleMemorySize /Value
TotalVisibleMemorySize=16677948
*/
func getWmicInfoUint(option string) (uint64, error) {
	strval, err := getWmicInfo(option)
	if err != nil {
		return 0, err
	}
	uintval, err := strconv.ParseUint(strval, 10, 64)
	if err != nil {
		return 0, err
	}
	return uintval, nil
}

func getWmicInfo(option string) (string, error) {
	command := &runcmd.Command{
		CommandLine: fmt.Sprintf("wmic OS get %s /Value", option),
	}
	res := command.Run()
	if !res.Success() {
		return "", res.Error()
	}
	value := extractWmicValue(res.Stdout().String())
	return value, nil
}

func extractWmicValue(res string) string {
	if res == "" {
		return ""
	}
	trim := strings.TrimSpace(res)
	tokens := strings.SplitAfterN(trim, "=", 2)
	if len(tokens) > 1 {
		return tokens[1]
	}
	return ""
}
