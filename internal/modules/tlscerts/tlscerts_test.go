package tlscerts

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (i issued) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.cert.Raw}))
}

// issue creates a certificate signed by parent (self-signed when nil).
func issue(t *testing.T, cn string, parent *issued, ca bool, notBefore, notAfter time.Time, dns ...string) issued {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              dns,
		BasicConstraintsValid: true,
		IsCA:                  ca,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return issued{cert: c, key: key}
}

type pki struct {
	root, inter issued
	roots       *x509.CertPool
}

func newPKI(t *testing.T) pki {
	root := issue(t, "Test Root", nil, true, now.AddDate(-5, 0, 0), now.AddDate(5, 0, 0))
	inter := issue(t, "Test Intermediate", &root, true, now.AddDate(-1, 0, 0), now.AddDate(2, 0, 0))
	roots := x509.NewCertPool()
	roots.AddCert(root.cert)
	return pki{root: root, inter: inter, roots: roots}
}

func configure(t *testing.T, m *Module, toml string) error {
	t.Helper()
	c, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	return m.Configure(c.Decoder(Name))
}

func findings(fs []model.Finding, id string) map[string]model.Finding {
	out := map[string]model.Finding{}
	for _, f := range fs {
		if f.ID == id {
			out[f.Subject] = f
		}
	}
	return out
}

func TestFiles(t *testing.T) {
	p := newPKI(t)
	good := issue(t, "example.test", &p.inter, false, now.AddDate(0, -2, 0), now.AddDate(0, 1, 0), "example.test", "www.example.test")
	soon := issue(t, "soon.test", &p.inter, false, now.AddDate(0, -3, 0), now.Add(10*24*time.Hour), "soon.test")
	expired := issue(t, "old.test", &p.inter, false, now.AddDate(0, -3, 0), now.Add(-24*time.Hour), "old.test")
	private := issue(t, "private.test", nil, false, now.AddDate(0, -1, 0), now.AddDate(0, 6, 0), "private.test")
	noSAN := issue(t, "nosan.test", &p.inter, false, now.AddDate(0, -1, 0), now.AddDate(0, 6, 0))
	// Caddy internal CA: 12 hours, 5 left.
	short := issue(t, "internal.test", &p.inter, false, now.Add(-7*time.Hour), now.Add(5*time.Hour), "internal.test")

	fs := hostfstest.New(t, map[string]string{
		"/etc/letsencrypt/archive/example.test/cert1.pem":      good.pem(),
		"/etc/letsencrypt/archive/example.test/chain1.pem":     p.inter.pem(),
		"/etc/letsencrypt/archive/example.test/fullchain1.pem": good.pem() + p.inter.pem(),
		"/etc/letsencrypt/live/example.test/cert.pem":          hostfstest.Symlink + "../../archive/example.test/cert1.pem",
		"/etc/letsencrypt/live/example.test/chain.pem":         hostfstest.Symlink + "../../archive/example.test/chain1.pem",
		"/etc/letsencrypt/live/example.test/fullchain.pem":     hostfstest.Symlink + "../../archive/example.test/fullchain1.pem",
		"/etc/letsencrypt/live/example.test/privkey.pem":       "-----BEGIN PRIVATE KEY-----\nMC4=\n-----END PRIVATE KEY-----\n",
		"/etc/letsencrypt/live/example.test/README":            "not a certificate",
		"/etc/letsencrypt/live/soon.test/cert.pem":             soon.pem(),
		"/etc/letsencrypt/live/old.test/cert.pem":              expired.pem(),
		"/srv/certs/private.crt":                               private.pem(),
		"/srv/certs/nosan.crt":                                 noSAN.pem(),
		"/srv/certs/sub/internal.cer":                          short.pem(),
		"/srv/certs/garbage.pem":                               "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	})
	m := New()
	m.fs, m.now = fs, func() time.Time { return now }
	m.roots = func() *x509.CertPool { return p.roots.Clone() }
	if err := configure(t, m, `
[modules.tls]
paths = ["/etc/letsencrypt/live", "/srv/certs"]
`); err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "garbage.pem") {
		t.Errorf("expected the parse error of garbage.pem, got %v", err)
	}
	f := got.(*Facts)
	byName := map[string]FileCertificate{}
	for _, fc := range f.Files {
		byName[fc.Subject] = fc
	}
	if len(f.Files) != 6 {
		t.Fatalf("files: %+v", f.Files)
	}
	ex := byName["example.test"]
	if strings.Join(ex.Files, ",") != "/etc/letsencrypt/live/example.test/cert.pem,/etc/letsencrypt/live/example.test/fullchain.pem" || !ex.Trusted || ex.Issuer != "Test Intermediate" {
		t.Errorf("example.test: %+v", ex)
	}
	if !byName["old.test"].Trusted {
		t.Errorf("an expired certificate is still verified at a time it was valid: %+v", byName["old.test"])
	}
	if byName["private.test"].Trusted || byName["private.test"].TrustError == "" {
		t.Errorf("private.test: %+v", byName["private.test"])
	}

	fnd := m.Check(nil, f)
	exp := findings(fnd, "tls.expiry")
	want := map[string]model.Severity{
		"/etc/letsencrypt/live/example.test/cert.pem": model.SeverityOK,
		"/etc/letsencrypt/live/soon.test/cert.pem":    model.SeverityWarn,
		"/etc/letsencrypt/live/old.test/cert.pem":     model.SeverityFail,
		"/srv/certs/sub/internal.cer":                 model.SeverityOK,
	}
	for s, sev := range want {
		if exp[s].Severity != sev {
			t.Errorf("tls.expiry %s: %v, want %v (%s)", s, exp[s].Severity, sev, exp[s].Message)
		}
	}
	if !strings.Contains(exp["/etc/letsencrypt/live/old.test/cert.pem"].Message, "expired on") {
		t.Errorf("expired message: %s", exp["/etc/letsencrypt/live/old.test/cert.pem"].Message)
	}
	chain := findings(fnd, "tls.chain")
	if len(chain) != 1 || chain["/srv/certs/private.crt"].Severity != model.SeverityInfo {
		t.Errorf("tls.chain: %+v", chain)
	}
	host := findings(fnd, "tls.hostname")
	if len(host) != 1 || host["/srv/certs/nosan.crt"].Severity != model.SeverityWarn {
		t.Errorf("tls.hostname: %+v", host)
	}

	// Thresholds apply.
	env := &module.Env{Checks: &module.CheckSettings{Thresholds: map[string]module.Threshold{"tls.expiry": {Warn: 60, Fail: 45}}}}
	if s := findings(m.Check(env, f), "tls.expiry")["/etc/letsencrypt/live/example.test/cert.pem"].Severity; s != model.SeverityFail {
		t.Errorf("threshold: %v", s)
	}
}

func TestDefaults(t *testing.T) {
	m := New()
	m.fs = hostfstest.New(t, map[string]string{"/etc/hostname": "x"})
	if _, err := m.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("no store: %v", err)
	}
	if d := m.Detect(context.Background(), nil); d.Found {
		t.Errorf("detect: %+v", d)
	}
	m.fs = hostfstest.New(t, map[string]string{"/etc/letsencrypt/live/README": "x"})
	d := m.Detect(context.Background(), nil)
	if !d.Found || !strings.Contains(d.Config, `paths = ["/etc/letsencrypt/live"]`) {
		t.Errorf("detect: %+v", d)
	}
	got, err := m.Collect(context.Background(), nil)
	if err != nil || len(got.(*Facts).Files) != 0 || got.(*Facts).Paths[0] != "/etc/letsencrypt/live" {
		t.Errorf("collect: %+v %v", got, err)
	}

	// A configured path must exist.
	if err := configure(t, m, "[modules.tls]\npaths = [\"/missing\"]\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "/missing") {
		t.Errorf("missing path: %v", err)
	}
	if err := configure(t, m, "[modules.tls]\npaths = [\"relative\"]\ntimeout = \"x\"\n[[modules.tls.endpoints]]\naddress = \"\"\n"); err == nil ||
		!strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "address missing") {
		t.Errorf("invalid config: %v", err)
	}
}

func serve(t *testing.T, chain ...issued) string {
	t.Helper()
	cert := tls.Certificate{PrivateKey: chain[0].key}
	for _, c := range chain {
		cert.Certificate = append(cert.Certificate, c.cert.Raw)
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

func TestEndpoints(t *testing.T) {
	p := newPKI(t)
	leaf := issue(t, "app.test", &p.inter, false, now.AddDate(0, -1, 0), now.AddDate(0, 2, 0), "app.test", "localhost")
	full := serve(t, leaf, p.inter)
	partial := serve(t, leaf)
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closed.Addr().String()
	closed.Close()

	m := New()
	m.fs = hostfstest.New(t, map[string]string{})
	m.now = func() time.Time { return now }
	m.roots = func() *x509.CertPool { return p.roots.Clone() }
	if err := configure(t, m, `
[modules.tls]
paths = []
timeout = "2s"

[[modules.tls.endpoints]]
address = "`+full+`"
server_name = "app.test"

[[modules.tls.endpoints]]
address = "`+partial+`"
server_name = "app.test"

[[modules.tls.endpoints]]
address = "`+full+`"
server_name = "other.test"

[[modules.tls.endpoints]]
address = "`+closedAddr+`"
`); err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	eps := got.(*Facts).Endpoints
	if eps[0].Error != "" || eps[0].ChainLength != 2 || !eps[0].HostnameMatch || !eps[0].Certificate.Trusted || eps[0].TLSVersion == "" {
		t.Errorf("full chain: %+v", eps[0])
	}
	if eps[1].ChainLength != 1 || eps[1].Certificate.Trusted {
		t.Errorf("partial chain: %+v", eps[1])
	}
	if eps[2].HostnameMatch || eps[3].Error == "" || eps[3].ServerName != "127.0.0.1" {
		t.Errorf("endpoints: %+v %+v", eps[2], eps[3])
	}

	fnd := m.Check(nil, got)
	sub := func(i int) string { return eps[i].ServerName + " (" + eps[i].Address + ")" }
	chain := findings(fnd, "tls.chain")
	if chain[sub(0)].Severity != model.SeverityOK || chain[sub(1)].Severity != model.SeverityFail {
		t.Errorf("tls.chain: %+v", chain)
	}
	host := findings(fnd, "tls.hostname")
	if len(host) != 1 || host[sub(2)].Severity != model.SeverityFail || !strings.Contains(host[sub(2)].Message, "app.test, localhost") {
		t.Errorf("tls.hostname: %+v", host)
	}
	if e := findings(fnd, "tls.endpoint")[closedAddr]; e.Severity != model.SeverityFail {
		t.Errorf("tls.endpoint: %+v", findings(fnd, "tls.endpoint"))
	}
	if e := findings(fnd, "tls.expiry")[sub(0)]; e.Severity != model.SeverityOK {
		t.Errorf("tls.expiry: %+v", e)
	}
}

func TestWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"example.org":     "example.org:443",
		"example.org:993": "example.org:993",
		"::1":             "[::1]:443",
		"[::1]:8443":      "[::1]:8443",
	} {
		if got := withPort(in); got != want {
			t.Errorf("withPort(%q) = %q, want %q", in, got, want)
		}
	}
}
