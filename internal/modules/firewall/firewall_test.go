package firewall

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/modules/network"
	"github.com/enr/terminus/internal/runner"
)

// nftInet is an inet table written by hand (captured with nft -j list ruleset): policy accept,
// established, ssh and https open, loopback, a jump to a chain that rejects, a final drop.
const nftInet = `{"nftables": [{"metainfo": {"version": "1.0.9", "release_name": "Old Doc Yak #3", "json_schema_version": 1}},
{"table": {"family": "inet", "name": "tfilter", "handle": 1}},
{"chain": {"family": "inet", "table": "tfilter", "name": "input", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "accept"}},
{"chain": {"family": "inet", "table": "tfilter", "name": "extra", "handle": 7}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 2, "expr": [{"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": ["established", "related"]}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 4, "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": {"set": [22, 443]}}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 5, "expr": [{"match": {"op": "==", "left": {"meta": {"key": "iif"}}, "right": "lo"}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 6, "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "ip", "field": "saddr"}}, "right": {"prefix": {"addr": "10.0.0.0", "len": 8}}}}, {"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 5432}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 8, "expr": [{"jump": {"target": "extra"}}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "input", "handle": 10, "expr": [{"counter": {"packets": 0, "bytes": 0}}, {"drop": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "extra", "handle": 11, "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "udp", "field": "dport"}}, "right": {"range": [60000, 61000]}}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "tfilter", "chain": "extra", "handle": 9, "expr": [{"reject": {"type": "icmpx", "expr": "admin-prohibited"}}]}}
]}`

// nftFirewalld reproduces the structure of the firewalld tables: zones picked by interface with
// a verdict map, services opened in the zone chain, a final reject.
const nftFirewalld = `{"nftables": [
{"table": {"family": "inet", "name": "firewalld"}},
{"chain": {"family": "inet", "table": "firewalld", "name": "filter_INPUT", "type": "filter", "hook": "input", "prio": 10, "policy": "accept"}},
{"chain": {"family": "inet", "table": "firewalld", "name": "filter_INPUT_ZONES"}},
{"chain": {"family": "inet", "table": "firewalld", "name": "filter_IN_public"}},
{"chain": {"family": "inet", "table": "firewalld", "name": "filter_IN_public_allow"}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "expr": [{"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": ["established", "related"]}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "expr": [{"match": {"op": "==", "left": {"meta": {"key": "iifname"}}, "right": "lo"}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "expr": [{"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": "invalid"}}, {"drop": null}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "expr": [{"jump": {"target": "filter_INPUT_ZONES"}}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT", "expr": [{"reject": {"type": "icmpx", "expr": "admin-prohibited"}}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_INPUT_ZONES", "expr": [{"vmap": {"key": {"meta": {"key": "iifname"}}, "data": {"set": [["lo", {"goto": {"target": "filter_IN_trusted"}}], ["eth0", {"goto": {"target": "filter_IN_public"}}]]}}}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_IN_public", "expr": [{"jump": {"target": "filter_IN_public_allow"}}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_IN_public", "expr": [{"match": {"op": "==", "left": {"meta": {"key": "l4proto"}}, "right": {"set": ["icmp", "ipv6-icmp"]}}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_IN_public_allow", "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 22}}, {"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": ["new", "untracked"]}}, {"accept": null}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_IN_public_allow", "expr": [{"vmap": {"key": {"payload": {"protocol": "tcp", "field": "dport"}}, "data": {"set": [[8080, {"accept": null}], [9090, {"drop": null}]]}}}]}},
{"rule": {"family": "inet", "table": "firewalld", "chain": "filter_IN_public_allow", "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": "@allowed"}}, {"accept": null}]}}
]}`

// ufwLegacy is iptables-legacy-save on a machine with ufw (ssh, http/https, postgres from a subnet).
const ufwLegacy = `# Generated by iptables-save v1.8.10
*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
:ufw-before-input - [0:0]
:ufw-user-input - [0:0]
:ufw-after-input - [0:0]
:ufw-not-local - [0:0]
-A INPUT -j ufw-before-input
-A INPUT -j ufw-after-input
-A ufw-before-input -i lo -j ACCEPT
-A ufw-before-input -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A ufw-before-input -m conntrack --ctstate INVALID -j DROP
-A ufw-before-input -p icmp -m icmp --icmp-type 8 -j ACCEPT
-A ufw-before-input -p udp -m udp --sport 67 --dport 68 -j ACCEPT
-A ufw-before-input -j ufw-not-local
-A ufw-before-input -d 224.0.0.251/32 -p udp -m udp --dport 5353 -j ACCEPT
-A ufw-before-input -j ufw-user-input
-A ufw-not-local -m addrtype --dst-type LOCAL -j RETURN
-A ufw-not-local -m limit --limit 3/min --limit-burst 10 -j LOG --log-prefix "[UFW BLOCK] "
-A ufw-not-local -j DROP
-A ufw-user-input -p tcp -m tcp --dport 22 -m comment --comment "'dapp_OpenSSH'" -j ACCEPT
-A ufw-user-input -p tcp -m multiport --dports 80,443 -j ACCEPT
-A ufw-user-input -s 10.0.0.0/8 -p tcp -m tcp --dport 5432 -j ACCEPT
COMMIT
# Completed
*nat
:PREROUTING ACCEPT [0:0]
-A PREROUTING -p tcp --dport 80 -j DROP
COMMIT
`

// ip6BlockAll drops everything but established connections and loopback: no accept rule for any
// port, unlike ufwLegacy which opens 22, 80, 443 and 5432 (from a subnet) over IPv4.
const ip6BlockAll = `*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
-A INPUT -i lo -j ACCEPT
-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
COMMIT
`

func listeners(ls ...network.Listener) func(hostfs.FS) ([]network.Listener, error) {
	return func(hostfs.FS) ([]network.Listener, error) { return ls, nil }
}

func tcp(port int, family, scope, process string) network.Listener {
	return network.Listener{Protocol: "tcp", Family: family, Port: port, Scope: scope, Process: process}
}

func udp(port int, family, scope, process string) network.Listener {
	return network.Listener{Protocol: "udp", Family: family, Port: port, Scope: scope, Process: process}
}

var commonListeners = listeners(
	tcp(22, "inet", "any", "sshd"),
	tcp(22, "inet6", "any", "sshd"),
	tcp(443, "inet", "any", "caddy"),
	tcp(5432, "inet", "any", "postgres"),
	tcp(6379, "inet", "any", "redis-server"),
	tcp(6379, "inet", "any", "redis-server"), // SO_REUSEPORT duplicate
	tcp(8080, "inet", "loopback", "app"),
	tcp(8081, "inet", "any", "docker-proxy"),
	udp(60001, "inet", "any", "mosh-server"),
	udp(5353, "inet", "any", "avahi-daemon"),
)

func newModule(t *testing.T) *Module {
	m := New()
	m.fs = hostfstest.New(t, map[string]string{})
	m.euid = func() int { return 0 }
	m.listeners = commonListeners
	return m
}

func reachOf(f *Facts) map[string]string {
	out := map[string]string{}
	for _, l := range f.Listeners {
		out[l.Protocol+"/"+l.Family+"/"+strconv.Itoa(l.Port)] = l.Reach
	}
	return out
}

func TestNftables(t *testing.T) {
	r := &runner.Fake{
		Paths:   map[string]string{"nft": "/usr/sbin/nft", "ufw": "/usr/sbin/ufw"},
		Results: map[string]runner.Result{"nft -j list ruleset": {Stdout: []byte(nftInet)}, "ufw status": {Stdout: []byte("Status: inactive\n")}},
	}
	m := newModule(t)
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if !f.FilteredIPv4 || !f.FilteredIPv6 || len(f.Rulesets) != 2 || f.Rulesets[0].FilteredBy != "inet tfilter input" || f.Rulesets[0].InputChains[0].Rules != 6 {
		t.Errorf("rulesets: %+v", f.Rulesets)
	}
	if len(f.Managers) != 1 || f.Managers[0].Active || f.Managers[0].State != "inactive" {
		t.Errorf("managers: %+v", f.Managers)
	}
	want := map[string]string{
		"tcp/inet/22": Open, "tcp/inet6/22": Open, "tcp/inet/443": Open,
		"tcp/inet/5432": Restricted, "tcp/inet/6379": Filtered, "tcp/inet/8081": Filtered,
		"udp/inet/60001": Open, "udp/inet/5353": Filtered,
	}
	got2 := reachOf(f)
	for k, w := range want {
		if got2[k] != w {
			t.Errorf("%s: %q, want %q", k, got2[k], w)
		}
	}
	if len(got2) != len(want) {
		t.Errorf("listeners: %v", got2)
	}

	fnd := m.Check(nil, f)
	if fnd[0].ID != "firewall.active" || fnd[0].Severity != model.SeverityOK || !strings.Contains(fnd[0].Message, "inet tfilter input (ipv4)") {
		t.Errorf("active: %+v", fnd[0])
	}
	exp := fnd[1]
	if exp.Severity != model.SeverityInfo || !strings.Contains(exp.Message, "22/tcp (sshd), 443/tcp (caddy), 8081/tcp (docker-proxy, container: DNAT bypasses the input chain), 60001/udp (mosh-server)") ||
		exp.Evidence["restricted"] != "5432/tcp (postgres)" {
		t.Errorf("exposed: %+v", exp)
	}

	c, _ := config.Parse("[modules.firewall]\npublic_ports = [22, 443]\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	exp = m.Check(nil, f)[1]
	if exp.Severity != model.SeverityWarn || !strings.HasPrefix(exp.Message, "2 ports reachable") {
		t.Errorf("public_ports: %+v", exp)
	}
	c, _ = config.Parse("[modules.firewall]\npublic_ports = [0]\n")
	if err := m.Configure(c.Decoder(Name)); err == nil {
		t.Error("invalid port")
	}
}

func TestFirewalld(t *testing.T) {
	v4, v6, err := parseNft([]byte(nftFirewalld))
	if err != nil {
		t.Fatal(err)
	}
	// The last rule names a set kept elsewhere (@allowed): any port may be in it, so the ports not
	// opened otherwise are restricted (maybe reachable), not filtered; 9090 is dropped before it.
	for port, want := range map[int]string{22: Open, 8080: Open, 9090: Filtered, 443: Restricted} {
		if got := v4.reach("tcp", port); got != want {
			t.Errorf("tcp %d: %s, want %s", port, got, want)
		}
		if got := v6.reach("tcp", port); got != want {
			t.Errorf("ipv6 tcp %d: %s, want %s", port, got, want)
		}
	}
	if got := v4.reach("udp", 53); got != Filtered {
		t.Errorf("udp 53: %s", got)
	}
	if v, by := v4.verdictFor("tcp", anyPort, false); v != "reject" || by != "inet firewalld filter_INPUT" {
		t.Errorf("closed port: %s %s", v, by)
	}
}

func TestIptablesLegacy(t *testing.T) {
	r := &runner.Fake{
		Paths: map[string]string{"iptables-legacy-save": "/usr/sbin/iptables-legacy-save", "ip6tables-legacy-save": "/usr/sbin/ip6tables-legacy-save", "ufw": "/usr/sbin/ufw"},
		Results: map[string]runner.Result{
			"iptables-legacy-save -t filter":  {Stdout: []byte(ufwLegacy)},
			"ip6tables-legacy-save -t filter": {ExitCode: 1, Stderr: []byte("ip6tables-save v1.8.10 (legacy): Cannot initialize: Address family not supported by protocol\n")},
			"ufw status":                      {Stdout: []byte("Status: active\n\nTo Action From\n")},
		},
	}
	m := newModule(t)
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if !f.FilteredIPv4 || f.FilteredIPv6 || len(f.Rulesets) != 1 || f.Rulesets[0].Source != "iptables-legacy" || f.Rulesets[0].InputChains[0].Policy != "drop" {
		t.Errorf("rulesets: %+v", f.Rulesets)
	}
	want := map[string]string{
		"tcp/inet/22": Open, "tcp/inet/443": Open, "tcp/inet/5432": Restricted, "tcp/inet/6379": Filtered,
		"udp/inet/60001": Filtered, "udp/inet/5353": Restricted,
		"tcp/inet6/22": Open, // no IPv6 rules
	}
	got2 := reachOf(f)
	for k, w := range want {
		if got2[k] != w {
			t.Errorf("%s: %q, want %q", k, got2[k], w)
		}
	}
	active := m.Check(nil, f)[0]
	if active.Severity != model.SeverityWarn || !strings.Contains(active.Message, "IPv6 is not") {
		t.Errorf("active: %+v", active)
	}
}

func TestDualStackSocketMergesIPv4AndIPv6(t *testing.T) {
	// Port 22 is open over IPv4 (ufwLegacy) but dropped by every IPv6 rule (ip6BlockAll). A
	// socket bound to :: still accepts it over IPv4 (through IPv4-mapped addresses) unless
	// net.ipv6.bindv6only says otherwise: reporting it as filtered would be wrong.
	r := &runner.Fake{
		Paths: map[string]string{"iptables-legacy-save": "/usr/sbin/iptables-legacy-save", "ip6tables-legacy-save": "/usr/sbin/ip6tables-legacy-save"},
		Results: map[string]runner.Result{
			"iptables-legacy-save -t filter":  {Stdout: []byte(ufwLegacy)},
			"ip6tables-legacy-save -t filter": {Stdout: []byte(ip6BlockAll)},
		},
	}
	only := listeners(tcp(22, "inet6", "any", "sshd"))

	m := New()
	m.fs = hostfstest.New(t, map[string]string{}) // no bindv6only: the kernel default (dual-stack)
	m.euid = func() int { return 0 }
	m.listeners = only
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	if reach := reachOf(got.(*Facts))["tcp/inet6/22"]; reach != Open {
		t.Errorf("dual-stack default: reach = %q, want %q", reach, Open)
	}

	// net.ipv6.bindv6only=1: the socket only ever accepts IPv6, so the IPv4 rules do not apply.
	m2 := New()
	m2.fs = hostfstest.New(t, map[string]string{"/proc/sys/net/ipv6/bindv6only": "1\n"})
	m2.euid = func() int { return 0 }
	m2.listeners = only
	got2, err := m2.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	if reach := reachOf(got2.(*Facts))["tcp/inet6/22"]; reach != Filtered {
		t.Errorf("bindv6only=1: reach = %q, want %q", reach, Filtered)
	}
}

func TestNoFirewall(t *testing.T) {
	r := &runner.Fake{
		Paths: map[string]string{"nft": "/usr/sbin/nft", "firewall-cmd": "/usr/bin/firewall-cmd", "iptables-legacy-save": "/usr/sbin/iptables-legacy-save"},
		Results: map[string]runner.Result{
			"nft -j list ruleset":            {Stdout: []byte(`{"nftables": [{"metainfo": {}}]}`)},
			"firewall-cmd --state":           {ExitCode: 252, Stdout: []byte("not running\n")},
			"iptables-legacy-save -t filter": {Stdout: []byte("*filter\n:INPUT ACCEPT [0:0]\n:FORWARD ACCEPT [0:0]\nCOMMIT\n")},
		},
	}
	m := newModule(t)
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	if rs := got.(*Facts).Rulesets; len(rs) != 2 || rs[0].Source != "nftables" || rs[1].Source != "nftables" {
		t.Errorf("empty legacy tables are left out: %+v", rs)
	}
	fnd := m.Check(nil, got)
	if fnd[0].Severity != model.SeverityWarn || !strings.Contains(fnd[0].Message, "firewalld not running") {
		t.Errorf("active: %+v", fnd[0])
	}
	if !strings.Contains(fnd[1].Message, "6379/tcp (redis-server)") {
		t.Errorf("exposed: %+v", fnd[1])
	}
}

func TestSkipAndErrors(t *testing.T) {
	m := New()
	m.euid = func() int { return 1000 }
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Errorf("not root: %v", err)
	}
	m.euid = func() int { return 0 }
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("nothing installed: %v", err)
	}
	r := &runner.Fake{
		Paths:   map[string]string{"nft": "/usr/sbin/nft"},
		Results: map[string]runner.Result{"nft -j list ruleset": {ExitCode: 1, Stderr: []byte("Operation not permitted")}},
	}
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") || m.Check(nil, got) != nil {
		t.Errorf("nft error: %v", err)
	}
	if d := m.Detect(context.Background(), &module.Env{Runner: r}); !d.Found {
		t.Errorf("detect: %+v", d)
	}
}

func TestParseHelpers(t *testing.T) {
	args := splitArgs(`-A INPUT -p tcp -m comment --comment "allow \"web\" ports" --dport 80 -j ACCEPT`)
	if len(args) != 12 || args[7] != `allow "web" ports` {
		t.Errorf("split: %q", args)
	}
	if r := parseIptablesRule(args[2:]); r.partial || r.proto != "tcp" || r.verdict != "accept" || !matchPorts(r.ports, 80) {
		t.Errorf("rule: %+v", r)
	}
	if r := parseIptablesRule(strings.Fields("! -i lo -s 127.0.0.0/8 -j DROP")); !r.partial {
		t.Errorf("negated: %+v", r)
	}
	ports, err := parsePorts("22,1000:2000,3000-3001")
	if err != nil || len(ports) != 3 || ports[1].String() != "1000-2000" || ports[0].String() != "22" {
		t.Errorf("ports: %v %v", ports, err)
	}
	if _, err := parsePorts("ssh"); err == nil {
		t.Error("invalid port")
	}
	if moreOpen(Filtered, Open) != Open || moreOpen(Open, Filtered) != Open ||
		moreOpen(Restricted, Filtered) != Restricted || moreOpen(Filtered, Filtered) != Filtered {
		t.Error("moreOpen")
	}
}
