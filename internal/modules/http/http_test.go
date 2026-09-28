package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

func configure(t *testing.T, m *Module, toml string) error {
	t.Helper()
	c, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	return m.Configure(c.Decoder(Name))
}

func TestProbeAndCheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "terminus/") {
			t.Errorf("user agent %q", r.UserAgent())
		}
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	plain := httptest.NewServer(mux)
	defer plain.Close()

	m := New()
	m.transport = srv.Client().Transport // trusts the test certificate
	err := configure(t, m, `
[modules.http]
timeout = "2s"

[[modules.http.endpoints]]
url = "`+srv.URL+`/ok"

[[modules.http.endpoints]]
url = "`+srv.URL+`/moved"

[[modules.http.endpoints]]
url = "`+srv.URL+`/ok?expect=204"
status = 204

[[modules.http.endpoints]]
url = "`+srv.URL+`/broken"

[[modules.http.endpoints]]
url = "`+srv.URL+`/slow"

[[modules.http.endpoints]]
url = "http://127.0.0.1:1/"
`)
	if err != nil {
		t.Fatal(err)
	}
	// The test certificate expires in 2084: pretend it is 10 days before.
	m.now = func() time.Time { return time.Date(2084, 1, 19, 0, 0, 0, 0, time.UTC) }

	got, err := m.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	eps := got.([]Endpoint)
	if eps[0].Status != 200 || eps[0].TLS == nil || eps[1].Status != 302 || eps[5].Error == "" {
		t.Fatalf("endpoints: %+v", eps)
	}

	env := &module.Env{Checks: &module.CheckSettings{Thresholds: map[string]module.Threshold{"http.latency": {Warn: 0.2, Fail: 1}}}}
	findings := m.Check(env, got)
	sev := map[string]model.Severity{}
	for _, f := range findings {
		sev[f.ID+" "+strings.TrimPrefix(f.Subject, srv.URL)] = f.Severity
	}
	want := map[string]model.Severity{
		"http.status /ok":                 model.SeverityOK,
		"http.status /ok?expect=204":      model.SeverityFail,
		"http.status /moved":              model.SeverityOK,
		"http.status /broken":             model.SeverityFail,
		"http.status /slow":               model.SeverityOK,
		"http.status http://127.0.0.1:1/": model.SeverityFail,
		"http.latency /slow":              model.SeverityWarn,
		"http.tls-expiry /ok":             model.SeverityWarn,
	}
	for k, v := range want {
		if sev[k] != v {
			t.Errorf("%s: %s, want %s", k, sev[k], v)
		}
	}
	var expected204 bool
	for _, f := range findings {
		if f.ID == "http.status" && strings.Contains(f.Message, "expected 204") && f.Severity == model.SeverityFail {
			expected204 = true
		}
	}
	if !expected204 {
		t.Error("expected status not checked")
	}
}

func TestConfigureAndSkip(t *testing.T) {
	m := New()
	if _, err := m.Collect(context.Background(), nil); err == nil {
		t.Fatal("no endpoints: expected skip")
	} else if _, ok := err.(*module.SkipError); !ok {
		t.Fatalf("no endpoints: %v", err)
	}
	if err := configure(t, m, "[[modules.http.endpoints]]\nurl = \"ftp://x\"\n"); err == nil {
		t.Error("invalid url accepted")
	}
	if err := configure(t, m, "[modules.http]\ntimeout = \"soon\"\n"); err == nil {
		t.Error("invalid timeout accepted")
	}
	if d := m.Detect(context.Background(), nil); d.Found || d.Config == "" {
		t.Errorf("detect: %+v", d)
	}
}
