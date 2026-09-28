package network

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Check implements module.Checker.
func (*Module) Check(_ *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding

	route := model.Finding{ID: "net.default-route", Subject: "routing", Severity: model.SeverityOK}
	if len(f.DefaultRoutes) == 0 {
		route.Severity = model.SeverityWarn
		route.Message = "no default route: the machine cannot reach other networks"
		route.Hint = "check ip route and the network configuration (NetworkManager, systemd-networkd, netplan)"
	} else {
		var gws []string
		for _, r := range f.DefaultRoutes {
			gws = append(gws, r.Gateway+" via "+r.Interface)
		}
		route.Message = "default route " + strings.Join(gws, ", ")
	}
	out = append(out, route)

	dns := model.Finding{ID: "net.dns", Subject: "resolv.conf", Severity: model.SeverityOK}
	servers := f.DNS.Nameservers
	if f.DNS.Resolver == "systemd-resolved" && f.DNS.UpstreamNameservers != nil {
		servers = f.DNS.UpstreamNameservers
	}
	if len(servers) == 0 {
		dns.Severity = model.SeverityFail
		dns.Message = "no DNS server configured"
		dns.Hint = "names cannot be resolved: check /etc/resolv.conf or resolvectl status"
	} else {
		dns.Message = "DNS servers " + strings.Join(servers, ", ")
	}
	out = append(out, dns)

	var public []string
	for _, l := range f.Listeners {
		if l.Protocol == "tcp" && l.Scope == "any" {
			name := fmt.Sprintf("%d", l.Port)
			if l.Process != "" {
				name += " (" + l.Process + ")"
			}
			public = append(public, name)
		}
	}
	if len(public) > 0 {
		out = append(out, model.Finding{
			ID:       "net.public-listeners",
			Subject:  "tcp",
			Severity: model.SeverityInfo,
			Message:  fmt.Sprintf("%d TCP ports listen on all addresses: %s", len(public), strings.Join(public, ", ")),
			Hint:     "services meant to be internal should listen on 127.0.0.1 or be filtered by the firewall",
		})
	}
	return out
}
