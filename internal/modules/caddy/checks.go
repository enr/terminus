package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

var expiryThreshold = module.Threshold{Warn: 14, Fail: 7, Below: true, Unit: "days"}

// serverCertificate reads the certificate Caddy serves for a domain, connecting to Caddy itself
// (not to what the DNS points to) with the domain as SNI.
func serverCertificate(ctx context.Context, addr, domain string, now time.Time) Certificate {
	c := Certificate{Domain: domain}
	d := tls.Dialer{Config: &tls.Config{ServerName: domain, InsecureSkipVerify: true}, NetDialer: &net.Dialer{Timeout: probeTimeout}}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		c.Error = "no certificate"
		return c
	}
	leaf := certs[0]
	c.Issuer = leaf.Issuer.CommonName
	c.NotBefore = leaf.NotBefore.UTC()
	c.NotAfter = leaf.NotAfter.UTC()
	c.DaysLeft = leaf.NotAfter.Sub(now).Hours() / 24
	inter := x509.NewCertPool()
	for _, ic := range certs[1:] {
		inter.AddCert(ic)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: domain, Intermediates: inter, CurrentTime: now})
	c.Trusted = err == nil
	return c
}

// shortLived are certificates graded on the share of their lifetime left instead of days: the
// Caddy internal CA (12 hours) and the short-lived ACME certificates (6 days).
const shortLived = 30 * 24 * time.Hour

// gradeExpiry grades the expiry of a certificate. Caddy renews when a third of the lifetime is
// left: for short-lived certificates less than a sixth left means the renewal is late.
func gradeExpiry(c Certificate, t module.Threshold) model.Severity {
	lifetime := c.NotAfter.Sub(c.NotBefore)
	if c.NotBefore.IsZero() || lifetime <= 0 || lifetime >= shortLived {
		return t.Grade(c.DaysLeft)
	}
	switch left := c.DaysLeft * 24 * float64(time.Hour) / float64(lifetime); {
	case c.DaysLeft <= 0:
		return model.SeverityFail
	case left < 1.0/6:
		return model.SeverityWarn
	default:
		return model.SeverityOK
	}
}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "caddy.upstream", Description: "reverse proxy upstreams that do not accept connections"},
		{ID: "caddy.tls-expiry", Description: "days before the certificate Caddy serves for a domain expires", Threshold: &expiryThreshold},
		{ID: "caddy.dns", Description: "domains that do not resolve to an IP of this machine (behind a proxy/CDN: disable it)"},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	for _, u := range f.Upstreams {
		if strings.Contains(u.Error, "placeholder") {
			continue
		}
		fnd := model.Finding{ID: "caddy.upstream", Subject: u.Dial, Severity: model.SeverityOK, Message: "accepts connections"}
		if len(u.Hosts) > 0 {
			fnd.Message += " (" + strings.Join(u.Hosts, ", ") + ")"
		}
		if !u.Reachable {
			fnd.Severity = model.SeverityFail
			fnd.Message = "does not accept connections: " + u.Error
			if len(u.Hosts) > 0 {
				fnd.Message += "; sites affected: " + strings.Join(u.Hosts, ", ")
			}
			fnd.Hint = "is the service (or its container) running and listening on that address? ss -ltnp"
		}
		out = append(out, fnd)
	}

	expiry := env.Threshold("caddy.tls-expiry", expiryThreshold)
	for _, c := range f.Certificates {
		fnd := model.Finding{ID: "caddy.tls-expiry", Subject: c.Domain}
		if c.Error != "" {
			fnd.Severity = model.SeverityFail
			fnd.Message = "no certificate served: " + c.Error
			fnd.Hint = "check the Caddy logs for ACME errors (journalctl -u caddy)"
			out = append(out, fnd)
			continue
		}
		fnd.Severity = gradeExpiry(c, expiry)
		fnd.Message = fmt.Sprintf("certificate expires in %.1f days (%s, %s)", c.DaysLeft, c.NotAfter.Format("2006-01-02 15:04"), c.Issuer)
		fnd.Evidence = map[string]any{"not_after": c.NotAfter, "trusted": c.Trusted}
		if !c.Trusted {
			fnd.Message += "; not trusted by the system roots (internal CA?)"
		}
		if fnd.Severity >= model.SeverityWarn {
			fnd.Hint = "Caddy renews certificates about 30 days before expiry: look for ACME errors in its logs"
		}
		out = append(out, fnd)
	}

	for _, d := range f.DNS {
		fnd := model.Finding{ID: "caddy.dns", Subject: d.Domain, Severity: model.SeverityOK}
		switch {
		case d.Error != "":
			fnd.Severity = model.SeverityWarn
			fnd.Message = "does not resolve: " + d.Error
			fnd.Hint = "Caddy cannot obtain certificates for names that do not resolve"
		case !d.PointsHere:
			fnd.Severity = model.SeverityWarn
			fnd.Message = "resolves to " + strings.Join(d.Addresses, ", ") + ", not to this machine"
			fnd.Hint = "fine behind a proxy or CDN (disable caddy.dns); behind NAT add the public IP to public_ips"
			fnd.Evidence = map[string]any{"host_ips": strings.Join(f.HostIPs, ", ")}
		default:
			fnd.Message = "resolves to this machine (" + strings.Join(d.Addresses, ", ") + ")"
		}
		out = append(out, fnd)
	}
	return out
}
