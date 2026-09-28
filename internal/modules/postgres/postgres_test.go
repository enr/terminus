package postgres

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

func configure(t *testing.T, toml string) (*Module, error) {
	t.Helper()
	c, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	return m, m.Configure(c.Decoder(Name))
}

func TestConfigure(t *testing.T) {
	m, err := configure(t, "[modules.postgres]\ndsn = \"postgres://u@127.0.0.1:5432/db\"\n\n[[modules.postgres.instances]]\nname = \"app\"\ndsn = \"postgres://u@127.0.0.1:5433/app\"\n")
	if err != nil || len(m.instances) != 2 || m.instances[0].Name != "default" || m.instances[1].Name != "app" {
		t.Fatalf("instances: %+v %v", m.instances, err)
	}
	for _, bad := range []string{
		"[[modules.postgres.instances]]\ndsn = \"postgres://x\"\n",
		"[modules.postgres]\ndsn = \"postgres://a\"\n[[modules.postgres.instances]]\nname = \"default\"\ndsn = \"postgres://b\"\n",
		"[modules.postgres]\ndsn = \"postgres://u:secret@host:notaport/db\"\n",
	} {
		_, err := configure(t, bad)
		if err == nil {
			t.Errorf("accepted:\n%s", bad)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("password leaked in error: %v", err)
		}
	}
	if _, err := New().Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "no instance") {
		t.Errorf("skip: %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	m, err := configure(t, "[modules.postgres]\ndsn = \"postgres://u:secret@"+addr+"/db?connect_timeout=2\"\n")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	in := got.(*Facts).Instances[0]
	if in.Reachable || in.Error == "" || strings.Contains(in.Error, "secret") || in.Endpoint != addr+"/db" {
		t.Fatalf("instance: %+v", in)
	}
	fs := m.Check(nil, got)
	if len(fs) != 1 || fs[0].ID != "pg.reachable" || fs[0].Severity != model.SeverityFail {
		t.Errorf("findings: %+v", fs)
	}
}

func TestChecksAndHelpers(t *testing.T) {
	f := &Facts{Instances: []Instance{{
		Name: "default", Endpoint: "127.0.0.1:5432/postgres", Reachable: true, Version: "16.4",
		MaxConnections: 100, ReservedConnections: 3, ClientConnections: 90, ConnectionsRatio: 90.0 / 97,
		LongestIdleInTransactionSeconds: 4000,
		Sessions: []Group{
			{Application: "app", Client: "10.88.0.5", State: "idle", BackendType: "client backend", Count: 80},
			{Application: "", Client: "local", State: "active", BackendType: "client backend", Count: 10},
			{Application: "", Client: "local", BackendType: "checkpointer", Count: 1},
		},
	}}}
	sev := map[string]model.Severity{}
	var conns model.Finding
	for _, x := range New().Check(nil, f) {
		sev[x.ID] = x.Severity
		if x.ID == "pg.connections" {
			conns = x
		}
	}
	if sev["pg.reachable"] != model.SeverityOK || sev["pg.connections"] != model.SeverityWarn || sev["pg.idle-in-transaction"] != model.SeverityFail {
		t.Errorf("findings: %v", sev)
	}
	if conns.Evidence["top_clients"] != "app@10.88.0.5 ×80, (no name)@local ×10" || !strings.Contains(conns.Message, "90 client connections of 97 usable") {
		t.Errorf("connections: %+v", conns)
	}
	for in, want := range map[[2]string]any{
		{"16384", "8kB"}: int64(128 << 20), {"4096", "kB"}: int64(4 << 20), {"100", ""}: int64(100), {"on", ""}: "on",
	} {
		if got := settingValue(in[0], in[1]); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
}

// TestLive needs a server: TERMINUS_LIVE_PG=postgres://monitor:pw@127.0.0.1:5432/app
func TestLive(t *testing.T) {
	dsn := os.Getenv("TERMINUS_LIVE_PG")
	if dsn == "" {
		t.Skip("set TERMINUS_LIVE_PG to a DSN to run against a server")
	}
	ctx := context.Background()
	// A session idle in transaction for a while, and a few more connections.
	idle, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close(ctx)
	if _, err := idle.Exec(ctx, "BEGIN; SELECT 1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		c, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
	}
	time.Sleep(1100 * time.Millisecond)

	// The password goes through password_file, not the DSN.
	cfg, _ := pgx.ParseConfig(dsn)
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte(cfg.Password+"\n"), 0o600)
	noPw := strings.Replace(dsn, ":"+cfg.Password+"@", "@", 1)
	m, err := configure(t, "[modules.postgres]\ndsn = \""+noPw+"\"\npassword_file = \""+pw+"\"\n")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := got.(*Facts).Instances[0]
	t.Logf("%+v", in)
	if !in.Reachable || in.MaxConnections == 0 || in.ClientConnections < 4 || in.LongestIdleInTransactionSeconds < 1 ||
		in.Settings["shared_buffers"] == nil || len(in.Databases) == 0 || len(in.Errors) > 0 {
		t.Fatalf("live facts: %+v", in)
	}
	env := &module.Env{Checks: &module.CheckSettings{Thresholds: map[string]module.Threshold{"pg.idle-in-transaction": {Warn: 1, Fail: 600}}}}
	for _, f := range m.Check(env, got) {
		t.Logf("%s %s %s", f.Severity, f.ID, f.Message)
		if f.ID == "pg.idle-in-transaction" && f.Severity != model.SeverityWarn {
			t.Errorf("idle in transaction not detected: %+v", f)
		}
	}
}
