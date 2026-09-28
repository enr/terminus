package network

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
)

const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0 100 0 0 10 0
   1: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1002 1 0 100 0 0 10 0
   2: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1003 1 0 100 0 0 10 0
   3: 0F02000A:0016 0202000A:D431 01 00000000:00000000 00:00000000 00000000     0        0 1004 1 0 100 0 0 10 0
`

const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2001 1 0 100 0 0 10 0
   1: 00000000000000000000000001000000:0CEA 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2002 1 0 100 0 0 10 0
`

const udp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   0: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 3001 2 0 0
   1: 0F02000A:A1B2 08080808:0035 07 00000000:00000000 00:00000000 00000000  1000        0 3002 2 0 0
`

const routes4 = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	0102000A	0003	0	0	100	00000000	0	0	0
eth0	0002000A	00000000	0001	0	0	100	00FFFFFF	0	0	0
`

const routes6 = `fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003     eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo
`

func fixture(t *testing.T, resolv string) *Module {
	fs := hostfstest.New(t, map[string]string{
		"/proc/net/tcp":                            tcp4,
		"/proc/net/tcp6":                           tcp6,
		"/proc/net/udp":                            udp4,
		"/proc/net/route":                          routes4,
		"/proc/net/ipv6_route":                     routes6,
		"/etc/resolv.conf":                         resolv,
		"/run/systemd/resolve/resolv.conf":         "nameserver 9.9.9.9\nnameserver 1.1.1.1\n",
		"/proc/42/comm":                            "sshd\n",
		"/proc/42/fd/3":                            hostfstest.Symlink + "socket:[1001]",
		"/proc/42/fd/4":                            hostfstest.Symlink + "/dev/null",
		"/proc/77/comm":                            "caddy\n",
		"/proc/77/fd/9":                            hostfstest.Symlink + "socket:[2001]",
		"/proc/self":                               hostfstest.Symlink + "42",
		"/sys/class/net/eth0/operstate":            "up\n",
		"/sys/class/net/eth0/speed":                "1000\n",
		"/sys/class/net/eth0/statistics/rx_bytes":  "123\n",
		"/sys/class/net/eth0/statistics/tx_errors": "2\n",
		"/sys/class/net/lo/operstate":              "unknown\n",
		"/sys/devices/virtual/net/lo/uevent":       "",
	})
	_, v4, _ := net.ParseCIDR("10.0.2.15/24")
	v4.IP = net.ParseIP("10.0.2.15")
	return &Module{
		fs: fs,
		interfaces: func() ([]net.Interface, error) {
			hw, _ := net.ParseMAC("52:54:00:12:34:56")
			return []net.Interface{
				{Index: 2, Name: "eth0", MTU: 1500, HardwareAddr: hw, Flags: net.FlagUp},
				{Index: 1, Name: "lo", MTU: 65536, Flags: net.FlagUp | net.FlagLoopback},
			}, nil
		},
		addrs: func(i net.Interface) ([]net.Addr, error) {
			if i.Name == "eth0" {
				return []net.Addr{v4, &net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}}, nil
			}
			return nil, errors.New("addrs failed")
		},
	}
}

func TestCollect(t *testing.T) {
	got, err := fixture(t, "# generated\nnameserver 127.0.0.53\nsearch lan example.com\noptions edns0 trust-ad\n").Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "addrs failed") {
		t.Errorf("interface error not reported: %v", err)
	}
	f := got.(*Facts)

	if len(f.Interfaces) != 2 || f.Interfaces[0].Name != "lo" || !f.Interfaces[0].Virtual {
		t.Fatalf("interfaces: %+v", f.Interfaces)
	}
	eth := f.Interfaces[1]
	if eth.Virtual || !eth.Up || eth.OperState != "up" || eth.SpeedMbps != 1000 || eth.HardwareAddr != "52:54:00:12:34:56" ||
		eth.Stats.RxBytes != 123 || eth.Stats.TxErrors != 2 || len(eth.Addresses) != 2 ||
		eth.Addresses[0] != (Address{"inet", "10.0.2.15", 24, "10.0.2.15/24"}) || eth.Addresses[1].Family != "inet6" {
		t.Errorf("eth0: %+v %+v", eth, eth.Addresses)
	}

	wantRoutes := []Route{{"inet", "10.0.2.1", "eth0", 100}, {"inet6", "fe80::1", "eth0", 1024}}
	if len(f.DefaultRoutes) != 2 || f.DefaultRoutes[0] != wantRoutes[0] || f.DefaultRoutes[1] != wantRoutes[1] {
		t.Errorf("routes: %+v", f.DefaultRoutes)
	}

	if f.DNS.Resolver != "systemd-resolved" || f.DNS.UpstreamNameservers[1] != "1.1.1.1" || len(f.DNS.Search) != 2 || len(f.DNS.Options) != 2 {
		t.Errorf("dns: %+v", f.DNS)
	}

	var got2 []string
	for _, l := range f.Listeners {
		got2 = append(got2, strings.Join([]string{l.Protocol, l.Address, strconv.Itoa(l.Port), l.Scope, l.Process}, " "))
	}
	want := []string{
		"tcp 0.0.0.0 22 any sshd",
		"tcp :: 443 any caddy",
		"tcp ::1 3306 loopback ",
		"tcp 127.0.0.1 5432 loopback ",
		"udp 127.0.0.53 53 loopback ",
	}
	if strings.Join(got2, "|") != strings.Join(want, "|") {
		t.Errorf("listeners:\n%s\nwant:\n%s", strings.Join(got2, "\n"), strings.Join(want, "\n"))
	}

	findings := (&Module{}).Check(nil, f)
	sev := map[string]model.Severity{}
	for _, x := range findings {
		sev[x.ID] = x.Severity
	}
	if sev["net.default-route"] != model.SeverityOK || sev["net.dns"] != model.SeverityOK || sev["net.public-listeners"] != model.SeverityInfo {
		t.Errorf("findings: %+v", findings)
	}
}

func TestChecksProblems(t *testing.T) {
	f := &Facts{DNS: DNS{Nameservers: []string{}}}
	for _, x := range (&Module{}).Check(nil, f) {
		want := map[string]model.Severity{"net.default-route": model.SeverityWarn, "net.dns": model.SeverityFail}[x.ID]
		if x.Severity != want || x.Hint == "" {
			t.Errorf("%s: %s", x.ID, x.Severity)
		}
	}
}

func TestParseHexIP(t *testing.T) {
	cases := map[string]string{
		"0100007F":                         "127.0.0.1",
		"00000000":                         "0.0.0.0",
		"00000000000000000000000001000000": "::1",
		"B80D0120000000000000000001000000": "2001:db8::1",
	}
	for in, want := range cases {
		ip, err := parseHexIP(in)
		if err != nil || ip.String() != want {
			t.Errorf("%s: %v %v, want %s", in, ip, err, want)
		}
	}
	for _, bad := range []string{"xyz", "0100", ""} {
		if _, err := parseHexIP(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCollectLive(t *testing.T) {
	got, _ := New().Collect(context.Background(), nil)
	f := got.(*Facts)
	if len(f.Interfaces) == 0 {
		t.Fatal("no interfaces on the live machine")
	}
}

func TestPublicPorts(t *testing.T) {
	f := &Facts{Listeners: []Listener{
		{Protocol: "tcp", Port: 22, Scope: "any", Process: "sshd"},
		{Protocol: "tcp", Port: 22, Scope: "any", Family: "inet6"},
		{Protocol: "tcp", Port: 5432, Scope: "any", Process: "rootlessport"},
		{Protocol: "tcp", Port: 6379, Scope: "loopback"},
	}}
	m := New()
	c, _ := config.Parse("[modules.network]\npublic_ports = [22, 443]\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	var got model.Finding
	for _, x := range m.Check(nil, f) {
		if x.ID == "net.public-listeners" {
			got = x
		}
	}
	if got.Severity != model.SeverityWarn || !strings.Contains(got.Message, "5432 (rootlessport)") || strings.Contains(got.Message, "22 ") {
		t.Errorf("unexpected ports: %+v", got)
	}
	c, _ = config.Parse("[modules.network]\npublic_ports = [22, 5432]\n")
	m.Configure(c.Decoder(Name))
	for _, x := range m.Check(nil, f) {
		if x.ID == "net.public-listeners" && x.Severity != model.SeverityOK {
			t.Errorf("expected ports: %+v", x)
		}
	}
	c, _ = config.Parse("[modules.network]\npublic_ports = [0]\n")
	if err := New().Configure(c.Decoder(Name)); err == nil {
		t.Error("port 0 accepted")
	}
	for _, x := range New().Check(nil, f) {
		if x.ID == "net.public-listeners" && x.Severity != model.SeverityInfo {
			t.Errorf("without public_ports: %+v", x)
		}
	}
}
