package firewall

import (
	"strings"
)

// parseIptablesSave parses the filter table of `iptables-save`/`ip6tables-save`.
func parseIptablesSave(source string, lines []string) *ruleset {
	rs := &ruleset{source: source, chains: map[string]*chain{}}
	inFilter := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "*"):
			inFilter = l == "*filter"
		case !inFilter || l == "" || strings.HasPrefix(l, "#") || l == "COMMIT":
		case strings.HasPrefix(l, ":"):
			// :INPUT DROP [0:0] or :user-chain - [0:0]
			f := strings.Fields(l[1:])
			if len(f) < 2 {
				continue
			}
			c := &chain{name: "filter " + f[0], policy: strings.ToLower(f[1])}
			if f[0] == "INPUT" {
				c.hook = "input"
				rs.order = append(rs.order, f[0])
			}
			rs.chains[f[0]] = c
		case strings.HasPrefix(l, "-A "):
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			if c, ok := rs.chains[f[1]]; ok {
				c.rules = append(c.rules, parseIptablesRule(splitArgs(l)[2:]))
			}
		}
	}
	return rs
}

// parseIptablesRule reduces the options of an -A line.
func parseIptablesRule(args []string) rule {
	var r rule
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		negated := false
		if a == "!" {
			negated = true
			i++
			if i >= len(args) {
				break
			}
			a = args[i]
		}
		switch a {
		case "-p", "--protocol":
			p := next()
			if negated {
				r.partial = true
			} else {
				r.proto = p // icmp and the others never match tcp/udp
			}
		case "--dport", "--destination-port", "--dports", "--destination-ports":
			ports, err := parsePorts(next())
			if err != nil || negated {
				r.partial = true
			} else {
				r.ports = ports
			}
		case "-i", "--in-interface":
			iface := next()
			switch {
			case iface == "lo" && !negated:
				r.loopback = true
			case negated && iface != "lo":
				r.partial = true
			}
		case "-m", "--match", "--comment":
			next() // modules (tcp, multiport, conntrack, comment) and comments are neutral
		case "--ctstate", "--state":
			states := strings.Split(strings.ToUpper(next()), ",")
			hasNew := false
			for _, s := range states {
				hasNew = hasNew || s == "NEW"
			}
			if hasNew == negated {
				r.stateOnly = true
			}
		case "-j", "--jump":
			switch t := next(); t {
			case "ACCEPT", "DROP", "REJECT", "RETURN":
				r.verdict = strings.ToLower(t)
			case "LOG", "NFLOG", "":
			default:
				r.verdict, r.target = "jump", t
			}
		case "-g", "--goto":
			r.verdict, r.target = "goto", next()
		case "--dst-type":
			// ufw-not-local: RETURN for local addresses, DROP for broadcast and multicast.
			if t := next(); t != "LOCAL" || negated {
				r.partial = true
			}
		case "--tcp-flags":
			next()
			next()
		case "--syn", "--limit", "--limit-burst", "--reject-with", "--log-prefix", "--log-level":
			if a != "--syn" {
				next()
			}
		default:
			// Source addresses, ICMP types, recent, owner, ...: not every client.
			r.partial = true
			// Skip the value of the option, when it has one.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && args[i+1] != "!" {
				i++
			}
		}
	}
	return r
}

// splitArgs splits an iptables-save line, keeping quoted words ("allow ssh") together.
func splitArgs(l string) []string {
	var out []string
	var cur strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c == '\\' && quoted && i+1 < len(l):
			i++
			cur.WriteByte(l[i])
		case c == '"':
			quoted, inWord = !quoted, true
		case (c == ' ' || c == '\t') && !quoted:
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}
