// Package http is an optional module that probes HTTP endpoints listed in the configuration:
// status code, latency and TLS certificate expiry.
//
//	[modules.http]
//	enabled = true
//	timeout = "10s"
//
//	[[modules.http.endpoints]]
//	url = "https://example.org/"
//	status = 200             # expected status; default: any 2xx or 3xx
package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "http"

const defaultTimeout = 10 * time.Second

// Default thresholds.
var (
	latencyThreshold = module.Threshold{Warn: 2, Fail: 5, Unit: "seconds"}
	expiryThreshold  = module.Threshold{Warn: 14, Fail: 7, Below: true, Unit: "days"}
)

// Endpoint is the outcome of a probe.
type Endpoint struct {
	URL            string   `json:"url"`
	ExpectedStatus int      `json:"expected_status,omitempty"`
	Status         int      `json:"status,omitempty"`
	DurationMs     int64    `json:"duration_ms"`
	Error          string   `json:"error,omitempty"`
	TLS            *TLSInfo `json:"tls,omitempty"`
}

// TLSInfo describes the certificate served by the endpoint.
type TLSInfo struct {
	Subject  string    `json:"subject"`
	Issuer   string    `json:"issuer"`
	DNSNames []string  `json:"dns_names,omitempty"`
	NotAfter time.Time `json:"not_after"`
	DaysLeft float64   `json:"days_left"`
}

type endpointConfig struct {
	URL    string `toml:"url"`
	Status int    `toml:"status"`
}

// Module probes the configured endpoints.
type Module struct {
	endpoints []endpointConfig
	timeout   time.Duration
	transport http.RoundTripper
	now       func() time.Time
}

// New returns the http module, without endpoints until configured.
func New() *Module { return &Module{timeout: defaultTimeout, now: time.Now} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "HTTP endpoints: status, latency, TLS certificate expiry"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Timeout   string           `toml:"timeout"`
		Endpoints []endpointConfig `toml:"endpoints"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Timeout != "" {
		d, err := config.ParseDuration(c.Timeout)
		if err != nil {
			return fmt.Errorf("modules.http.timeout: %w", err)
		}
		m.timeout = d
	}
	var errs []error
	for i, e := range c.Endpoints {
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("modules.http.endpoints[%d]: invalid url %q", i, e.URL))
		}
	}
	m.endpoints = c.Endpoints
	return errors.Join(errs...)
}

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `timeout = "10s"

[[modules.http.endpoints]]
url = "https://example.org/"
# status = 200  # expected status; default: any 2xx or 3xx`
}

// Detect implements module.Detector: endpoints cannot be guessed, they must be configured.
func (m *Module) Detect(context.Context, *module.Env) module.Detection {
	return module.Detection{
		Reason: "endpoints to probe must be listed in the configuration",
		Config: "enabled = true\n" + m.ConfigExample(),
	}
}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "http.status", Description: "the endpoint answers with the expected status (default 2xx/3xx)"},
		{ID: "http.latency", Description: "time to the response headers", Threshold: &latencyThreshold},
		{ID: "http.tls-expiry", Description: "days before the TLS certificate expires", Threshold: &expiryThreshold},
	}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, _ *module.Env) (any, error) {
	if len(m.endpoints) == 0 {
		return nil, module.Skip("no endpoints configured ([[modules.http.endpoints]])")
	}
	client := &http.Client{
		Timeout:   m.timeout,
		Transport: m.transport,
		// Redirects are answers too: report them instead of following them.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	out := make([]Endpoint, len(m.endpoints))
	var wg sync.WaitGroup
	for i, e := range m.endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = m.probe(ctx, client, e)
		}()
	}
	wg.Wait()
	return out, nil
}

func (m *Module) probe(ctx context.Context, client *http.Client, e endpointConfig) Endpoint {
	ep := Endpoint{URL: e.URL, ExpectedStatus: e.Status}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		ep.Error = err.Error()
		return ep
	}
	v := buildinfo.Version
	if v == "" {
		v = "dev"
	}
	req.Header.Set("User-Agent", "terminus/"+v)
	start := time.Now()
	res, err := client.Do(req)
	ep.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		ep.Error = err.Error()
		return ep
	}
	res.Body.Close()
	ep.Status = res.StatusCode
	ep.TLS = tlsInfo(res.TLS, m.now())
	return ep
}

func tlsInfo(cs *tls.ConnectionState, now time.Time) *TLSInfo {
	if cs == nil || len(cs.PeerCertificates) == 0 {
		return nil
	}
	c := cs.PeerCertificates[0]
	return &TLSInfo{
		Subject:  c.Subject.String(),
		Issuer:   c.Issuer.String(),
		DNSNames: c.DNSNames,
		NotAfter: c.NotAfter.UTC(),
		DaysLeft: c.NotAfter.Sub(now).Hours() / 24,
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	eps, ok := facts.([]Endpoint)
	if !ok {
		return nil
	}
	latency := env.Threshold("http.latency", latencyThreshold)
	expiry := env.Threshold("http.tls-expiry", expiryThreshold)
	var out []model.Finding
	for _, e := range eps {
		status := model.Finding{ID: "http.status", Subject: e.URL, Severity: model.SeverityOK}
		switch {
		case e.Error != "":
			status.Severity = model.SeverityFail
			status.Message = "request failed: " + e.Error
			status.Hint = "check DNS, the reverse proxy and the service behind it"
		case e.ExpectedStatus != 0 && e.Status != e.ExpectedStatus:
			status.Severity = model.SeverityFail
			status.Message = fmt.Sprintf("status %d, expected %d", e.Status, e.ExpectedStatus)
		case e.ExpectedStatus == 0 && e.Status >= 400:
			status.Severity = model.SeverityFail
			status.Message = fmt.Sprintf("status %d", e.Status)
		default:
			status.Message = fmt.Sprintf("status %d", e.Status)
		}
		status.Evidence = map[string]any{"status": e.Status, "duration_ms": e.DurationMs}
		out = append(out, status)
		if e.Error != "" {
			continue
		}

		secs := float64(e.DurationMs) / 1000
		out = append(out, model.Finding{
			ID:       "http.latency",
			Subject:  e.URL,
			Severity: latency.Grade(secs),
			Message:  fmt.Sprintf("answered in %.2fs", secs),
		})
		if e.TLS != nil {
			f := model.Finding{
				ID:       "http.tls-expiry",
				Subject:  e.URL,
				Severity: expiry.Grade(e.TLS.DaysLeft),
				Message:  fmt.Sprintf("certificate expires in %.0f days (%s)", e.TLS.DaysLeft, e.TLS.NotAfter.Format("2006-01-02")),
				Evidence: map[string]any{"issuer": e.TLS.Issuer, "not_after": e.TLS.NotAfter},
			}
			if f.Severity >= model.SeverityWarn {
				f.Hint = "renewal is not working: check the ACME client (caddy, certbot) logs"
			}
			out = append(out, f)
		}
	}
	return out
}
