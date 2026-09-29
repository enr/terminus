// Package tlscerts is the optional "tls" module: the TLS certificates stored on the machine
// (Let's Encrypt, Caddy storage, configured paths) and the ones served by configured endpoints,
// with their expiry, chain and host names.
//
//	[modules.tls]
//	enabled = true
//	paths = ["/etc/letsencrypt/live"]
//
//	[[modules.tls.endpoints]]
//	address = "mail.example.org:993"
package tlscerts

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "tls"

const (
	defaultTimeout = 5 * time.Second
	// maxFileSize bounds the files read: certificates are a few KiB.
	maxFileSize = 1 << 20
	// maxFiles bounds the files read under the paths.
	maxFiles = 2000
)

// defaultPaths are the usual certificate stores: certbot, Caddy run by its package (home
// /var/lib/caddy) and by root.
var defaultPaths = []string{
	"/etc/letsencrypt/live",
	"/var/lib/caddy/.local/share/caddy/certificates",
	"/root/.local/share/caddy/certificates",
}

// certExtensions are the file extensions read under the paths.
var certExtensions = map[string]bool{".pem": true, ".crt": true, ".cer": true}

// Facts about the certificates.
type Facts struct {
	// Paths are the directories and files searched.
	Paths []string `json:"paths"`
	// Files are the certificates found in the files, once each (cert.pem and fullchain.pem
	// hold the same one): CA certificates are used only to verify the chains.
	Files     []FileCertificate `json:"files"`
	Endpoints []Endpoint        `json:"endpoints,omitempty"`
}

// Certificate describes a leaf certificate.
type Certificate struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dns_names,omitempty"`
	IPAddresses []string  `json:"ip_addresses,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DaysLeft    float64   `json:"days_left"`
	SHA256      string    `json:"sha256"`
	// Trusted tells whether the chain verifies against the system roots and the configured CA
	// files (at a time when the certificate is valid: expiry is a separate check).
	Trusted    bool   `json:"trusted"`
	TrustError string `json:"trust_error,omitempty"`
}

// FileCertificate is a certificate found in one or more files.
type FileCertificate struct {
	Files []string `json:"files"`
	Certificate
}

// Endpoint is the certificate served by an endpoint.
type Endpoint struct {
	Address    string `json:"address"`
	ServerName string `json:"server_name"`
	Error      string `json:"error,omitempty"`
	TLSVersion string `json:"tls_version,omitempty"`
	// ChainLength is the number of certificates sent, the leaf included.
	ChainLength int `json:"chain_length,omitempty"`
	// HostnameMatch tells whether the certificate covers the server name.
	HostnameMatch bool         `json:"hostname_match"`
	Certificate   *Certificate `json:"certificate,omitempty"`
}

type endpointConfig struct {
	Address    string `toml:"address"`
	ServerName string `toml:"server_name"`
}

// Module collects the certificates.
type Module struct {
	fs         hostfs.FS
	paths      []string
	configured bool
	caFiles    []string
	endpoints  []endpointConfig
	timeout    time.Duration
	now        func() time.Time
	// roots returns the system roots; nil when they are not available.
	roots func() *x509.CertPool
}

// New returns the tls module.
func New() *Module {
	return &Module{
		fs:      hostfs.Host,
		paths:   defaultPaths,
		timeout: defaultTimeout,
		now:     time.Now,
		roots: func() *x509.CertPool {
			p, err := x509.SystemCertPool()
			if err != nil {
				return nil
			}
			return p
		},
	}
}

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "TLS certificates in files and served by endpoints: expiry, chain, host names"
}

// Tables implements module.Tabular.
func (*Module) Tables() map[string]module.Table {
	return map[string]module.Table{
		"files":     {Columns: []string{"subject", "days_left", "not_after", "trusted", "files"}},
		"endpoints": {Columns: []string{"address", "server_name", "days_left=certificate.days_left", "not_after=certificate.not_after", "tls_version", "hostname_match", "error"}},
	}
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Directories (searched recursively for .pem, .crt, .cer) and files; default:
# /etc/letsencrypt/live and the Caddy storage of the caddy user and of root.
paths = ["/etc/letsencrypt/live"]
# Roots of private CAs, trusted besides the system ones.
ca_files = []
timeout = "5s"

# Endpoints: host:port (default port 443); server_name is the SNI, default the host.
# [[modules.tls.endpoints]]
# address = "mail.example.org:993"`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		Paths     *[]string        `toml:"paths"`
		CAFiles   []string         `toml:"ca_files"`
		Timeout   string           `toml:"timeout"`
		Endpoints []endpointConfig `toml:"endpoints"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	var errs []error
	if c.Paths != nil {
		m.paths, m.configured = *c.Paths, true
		for _, p := range m.paths {
			if !filepath.IsAbs(p) {
				errs = append(errs, fmt.Errorf("modules.tls.paths: %q is not an absolute path", p))
			}
		}
	}
	m.caFiles = c.CAFiles
	if c.Timeout != "" {
		d, err := config.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("modules.tls.timeout: invalid duration %q", c.Timeout))
		}
		m.timeout = d
	}
	for i, e := range c.Endpoints {
		if e.Address == "" {
			errs = append(errs, fmt.Errorf("modules.tls.endpoints[%d]: address missing", i))
			continue
		}
		c.Endpoints[i].Address = withPort(e.Address)
		if _, _, err := net.SplitHostPort(c.Endpoints[i].Address); err != nil {
			errs = append(errs, fmt.Errorf("modules.tls.endpoints[%d]: invalid address %q", i, e.Address))
		}
	}
	m.endpoints = c.Endpoints
	return errors.Join(errs...)
}

// withPort adds the HTTPS port to an address without one.
func withPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), "443")
}

// Detect implements module.Detector.
func (m *Module) Detect(context.Context, *module.Env) module.Detection {
	var found []string
	for _, p := range defaultPaths {
		if m.fs.Exists(p) {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return module.Detection{Reason: "no certificate store found (" + strings.Join(defaultPaths, ", ") + "); paths and endpoints can be configured"}
	}
	return module.Detection{
		Found:  true,
		Reason: "certificates in " + strings.Join(found, ", "),
		Config: fmt.Sprintf("enabled = true\npaths = [%q]", strings.Join(found, `", "`)),
	}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, _ *module.Env) (any, error) {
	f := &Facts{Paths: []string{}, Files: []FileCertificate{}}
	var errs []error
	for _, p := range m.paths {
		if m.fs.Exists(p) || m.configured {
			f.Paths = append(f.Paths, p)
		}
	}
	if len(f.Paths) == 0 && len(m.endpoints) == 0 {
		return nil, module.Skip("no certificate store found and no endpoints configured")
	}
	now := m.now()
	roots := m.roots()
	if roots == nil {
		roots = x509.NewCertPool()
		errs = append(errs, errors.New("system roots not available: chains cannot be verified"))
	}
	for _, p := range m.caFiles {
		b, err := m.fs.ReadFile(p)
		if err == nil && !roots.AppendCertsFromPEM(b) {
			err = errors.New("no certificates")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("ca_files %s: %w", p, err))
		}
	}

	leaves, inter, err := m.readFiles(f.Paths)
	if err != nil {
		errs = append(errs, err)
	}
	for _, l := range leaves {
		fc := FileCertificate{Files: l.files, Certificate: describe(l.cert, now)}
		fc.Trusted, fc.TrustError = verify(l.cert, inter, roots, now)
		f.Files = append(f.Files, fc)
	}
	sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].Files[0] < f.Files[j].Files[0] })

	f.Endpoints = make([]Endpoint, len(m.endpoints))
	var wg sync.WaitGroup
	for i, e := range m.endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Endpoints[i] = m.probe(ctx, e, roots, now)
		}()
	}
	wg.Wait()
	return f, errors.Join(errs...)
}

type leaf struct {
	cert  *x509.Certificate
	files []string
}

// readFiles reads the certificates under the paths: the leaves, once each, and a pool with the
// CA certificates found, to verify the chains of the leaves stored apart from their chain
// (cert.pem and chain.pem).
func (m *Module) readFiles(paths []string) ([]*leaf, *x509.CertPool, error) {
	byHash := map[string]*leaf{}
	var leaves []*leaf
	inter := x509.NewCertPool()
	var errs []error
	read := 0
	add := func(p string) {
		b, err := m.readFile(p)
		if err != nil {
			if !hostfs.IsIgnorable(err) {
				errs = append(errs, err)
			}
			return
		}
		read++
		for len(b) > 0 {
			var block *pem.Block
			block, b = pem.Decode(b)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
				continue
			}
			if c.IsCA {
				inter.AddCert(c)
				continue
			}
			h := fingerprint(c)
			l, ok := byHash[h]
			if !ok {
				l = &leaf{cert: c}
				byHash[h] = l
				leaves = append(leaves, l)
			}
			l.files = append(l.files, p)
		}
	}
	for _, root := range paths {
		info, err := os.Stat(m.fs.Path(root))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", root, err))
			continue
		}
		if !info.IsDir() {
			add(root)
			continue
		}
		err = filepath.WalkDir(m.fs.Path(root), func(real string, e fs.DirEntry, err error) error {
			if err != nil {
				if !hostfs.IsIgnorable(err) {
					errs = append(errs, err)
				}
				return nil
			}
			if e.IsDir() || !certExtensions[strings.ToLower(filepath.Ext(e.Name()))] || strings.Contains(strings.ToLower(e.Name()), "key") {
				return nil
			}
			if read >= maxFiles {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(m.fs.Path(root), real)
			add(filepath.Join(root, rel))
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	if read >= maxFiles {
		errs = append(errs, fmt.Errorf("stopped after %d files", maxFiles))
	}
	return leaves, inter, errors.Join(errs...)
}

// readFile reads a certificate file (following symlinks, as certbot's live directory holds),
// refusing big ones.
func (m *Module) readFile(p string) ([]byte, error) {
	info, err := os.Stat(m.fs.Path(p))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil, fs.ErrNotExist
	}
	return m.fs.ReadFile(p)
}

func (m *Module) probe(ctx context.Context, e endpointConfig, roots *x509.CertPool, now time.Time) Endpoint {
	host, _, _ := net.SplitHostPort(e.Address)
	ep := Endpoint{Address: e.Address, ServerName: e.ServerName}
	if ep.ServerName == "" {
		ep.ServerName = host
	}
	cfg := &tls.Config{InsecureSkipVerify: true} // verified below, to report instead of failing
	if net.ParseIP(ep.ServerName) == nil {
		cfg.ServerName = ep.ServerName
	}
	d := tls.Dialer{Config: cfg, NetDialer: &net.Dialer{Timeout: m.timeout}}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", e.Address)
	if err != nil {
		ep.Error = err.Error()
		return ep
	}
	defer conn.Close()
	cs := conn.(*tls.Conn).ConnectionState()
	ep.TLSVersion = tls.VersionName(cs.Version)
	ep.ChainLength = len(cs.PeerCertificates)
	if len(cs.PeerCertificates) == 0 {
		ep.Error = "no certificate"
		return ep
	}
	c := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, ic := range cs.PeerCertificates[1:] {
		inter.AddCert(ic)
	}
	cert := describe(c, now)
	cert.Trusted, cert.TrustError = verify(c, inter, roots, now)
	ep.Certificate = &cert
	ep.HostnameMatch = c.VerifyHostname(ep.ServerName) == nil
	return ep
}

func describe(c *x509.Certificate, now time.Time) Certificate {
	out := Certificate{
		Subject:   name(c.Subject.CommonName, c.Subject.String()),
		Issuer:    name(c.Issuer.CommonName, c.Issuer.String()),
		DNSNames:  c.DNSNames,
		NotBefore: c.NotBefore.UTC(),
		NotAfter:  c.NotAfter.UTC(),
		DaysLeft:  c.NotAfter.Sub(now).Hours() / 24,
		SHA256:    fingerprint(c),
	}
	for _, ip := range c.IPAddresses {
		out.IPAddresses = append(out.IPAddresses, ip.String())
	}
	return out
}

func name(cn, full string) string {
	if cn != "" {
		return cn
	}
	return full
}

func fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}

// verify checks the chain of a certificate at a time when it is valid, so that an expired
// certificate is reported by the expiry check and not also as untrusted.
func verify(c *x509.Certificate, inter, roots *x509.CertPool, now time.Time) (bool, string) {
	at := now
	if at.After(c.NotAfter) {
		at = c.NotAfter.Add(-time.Minute)
	}
	if at.Before(c.NotBefore) {
		at = c.NotBefore.Add(time.Minute)
	}
	_, err := c.Verify(x509.VerifyOptions{
		Intermediates: inter,
		Roots:         roots,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}
