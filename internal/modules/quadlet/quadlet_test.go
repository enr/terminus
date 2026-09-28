package quadlet

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

const dbContainer = `[Unit]
Description=Postgres

[Container]
Image=docker.io/library/postgres:16
Volume=pgdata:/var/lib/postgresql/data
Network=app.network
Environment=POSTGRES_PASSWORD=x \
  POSTGRES_DB=app

[Service]
MemoryMax=512M
`

const pgdataVolume = "[Volume]\nVolumeName=pgdata-vol\nUser=999\nGroup=999\n"

var apps = runner.Account{Name: "apps", UID: 1001, GID: 1001, Home: "/home/apps"}

func lookup(n string) (runner.Account, error) {
	if n == "apps" || n == "1001" {
		return apps, nil
	}
	return runner.Account{}, errors.New("unknown user " + n)
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRootlessScope(t *testing.T) {
	dir := "/home/apps/.config/containers/systemd"
	fs := hostfstest.New(t, map[string]string{
		"/usr/libexec/podman/quadlet":                        "",
		"/run/systemd/system/.keep":                          "",
		"/run/user/1001/systemd/.keep":                       "",
		"/var/lib/systemd/linger/apps":                       "",
		dir + "/db.container":                                dbContainer,
		dir + "/pgdata.volume":                               pgdataVolume,
		dir + "/app.network":                                 "[Network]\nSubnet=10.90.0.0/24\n",
		dir + "/broken.container":                            "[Container]\nImagee=x\n",
		dir + "/notes.txt":                                   "ignored",
		"/etc/containers/systemd/users/proxy.container":      "[Container]\nImage=caddy\n",
		"/etc/containers/systemd/users/2000/other.container": "[Container]\nImage=x\n",
		"/etc/subuid":                                        "apps:100000:65536\n",
		"/home/apps/.local/share/containers/storage/volumes/pgdata-vol/_data/.keep": "",
	})
	started := time.Now().Add(-time.Hour).Unix()
	show := strings.Join([]string{
		"Id=app-network.service\nLoadState=loaded\nActiveState=active\nSubState=exited\nActiveEnterTimestamp=@" + itoa(started),
		"Id=broken.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nActiveEnterTimestamp=",
		"Id=db.service\nLoadState=loaded\nActiveState=active\nSubState=running\nActiveEnterTimestamp=@" + itoa(started),
		"Id=pgdata-volume.service\nLoadState=loaded\nActiveState=inactive\nSubState=dead\nActiveEnterTimestamp=",
		"Id=proxy.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nActiveEnterTimestamp=",
	}, "\n\n") + "\n"
	volumes := `[{"Name":"pgdata","Mountpoint":"/home/apps/.local/share/containers/storage/volumes/pgdata/_data"},
	{"Name":"pgdata-vol","Mountpoint":"/home/apps/.local/share/containers/storage/volumes/pgdata-vol/_data"}]`
	r := &runner.Fake{
		Paths: map[string]string{"podman": "/usr/bin/podman"},
		Results: map[string]runner.Result{
			"apps|/usr/libexec/podman/quadlet -dryrun -user": {Stdout: read(t, "dryrun-bug.out"), Stderr: read(t, "dryrun-bug.err"), ExitCode: 1},
			"apps|systemctl --user show --no-pager --timestamp=unix -p Id,LoadState,ActiveState,SubState,ActiveEnterTimestamp app-network.service broken.service db.service pgdata-volume.service proxy.service": {Stdout: []byte(show)},
			"apps|podman volume ls --format json": {Stdout: []byte(volumes)},
		},
	}
	m := New()
	m.fs = fs
	m.resolver = users.Resolver{FS: fs, Lookup: lookup, Euid: func() int { return 0 }}
	c, _ := config.Parse("[modules.quadlet]\nrootful = false\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if len(f.Scopes) != 1 {
		t.Fatalf("scopes: %+v", f.Scopes)
	}
	s := f.Scopes[0]
	var names []string
	for _, fl := range s.Files {
		names = append(names, fl.Name)
	}
	if strings.Join(names, ",") != "app.network,broken.container,db.container,pgdata.volume,proxy.container" {
		t.Fatalf("files: %v", names)
	}
	if strings.Join(s.Dirs, ":") != dir+":/etc/containers/systemd/users" || s.DryRun != "failed" {
		t.Errorf("dirs %v dry run %s", s.Dirs, s.DryRun)
	}
	// The dry run command must get the directories of the scope.
	for _, call := range r.Calls {
		if call.Name == "/usr/libexec/podman/quadlet" && (len(call.Env) != 1 || call.Env[0] != "QUADLET_UNIT_DIRS="+dir+":/etc/containers/systemd/users") {
			t.Errorf("dry run env: %v", call.Env)
		}
	}
	db := s.Files[2]
	if db.Generated == nil || strings.Join(db.Generated.Volumes, ",") != "pgdata" || db.Generated.Networks[0] != "systemd-app" || db.State.Active != "active" {
		t.Errorf("db: %+v %+v", db.Generated, db.State)
	}
	if v := s.Files[3]; v.ObjectName != "pgdata-vol" || v.Generated.Creates != "pgdata-vol" || v.State.Active != "inactive" {
		t.Errorf("volume: %+v", v)
	}
	if vs := s.Volumes["pgdata-vol"]; vs.OwnerUID == nil || *vs.OwnerUID != -1 {
		t.Errorf("volume owner mapping (root-owned fixture is not mapped): %+v", vs)
	}

	sev := map[string]model.Severity{}
	for _, x := range m.Check(nil, f) {
		sev[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"quadlet.dryrun apps:broken.container":          model.SeverityFail,
		"quadlet.volume-not-used apps:db.container":     model.SeverityFail,
		"quadlet.unit-never-active apps:pgdata.volume":  model.SeverityWarn,
		"quadlet.changed-since-start apps:db.container": model.SeverityWarn,
		"quadlet.changed-since-start apps:app.network":  model.SeverityWarn,
		"quadlet.unit-not-loaded apps:proxy.container":  model.SeverityFail,
		"quadlet.volume-owner apps:pgdata.volume":       model.SeverityWarn,
	}
	for k, v := range want {
		if sev[k] != v {
			t.Errorf("%s: %s, want %s", k, sev[k], v)
		}
	}
	if len(sev) != len(want) {
		t.Errorf("findings: %v", sev)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestReferenceFixedAndNetwork(t *testing.T) {
	fixed := parseDryRun(hostfs.SplitLines(read(t, "dryrun-fixed.out")))
	db := fixed["db.service"]
	if db == nil || strings.Join(db.Volumes, ",") != "pgdata-vol" || !contains(db.Requires, "pgdata-volume.service") {
		t.Fatalf("fixed: %+v", db)
	}
	s := Scope{Name: "root", Files: []File{
		{Name: "db.container", Kind: "container", Service: "db.service", Volumes: []string{"pgdata.volume:/data", "/srv/x:/x", "other:/o"}, Networks: []string{"app", "host"}},
		{Name: "pgdata.volume", Kind: "volume", ObjectName: "pgdata-vol"},
		{Name: "app.network", Kind: "network", ObjectName: "systemd-app"},
	}}
	got := scopeFindings("/usr/libexec/podman/quadlet", s)
	if len(got) != 1 || got[0].ID != "quadlet.network-not-used" || !strings.Contains(got[0].Hint, "Network=app.network") {
		t.Errorf("findings: %+v", got)
	}
}

func TestParsers(t *testing.T) {
	u := ParseUnit(strings.Split("# c\n[Container]\nVolume=a:/a\nVolume=b:/b\nEnvironment=A=1 \\\n  B=2\n[Service]\nX=1\nX=\nX=2\n", "\n"))
	if strings.Join(u.Values("Container", "Volume"), ",") != "a:/a,b:/b" || u.Value("Container", "Environment") != "A=1 B=2" ||
		strings.Join(u.Values("Service", "X"), ",") != "2" || u.Has("Service", "Y") {
		t.Errorf("unit: %+v", u)
	}
	g := &Generated{}
	parseExecStart("/usr/bin/podman run --name=x --volume=v1:/a -v /host:/b --mount type=volume,source=v2,destination=/c --network host img", g)
	if strings.Join(g.Volumes, ",") != "v1,/host,v2" || strings.Join(g.Networks, ",") != "host" {
		t.Errorf("exec: %+v", g)
	}
	byFile, general := parseDiagnostics(string(read(t, "dryrun-bug.err")) + "quadlet-generator[1]: some other problem\n")
	if len(byFile["broken.container"]) != 1 || len(general) != 1 {
		t.Errorf("diagnostics: %v %v", byFile, general)
	}
	if parseTimestamp("@1790000000").Unix() != 1790000000 || !parseTimestamp("").IsZero() ||
		parseTimestamp("Mon 2026-09-28 09:00:00 UTC").Hour() != 9 {
		t.Error("timestamps")
	}
}

func TestSkipAndDetect(t *testing.T) {
	m := New()
	m.fs = hostfstest.New(t, nil)
	m.resolver = users.Resolver{FS: m.fs, Lookup: lookup, Euid: func() int { return 0 }}
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("skip: %v", err)
	}
	m.fs = hostfstest.New(t, map[string]string{"/usr/lib/podman/quadlet": "", "/etc/containers/systemd/a.container": "[Container]\n"})
	m.resolver.FS = m.fs
	if d := m.Detect(context.Background(), nil); !d.Found || d.Reason != "1 quadlet files" {
		t.Errorf("detect: %+v", d)
	}
}
