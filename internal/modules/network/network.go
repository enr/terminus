// Package network is the core module with interfaces, default routes, DNS configuration and
// listening sockets.
package network

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "network"

// Facts about the network.
type Facts struct {
	Interfaces    []Interface `json:"interfaces"`
	DefaultRoutes []Route     `json:"default_routes"`
	DNS           DNS         `json:"dns"`
	Listeners     []Listener  `json:"listeners"`
}

// Interface is a network interface.
type Interface struct {
	Name         string    `json:"name"`
	Index        int       `json:"index"`
	HardwareAddr string    `json:"hardware_addr,omitempty"`
	MTU          int       `json:"mtu"`
	Up           bool      `json:"up"`
	OperState    string    `json:"oper_state,omitempty"`
	Virtual      bool      `json:"virtual"`
	SpeedMbps    int       `json:"speed_mbps,omitempty"`
	Addresses    []Address `json:"addresses"`
	Stats        *IfStats  `json:"stats,omitempty"`
}

// Address is an IP address of an interface.
type Address struct {
	Family string `json:"family"` // inet or inet6
	IP     string `json:"ip"`
	Prefix int    `json:"prefix"`
	CIDR   string `json:"cidr"`
}

// IfStats are the interface counters since boot.
type IfStats struct {
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

// Route is a default route.
type Route struct {
	Family    string `json:"family"`
	Gateway   string `json:"gateway"`
	Interface string `json:"interface"`
	Metric    int    `json:"metric"`
}

// DNS is the resolver configuration.
type DNS struct {
	Nameservers []string `json:"nameservers"`
	Search      []string `json:"search,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Resolver is "systemd-resolved" when resolv.conf points to its local stub.
	Resolver string `json:"resolver,omitempty"`
	// UpstreamNameservers are the servers systemd-resolved forwards to.
	UpstreamNameservers []string `json:"upstream_nameservers,omitempty"`
}

// Module collects the network facts.
type Module struct {
	fs         hostfs.FS
	interfaces func() ([]net.Interface, error)
	addrs      func(net.Interface) ([]net.Addr, error)
}

// New returns the network module reading the running machine.
func New() *Module {
	return &Module{
		fs:         hostfs.Host,
		interfaces: net.Interfaces,
		addrs:      func(i net.Interface) ([]net.Addr, error) { return i.Addrs() },
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (m *Module) Collect(_ context.Context, _ *module.Env) (any, error) {
	var errs []error
	f := &Facts{}
	var err error
	if f.Interfaces, err = m.readInterfaces(); err != nil {
		errs = append(errs, err)
	}
	if f.DefaultRoutes, err = readDefaultRoutes(m.fs); err != nil {
		errs = append(errs, err)
	}
	if f.DNS, err = readDNS(m.fs); err != nil {
		errs = append(errs, err)
	}
	if f.Listeners, err = readListeners(m.fs); err != nil {
		errs = append(errs, err)
	}
	return f, errors.Join(errs...)
}

func (m *Module) readInterfaces() ([]Interface, error) {
	ifs, err := m.interfaces()
	if err != nil {
		return nil, err
	}
	var errs []error
	out := make([]Interface, 0, len(ifs))
	for _, i := range ifs {
		it := Interface{
			Name:         i.Name,
			Index:        i.Index,
			HardwareAddr: i.HardwareAddr.String(),
			MTU:          i.MTU,
			Up:           i.Flags&net.FlagUp != 0,
			Addresses:    []Address{},
		}
		addrs, err := m.addrs(i)
		if err != nil {
			errs = append(errs, err)
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			family := "inet6"
			if ipn.IP.To4() != nil {
				family = "inet"
			}
			it.Addresses = append(it.Addresses, Address{Family: family, IP: ipn.IP.String(), Prefix: ones, CIDR: ipn.String()})
		}
		dir := "/sys/class/net/" + i.Name + "/"
		it.OperState, _ = m.fs.ReadString(dir + "operstate")
		it.Virtual = m.fs.Exists("/sys/devices/virtual/net/" + i.Name)
		// speed is -1 or unreadable (EINVAL) for virtual and down interfaces.
		if s, err := m.fs.ReadString(dir + "speed"); err == nil {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				it.SpeedMbps = n
			}
		}
		if m.fs.Exists(dir + "statistics") {
			st := &IfStats{}
			for name, p := range map[string]*uint64{
				"rx_bytes": &st.RxBytes, "tx_bytes": &st.TxBytes,
				"rx_errors": &st.RxErrors, "tx_errors": &st.TxErrors,
				"rx_dropped": &st.RxDropped, "tx_dropped": &st.TxDropped,
			} {
				*p, _ = m.fs.ReadUint(dir + "statistics/" + name)
			}
			it.Stats = st
		}
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Index < out[b].Index })
	return out, errors.Join(errs...)
}

// readDefaultRoutes reads the default routes of /proc/net/route and /proc/net/ipv6_route.
func readDefaultRoutes(fs hostfs.FS) ([]Route, error) {
	routes := []Route{}
	lines, err := fs.Lines("/proc/net/route")
	if err != nil {
		return routes, err
	}
	for i, l := range lines {
		f := strings.Fields(l)
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if i == 0 || len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		gw, err := parseHexIP(f[2])
		if err != nil {
			continue
		}
		metric, _ := strconv.Atoi(f[6])
		routes = append(routes, Route{Family: "inet", Gateway: gw.String(), Interface: f[0], Metric: metric})
	}
	lines, err = fs.Lines("/proc/net/ipv6_route")
	if err != nil {
		if hostfs.IsNotExist(err) { // IPv6 disabled
			return routes, nil
		}
		return routes, err
	}
	for _, l := range lines {
		f := strings.Fields(l)
		// dest prefix src srcprefix nexthop metric refcnt use flags iface
		if len(f) < 10 || f[0] != strings.Repeat("0", 32) || f[1] != "00" || f[9] == "lo" {
			continue
		}
		gw, err := parseHexIPv6BigEndian(f[4])
		if err != nil {
			continue
		}
		metric, _ := strconv.ParseInt(f[5], 16, 64)
		routes = append(routes, Route{Family: "inet6", Gateway: gw.String(), Interface: f[9], Metric: int(metric)})
	}
	return routes, nil
}

// resolvedStub is the local address of the systemd-resolved stub resolver.
const resolvedStub = "127.0.0.53"

func readDNS(fs hostfs.FS) (DNS, error) {
	d := DNS{Nameservers: []string{}}
	lines, err := fs.Lines("/etc/resolv.conf")
	if err != nil {
		return d, err
	}
	d.Nameservers, d.Search, d.Options = parseResolvConf(lines)
	if len(d.Nameservers) > 0 && (d.Nameservers[0] == resolvedStub || d.Nameservers[0] == "127.0.0.54") {
		d.Resolver = "systemd-resolved"
		if up, err := fs.Lines("/run/systemd/resolve/resolv.conf"); err == nil {
			d.UpstreamNameservers, _, _ = parseResolvConf(up)
		}
	}
	return d, nil
}

func parseResolvConf(lines []string) (servers, search, options []string) {
	servers = []string{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
			continue
		}
		switch f[0] {
		case "nameserver":
			servers = append(servers, f[1])
		case "search", "domain":
			search = append(search, f[1:]...)
		case "options":
			options = append(options, f[1:]...)
		}
	}
	return servers, search, options
}
