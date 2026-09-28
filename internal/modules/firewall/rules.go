package firewall

import (
	"fmt"
	"strconv"
	"strings"
)

// The rules of nftables and iptables are reduced to what decides whether a new connection from
// anywhere to a local port gets in: protocol, destination ports, input interface, connection
// state. Rules with other conditions (source addresses, ICMP types, marks, ...) apply to some
// clients only and are left out of the evaluation.

// portRange is an inclusive range of ports.
type portRange struct{ from, to int }

func (p portRange) String() string {
	if p.from == p.to {
		return strconv.Itoa(p.from)
	}
	return fmt.Sprintf("%d-%d", p.from, p.to)
}

// rule is a filter rule, reduced.
type rule struct {
	// proto is "tcp", "udp", "" for any, or another protocol (icmp) that never matches.
	proto string
	// ports are the destination ports; nil matches any port.
	ports []portRange
	// unknownPorts rules match destination ports kept elsewhere (named sets): maybe the port
	// evaluated, never a port nobody opened. They are partial.
	unknownPorts bool
	// loopback rules match only traffic on lo.
	loopback bool
	// partial rules have conditions that hold only for some clients (source addresses, marks,
	// matches not understood): they decide only in the lenient evaluation, and only to accept.
	partial bool
	// stateOnly rules match only packets of existing connections (established, related, invalid):
	// they never decide for a new connection.
	stateOnly bool
	// verdict is accept, drop, reject, jump, goto, return, or "" (counter, log, ...).
	verdict string
	target  string
	// vmap holds the verdicts of a verdict map on the destination port.
	vmap []vmapEntry
}

type vmapEntry struct {
	ports   []portRange
	verdict string
	target  string
}

// chain is a chain of a table.
type chain struct {
	name string
	// hook is "input" for the base chains that see the traffic to local processes.
	hook   string
	policy string
	rules  []rule
}

// ruleset is a set of tables: chains are looked up by name for jumps.
type ruleset struct {
	source string
	chains map[string]*chain
	// order lists the input base chains, in the order they were read.
	order []string
}

// anyPort is a port that no rule names: it tells what happens to the ports nobody opened.
const anyPort = -1

// Reachability of a port.
const (
	// Open: new connections from anywhere get in.
	Open = "open"
	// Restricted: only from some clients (rules with source addresses, or not understood).
	Restricted = "restricted"
	// Filtered: dropped or rejected.
	Filtered = "filtered"
)

// reach evaluates the input base chains for a new connection to port/proto: strictly (only the
// rules that hold for any client) and leniently (also the accept rules for some clients).
func (rs *ruleset) reach(proto string, port int) string {
	if v, _ := rs.verdictFor(proto, port, false); v == "accept" {
		return Open
	}
	if v, _ := rs.verdictFor(proto, port, true); v == "accept" {
		return Restricted
	}
	return Filtered
}

// verdictFor returns "accept", or the verdict that stops the connection ("drop", "reject") and the
// base chain where it happens. Base chains are all traversed: an accept in one does not skip the
// others.
func (rs *ruleset) verdictFor(proto string, port int, lenient bool) (string, string) {
	for _, name := range rs.order {
		c := rs.chains[name]
		v := rs.eval(c, proto, port, 0, lenient)
		if v == "" || v == "return" {
			v = c.policy
		}
		if v == "drop" || v == "reject" {
			return v, c.name
		}
	}
	return "accept", ""
}

// maxDepth bounds the jumps followed (loops are rejected by the kernel, but not by fixtures).
const maxDepth = 16

func (rs *ruleset) eval(c *chain, proto string, port, depth int, lenient bool) string {
	if depth > maxDepth {
		return ""
	}
	for _, r := range c.rules {
		if r.stateOnly || r.loopback || (r.proto != "" && r.proto != proto) || !matchPorts(r.ports, port) || (r.unknownPorts && port == anyPort) {
			continue
		}
		verdict, target := r.verdict, r.target
		if len(r.vmap) > 0 {
			verdict, target = "", ""
			for _, e := range r.vmap {
				if matchPorts(e.ports, port) {
					verdict, target = e.verdict, e.target
					break
				}
			}
		}
		if r.partial && (!lenient || verdict == "drop" || verdict == "reject" || verdict == "return") {
			continue
		}
		switch verdict {
		case "accept", "drop", "reject":
			return verdict
		case "return":
			return ""
		case "jump", "goto":
			next, ok := rs.chains[target]
			if !ok {
				continue
			}
			v := rs.eval(next, proto, port, depth+1, lenient)
			if v != "" {
				return v
			}
			if verdict == "goto" {
				return ""
			}
		}
	}
	return ""
}

func matchPorts(ports []portRange, port int) bool {
	if ports == nil {
		return true
	}
	for _, p := range ports {
		if port >= p.from && port <= p.to {
			return true
		}
	}
	return false
}

// parsePorts parses "22", "80,443", "60000:61000" and "1000-2000".
func parsePorts(s string) ([]portRange, error) {
	var out []portRange
	for _, part := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(part, ":")
		if !ok {
			a, b, ok = strings.Cut(part, "-")
		}
		from, err := strconv.Atoi(strings.TrimSpace(a))
		if err != nil {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		to := from
		if ok {
			if to, err = strconv.Atoi(strings.TrimSpace(b)); err != nil {
				return nil, fmt.Errorf("invalid port %q", part)
			}
		}
		out = append(out, portRange{from, to})
	}
	return out, nil
}
