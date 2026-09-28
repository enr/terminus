package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

var now = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

const resticOut = `[
 {"time":"2026-03-10T02:00:05.1+01:00","tree":"t","paths":["/home"],"hostname":"srv-01","username":"root","id":"4bd2a5c08e1f9d0a","short_id":"4bd2a5c0",
  "summary":{"total_bytes_processed":123456789}},
 {"time":"2026-03-09T02:00:00Z","paths":["/etc"],"hostname":"srv-01","id":"77aa11bb22cc33dd"}
]`

const borgOut = `{"archives":[{"archive":"srv-01-2026-03-07","barchive":"srv-01-2026-03-07","id":"x","name":"srv-01-2026-03-07","start":"2026-03-07T03:00:00.000000","time":"2026-03-07T03:00:00.000000"}],
"encryption":{"mode":"repokey"},"repository":{"id":"r","last_modified":"2026-03-07T03:10:00.000000","location":"/srv/borg"}}`

const pgbackrestOut = `[{"archive":[],"backup":[
 {"label":"20260308-010001F","type":"full","error":false,"timestamp":{"start":1772931601,"stop":1772932000},"info":{"size":5000000}},
 {"label":"20260308-010001F_20260309-010001I","type":"incr","error":true,"timestamp":{"start":1773018001,"stop":1773018400},"info":{"size":6000000}}
],"name":"main","status":{"code":0,"message":"ok"}}]`

func configure(t *testing.T, m *Module, toml string) error {
	t.Helper()
	c, err := config.Parse(toml)
	if err != nil {
		t.Fatal(err)
	}
	return m.Configure(c.Decoder(Name))
}

const cfg = `
[[modules.backup.repositories]]
name = "home"
type = "restic"
repository = "sftp:nas:/restic"
password_file = "/etc/restic/pass"
env_file = "/etc/restic/env"
host = "srv-01"
unit = "restic.service"

[[modules.backup.repositories]]
type = "borg"
repository = "/srv/borg"
password_file = "/etc/borg/pass"
max_age = "7d"

[[modules.backup.repositories]]
name = "db"
type = "pgbackrest"
repository = "main"
user = "postgres"
unit = "pgbackrest.service"

[[modules.backup.repositories]]
name = "offsite"
type = "restic"
repository = "b2:bucket"

[[modules.backup.repositories]]
name = "empty"
type = "borg"
repository = "/srv/empty"
`

func fakeRunner() *runner.Fake {
	return &runner.Fake{
		Paths: map[string]string{"restic": "/usr/bin/restic", "borg": "/usr/bin/borg", "pgbackrest": "/usr/bin/pgbackrest"},
		Results: map[string]runner.Result{
			"restic snapshots --json --no-lock --latest 1 --host srv-01": {Stdout: []byte(resticOut)},
			"restic snapshots --json --no-lock --latest 1":               {ExitCode: 1, Stderr: []byte("Fatal: unable to open config file\nFatal: wrong password or no key found\n")},
			"borg list --json --last 1 --bypass-lock":                    {Stdout: []byte(borgOut)},
			"postgres|pgbackrest info --output=json --stanza=main":       {Stdout: []byte(pgbackrestOut)},
			"systemctl show --property=LoadState,ActiveState,Result,ExecMainStatus,InactiveEnterTimestamp -- restic.service": {
				Stdout: []byte("LoadState=loaded\nActiveState=inactive\nResult=success\nExecMainStatus=0\nInactiveEnterTimestamp=Tue 2026-03-10 02:10:00 CET\n")},
			"systemctl show --property=LoadState,ActiveState,Result,ExecMainStatus,InactiveEnterTimestamp -- pgbackrest.service": {
				Stdout: []byte("LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainStatus=56\nInactiveEnterTimestamp=Tue 2026-03-10 01:10:00 CET\n")},
		},
	}
}

func TestCollectAndCheck(t *testing.T) {
	m := New()
	m.fs = hostfstest.New(t, map[string]string{"/etc/restic/env": "# credentials\nexport AWS_ACCESS_KEY_ID=abc\nAWS_SECRET_ACCESS_KEY='s3cr3t'\n"})
	m.now = func() time.Time { return now }
	if err := configure(t, m, cfg); err != nil {
		t.Fatal(err)
	}
	r := fakeRunner()
	// The borg repository without archives: the key of the second borg call is the same, so it
	// is told apart by the environment below.
	borgEmpty := `{"archives":[],"repository":{"location":"/srv/empty"}}`
	rr := &envRunner{Fake: r, byEnv: map[string]runner.Result{"BORG_REPO=/srv/empty": {Stdout: []byte(borgEmpty)}}}

	got, err := m.Collect(context.Background(), &module.Env{Runner: rr})
	if err == nil || !strings.Contains(err.Error(), "offsite: restic exited with code 1: Fatal: wrong password or no key found") {
		t.Errorf("error: %v", err)
	}
	f := got.(*Facts)
	home, borg, db, offsite, empty := f.Repositories[0], f.Repositories[1], f.Repositories[2], f.Repositories[3], f.Repositories[4]
	if home.Latest == nil || home.Latest.ID != "4bd2a5c0" || *home.Latest.SizeBytes != 123456789 || home.AgeSeconds != 11*3600-5.1 || home.Job.Result != "success" {
		t.Errorf("restic: %+v %+v", home, home.Latest)
	}
	if borg.Name != "/srv/borg" || borg.Latest == nil || borg.Latest.ID != "srv-01-2026-03-07" || borg.MaxAgeSeconds != 7*24*3600 {
		t.Errorf("borg: %+v", borg)
	}
	if db.Latest == nil || db.Latest.Kind != "incr" || !db.Latest.Failed || db.Job.ExitStatus != 56 {
		t.Errorf("pgbackrest: %+v %+v", db, db.Latest)
	}
	if offsite.Error == "" || empty.Latest != nil || empty.Error != "" {
		t.Errorf("offsite %+v empty %+v", offsite, empty)
	}

	// The environment of restic: env_file, repository, password file.
	var env []string
	for _, c := range rr.Calls {
		if strings.Contains(runner.Key(c), "--host srv-01") {
			env = c.Env
		}
	}
	if strings.Join(env, " ") != "AWS_ACCESS_KEY_ID=abc AWS_SECRET_ACCESS_KEY=s3cr3t RESTIC_REPOSITORY=sftp:nas:/restic RESTIC_PASSWORD_FILE=/etc/restic/pass" {
		t.Errorf("restic env: %v", env)
	}

	fnd := m.Check(nil, f)
	sev := map[string]model.Severity{}
	for _, x := range fnd {
		sev[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"backup.job restic:home restic.service":       model.SeverityOK,
		"backup.age restic:home":                      model.SeverityOK,
		"backup.age borg:/srv/borg":                   model.SeverityOK, // 3 days, max_age 7d
		"backup.job pgbackrest:db pgbackrest.service": model.SeverityFail,
		"backup.age pgbackrest:db":                    model.SeverityWarn, // recent, but ended with an error
		"backup.repository restic:offsite":            model.SeverityFail,
		"backup.repository borg:empty":                model.SeverityFail,
	}
	for k, w := range want {
		if s, ok := sev[k]; !ok || s != w {
			t.Errorf("%s: %v (present %v), want %v", k, s, ok, w)
		}
	}
	if len(fnd) != len(want) {
		t.Errorf("findings: %+v", fnd)
	}

	// Default threshold: the borg archive is 3 days old.
	m.repos[1].maxAge = 0
	f.Repositories[1].MaxAgeSeconds = 0
	for _, x := range m.Check(nil, f) {
		if x.ID == "backup.age" && x.Subject == "borg:/srv/borg" && x.Severity != model.SeverityFail {
			t.Errorf("borg default threshold: %+v", x)
		}
	}
}

// envRunner answers some commands by one of their environment variables.
type envRunner struct {
	*runner.Fake
	byEnv map[string]runner.Result
}

func (e *envRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	for _, v := range c.Env {
		if r, ok := e.byEnv[v]; ok {
			return r, nil
		}
	}
	return e.Fake.Run(ctx, c)
}

func TestConfigure(t *testing.T) {
	m := New()
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("no repositories: %v", err)
	}
	err := configure(t, m, `
[[modules.backup.repositories]]
type = "tar"
repository = "/x"

[[modules.backup.repositories]]
type = "borg"
host = "a"
max_age = "soon"

[[modules.backup.repositories]]
type = "pgbackrest"
repository = "/x"
password_file = "/p"
`)
	for _, want := range []string{`not "tar"`, "repository missing", "host applies only to restic", "invalid max_age", `name "/x" used twice`, "password_file does not apply"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}
}

func TestDetect(t *testing.T) {
	m := New()
	if d := m.Detect(context.Background(), &module.Env{Runner: &runner.Fake{}}); d.Found {
		t.Errorf("nothing installed: %+v", d)
	}
	d := m.Detect(context.Background(), &module.Env{Runner: &runner.Fake{Paths: map[string]string{"borg": "/usr/bin/borg"}}})
	if !d.Found || !strings.Contains(d.Config, `type = "borg"`) {
		t.Errorf("borg: %+v", d)
	}
	if _, err := config.Parse("[modules.backup]\n" + d.Config); err != nil {
		t.Errorf("suggested config: %v", err)
	}
}

func TestParse(t *testing.T) {
	if b, err := parseRestic([]byte("[]")); b != nil || err != nil {
		t.Errorf("empty restic: %v %v", b, err)
	}
	if _, err := parseRestic([]byte("Fatal")); err == nil {
		t.Error("invalid restic")
	}
	if _, err := parseBorg([]byte(`{"archives":[{"name":"a","time":"yesterday"}]}`)); err == nil {
		t.Error("invalid borg time")
	}
	b, err := parsePgBackRest([]byte(`[{"name":"main","backup":[],"status":{"code":2,"message":"no valid backups"}}]`))
	if b != nil || err != nil {
		t.Errorf("no valid backups: %v %v", b, err)
	}
	if _, err := parsePgBackRest([]byte(`[{"name":"main","backup":[],"status":{"code":1,"message":"missing stanza path"}}]`)); err == nil || !strings.Contains(err.Error(), "missing stanza path") {
		t.Errorf("missing stanza: %v", err)
	}
	if _, err := parsePgBackRest([]byte(`[]`)); err == nil {
		t.Error("no stanza")
	}
	if age(1800) != "30m" || age(5*3600) != "5h" || age(76*3600) != "3d 4h" {
		t.Error("age")
	}
	if lastLine([]byte("a\nb\n\n")) != "b" || errors.New(lastLine(nil)).Error() != "" {
		t.Error("lastLine")
	}
}
