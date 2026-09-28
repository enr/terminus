// Package firewall is an optional module about the packet filter: which firewall manages it
// (firewalld, ufw), whether incoming connections are filtered (nftables, iptables-legacy), and which
// of the ports listening on all addresses are reachable from other machines.
//
//	[modules.firewall]
//	enabled = true
//	public_ports = [22, 80, 443]
package firewall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/modules/network"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "firewall"

// Facts about the firewall.
type Facts struct {
	// Managers are the firewall front ends installed: firewalld, ufw.
	Managers []Manager `json:"managers"`
	// Rulesets are the rule sources read: nftables (which also shows iptables-nft, firewalld and
	// ufw rules) and the legacy iptables tables, by IP version.
	Rulesets []Ruleset `json:"rulesets"`
	// Filtered tells whether new connections from anywhere to ports nobody opened are dropped or
	// rejected, for IPv4 and for IPv6.
	FilteredIPv4 bool `json:"filtered_ipv4"`
	FilteredIPv6 bool `json:"filtered_ipv6"`
	// Listeners are the sockets listening on all addresses, with their reachability.
	Listeners []Listener `json:"listeners"`
}

// Manager is a firewall front end.
type Manager struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	State  string `json:"state"`
}

// Ruleset summarizes a rule source.
type Ruleset struct {
	Source string `json:"source"`
	Family string `json:"family"`
	// InputChains are the base chains that see the traffic to local processes.
	InputChains []Chain `json:"input_chains"`
	// Filtered tells whether this ruleset drops new connections to a port nobody opened.
	Filtered bool `json:"filtered"`
	// FilteredBy is the chain that drops them.
	FilteredBy string `json:"filtered_by,omitempty"`
}

// Chain is an input base chain.
type Chain struct {
	Name   string `json:"name"`
	Policy string `json:"policy"`
	Rules  int    `json:"rules"`
}

// Listener is a socket listening on all addresses.
type Listener struct {
	Protocol string `json:"protocol"`
	Family   string `json:"family"`
	Port     int    `json:"port"`
	Process  string `json:"process,omitempty"`
	// Reach is open, restricted (from some addresses) or filtered.
	Reach string `json:"reach"`
	// Container tells that a container runtime holds the port: rootful containers are reached
	// through DNAT and the forward chain, which the input chain does not filter.
	Container bool `json:"container,omitempty"`
}

// containerProxies are the processes that hold the ports published by rootful containers.
var containerProxies = map[string]bool{"conmon": true, "docker-proxy": true}

// Module collects the firewall facts.
type Module struct {
	fs          hostfs.FS
	publicPorts map[int]bool
	euid        func() int
	listeners   func(hostfs.FS) ([]network.Listener, error)
}

// New returns the firewall module.
func New() *Module {
	return &Module{fs: hostfs.Host, euid: os.Geteuid, listeners: network.ReadListeners}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "packet filter (nftables, iptables, firewalld, ufw): incoming connections filtered, ports reachable from outside"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Ports expected to be reachable from other machines; the others are warnings.
# Unset: the reachable ports are listed as info.
# public_ports = [22, 80, 443]`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		PublicPorts *[]int `toml:"public_ports"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.PublicPorts != nil {
		m.publicPorts = map[int]bool{}
		for _, p := range *c.PublicPorts {
			if p < 1 || p > 65535 {
				return fmt.Errorf("modules.firewall.public_ports: invalid port %d", p)
			}
			m.publicPorts[p] = true
		}
	}
	return nil
}

// Detect implements module.Detector.
func (m *Module) Detect(_ context.Context, env *module.Env) module.Detection {
	if env == nil || env.Runner == nil {
		return module.Detection{Reason: "no command runner"}
	}
	var found []string
	for _, b := range []string{"nft", "iptables-save", "firewall-cmd", "ufw"} {
		if _, err := env.Runner.LookPath(b); err == nil {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return module.Detection{Reason: "neither nft nor iptables installed"}
	}
	return module.Detection{Found: true, Reason: strings.Join(found, ", ") + " installed (reading the rules requires root)", Config: "enabled = true"}
}

// dualStack tells whether a socket bound to the IPv6 wildcard address also accepts IPv4 clients
// (through IPv4-mapped addresses): the kernel-wide default unless net.ipv6.bindv6only=1. A
// process can still opt out per socket with IPV6_V6ONLY, which cannot be observed from outside
// it, so this is the safer of the two assumptions to get wrong.
func (m *Module) dualStack() bool {
	v, err := m.fs.ReadString("/proc/sys/net/ipv6/bindv6only")
	return err != nil || strings.TrimSpace(v) != "1"
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	if env == nil || env.Runner == nil {
		return nil, errors.New("no command runner")
	}
	if m.euid() != 0 {
		return nil, module.Skip("reading the firewall rules requires root")
	}
	r := env.Runner
	f := &Facts{Managers: []Manager{}, Rulesets: []Ruleset{}, Listeners: []Listener{}}
	var errs []error
	f.Managers = managers(ctx, r)

	var v4, v6 []*ruleset
	nftFound := false
	if _, err := r.LookPath("nft"); err == nil {
		nftFound = true
		res, err := r.Run(ctx, runner.Cmd{Name: "nft", Args: []string{"-j", "list", "ruleset"}})
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("nft: %w", err))
		case res.ExitCode != 0:
			errs = append(errs, fmt.Errorf("nft list ruleset: exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr))))
		default:
			a, b, err := parseNft(res.Stdout)
			if err != nil {
				errs = append(errs, err)
			} else {
				v4, v6 = append(v4, a), append(v6, b)
			}
		}
	}
	// Legacy tables are not visible to nft. Without nft, iptables-save is whatever backend there is.
	for _, c := range []struct{ bin, fallback, family string }{
		{"iptables-legacy-save", "iptables-save", "ipv4"},
		{"ip6tables-legacy-save", "ip6tables-save", "ipv6"},
	} {
		bin := c.bin
		if _, err := r.LookPath(bin); err != nil {
			if nftFound {
				continue
			}
			bin = c.fallback
			if _, err := r.LookPath(bin); err != nil {
				continue
			}
		}
		res, err := r.Run(ctx, runner.Cmd{Name: bin, Args: []string{"-t", "filter"}, Env: []string{"LANG=C"}})
		if err == nil && res.ExitCode != 0 && noLegacyTables(string(res.Stderr)) {
			continue // the kernel has no legacy tables for this family
		}
		if err != nil || res.ExitCode != 0 {
			if err == nil {
				err = fmt.Errorf("exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
			}
			errs = append(errs, fmt.Errorf("%s: %w", bin, err))
			continue
		}
		rs := parseIptablesSave(strings.TrimSuffix(bin, "-save"), hostfs.SplitLines(res.Stdout))
		if len(rs.order) == 0 || (bin != c.fallback && unused(rs)) {
			continue // no tables, or legacy tables left empty next to nftables
		}
		if c.family == "ipv4" {
			v4 = append(v4, rs)
		} else {
			v6 = append(v6, rs)
		}
	}
	if v4 == nil && v6 == nil {
		if len(errs) == 0 {
			return nil, module.Skip("neither nft nor iptables installed")
		}
		return f, errors.Join(errs...) // no rules read: reachability unknown
	}

	f.FilteredIPv4 = summarize(f, v4, "ipv4")
	f.FilteredIPv6 = summarize(f, v6, "ipv6")

	ls, err := m.listeners(m.fs)
	if err != nil {
		errs = append(errs, fmt.Errorf("listening sockets: %w", err))
	}
	dualStack := m.dualStack()
	seen := map[string]bool{}
	for _, l := range ls {
		if l.Scope != "any" {
			continue
		}
		key := fmt.Sprintf("%s/%s/%d", l.Protocol, l.Family, l.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		reach := reachAll(v4, l.Protocol, l.Port)
		if l.Family == "inet6" {
			reach = reachAll(v6, l.Protocol, l.Port)
			// A socket bound to :: also accepts IPv4 clients through IPv4-mapped addresses
			// unless the application opted out with IPV6_V6ONLY (which cannot be observed from
			// outside the process) or the kernel default (net.ipv6.bindv6only) says otherwise. A
			// client gets in through whichever path lets it, so the more open of the two wins:
			// otherwise a listener open over IPv4 but filtered over IPv6 would be reported as
			// filtered.
			if dualStack {
				reach = moreOpen(reach, reachAll(v4, l.Protocol, l.Port))
			}
		}
		fl := Listener{Protocol: l.Protocol, Family: l.Family, Port: l.Port, Process: l.Process, Reach: reach}
		if containerProxies[l.Process] {
			fl.Container = true
		}
		f.Listeners = append(f.Listeners, fl)
	}
	sort.SliceStable(f.Listeners, func(i, j int) bool {
		a, b := f.Listeners[i], f.Listeners[j]
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Family < b.Family
	})
	return f, errors.Join(errs...)
}

// noLegacyTables tells whether an iptables-legacy error means that there are no tables.
func noLegacyTables(stderr string) bool {
	for _, s := range []string{"Cannot initialize", "Table does not exist", "Address family not supported"} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// unused tells whether a ruleset has only empty input chains with policy accept.
func unused(rs *ruleset) bool {
	for _, name := range rs.order {
		if c := rs.chains[name]; len(c.rules) > 0 || c.policy != "accept" {
			return false
		}
	}
	return true
}

// summarize adds the rulesets of a family to the facts and tells whether they filter.
func summarize(f *Facts, views []*ruleset, family string) bool {
	filtered := false
	for _, rs := range views {
		s := Ruleset{Source: rs.source, Family: family, InputChains: []Chain{}}
		for _, name := range rs.order {
			c := rs.chains[name]
			s.InputChains = append(s.InputChains, Chain{Name: c.name, Policy: c.policy, Rules: len(c.rules)})
		}
		if v, by := rs.verdictFor("tcp", anyPort, false); v != "accept" {
			s.Filtered, s.FilteredBy = true, by
			filtered = true
		}
		f.Rulesets = append(f.Rulesets, s)
	}
	return filtered
}

// reachAll combines the rulesets of a family: a connection must get through all of them.
func reachAll(views []*ruleset, proto string, port int) string {
	out := Open
	for _, rs := range views {
		switch rs.reach(proto, port) {
		case Filtered:
			return Filtered
		case Restricted:
			out = Restricted
		}
	}
	return out
}

// managers reads the state of firewalld and ufw, when installed.
func managers(ctx context.Context, r runner.Runner) []Manager {
	out := []Manager{}
	if _, err := r.LookPath("firewall-cmd"); err == nil {
		m := Manager{Name: "firewalld"}
		res, err := r.Run(ctx, runner.Cmd{Name: "firewall-cmd", Args: []string{"--state"}})
		if err != nil {
			m.State = err.Error()
		} else {
			m.State = strings.TrimSpace(string(res.Stdout) + string(res.Stderr))
			m.Active = res.ExitCode == 0 && m.State == "running"
		}
		out = append(out, m)
	}
	if _, err := r.LookPath("ufw"); err == nil {
		m := Manager{Name: "ufw"}
		res, err := r.Run(ctx, runner.Cmd{Name: "ufw", Args: []string{"status"}, Env: []string{"LANG=C"}})
		if err != nil {
			m.State = err.Error()
		} else {
			first, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\n")
			m.State = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(first, "Status:")))
			m.Active = m.State == "active"
		}
		out = append(out, m)
	}
	return out
}
