package caddy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/config.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseConfig(t *testing.T) {
	f, err := parseConfig(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Domains, ",") != "app.example.test,example.test,legacy.example.test,www.example.test" {
		t.Errorf("domains: %v", f.Domains)
	}
	var dials []string
	for _, u := range f.Upstreams {
		dials = append(dials, u.Dial+"="+strings.Join(u.Hosts, "+"))
	}
	if strings.Join(dials, ",") != "127.0.0.1:18081=app.example.test,127.0.0.1:18082=app.example.test,127.0.0.1:18083=app.example.test,localhost:18084=legacy.example.test" {
		t.Errorf("upstreams: %v", dials)
	}
	if len(f.Servers) != 2 || f.Servers[0].Listen[0] != ":443" || f.Servers[1].Listen[0] != ":8080" || strings.Join(f.Issuers, ",") != "internal" {
		t.Errorf("servers: %+v issuers %v", f.Servers, f.Issuers)
	}
	var www Site
	for _, s := range f.Servers[0].Sites {
		if len(s.Hosts) > 0 && s.Hosts[0] == "www.example.test" {
			www = s
		}
	}
	if strings.Join(www.Handlers, ",") != "file_server,vars" || len(www.Upstreams) != 0 {
		t.Errorf("www: %+v", www)
	}
	if httpsAddress(f.Servers) != "127.0.0.1:443" || httpsAddress(nil) != "" {
		t.Error("https address")
	}
	if _, err := parseConfig([]byte("not json")); err == nil {
		t.Error("invalid config accepted")
	}
}

func TestCollectFromAdminWithProbes(t *testing.T) {
	// Replace the fixture upstreams with a live listener and a closed port.
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closed.Addr().String()
	closed.Close()
	cfg := strings.NewReplacer("127.0.0.1:18081", live.Addr().String(), "127.0.0.1:18082", closedAddr,
		"127.0.0.1:18083", live.Addr().String(), "localhost:18084", "{http.reverse_proxy.upstream}").Replace(string(fixture(t)))

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(cfg))
	}))
	defer admin.Close()
	// The test TLS server serves a certificate for example.com, valid until 2084.
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()

	m := New()
	c, _ := config.Parse("[modules.caddy]\nadmin = \"" + admin.URL + "\"\ntls_address = \"" + tlsSrv.Listener.Addr().String() + "\"\npublic_ips = [\"203.0.113.7\"]\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	m.hostIPs = func() ([]string, error) { return []string{"10.0.0.5"}, nil }
	m.resolve = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "app.example.test":
			return []string{"203.0.113.7"}, nil
		case "www.example.test":
			return []string{"198.51.100.1"}, nil
		}
		return nil, errors.New("no such host")
	}
	m.now = func() time.Time { return time.Date(2084, 1, 25, 0, 0, 0, 0, time.UTC) }

	got, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if !strings.HasPrefix(f.Source, "admin API") || len(f.Certificates) != 4 || f.Certificates[0].Error != "" {
		t.Fatalf("facts: %+v", f)
	}

	sev := map[string]model.Severity{}
	for _, x := range m.Check(nil, f) {
		sev[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"caddy.upstream " + live.Addr().String(): model.SeverityOK,
		"caddy.upstream " + closedAddr:           model.SeverityFail,
		"caddy.tls-expiry app.example.test":      model.SeverityFail, // under 5 days left
		"caddy.dns app.example.test":             model.SeverityOK,   // public_ips
		"caddy.dns www.example.test":             model.SeverityWarn,
		"caddy.dns example.test":                 model.SeverityWarn,
	}
	for k, v := range want {
		if sev[k] != v {
			t.Errorf("%s: %s, want %s", k, sev[k], v)
		}
	}
	if _, ok := sev["caddy.upstream {http.reverse_proxy.upstream}"]; ok {
		t.Error("placeholder upstream checked")
	}
}

func TestAdaptFallback(t *testing.T) {
	r := &runner.Fake{
		Paths: map[string]string{"caddy": "/usr/bin/caddy"},
		Results: map[string]runner.Result{
			"caddy adapt --config /etc/caddy/Caddyfile":                      {Stdout: fixture(t)},
			"apps|podman exec web caddy adapt --config /etc/caddy/Caddyfile": {Stdout: fixture(t)},
		},
	}
	m := New()
	m.admin = "http://127.0.0.1:1" // nothing listens there
	m.hostIPs = func() ([]string, error) { return nil, nil }
	m.resolve = func(context.Context, string) ([]string, error) { return nil, errors.New("offline") }
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil || got.(*Facts).Source != "caddy adapt /etc/caddy/Caddyfile" {
		t.Fatalf("adapt: %v %v", got, err)
	}

	c, _ := config.Parse("[modules.caddy]\nadmin = \"\"\ncontainer = \"web\"\nuser = \"apps\"\n")
	m.Configure(c.Decoder(Name))
	got, err = m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil || got.(*Facts).Source != "caddy adapt in container web" {
		t.Fatalf("container: %v %v", got, err)
	}

	m.container = ""
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "caddy not found") {
		t.Errorf("no source: %v", err)
	}
}

func TestAdminUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "admin.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t)) })}
	go srv.Serve(l)
	defer srv.Close()
	m := New()
	m.admin = "unix/" + sock
	if b, err := m.fromAdmin(context.Background()); err != nil || len(b) == 0 {
		t.Fatalf("unix admin: %v", err)
	}
	if d := m.Detect(context.Background(), nil); !d.Found {
		t.Errorf("detect: %+v", d)
	}
}

func TestGradeExpiryAndConfig(t *testing.T) {
	now := time.Now()
	long := Certificate{NotBefore: now.Add(-80 * 24 * time.Hour), NotAfter: now.Add(10 * 24 * time.Hour), DaysLeft: 10}
	internal := Certificate{NotBefore: now.Add(-11 * time.Hour), NotAfter: now.Add(time.Hour), DaysLeft: 1.0 / 24}
	fresh := Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(11 * time.Hour), DaysLeft: 11.0 / 24}
	expired := Certificate{NotBefore: now.Add(-13 * time.Hour), NotAfter: now.Add(-time.Hour), DaysLeft: -1.0 / 24}
	for c, want := range map[*Certificate]model.Severity{&long: model.SeverityWarn, &internal: model.SeverityWarn, &fresh: model.SeverityOK, &expired: model.SeverityFail} {
		if got := gradeExpiry(*c, expiryThreshold); got != want {
			t.Errorf("%+v: %s, want %s", *c, got, want)
		}
	}
	for _, bad := range []string{`public_ips = ["x"]`, `tls_address = "nope"`} {
		c, _ := config.Parse("[modules.caddy]\n" + bad + "\n")
		if err := New().Configure(c.Decoder(Name)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
