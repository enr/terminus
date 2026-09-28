package network

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
)

// Listener is a socket accepting connections (TCP) or datagrams (UDP) on the host network
// namespace. Containers with their own network namespace show up through the process that
// publishes their ports (conmon, rootlessport, pasta, ...).
type Listener struct {
	Protocol string `json:"protocol"` // tcp or udp
	Family   string `json:"family"`   // inet or inet6
	Address  string `json:"address"`
	Port     int    `json:"port"`
	// Scope is "any" (0.0.0.0 or ::), "loopback" or "address" (a specific address).
	Scope string `json:"scope"`
	UID   int    `json:"uid"`
	// PID and Process are known only for the processes the current user can inspect.
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	inode   string
}

// socket states in /proc/net/*: TCP_LISTEN, and TCP_CLOSE for bound UDP sockets.
const (
	stateListen = "0A"
	stateClose  = "07"
)

// ReadListeners reads the listening sockets of the host with their processes (the firewall module
// compares them with what the firewall lets in).
func ReadListeners(fs hostfs.FS) ([]Listener, error) {
	var ls []Listener
	var errs []error
	for _, src := range []struct{ file, proto, family, state string }{
		{"tcp", "tcp", "inet", stateListen},
		{"tcp6", "tcp", "inet6", stateListen},
		{"udp", "udp", "inet", stateClose},
		{"udp6", "udp", "inet6", stateClose},
	} {
		lines, err := fs.Lines("/proc/net/" + src.file)
		if err != nil {
			if !hostfs.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		parsed, err := parseSockets(lines, src.proto, src.family, src.state)
		if err != nil {
			errs = append(errs, err)
		}
		ls = append(ls, parsed...)
	}
	ls = dedupe(ls)
	attachProcesses(fs, ls)
	sort.Slice(ls, func(i, j int) bool {
		a, b := ls[i], ls[j]
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol // tcp first
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Address < b.Address
	})
	if ls == nil {
		ls = []Listener{}
	}
	return ls, errors.Join(errs...)
}

// parseSockets parses a /proc/net/{tcp,udp}{,6} table, keeping the sockets in the given state.
func parseSockets(lines []string, proto, family, state string) ([]Listener, error) {
	var out []Listener
	for i, l := range lines {
		f := strings.Fields(l)
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		if i == 0 || len(f) < 10 || f[3] != state {
			continue
		}
		if proto == "udp" && !isZeroRemote(f[2]) {
			continue // connected UDP socket: a client, not a server
		}
		host, port, ok := strings.Cut(f[1], ":")
		if !ok {
			return out, fmt.Errorf("/proc/net: malformed address %q", f[1])
		}
		ip, err := parseHexIP(host)
		if err != nil {
			return out, err
		}
		p, err := strconv.ParseUint(port, 16, 16)
		if err != nil {
			return out, fmt.Errorf("/proc/net: malformed port %q", port)
		}
		uid, _ := strconv.Atoi(f[7])
		out = append(out, Listener{
			Protocol: proto,
			Family:   family,
			Address:  ip.String(),
			Port:     int(p),
			Scope:    scope(ip),
			UID:      uid,
			inode:    f[9],
		})
	}
	return out, nil
}

func isZeroRemote(s string) bool {
	host, port, _ := strings.Cut(s, ":")
	return strings.Trim(host, "0") == "" && strings.Trim(port, "0") == ""
}

func scope(ip net.IP) string {
	switch {
	case ip.IsUnspecified():
		return "any"
	case ip.IsLoopback():
		return "loopback"
	default:
		return "address"
	}
}

// dedupe drops repeated sockets (SO_REUSEPORT, one per worker) keeping the first.
func dedupe(ls []Listener) []Listener {
	seen := map[string]bool{}
	out := ls[:0]
	for _, l := range ls {
		k := fmt.Sprintf("%s/%s/%d", l.Protocol, l.Address, l.Port)
		if !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out
}

// attachProcesses maps socket inodes to processes by scanning /proc/<pid>/fd. Without root only
// the processes of the current user can be inspected: the others stay without PID.
func attachProcesses(fs hostfs.FS, ls []Listener) {
	want := map[string][]int{}
	for i, l := range ls {
		want["socket:["+l.inode+"]"] = append(want["socket:["+l.inode+"]"], i)
	}
	if len(want) == 0 {
		return
	}
	procs, err := fs.ReadDir("/proc")
	if err != nil {
		return
	}
	found := 0
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fds, err := fs.ReadDir("/proc/" + p.Name() + "/fd")
		if err != nil {
			continue
		}
		var comm string
		for _, fd := range fds {
			link, err := fs.Readlink("/proc/" + p.Name() + "/fd/" + fd.Name())
			if err != nil {
				continue
			}
			idx, ok := want[link]
			if !ok {
				continue
			}
			if comm == "" {
				comm, _ = fs.ReadString("/proc/" + p.Name() + "/comm")
			}
			for _, i := range idx {
				if ls[i].PID == 0 {
					ls[i].PID, ls[i].Process = pid, comm
					found++
				}
			}
			if found == len(ls) {
				return
			}
		}
	}
}

// parseHexIP decodes the addresses of /proc/net: IPv4 as one little-endian 32-bit word, IPv6 as
// four of them.
func parseHexIP(s string) (net.IP, error) {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil, fmt.Errorf("/proc/net: malformed address %q", s)
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		binary.BigEndian.PutUint32(ip[i:], binary.LittleEndian.Uint32(b[i:]))
	}
	return ip, nil
}

// parseHexIPv6BigEndian decodes the addresses of /proc/net/ipv6_route, which are in network order.
func parseHexIPv6BigEndian(s string) (net.IP, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("/proc/net/ipv6_route: malformed address %q", s)
	}
	return net.IP(b), nil
}
