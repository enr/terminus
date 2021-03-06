package facts

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/lib/config"
)

// Vars
var (
	c config.Config
)

// SystemFacts holds the system facts.
type SystemFacts struct {
	Architecture string
	BootID       string
	DMI          DMI
	Date         Date
	Domainname   string
	Hostname     string
	Network      Network
	Kernel       Kernel
	MachineID    string
	Memory       Memory
	OSRelease    OSRelease
	Swap         Swap
	Uptime       int64
	LoadAverage  LoadAverage
	FileSystems  FileSystems
	BlockDevices BlockDevices
	Processors   Processors

	mu sync.Mutex
}

// DMI holds the DMI / Hardware Information.
type DMI struct {
	BIOSDate        string
	BIOSVendor      string
	BIOSVersion     string
	ChassisAssetTag string
	ChassisSerial   string
	ChassisType     string
	ChassisVendor   string
	ChassisVersion  string
	ProductName     string
	ProductSerial   string
	ProductUUID     string
	ProductVersion  string
	SysVendor       string
}

// LoadAverage holds the load average facts.
type LoadAverage struct {
	One  string
	Five string
	Ten  string
}

// Date holds the date facts.
type Date struct {
	Unix     int64
	UTC      string
	Timezone string
	Offset   int
}

// Swap holds the swap facts.
type Swap struct {
	Total uint64
	Free  uint64
}

// OSRelease holds the OS release facts.
type OSRelease struct {
	Name       string
	ID         string
	PrettyName string
	Version    string
	VersionID  string
	CodeName   string
}

// Kernel holds the kernel facts.
type Kernel struct {
	Name    string
	Release string
	Version string
}

// Memory holds the memory facts.
type Memory struct {
	Total    uint64
	Free     uint64
	Shared   uint64
	Buffered uint64
}

// Network holds the network facts.
type Network struct {
	Interfaces Interfaces
}

// Interfaces holds the interface facts.
type Interfaces map[string]Interface

// Interface holds facts for a single interface.
type Interface struct {
	Name         string
	Index        int
	HardwareAddr string
	IPAddresses  []string
	IP4Addresses []IP4Address
	IP6Addresses []IP6Address
}

// IP4Address IP 4 address
type IP4Address struct {
	CIDR    string
	IP      string
	Netmask string
}

// IP6Address IP 6 address
type IP6Address struct {
	CIDR   string
	IP     string
	Prefix int
}

// FileSystems holds the Filesystem facts.
type FileSystems map[string]FileSystem

// FileSystem holds facts for a filesystem (man fstab).
type FileSystem struct {
	Device     string
	MountPoint string
	Type       string
	Options    []string
	DumpFreq   uint64
	PassNo     uint64
}

// BlockDevices holds the BlockDevice facts.
type BlockDevices map[string]BlockDevice

// BlockDevice holds facts for a block device
type BlockDevice struct {
	Device       string
	Size         uint64
	Vendor       string
	ReadIOs      uint64
	ReadMerges   uint64
	ReadSectors  uint64
	ReadTicks    uint64
	WriteIOs     uint64
	WriteMerges  uint64
	WriteSectors uint64
	WriteTicks   uint64
	InFlight     uint64
	IOTicks      uint64
	TimeInQueue  uint64
}

// Processors holds facts about the Processors / CPUs.
type Processors struct {
	Count     int
	Processor []Processor
}

// Processor holds facts about a single Processor / CPU.
type Processor struct {
	VendorID  string
	CPUFamily uint64
	Model     uint64
	ModelName string
	MHz       string
	CacheSize string
	CPUCores  uint64
	Flags     []string
	BogoMips  float64
}

// GetFacts returns system facts
func GetFacts(cfg config.Config) *Facts {
	c = cfg
	f := New()
	if c.Path == "" || (c.Path != "" && strings.Contains(c.Path, "System")) {
		systemFacts := getSystemFacts()
		f.add("System", systemFacts)
	}
	processExternalFacts(c, f)
	return f
}

func getSystemFacts() *SystemFacts {
	facts := new(SystemFacts)
	var wg sync.WaitGroup

	wg.Add(11)
	go facts.getOSRelease(&wg)
	go facts.getInterfaces(&wg)
	go facts.getBootID(&wg)
	go facts.getMachineID(&wg)
	go facts.getUname(&wg)
	go facts.getSysInfo(&wg)
	go facts.getDate(&wg)
	go facts.getFileSystems(&wg)
	go facts.getDMI(&wg)
	go facts.getBlockDevices(&wg)
	go facts.getProcessors(&wg)

	wg.Wait()
	return facts
}

func (f *SystemFacts) getDate(wg *sync.WaitGroup) {
	defer wg.Done()

	now := time.Now()
	f.Date.Unix = now.Unix()
	f.Date.UTC = now.UTC().String()
	f.Date.Timezone, f.Date.Offset = now.Zone()

	return
}

func (f *SystemFacts) getMachineID(wg *sync.WaitGroup) {
	defer wg.Done()
	machineIDFile, err := os.Open("/etc/machine-id")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}
	defer machineIDFile.Close()
	data, err := ioutil.ReadAll(machineIDFile)
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.MachineID = strings.TrimSpace(string(data))
	return
}

func (f *SystemFacts) getBootID(wg *sync.WaitGroup) {
	defer wg.Done()
	bootIDFile, err := os.Open("/proc/sys/kernel/random/boot_id")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}
	defer bootIDFile.Close()
	data, err := ioutil.ReadAll(bootIDFile)
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.BootID = strings.TrimSpace(string(data))
	return
}

func (f *SystemFacts) getInterfaces(wg *sync.WaitGroup) {
	defer wg.Done()
	ls, err := net.Interfaces()
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	m := make(Interfaces)
	for _, i := range ls {
		ipaddreses := make([]string, 0)
		ip4addrs := make([]IP4Address, 0)
		ip6addrs := make([]IP6Address, 0)

		addrs, err := i.Addrs()
		if err != nil {
			if c.Debug {
				log.Println(err.Error())
			}
			return
		}
		for _, ip := range addrs {
			cidr := ip.String()
			ipaddreses = append(ipaddreses, cidr)
			ip, ipnet, _ := net.ParseCIDR(cidr)
			if ip.To4() != nil {
				ip4addrs = append(ip4addrs, IP4Address{cidr, ip.String(), toNetmask(ipnet.Mask)})
				continue
			}
			if ip.To16() != nil {
				ones, _ := ipnet.Mask.Size()
				ip6addrs = append(ip6addrs, IP6Address{cidr, ip.String(), ones})
			}
		}
		m[i.Name] = Interface{
			Name:         i.Name,
			Index:        i.Index,
			HardwareAddr: i.HardwareAddr.String(),
			IPAddresses:  ipaddreses,
			IP4Addresses: ip4addrs,
			IP6Addresses: ip6addrs,
		}
	}
	f.Network.Interfaces = m
	return
}

func toNetmask(m net.IPMask) string {
	return fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3])
}

func (f *SystemFacts) getFileSystems(wg *sync.WaitGroup) {
	defer wg.Done()

	mtab, err := ioutil.ReadFile("/etc/mtab")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}

	fsMap := make(FileSystems)

	f.mu.Lock()
	defer f.mu.Unlock()

	s := bufio.NewScanner(bytes.NewBuffer(mtab))
	for s.Scan() {
		line := s.Text()
		if string(line[0]) == "#" {
			continue
		}
		fields := strings.Fields(s.Text())
		fs := FileSystem{}
		fs.Device = fields[0]
		fs.MountPoint = fields[1]
		fs.Type = fields[2]
		fs.Options = strings.Split(fields[3], ",")
		dumpFreq, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			if c.Debug {
				log.Println(err.Error())
			}
			return
		}
		fs.DumpFreq = dumpFreq

		passNo, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			if c.Debug {
				log.Println(err.Error())
			}
			return
		}
		fs.PassNo = passNo

		fsMap[fs.Device] = fs
	}

	f.FileSystems = fsMap
	return
}

func (f *SystemFacts) getDMI(wg *sync.WaitGroup) {
	defer wg.Done()
	f.mu.Lock()
	defer f.mu.Unlock()

	var err error
	if f.DMI.BIOSDate, err = readFileAndReturnValue("/sys/class/dmi/id/bios_date"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.BIOSVendor, err = readFileAndReturnValue("/sys/class/dmi/id/bios_vendor"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.BIOSVersion, err = readFileAndReturnValue("/sys/class/dmi/id/bios_version"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ChassisAssetTag, err = readFileAndReturnValue("/sys/class/dmi/id/chassis_asset_tag"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ChassisSerial, err = readFileAndReturnValue("/sys/class/dmi/id/chassis_serial"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ChassisVendor, err = readFileAndReturnValue("/sys/class/dmi/id/chassis_vendor"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ChassisVersion, err = readFileAndReturnValue("/sys/class/dmi/id/chassis_version"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ProductName, err = readFileAndReturnValue("/sys/class/dmi/id/product_name"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ProductSerial, err = readFileAndReturnValue("/sys/class/dmi/id/product_serial"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ProductUUID, err = readFileAndReturnValue("/sys/class/dmi/id/product_uuid"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.ProductVersion, err = readFileAndReturnValue("/sys/class/dmi/id/product_version"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	if f.DMI.SysVendor, err = readFileAndReturnValue("/sys/class/dmi/id/sys_vendor"); err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	return
}

func (f *SystemFacts) getBlockDevices(wg *sync.WaitGroup) {
	defer wg.Done()

	d, err := os.Open("/sys/block")
	if err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}
	defer d.Close()

	files, err := d.Readdir(0)
	if err != nil {
		if c.Debug {
			log.Println(err)
		}
		return
	}

	bdMap := make(BlockDevices)
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, fi := range files {
		path := fmt.Sprintf("/sys/block/%s/device", fi.Name())
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			bd := BlockDevice{}
			bd.Device = fi.Name()

			sizePath := fmt.Sprintf("/sys/block/%s/size", fi.Name())
			size, err := readFileAndReturnValue(sizePath)
			if err != nil {
				if c.Debug {
					log.Println(err)
				}
				return
			}

			bd.Size, _ = strconv.ParseUint(size, 10, 64)

			vendorPath := fmt.Sprintf("/sys/block/%s/device/vendor", fi.Name())
			if bd.Vendor, err = readFileAndReturnValue(vendorPath); err != nil {
				if c.Debug {
					log.Println(err)
				}
				return
			}

			statPath := fmt.Sprintf("/sys/block/%s/stat", fi.Name())
			sf, err := os.Open(statPath)
			if err != nil {
				if c.Debug {
					log.Println(err)
				}
			}
			defer sf.Close()

			scanner := bufio.NewScanner(sf)
			for scanner.Scan() {
				columns := strings.Fields(scanner.Text())
				if len(columns) == 11 {
					bd.ReadIOs, _ = strconv.ParseUint(columns[0], 10, 64)
					bd.ReadMerges, _ = strconv.ParseUint(columns[1], 10, 64)
					bd.ReadSectors, _ = strconv.ParseUint(columns[2], 10, 64)
					bd.ReadTicks, _ = strconv.ParseUint(columns[3], 10, 64)
					bd.WriteIOs, _ = strconv.ParseUint(columns[4], 10, 64)
					bd.WriteMerges, _ = strconv.ParseUint(columns[5], 10, 64)
					bd.WriteSectors, _ = strconv.ParseUint(columns[6], 10, 64)
					bd.WriteTicks, _ = strconv.ParseUint(columns[7], 10, 64)
					bd.InFlight, _ = strconv.ParseUint(columns[8], 10, 64)
					bd.IOTicks, _ = strconv.ParseUint(columns[9], 10, 64)
					bd.TimeInQueue, _ = strconv.ParseUint(columns[10], 10, 64)
				}
			}
			bdMap[bd.Device] = bd
		}
	}

	f.BlockDevices = bdMap

	return
}

func (f *SystemFacts) getProcessors(wg *sync.WaitGroup) {
	defer wg.Done()
	processorFile, err := os.Open("/proc/cpuinfo")
	if err != nil {
		if c.Debug {
			log.Println(err.Error())
		}
		return
	}
	defer processorFile.Close()

	f.mu.Lock()
	defer f.mu.Unlock()

	var cpuCount int
	procs := []Processor{}
	p := Processor{}

	scanner := bufio.NewScanner(processorFile)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), ":")
		if len(columns) > 1 {
			key := strings.TrimSpace(columns[0])
			value := strings.TrimSpace(columns[1])

			switch key {
			case "processor":
				cpuCount++
			case "vendor_id":
				p.VendorID = value
			case "cpu family":
				p.CPUFamily, _ = strconv.ParseUint(value, 10, 64)
			case "model":
				p.Model, _ = strconv.ParseUint(value, 10, 64)
			case "model name":
				p.ModelName = value
			case "cpu MHz":
				p.MHz = value
			case "cache size":
				p.CacheSize = value
			case "cpu cores":
				p.CPUCores, _ = strconv.ParseUint(value, 10, 64)
			case "flags":
				value := strings.Fields(columns[1])
				p.Flags = value
			case "bogomips":
				p.BogoMips, _ = strconv.ParseFloat(value, 64)
			}
		} else {
			procs = append(procs, p)
			p = Processor{}
		}
	}

	f.Processors.Count = cpuCount
	f.Processors.Processor = procs

	return
}
