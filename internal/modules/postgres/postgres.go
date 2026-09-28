// Package postgres is an optional module about PostgreSQL instances: who is connected, how close
// to max_connections, the settings in effect, database sizes and stuck transactions. It connects
// with the Go driver: no psql needed on the host (PostgreSQL often runs in a container with a
// published port).
//
//	[modules.postgres]
//	enabled = true
//	dsn = "postgres://monitor@127.0.0.1:5432/postgres?sslmode=disable"
//	password_file = "/etc/terminus/pg.pass"
//
//	[[modules.postgres.instances]]     # more instances
//	name = "app"
//	dsn = "postgres://monitor@127.0.0.1:5433/app"
//
// A role in pg_monitor sees every session: CREATE ROLE monitor LOGIN PASSWORD '...' IN ROLE pg_monitor;
package postgres

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Name of the module.
const Name = "postgres"

const connectTimeout = 5 * time.Second

// Default thresholds.
var (
	connectionsThreshold = module.Threshold{Warn: 0.8, Fail: 0.95, Unit: "ratio"}
	idleTxThreshold      = module.Threshold{Warn: 300, Fail: 3600, Unit: "seconds"}
)

// settings are the parameters reported.
var settings = []string{
	"max_connections", "superuser_reserved_connections", "reserved_connections",
	"shared_buffers", "work_mem", "maintenance_work_mem", "effective_cache_size", "max_wal_size",
}

// Facts about the PostgreSQL instances.
type Facts struct {
	Instances []Instance `json:"instances"`
}

// Instance is a PostgreSQL server.
type Instance struct {
	Name string `json:"name"`
	// Endpoint is host:port/database, without credentials.
	Endpoint  string   `json:"endpoint"`
	Reachable bool     `json:"reachable"`
	Error     string   `json:"error,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Version   string   `json:"version,omitempty"`
	// Settings are in bytes for memory parameters, as numbers otherwise.
	Settings map[string]any `json:"settings,omitempty"`

	MaxConnections int `json:"max_connections"`
	// ReservedConnections are kept for superusers and pg_use_reserved_connections.
	ReservedConnections int `json:"reserved_connections"`
	// ClientConnections are the sessions of clients (not the internal processes).
	ClientConnections int     `json:"client_connections"`
	ConnectionsRatio  float64 `json:"connections_ratio"`
	Sessions          []Group `json:"sessions"`

	OldestTransactionSeconds        float64    `json:"oldest_transaction_seconds"`
	LongestIdleInTransactionSeconds float64    `json:"longest_idle_in_transaction_seconds"`
	Databases                       []Database `json:"databases,omitempty"`
}

// Group counts the sessions by application, client address, state and backend type.
type Group struct {
	Application string `json:"application"`
	Client      string `json:"client"`
	State       string `json:"state,omitempty"`
	BackendType string `json:"backend_type"`
	Count       int    `json:"count"`
}

// Database is a database with its size.
type Database struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

type instanceConfig struct {
	Name         string `toml:"name"`
	DSN          string `toml:"dsn"`
	PasswordFile string `toml:"password_file"`
}

// Module collects the PostgreSQL facts.
type Module struct {
	instances []instanceConfig
}

// New returns the postgres module, without instances until configured.
func New() *Module { return &Module{} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Description implements module.Module.
func (*Module) Description() string {
	return "PostgreSQL: connections by client, max_connections, settings, database sizes, stuck transactions"
}

// Core implements module.Module.
func (*Module) Core() bool { return false }

// ConfigExample implements module.Configurable.
func (*Module) ConfigExample() string {
	return `# Connection string; the password can stay out of it (password_file, PGPASSWORD, ~/.pgpass).
# A role in pg_monitor sees all the sessions.
dsn = "postgres://monitor@127.0.0.1:5432/postgres?sslmode=disable"
password_file = ""
# More instances:
# [[modules.postgres.instances]]
# name = "app"
# dsn = "postgres://monitor@127.0.0.1:5433/app"`
}

// Configure implements module.Configurable.
func (m *Module) Configure(decode module.Decoder) error {
	var c struct {
		DSN          string           `toml:"dsn"`
		PasswordFile string           `toml:"password_file"`
		Instances    []instanceConfig `toml:"instances"`
	}
	if err := decode(&c); err != nil {
		return err
	}
	var list []instanceConfig
	if c.DSN != "" {
		list = append(list, instanceConfig{Name: "default", DSN: c.DSN, PasswordFile: c.PasswordFile})
	}
	list = append(list, c.Instances...)
	seen := map[string]bool{}
	for i, in := range list {
		if in.Name == "" {
			return fmt.Errorf("modules.postgres.instances[%d]: name is required", i)
		}
		if seen[in.Name] {
			return fmt.Errorf("modules.postgres: instance %q defined twice", in.Name)
		}
		seen[in.Name] = true
		if _, err := pgx.ParseConfig(in.DSN); err != nil {
			return fmt.Errorf("modules.postgres %s: invalid dsn: %v", in.Name, redact(err.Error(), in.DSN))
		}
	}
	m.instances = list
	return nil
}

// redact removes the connection string from an error message (it can hold a password).
func redact(msg, dsn string) string {
	if dsn == "" {
		return msg
	}
	return strings.ReplaceAll(msg, dsn, "<dsn>")
}

// Detect implements module.Detector.
func (m *Module) Detect(ctx context.Context, env *module.Env) module.Detection {
	d := net.Dialer{Timeout: time.Second}
	if c, err := d.DialContext(ctx, "tcp", "127.0.0.1:5432"); err == nil {
		c.Close()
		return module.Detection{Found: true, Reason: "PostgreSQL port 5432 listening on 127.0.0.1",
			Config: "enabled = true\ndsn = \"postgres://monitor@127.0.0.1:5432/postgres?sslmode=disable\"\npassword_file = \"/etc/terminus/pg.pass\""}
	}
	if env != nil && env.Runner != nil {
		if _, err := env.Runner.LookPath("postgres"); err == nil {
			return module.Detection{Found: true, Reason: "postgres binary found", Config: "enabled = true\ndsn = \"postgres://monitor@127.0.0.1:5432/postgres\""}
		}
	}
	return module.Detection{Reason: "nothing listens on 127.0.0.1:5432"}
}

// Collect implements module.Module.
func (m *Module) Collect(ctx context.Context, _ *module.Env) (any, error) {
	if len(m.instances) == 0 {
		return nil, module.Skip("no instance configured (dsn or [[modules.postgres.instances]])")
	}
	f := &Facts{Instances: make([]Instance, len(m.instances))}
	var wg sync.WaitGroup
	for i, in := range m.instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Instances[i] = collectInstance(ctx, in)
		}()
	}
	wg.Wait()
	return f, nil
}

func collectInstance(ctx context.Context, in instanceConfig) Instance {
	out := Instance{Name: in.Name, Sessions: []Group{}}
	cfg, err := pgx.ParseConfig(in.DSN)
	if err != nil {
		out.Error = "invalid dsn"
		return out
	}
	out.Endpoint = fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
	if in.PasswordFile != "" {
		b, err := os.ReadFile(in.PasswordFile)
		if err != nil {
			out.Error = "password file: " + err.Error()
			return out
		}
		cfg.Password = strings.TrimSpace(string(b))
	}
	cfg.ConnectTimeout = connectTimeout
	cfg.RuntimeParams["application_name"] = "terminus"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		out.Error = redact(err.Error(), in.DSN)
		return out
	}
	defer conn.Close(context.Background())
	out.Reachable = true
	fail := func(what string, err error) { out.Errors = append(out.Errors, what+": "+err.Error()) }

	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&out.Version); err != nil {
		fail("version", err)
	}
	if err := readSettings(ctx, conn, &out); err != nil {
		fail("settings", err)
	}
	if err := readSessions(ctx, conn, &out); err != nil {
		fail("sessions", err)
	}
	if err := readDatabases(ctx, conn, &out); err != nil {
		fail("databases", err)
	}
	return out
}

func readSettings(ctx context.Context, conn *pgx.Conn, out *Instance) error {
	rows, err := conn.Query(ctx, "SELECT name, setting, coalesce(unit, '') FROM pg_settings WHERE name = ANY($1)", settings)
	if err != nil {
		return err
	}
	defer rows.Close()
	out.Settings = map[string]any{}
	for rows.Next() {
		var name, setting, unit string
		if err := rows.Scan(&name, &setting, &unit); err != nil {
			return err
		}
		v := settingValue(setting, unit)
		out.Settings[name] = v
		n, _ := v.(int64)
		switch name {
		case "max_connections":
			out.MaxConnections = int(n)
		case "superuser_reserved_connections", "reserved_connections":
			out.ReservedConnections += int(n)
		}
	}
	return rows.Err()
}

// settingValue converts a pg_settings value: memory units become bytes, numbers become numbers.
func settingValue(setting, unit string) any {
	n, err := strconv.ParseInt(setting, 10, 64)
	if err != nil {
		return setting
	}
	mult := map[string]int64{"B": 1, "kB": 1 << 10, "8kB": 8 << 10, "16kB": 16 << 10, "MB": 1 << 20, "GB": 1 << 30}[unit]
	if mult == 0 {
		return n
	}
	return n * mult
}

func readSessions(ctx context.Context, conn *pgx.Conn, out *Instance) error {
	rows, err := conn.Query(ctx, `
		SELECT coalesce(application_name, ''), coalesce(host(client_addr), 'local'), coalesce(state, ''),
		       coalesce(backend_type, ''), count(*)
		  FROM pg_stat_activity
		 GROUP BY 1, 2, 3, 4
		 ORDER BY 5 DESC, 1, 2`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.Application, &g.Client, &g.State, &g.BackendType, &g.Count); err != nil {
			return err
		}
		if g.BackendType == "client backend" {
			out.ClientConnections += g.Count
		}
		out.Sessions = append(out.Sessions, g)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if usable := out.MaxConnections - out.ReservedConnections; usable > 0 {
		out.ConnectionsRatio = float64(out.ClientConnections) / float64(usable)
	}
	return conn.QueryRow(ctx, `
		SELECT coalesce(extract(epoch FROM max(now() - xact_start)), 0)::float8,
		       coalesce(extract(epoch FROM max(now() - state_change) FILTER (WHERE state LIKE 'idle in transaction%')), 0)::float8
		  FROM pg_stat_activity
		 WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()`).
		Scan(&out.OldestTransactionSeconds, &out.LongestIdleInTransactionSeconds)
}

func readDatabases(ctx context.Context, conn *pgx.Conn, out *Instance) error {
	rows, err := conn.Query(ctx, `
		SELECT datname, pg_database_size(datname)
		  FROM pg_database
		 WHERE datallowconn AND has_database_privilege(datname, 'CONNECT')
		 ORDER BY 2 DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.Name, &d.SizeBytes); err != nil {
			return err
		}
		out.Databases = append(out.Databases, d)
	}
	return rows.Err()
}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "pg.reachable", Description: "terminus can connect to the instance"},
		{ID: "pg.connections", Description: "client connections over max_connections minus the reserved ones", Threshold: &connectionsThreshold},
		{ID: "pg.idle-in-transaction", Description: "seconds of the longest session idle in a transaction (holds locks, blocks vacuum)", Threshold: &idleTxThreshold},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	conns := env.Threshold("pg.connections", connectionsThreshold)
	idle := env.Threshold("pg.idle-in-transaction", idleTxThreshold)
	var out []model.Finding
	for _, in := range f.Instances {
		reach := model.Finding{ID: "pg.reachable", Subject: in.Name, Severity: model.SeverityOK, Message: "connected to " + in.Endpoint + " (PostgreSQL " + firstField(in.Version) + ")"}
		if !in.Reachable {
			reach.Severity = model.SeverityFail
			reach.Message = "cannot connect to " + in.Endpoint + ": " + in.Error
			reach.Hint = "is the server (or its container) running, and the port published on that address?"
			out = append(out, reach)
			continue
		}
		out = append(out, reach)

		if in.MaxConnections > 0 {
			c := model.Finding{
				ID: "pg.connections", Subject: in.Name, Severity: conns.Grade(in.ConnectionsRatio),
				Message: fmt.Sprintf("%d client connections of %d usable (max_connections %d, %d reserved)",
					in.ClientConnections, in.MaxConnections-in.ReservedConnections, in.MaxConnections, in.ReservedConnections),
				Evidence: map[string]any{"top_clients": topClients(in.Sessions, 5)},
			}
			if c.Severity >= model.SeverityWarn {
				c.Hint = "find who holds them (the top clients) before raising max_connections: a pool size or a leak?"
			}
			out = append(out, c)
		}

		i := model.Finding{
			ID: "pg.idle-in-transaction", Subject: in.Name, Severity: idle.Grade(in.LongestIdleInTransactionSeconds),
			Message: fmt.Sprintf("longest idle in transaction: %.0fs", in.LongestIdleInTransactionSeconds),
		}
		if i.Severity >= model.SeverityWarn {
			i.Hint = "SELECT pid, application_name, state_change FROM pg_stat_activity WHERE state LIKE 'idle in transaction%'; consider idle_in_transaction_session_timeout"
		}
		out = append(out, i)
	}
	return out
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// topClients summarizes the client sessions by application and address.
func topClients(groups []Group, n int) string {
	counts := map[string]int{}
	for _, g := range groups {
		if g.BackendType != "client backend" {
			continue
		}
		app := g.Application
		if app == "" {
			app = "(no name)"
		}
		counts[app+"@"+g.Client] += g.Count
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s ×%d", k, counts[k])
	}
	return strings.Join(parts, ", ")
}
