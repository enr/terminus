package firewall

import (
	"encoding/json"
	"fmt"
	"strings"
)

// nftObject is an element of `nft -j list ruleset`.
type nftObject struct {
	Chain *struct {
		Family string `json:"family"`
		Table  string `json:"table"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Hook   string `json:"hook"`
		Prio   any    `json:"prio"`
		Policy string `json:"policy"`
	} `json:"chain"`
	Rule *struct {
		Family string            `json:"family"`
		Table  string            `json:"table"`
		Chain  string            `json:"chain"`
		Expr   []json.RawMessage `json:"expr"`
	} `json:"rule"`
}

// parseNft parses `nft -j list ruleset` into an IPv4 and an IPv6 view: ip and inet tables see IPv4
// traffic, ip6 and inet tables IPv6 traffic.
func parseNft(b []byte) (v4, v6 *ruleset, err error) {
	var doc struct {
		Nftables []nftObject `json:"nftables"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, fmt.Errorf("nft -j list ruleset: %w", err)
	}
	chains := map[string]*chain{}
	families := map[string]string{}
	v4 = &ruleset{source: "nftables", chains: chains}
	v6 = &ruleset{source: "nftables", chains: chains}
	for _, o := range doc.Nftables {
		switch {
		case o.Chain != nil:
			c := o.Chain
			key := chainKey(c.Family, c.Table, c.Name)
			ch := &chain{name: c.Family + " " + c.Table + " " + c.Name, policy: c.Policy}
			if c.Type == "filter" && c.Hook == "input" {
				ch.hook = "input"
				if ch.policy == "" {
					ch.policy = "accept"
				}
			}
			chains[key] = ch
			families[key] = c.Family
		case o.Rule != nil:
			r := o.Rule
			c, ok := chains[chainKey(r.Family, r.Table, r.Chain)]
			if !ok {
				continue
			}
			c.rules = append(c.rules, parseNftRule(r.Family, r.Table, r.Expr))
		}
	}
	// Base chains in the order of the ruleset (tables are listed in creation order).
	for _, o := range doc.Nftables {
		if o.Chain == nil {
			continue
		}
		key := chainKey(o.Chain.Family, o.Chain.Table, o.Chain.Name)
		if chains[key].hook != "input" {
			continue
		}
		switch families[key] {
		case "ip":
			v4.order = append(v4.order, key)
		case "ip6":
			v6.order = append(v6.order, key)
		case "inet":
			v4.order = append(v4.order, key)
			v6.order = append(v6.order, key)
		}
	}
	return v4, v6, nil
}

func chainKey(family, table, name string) string { return family + "\x00" + table + "\x00" + name }

// parseNftRule reduces the expressions of a rule.
func parseNftRule(family, table string, exprs []json.RawMessage) rule {
	var r rule
	for _, raw := range exprs {
		var e map[string]json.RawMessage
		if json.Unmarshal(raw, &e) != nil {
			r.partial = true
			continue
		}
		for k, v := range e {
			switch k {
			case "match":
				nftMatch(&r, v)
			case "accept", "drop", "reject", "return":
				r.verdict = k
			case "jump", "goto":
				var t struct {
					Target string `json:"target"`
				}
				_ = json.Unmarshal(v, &t)
				r.verdict, r.target = k, chainKey(family, table, t.Target)
			case "vmap":
				nftVmap(&r, family, table, v)
			case "counter", "log", "limit", "notrack", "meter", "quota":
				// Neutral: they do not change whether the rule applies (limit: close enough).
			default:
				// xt (iptables-nft compat), set updates, NAT, marks: unknown effect.
				r.partial = true
			}
		}
	}
	return r
}

type nftMatchExpr struct {
	Op    string          `json:"op"`
	Left  json.RawMessage `json:"left"`
	Right json.RawMessage `json:"right"`
}

type nftLeft struct {
	Payload *struct {
		Protocol string `json:"protocol"`
		Field    string `json:"field"`
	} `json:"payload"`
	Meta *struct {
		Key string `json:"key"`
	} `json:"meta"`
	Ct *struct {
		Key string `json:"key"`
	} `json:"ct"`
	Fib *struct {
		Result string   `json:"result"`
		Flags  []string `json:"flags"`
	} `json:"fib"`
}

func nftMatch(r *rule, raw json.RawMessage) {
	var m nftMatchExpr
	var left nftLeft
	if json.Unmarshal(raw, &m) != nil || json.Unmarshal(m.Left, &left) != nil {
		r.partial = true
		return
	}
	positive := m.Op == "==" || m.Op == "in"
	switch {
	case left.Payload != nil && left.Payload.Field == "dport" && (left.Payload.Protocol == "tcp" || left.Payload.Protocol == "udp") && positive:
		r.proto = left.Payload.Protocol
		ports, ok := nftPorts(m.Right)
		if !ok {
			r.partial, r.unknownPorts = true, true
		}
		r.ports = ports
	case left.Meta != nil && (left.Meta.Key == "iifname" || left.Meta.Key == "iif"):
		names := nftStrings(m.Right)
		lo := len(names) == 1 && names[0] == "lo"
		switch {
		case positive && lo:
			r.loopback = true
		case !positive && !lo:
			r.partial = true
		}
		// Another interface: assume the traffic comes from there (firewalld zones by interface).
	case left.Meta != nil && left.Meta.Key == "l4proto" && positive,
		left.Payload != nil && (left.Payload.Field == "protocol" || left.Payload.Field == "nexthdr") && positive:
		protos := nftStrings(m.Right)
		switch {
		case len(protos) == 1:
			r.proto = protos[0] // icmp and the others never match tcp/udp
		case !contains(protos, "tcp") && !contains(protos, "udp"):
			r.proto = strings.Join(protos, ",")
		default:
			r.partial = true
		}
	case left.Fib != nil && left.Fib.Result == "type" && len(left.Fib.Flags) == 1 && left.Fib.Flags[0] == "daddr":
		// "fib daddr type local": traffic to a local address, the one evaluated.
		if t := nftStrings(m.Right); !positive || len(t) != 1 || t[0] != "local" {
			r.partial = true
		}
	case left.Meta != nil && left.Meta.Key == "nfproto":
		// The family view already selects the traffic.
	case left.Ct != nil && left.Ct.Key == "state":
		states := nftStrings(m.Right)
		hasNew := false
		for _, s := range states {
			hasNew = hasNew || s == "new"
		}
		if hasNew != positive {
			r.stateOnly = true // established/related/invalid only
		}
	default:
		r.partial = true
	}
}

// nftPorts reads a port, a set of ports and ranges, or a range.
func nftPorts(raw json.RawMessage) ([]portRange, bool) {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return []portRange{{n, n}}, true
	}
	var obj struct {
		Set   []json.RawMessage `json:"set"`
		Range []int             `json:"range"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}
	if len(obj.Range) == 2 {
		return []portRange{{obj.Range[0], obj.Range[1]}}, true
	}
	if obj.Set == nil {
		return nil, false // a named set (@ports): its content is elsewhere
	}
	var out []portRange
	for _, e := range obj.Set {
		p, ok := nftPorts(e)
		if !ok {
			return nil, false
		}
		out = append(out, p...)
	}
	return out, true
}

// nftStrings reads a string or a set/list of strings.
func nftStrings(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var obj struct {
		Set []string `json:"set"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.Set
}

// nftVmap reads a verdict map: on the destination port it gives verdicts by port; on the input
// interface (firewalld zones) the first zone that is not lo is followed.
func nftVmap(r *rule, family, table string, raw json.RawMessage) {
	var vm struct {
		Key  json.RawMessage `json:"key"`
		Data struct {
			Set [][2]json.RawMessage `json:"set"`
		} `json:"data"`
	}
	var key nftLeft
	if json.Unmarshal(raw, &vm) != nil || json.Unmarshal(vm.Key, &key) != nil {
		r.partial = true
		return
	}
	verdict := func(raw json.RawMessage) (string, string) {
		var v map[string]json.RawMessage
		if json.Unmarshal(raw, &v) != nil {
			return "", ""
		}
		for k, val := range v {
			var t struct {
				Target string `json:"target"`
			}
			_ = json.Unmarshal(val, &t)
			if t.Target != "" {
				return k, chainKey(family, table, t.Target)
			}
			return k, ""
		}
		return "", ""
	}
	switch {
	case key.Payload != nil && key.Payload.Field == "dport":
		r.proto = key.Payload.Protocol
		for _, e := range vm.Data.Set {
			ports, ok := nftPorts(e[0])
			if !ok {
				continue
			}
			v, t := verdict(e[1])
			r.vmap = append(r.vmap, vmapEntry{ports: ports, verdict: v, target: t})
		}
		if len(r.vmap) == 0 {
			r.partial = true
		}
	case key.Meta != nil && (key.Meta.Key == "iifname" || key.Meta.Key == "iif"):
		for _, e := range vm.Data.Set {
			if names := nftStrings(e[0]); len(names) == 1 && strings.Trim(names[0], `"`) == "lo" {
				continue
			}
			r.verdict, r.target = verdict(e[1])
			return
		}
		r.partial = true
	default:
		r.partial = true
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
