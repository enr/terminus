package tlscerts

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/certs"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

var expiryThreshold = module.Threshold{Warn: 14, Fail: 7, Below: true, Unit: "days"}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "tls.expiry", Description: "days before a certificate (file or endpoint) expires", Threshold: &expiryThreshold},
		{ID: "tls.chain", Description: "the chain does not verify against the system roots and ca_files: fail for endpoints, info for files"},
		{ID: "tls.hostname", Description: "an endpoint serves a certificate that does not cover its name; a certificate without SAN"},
		{ID: "tls.endpoint", Description: "an endpoint does not answer with TLS"},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	expiry := env.Threshold("tls.expiry", expiryThreshold)
	var out []model.Finding
	for _, fc := range f.Files {
		subject := fc.Files[0]
		out = append(out, expiryFinding(subject, fc.Certificate, expiry))
		if !fc.Trusted {
			out = append(out, model.Finding{
				ID:       "tls.chain",
				Subject:  subject,
				Severity: model.SeverityInfo,
				Message:  "not trusted by the system roots: " + fc.TrustError,
				Evidence: map[string]any{"issuer": fc.Issuer},
				Hint:     "a private CA (add its root to ca_files) or a file without its intermediate certificates",
			})
		}
		if len(fc.DNSNames) == 0 && len(fc.IPAddresses) == 0 {
			out = append(out, noSAN(subject, fc.Certificate))
		}
	}
	for _, e := range f.Endpoints {
		subject := e.Address
		if e.ServerName != "" && !strings.HasPrefix(e.Address, e.ServerName+":") && !strings.HasPrefix(e.Address, "["+e.ServerName+"]:") {
			subject = e.ServerName + " (" + e.Address + ")"
		}
		if e.Error != "" {
			out = append(out, model.Finding{
				ID:       "tls.endpoint",
				Subject:  subject,
				Severity: model.SeverityFail,
				Message:  "TLS connection failed: " + e.Error,
				Hint:     "is the service listening with TLS on that port? (STARTTLS ports such as 25 and 587 are not supported)",
			})
			continue
		}
		c := *e.Certificate
		out = append(out, expiryFinding(subject, c, expiry))
		chain := model.Finding{
			ID:       "tls.chain",
			Subject:  subject,
			Severity: model.SeverityOK,
			Message:  fmt.Sprintf("chain verified (%d certificates sent, %s)", e.ChainLength, e.TLSVersion),
		}
		if !c.Trusted {
			chain.Severity = model.SeverityFail
			chain.Message = "chain not verified: " + c.TrustError
			chain.Evidence = map[string]any{"issuer": c.Issuer, "chain_length": e.ChainLength}
			chain.Hint = "clients reject it: serve the full chain (fullchain.pem, not cert.pem), or add a private CA to ca_files"
		}
		out = append(out, chain)
		switch {
		case len(c.DNSNames) == 0 && len(c.IPAddresses) == 0:
			out = append(out, noSAN(subject, c))
		case !e.HostnameMatch:
			out = append(out, model.Finding{
				ID:       "tls.hostname",
				Subject:  subject,
				Severity: model.SeverityFail,
				Message:  fmt.Sprintf("the certificate does not cover %s (it covers %s)", e.ServerName, strings.Join(append(append([]string{}, c.DNSNames...), c.IPAddresses...), ", ")),
				Hint:     "the domain is missing from the certificate, or the server picks another certificate for this name",
			})
		}
	}
	return out
}

func expiryFinding(subject string, c Certificate, t module.Threshold) model.Finding {
	fnd := model.Finding{
		ID:       "tls.expiry",
		Subject:  subject,
		Severity: certs.GradeExpiry(c.NotBefore, c.NotAfter, c.DaysLeft, t),
		Message:  fmt.Sprintf("%s expires in %.1f days (%s, %s)", c.Subject, c.DaysLeft, c.NotAfter.Format("2006-01-02 15:04"), c.Issuer),
		Evidence: map[string]any{"not_after": c.NotAfter, "dns_names": strings.Join(c.DNSNames, ", ")},
	}
	if c.DaysLeft <= 0 {
		fnd.Message = fmt.Sprintf("%s expired on %s (%s)", c.Subject, c.NotAfter.Format("2006-01-02 15:04"), c.Issuer)
	}
	if fnd.Severity >= model.SeverityWarn {
		fnd.Hint = "renewal is not working: check the ACME client (certbot renew --dry-run, caddy logs); a certificate no longer used can be removed"
	}
	return fnd
}

func noSAN(subject string, c Certificate) model.Finding {
	return model.Finding{
		ID:       "tls.hostname",
		Subject:  subject,
		Severity: model.SeverityWarn,
		Message:  c.Subject + " has no subject alternative names: browsers and Go clients ignore the common name",
	}
}
