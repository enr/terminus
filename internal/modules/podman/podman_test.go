package podman

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var apps = runner.Account{Name: "apps", UID: 1001, GID: 1001, Home: "/home/apps"}

func lookup(n string) (runner.Account, error) {
	if n == "apps" || n == "1001" {
		return apps, nil
	}
	return runner.Account{}, errors.New("unknown user " + n)
}

// fakeScope answers the podman commands of a scope with the fixtures captured from podman 4.9.
func fakeScope(r *runner.Fake, t *testing.T, user string) {
	prefix := ""
	if user != "" {
		prefix = user + "|"
	}
	ids := "0adac39120d79345e3cd010a6e8248ddcf477b034beb3464019def6c85649586 7bacfcc8077f716563b04369c481eca249f0af8326a63a32156fee72ce916f54 148ed6f52f4a"
	for cmd, file := range map[string]string{
		"podman version --format json":           "version.json",
		"podman ps -a --format json":             "ps.json",
		"podman stats --no-stream --format json": "stats.json",
		"podman volume ls --format json":         "volls.json",
		"podman network ls --format json":        "netls.json",
		"podman system df --format json":         "df.json",
		"podman inspect --type container " + ids: "inspect.json",
	} {
		r.Results[prefix+cmd] = runner.Result{Stdout: testdata(t, file)}
	}
}

func TestCollectRootAndRootless(t *testing.T) {
	// The full ps fixture has the crashed container's full id: read it to build the key.
	ids, err := containerIDs(testdata(t, "ps.json"))
	if err != nil || len(ids) != 3 {
		t.Fatalf("ps: %v %v", ids, err)
	}
	r := &runner.Fake{Results: map[string]runner.Result{}, Paths: map[string]string{"podman": "/usr/bin/podman"}}
	fakeScope(r, t, "")
	fakeScope(r, t, "apps")
	for _, p := range []string{"", "apps|"} {
		for k, v := range r.Results {
			if strings.HasPrefix(k, p+"podman inspect") {
				delete(r.Results, k)
				r.Results[p+"podman inspect --type container "+strings.Join(ids, " ")] = v
			}
		}
	}
	// The rootless scope cannot read stats (cgroup v1).
	r.Errors = map[string]error{}
	delete(r.Results, "apps|podman stats --no-stream --format json")
	r.Results["apps|podman stats --no-stream --format json"] = runner.Result{ExitCode: 125, Stderr: []byte("Error: stats is not supported in rootless mode without cgroups v2")}

	fs := hostfstest.New(t, map[string]string{
		"/etc/subuid":                  "apps:100000:65536\n",
		"/etc/subgid":                  "apps:100000:65536\n",
		"/var/lib/systemd/linger/apps": "",
		"/var/lib/containers/storage/volumes/pgdata/_data/PG_VERSION": "16\n",
	})
	m := New()
	m.fs = fs
	m.resolver = users.Resolver{FS: fs, Lookup: lookup, Euid: func() int { return 0 }}
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if len(f.Scopes) != 2 || f.Scopes[0].Name != "root" || f.Scopes[1].Name != "apps" || !f.Scopes[1].Rootless {
		t.Fatalf("scopes: %+v", f.Scopes)
	}
	root := f.Scopes[0]
	if root.Version != "4.9.3" || len(root.Containers) != 3 || len(root.Errors) != 0 {
		t.Fatalf("root: %+v", root)
	}
	db := root.Containers[1]
	if db.Name != "db" || db.Health != "unhealthy" || db.SystemdUnit != "db.service" || db.RestartPolicy != "on-failure" ||
		db.Ports[0] != "127.0.0.1:15432->5432/tcp" || db.Mounts[0].Name != "pgdata" || db.Stats == nil || db.Stats.MemUsageBytes != 49150 {
		t.Errorf("db: %+v %+v", db, db.Stats)
	}
	if c := root.Containers[0]; c.Name != "crashed" || c.State != "exited" || c.ExitCode != 3 || c.StartedAt == "" {
		t.Errorf("crashed: %+v", c)
	}
	pg := root.Volumes[0]
	if pg.Name != "pgdata" || strings.Join(pg.UsedBy, ",") != "db" || pg.SizeBytes == nil || *pg.SizeBytes != 3 || *pg.Files != 2 || pg.OwnerUID == nil {
		t.Errorf("pgdata: %+v", pg)
	}
	if len(root.Networks) != 1 || root.Networks[0].Subnets[0] != "10.88.0.0/16" || len(root.Storage) != 3 {
		t.Errorf("networks/storage: %+v %+v", root.Networks, root.Storage)
	}
	if user := f.Scopes[1]; len(user.Errors) != 1 || !strings.Contains(user.Errors[0], "rootless mode") || len(user.Containers) != 3 {
		t.Errorf("rootless scope: %+v", user.Errors)
	}

	sev := map[string]model.Severity{}
	for _, x := range m.Check(nil, f) {
		sev[x.ID+" "+x.Subject] = x.Severity
	}
	for k, v := range map[string]model.Severity{
		"podman.unhealthy root:db":             model.SeverityFail,
		"podman.no-healthcheck root:web":       model.SeverityInfo,
		"podman.exited root:crashed":           model.SeverityWarn,
		"podman.volume-unused root:pgdata-vol": model.SeverityInfo,
		"podman.storage root":                  model.SeverityInfo,
		"podman.scope apps":                    model.SeverityWarn,
	} {
		if sev[k] != v {
			t.Errorf("%s: %s, want %s (all %v)", k, sev[k], v, sev)
		}
	}
}

func TestContainerFindings(t *testing.T) {
	c := Container{Name: "db", State: "exited", ExitCode: 137, SystemdUnit: "db.service", OOMKilled: true, Restarts: 12, RestartPolicy: "always"}
	got := map[string]model.Severity{}
	for _, f := range containerFindings(Scope{Name: "apps"}, c, restartsThreshold) {
		got[f.ID] = f.Severity
	}
	if got["podman.exited"] != model.SeverityFail || got["podman.oom-killed"] != model.SeverityFail || got["podman.restarts"] != model.SeverityFail {
		t.Errorf("findings: %v", got)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"0B": 0, "49.15kB": 49150, "16.88GB": 16880000000, "512MiB": 512 << 20, " 1.5 MB ": 1500000} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "kB", "12XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSkipDetectConfig(t *testing.T) {
	m := New()
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "podman not found") {
		t.Errorf("skip: %v", err)
	}
	if d := m.Detect(context.Background(), &module.Env{Runner: &runner.Fake{Paths: map[string]string{"podman": "/usr/bin/podman"}}}); !d.Found {
		t.Errorf("detect: %+v", d)
	}
	m.resolver = users.Resolver{Lookup: lookup, Euid: func() int { return 0 }}
	c, _ := config.Parse("[modules.podman]\nusers = [\"ghost\"]\n")
	if err := m.Configure(c.Decoder(Name)); err == nil {
		t.Error("unknown user accepted")
	}
	c, _ = config.Parse("[modules.podman]\nusers = [\"apps\"]\nrootful = false\nvolume_sizes = false\n")
	if err := m.Configure(c.Decoder(Name)); err != nil || m.rootful || m.volumeSizes || m.users.Names[0] != "apps" {
		t.Errorf("config: %v %+v", err, m)
	}
}
