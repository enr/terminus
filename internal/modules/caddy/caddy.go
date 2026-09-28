// Package caddy is an optional module about the Caddy web server: the sites and domains it
// serves, the upstreams of its reverse proxies, the certificates it serves and whether the
// domains point to this machine.
//
//	[modules.caddy]
//	enabled = true
//	admin = "http://localhost:2019"     # or "unix//run/caddy/admin.sock"; "" to skip it
//	caddyfile = "/etc/caddy/Caddyfile"  # used when the admin API does not answer
//	container = ""                      # run "caddy adapt" in this podman container
//	user = ""                           # owner of the rootless container
//	tls_address = ""                    # where Caddy serves HTTPS; default 127.0.0.1:<https port>
//	public_ips = []                     # IPs of the machine not on its interfaces (NAT)
package caddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// Name of the module.
const Name = "caddy"

const (
	defaultAdmin     = "http://localhost:2019"
	defaultCaddyfile = "/etc/caddy/Caddyfile"
	probeTimeout     = 3 * time.Second
)

// Facts about Caddy.
type Facts struct {
	// Source tells where the configuration comes from: admin API, caddy adapt, container.
	Source  string   `json:"source"`
	Servers []Server `json:"servers"`
	// Domains are all the host names served, sorted.
	Domains []string `json:"domains"`
	// Issuers are the certificate issuers of the TLS automation (acme, internal, zerossl).
	Issuers []string `json:"issuers,omitempty"`
	// Upstreams are the reverse proxy targets with their reachability.
	Upstreams []Upstream `json:"upstreams"`
	// Certificates are the certificates Caddy serves, one per domain.
	Certificates []Certificate `json:"certificates,omitempty"`
	// DNS is the resolution of each domain, compared with the IPs of the machine.
	DNS []DNSRecord `json:"dns,omitempty"`
	// HostIPs are the IPs the DNS records are compared with.
	HostIPs []string `json:"host_ips,omitempty"`
}

// Server is an HTTP server of the configuration.
type Server struct {
	Name   string   `json:"name"`
	Listen []string `json:"listen"`
	Sites  []Site   `json:"sites"`
}

// Site is a top level route: the hosts it matches and what it does.
type Site struct {
	Hosts     []string `json:"hosts,omitempty"`
	Handlers  []string `json:"handlers"`
	Upstreams []string `json:"upstreams,omitempty"`
}

// Upstream is a reverse proxy target.
type Upstream struct {
	Dial      string   `json:"dial"`
	Hosts     []string `json:"hosts,omitempty"`
	Reachable bool     `json:"reachable"`
	Error     string   `json:"error,omitempty"`
}

// Certificate is the certificate served for a domain.
type Certificate struct {
	Domain    string    `json:"domain"`
	Issuer    string    `json:"issuer,omitempty"`
	NotBefore time.Time `json:"not_before,omitzero"`
	NotAfter  time.Time `json:"not_after,omitzero"`
	DaysLeft  float64   `json:"days_left"`
	// Trusted tells whether the chain verifies against the system roots (internal CAs do not).
	Trusted bool   `json:"trusted"`
	Error   string `json:"error,omitempty"`
}

// DNSRecord is the resolution of a domain.
type DNSRecord struct {
	Domain    string   `json:"domain"`
	Addresses []string `json:"addresses,omitempty"`
	// PointsHere tells whether an address belongs to the machine.
	PointsHere bool   `json:"points_here"`
	Error      string `json:"error,omitempty"`
}

// Module collects the Caddy facts.
type Module struct {
	admin, caddyfile, container, user, tlsAddress string
	publicIPs                                     []string
	hostIPs                                       func() ([]string, error)
	resolve                                       func(context.Context, string) ([]string, error)
	now                                           func() time.Time
}

// New returns the caddy module.
func New() *Module {
	return &Module{
		admin:     defaultAdmin,
		caddyfile: defaultCaddyfile,
		hostIPs:   interfaceIPs,
		resolve:   net.DefaultResolver.LookupHost,
		now:       time.Now,
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "Caddy: domains served, reverse proxy upstreams, certificates, DNS of the domains"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Admin API ("unix//run/caddy/admin.sock" for a socket, "" to skip it).
admin = "http://localhost:2019"
# Adapted with "caddy adapt" when the admin API does not answer.
caddyfile = "/etc/caddy/Caddyfile"
# Podman container where Caddy runs ("caddy adapt" is run inside it), and its rootless owner.
container = ""
user = ""
# Where Caddy serves HTTPS; default 127.0.0.1 and the port of the server listening on 443.
tls_address = ""
# Public IPs of the machine that are not on its interfaces (NAT), for the DNS check.
public_ips = []`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Admin      *string  `toml:"admin"`
		Caddyfile  *string  `toml:"caddyfile"`
		Container  string   `toml:"container"`
		User       string   `toml:"user"`
		TLSAddress string   `toml:"tls_address"`
		PublicIPs  []string `toml:"public_ips"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	if c.Admin != nil {
		m.admin = *c.Admin
	}
	if c.Caddyfile != nil {
		m.caddyfile = *c.Caddyfile
	}
	m.container, m.user, m.tlsAddress = c.Container, c.User, c.TLSAddress
	for _, ip := range c.PublicIPs {
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("modules.caddy.public_ips: invalid IP %q", ip)
		}
	}
	m.publicIPs = c.PublicIPs
	if c.TLSAddress != "" {
		if _, _, err := net.SplitHostPort(c.TLSAddress); err != nil {
			return fmt.Errorf("modules.caddy.tls_address: %w", err)
		}
	}
	return nil
}

// Detect implements module.Detector.
func (m *Module) Detect(ctx context.Context, env *module.Env) module.Detection {
	if m.admin != "" {
		if _, err := m.fromAdmin(ctx); err == nil {
			return module.Detection{Found: true, Reason: "admin API at " + m.admin, Config: "enabled = true"}
		}
	}
	if env != nil && env.Runner != nil {
		if p, err := env.Runner.LookPath("caddy"); err == nil {
			return module.Detection{Found: true, Reason: "caddy at " + p, Config: "enabled = true"}
		}
	}
	return module.Detection{Reason: "no caddy binary and no admin API"}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, env *module.Env) (any, error) {
	raw, source, err := m.config(ctx, env)
	if err != nil {
		return nil, err
	}
	f, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	f.Source = source
	m.probe(ctx, f)
	return f, nil
}

// config reads the JSON configuration from the admin API, or adapts the Caddyfile.
func (m *Module) config(ctx context.Context, env *module.Env) ([]byte, string, error) {
	var errs []error
	if m.admin != "" {
		b, err := m.fromAdmin(ctx)
		if err == nil {
			return b, "admin API " + m.admin, nil
		}
		errs = append(errs, err)
	}
	if env != nil && env.Runner != nil {
		b, source, err := m.adapt(ctx, env.Runner)
		if err == nil {
			return b, source, nil
		}
		errs = append(errs, err)
	}
	return nil, "", fmt.Errorf("cannot read the Caddy configuration: %w", errors.Join(errs...))
}

func (m *Module) fromAdmin(ctx context.Context) ([]byte, error) {
	client := &http.Client{Timeout: probeTimeout}
	base := m.admin
	if path, ok := strings.CutPrefix(m.admin, "unix/"); ok {
		client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		base = "http://caddy-admin"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/config/", nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin API: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("admin API: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("admin API: status %d", res.StatusCode)
	}
	return b, nil
}

func (m *Module) adapt(ctx context.Context, r runner.Runner) ([]byte, string, error) {
	cmd := runner.Cmd{Name: "caddy", Args: []string{"adapt", "--config", m.caddyfile}, Timeout: 15 * time.Second}
	source := "caddy adapt " + m.caddyfile
	if m.container != "" {
		cmd = runner.Cmd{Name: "podman", Args: append([]string{"exec", m.container}, append([]string{cmd.Name}, cmd.Args...)...), User: m.user, Timeout: cmd.Timeout}
		source = "caddy adapt in container " + m.container
	} else if _, err := r.LookPath("caddy"); err != nil {
		return nil, "", errors.New("caddy adapt: caddy not found")
	}
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, "", fmt.Errorf("caddy adapt: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, "", fmt.Errorf("caddy adapt: exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, source, nil
}

// probe checks upstreams, certificates and DNS in parallel.
func (m *Module) probe(ctx context.Context, f *Facts) {
	var wg sync.WaitGroup
	for i := range f.Upstreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := &f.Upstreams[i]
			u.Reachable, u.Error = dialUpstream(ctx, u.Dial)
		}()
	}

	var hostIPs []string
	if ips, err := m.hostIPs(); err == nil {
		hostIPs = ips
	}
	hostIPs = append(hostIPs, m.publicIPs...)
	f.HostIPs = hostIPs
	here := map[string]bool{}
	for _, ip := range hostIPs {
		here[net.ParseIP(ip).String()] = true
	}

	tlsAddr := m.tlsAddress
	if tlsAddr == "" {
		tlsAddr = httpsAddress(f.Servers)
	}
	var domains []string
	for _, d := range f.Domains {
		if !strings.Contains(d, "*") && !strings.Contains(d, "{") && net.ParseIP(d) == nil && d != "localhost" {
			domains = append(domains, d)
		}
	}
	if tlsAddr != "" {
		f.Certificates = make([]Certificate, len(domains))
	}
	f.DNS = make([]DNSRecord, len(domains))
	for i, d := range domains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := DNSRecord{Domain: d}
			rctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			addrs, err := m.resolve(rctx, d)
			if err != nil {
				rec.Error = err.Error()
			}
			sort.Strings(addrs)
			rec.Addresses = addrs
			for _, a := range addrs {
				if here[net.ParseIP(a).String()] {
					rec.PointsHere = true
				}
			}
			f.DNS[i] = rec
		}()
		if tlsAddr != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f.Certificates[i] = serverCertificate(ctx, tlsAddr, d, m.now())
			}()
		}
	}
	wg.Wait()
}

// httpsAddress is 127.0.0.1 and the port of the server that listens on 443 (or on the only port
// with TLS, when a server has tls_connection_policies).
func httpsAddress(servers []Server) string {
	for _, s := range servers {
		for _, l := range s.Listen {
			if _, port, err := net.SplitHostPort(strings.TrimPrefix(l, "tcp/")); err == nil && port == "443" {
				return "127.0.0.1:443"
			}
		}
	}
	return ""
}

// dialUpstream tells whether an upstream accepts connections.
func dialUpstream(ctx context.Context, dial string) (bool, string) {
	network, addr := "tcp", dial
	if p, ok := strings.CutPrefix(dial, "unix/"); ok {
		network, addr = "unix", p
	}
	if strings.Contains(addr, "{") {
		return false, "placeholder, not checked"
	}
	if network == "tcp" {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			addr = net.JoinHostPort(addr, "80")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return false, err.Error()
	}
	c.Close()
	return true, ""
}

func interfaceIPs() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.String())
		}
	}
	return out, nil
}

// parseConfig extracts servers, sites, domains and upstreams from the JSON configuration.
func parseConfig(b []byte) (*Facts, error) {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string          `json:"listen"`
					Routes []json.RawMessage `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
			TLS struct {
				Automation struct {
					Policies []struct {
						Issuers []struct {
							Module string `json:"module"`
						} `json:"issuers"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("caddy configuration: %w", err)
	}
	f := &Facts{Servers: []Server{}, Domains: []string{}, Upstreams: []Upstream{}}
	domains := map[string]bool{}
	upstreams := map[string]map[string]bool{}
	for name, s := range cfg.Apps.HTTP.Servers {
		srv := Server{Name: name, Listen: s.Listen, Sites: []Site{}}
		for _, raw := range s.Routes {
			var rt route
			if err := json.Unmarshal(raw, &rt); err != nil {
				return nil, fmt.Errorf("caddy configuration, server %s: %w", name, err)
			}
			site := Site{Hosts: rt.hosts()}
			handlers, ups, nestedHosts := map[string]bool{}, map[string]bool{}, map[string]bool{}
			rt.walk(handlers, ups, nestedHosts)
			for h := range nestedHosts {
				if !contains(site.Hosts, h) {
					site.Hosts = append(site.Hosts, h)
				}
			}
			site.Handlers, site.Upstreams = keys(handlers), keys(ups)
			for _, h := range site.Hosts {
				domains[h] = true
			}
			for _, u := range site.Upstreams {
				if upstreams[u] == nil {
					upstreams[u] = map[string]bool{}
				}
				for _, h := range site.Hosts {
					upstreams[u][h] = true
				}
			}
			srv.Sites = append(srv.Sites, site)
		}
		f.Servers = append(f.Servers, srv)
	}
	sort.Slice(f.Servers, func(i, j int) bool { return f.Servers[i].Name < f.Servers[j].Name })
	f.Domains = keys(domains)
	for _, u := range keys(toSet(upstreams)) {
		f.Upstreams = append(f.Upstreams, Upstream{Dial: u, Hosts: keys(upstreams[u])})
	}
	issuers := map[string]bool{}
	for _, p := range cfg.Apps.TLS.Automation.Policies {
		for _, i := range p.Issuers {
			issuers[i.Module] = true
		}
	}
	f.Issuers = keys(issuers)
	return f, nil
}

// route is a Caddy route; handlers can hold subroutes.
type route struct {
	Match []struct {
		Host []string `json:"host"`
	} `json:"match"`
	Handle []handler `json:"handle"`
}

type handler struct {
	Handler   string  `json:"handler"`
	Routes    []route `json:"routes"`
	Upstreams []struct {
		Dial string `json:"dial"`
	} `json:"upstreams"`
}

func (r route) hosts() []string {
	var out []string
	for _, m := range r.Match {
		out = append(out, m.Host...)
	}
	return out
}

func (r route) walk(handlers, upstreams, hosts map[string]bool) {
	for _, h := range r.Handle {
		if h.Handler != "subroute" {
			handlers[h.Handler] = true
		}
		for _, u := range h.Upstreams {
			if u.Dial != "" {
				upstreams[u.Dial] = true
			}
		}
		for _, sub := range h.Routes {
			for _, host := range sub.hosts() {
				hosts[host] = true
			}
			sub.walk(handlers, upstreams, hosts)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toSet(m map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
