package firewall

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "firewall.active", Description: "warn: new connections to ports nobody opened are accepted (IPv4, or IPv6 with IPv6 listeners)"},
		{ID: "firewall.exposed", Description: "ports listening on all addresses and reachable from other machines: info, or warn when not in public_ports"},
	}
}

// Check implements module.Checker.
func (m *Module) Check(_ *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok || len(f.Rulesets) == 0 {
		return nil
	}
	var out []model.Finding

	v4Listeners, v6Listeners := false, false
	for _, l := range f.Listeners {
		v4Listeners = v4Listeners || (l.Family == "inet" && l.Reach == Open)
		v6Listeners = v6Listeners || (l.Family == "inet6" && l.Reach == Open)
	}
	var filteredBy []string
	for _, rs := range f.Rulesets {
		if rs.Filtered {
			filteredBy = append(filteredBy, rs.FilteredBy+" ("+rs.Family+")")
		}
	}
	active := model.Finding{ID: "firewall.active", Subject: "input", Severity: model.SeverityOK}
	switch {
	case !f.FilteredIPv4 && !f.FilteredIPv6:
		active.Severity = model.SeverityWarn
		active.Message = "incoming connections are not filtered: every listening port is reachable"
		if s := managerStates(f.Managers); s != "" {
			active.Message += " (" + s + ")"
		}
		active.Hint = "fine behind a filtering cloud security group or router; otherwise enable ufw/firewalld or an nftables input chain with policy drop"
	case !f.FilteredIPv6 && v6Listeners:
		active.Severity = model.SeverityWarn
		active.Message = "IPv4 is filtered, IPv6 is not: the ports listening on :: are reachable over IPv6"
		active.Hint = "add the IPv6 rules (inet family tables cover both), or disable IPv6"
	case !f.FilteredIPv4 && v4Listeners:
		active.Severity = model.SeverityWarn
		active.Message = "IPv6 is filtered, IPv4 is not: the ports listening on 0.0.0.0 are reachable over IPv4"
	case !f.FilteredIPv4 || !f.FilteredIPv6:
		active.Message = "only one IP version is filtered, but nothing listens on all addresses of the other: " + strings.Join(filteredBy, ", ")
	default:
		active.Message = "new connections to closed ports are dropped by " + strings.Join(filteredBy, ", ")
	}
	out = append(out, active)

	var open, restricted, unexpected []string
	seen := map[string]bool{}
	for _, l := range f.Listeners {
		reach := l.Reach
		if l.Container {
			reach = Open
		}
		if reach == Filtered {
			continue
		}
		name := fmt.Sprintf("%d/%s", l.Port, l.Protocol)
		if seen[name] {
			continue
		}
		seen[name] = true
		label := name
		switch {
		case l.Container:
			label += " (" + l.Process + ", container: DNAT bypasses the input chain)"
		case l.Process != "":
			label += " (" + l.Process + ")"
		}
		if reach == Restricted {
			restricted = append(restricted, label)
			continue
		}
		open = append(open, label)
		if m.publicPorts != nil && !m.publicPorts[l.Port] {
			unexpected = append(unexpected, label)
		}
	}
	exposed := model.Finding{ID: "firewall.exposed", Subject: "listeners", Severity: model.SeverityOK}
	switch {
	case len(unexpected) > 0:
		exposed.Severity = model.SeverityWarn
		exposed.Message = fmt.Sprintf("%d ports reachable from other machines are not in public_ports: %s", len(unexpected), strings.Join(unexpected, ", "))
		exposed.Hint = "filter them, bind them to 127.0.0.1, or add them to public_ports"
	case len(open) > 0 && m.publicPorts != nil:
		exposed.Message = "only the expected ports are reachable: " + strings.Join(open, ", ")
	case len(open) > 0:
		exposed.Severity = model.SeverityInfo
		exposed.Message = fmt.Sprintf("%d ports reachable from other machines: %s", len(open), strings.Join(open, ", "))
	default:
		exposed.Message = "no listening port is reachable from other machines"
	}
	if len(restricted) > 0 {
		exposed.Evidence = map[string]any{"restricted": strings.Join(restricted, ", ")}
	}
	return append(out, exposed)
}

func managerStates(ms []Manager) string {
	var s []string
	for _, m := range ms {
		s = append(s, m.Name+" "+m.State)
	}
	return strings.Join(s, ", ")
}
