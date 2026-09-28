package systemd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

var accounts = map[string]runner.Account{
	"root": {Name: "root", UID: 0, Home: "/root"},
	"apps": {Name: "apps", UID: 1001, GID: 1001, Home: "/home/apps"},
	"web":  {Name: "web", UID: 1002, GID: 1002, Home: "/home/web"},
}

func lookup(n string) (runner.Account, error) {
	if a, ok := accounts[n]; ok {
		return a, nil
	}
	for _, a := range accounts {
		if fmt.Sprint(a.UID) == n {
			return a, nil
		}
	}
	return runner.Account{}, fmt.Errorf("user %q: unknown user", n)
}

const systemUnits = `  cron.service        loaded active   running Regular background program processing daemon
● backup.service      loaded failed   failed  Nightly backup
  getty@tty1.service  loaded active   running Getty on tty1
  nginx.service       loaded inactive dead    nginx
  -.mount             loaded active   mounted Root Mount
  ghost.service       not-found inactive dead ghost.service
`

const systemShow = `Id=backup.service
LoadState=loaded
ActiveState=failed
SubState=failed
Result=exit-code
NRestarts=0
ExecMainCode=1
ExecMainStatus=2
MemoryCurrent=[not set]
MemoryPeak=[not set]
MemoryMax=infinity
MemoryHigh=infinity
TasksCurrent=[not set]
TasksMax=4915
CPUUsageNSec=[not set]
ControlGroup=
FragmentPath=/etc/systemd/system/backup.service
SourcePath=
ActiveEnterTimestamp=

Id=cron.service
LoadState=loaded
ActiveState=active
SubState=running
Result=success
NRestarts=0
ExecMainCode=0
ExecMainStatus=0
MemoryCurrent=1048576
MemoryPeak=2097152
MemoryMax=infinity
MemoryHigh=infinity
TasksCurrent=1
TasksMax=4915
CPUUsageNSec=150000000
ControlGroup=/system.slice/cron.service
FragmentPath=/usr/lib/systemd/system/cron.service
SourcePath=
ActiveEnterTimestamp=Mon 2026-09-28 08:00:00 UTC

Id=getty@tty1.service
LoadState=loaded
ActiveState=active
SubState=running
Result=success
NRestarts=0
MemoryMax=infinity
ControlGroup=/system.slice/system-getty.slice/getty@tty1.service
`

const userUnits = `  myapp.service   loaded active running myapp container
  db.service      loaded active running db container
  dbus.socket     loaded active running D-Bus User Message Bus Socket
`

const userShow = `Id=db.service
LoadState=loaded
ActiveState=active
SubState=running
Result=success
NRestarts=1
ExecMainCode=1
ExecMainStatus=0
MemoryCurrent=400000000
MemoryPeak=[not set]
MemoryMax=536870912
MemoryHigh=infinity
TasksCurrent=12
TasksMax=512
CPUUsageNSec=9000000000
ControlGroup=/user.slice/user-1001.slice/user@1001.service/app.slice/db.service
FragmentPath=/run/user/1001/systemd/generator/db.service
SourcePath=/home/apps/.config/containers/systemd/db.container
ActiveEnterTimestamp=Mon 2026-09-28 09:00:00 UTC

Id=myapp.service
LoadState=loaded
ActiveState=active
SubState=running
Result=success
NRestarts=5
ExecMainCode=1
ExecMainStatus=0
MemoryCurrent=100000000
MemoryPeak=150000000
MemoryMax=infinity
MemoryHigh=infinity
TasksCurrent=20
TasksMax=512
ControlGroup=/user.slice/user-1001.slice/user@1001.service/app.slice/myapp.service
SourcePath=/home/apps/.config/containers/systemd/myapp.container
`

var journal = strings.Join([]string{
	`{"MESSAGE_ID":"fe6faa94e7774663a0da52717891d8ef","USER_UNIT":"db.service","_UID":"1001","__REALTIME_TIMESTAMP":"1790000000000000"}`,
	`{"MESSAGE_ID":"98e322203f7a4ed290d09fe03c09fe15","USER_UNIT":"myapp.service","_UID":"1001","EXIT_CODE":"exited","EXIT_STATUS":"143"}`,
	`{"MESSAGE_ID":"98e322203f7a4ed290d09fe03c09fe15","USER_UNIT":"myapp.service","_UID":"1001","EXIT_CODE":"exited","EXIT_STATUS":"143"}`,
	`{"MESSAGE_ID":"98e322203f7a4ed290d09fe03c09fe15","USER_UNIT":"myapp.service","_UID":"1001","EXIT_CODE":"exited","EXIT_STATUS":"137"}`,
	`{"MESSAGE_ID":"5eb03494b6584870a536b337290809b3","USER_UNIT":"myapp.service","_UID":"1001"}`,
	`{"MESSAGE_ID":"d9b373ed55a64feb8242e02dbe79a49c","UNIT":"backup.service","UNIT_RESULT":"exit-code"}`,
	`{"MESSAGE_ID":"d9b373ed55a64feb8242e02dbe79a49c","UNIT":"old.service","UNIT_RESULT":"oom-kill"}`,
	`{"MESSAGE_ID":"fe6faa94e7774663a0da52717891d8ef","USER_UNIT":"x.service","_UID":"4000"}`,
	`{"MESSAGE_ID":[1,2,3]}`,
	`not json`,
}, "\n") + "\n"

const (
	listArgs    = "list-units --all --plain --no-legend --no-pager --full"
	journalArgs = "journalctl --no-pager -o json --since=-604800s --output-fields=MESSAGE_ID,UNIT,USER_UNIT,_UID,UNIT_RESULT,EXIT_CODE,EXIT_STATUS MESSAGE_ID=fe6faa94e7774663a0da52717891d8ef MESSAGE_ID=d9b373ed55a64feb8242e02dbe79a49c MESSAGE_ID=5eb03494b6584870a536b337290809b3 MESSAGE_ID=98e322203f7a4ed290d09fe03c09fe15"
)

func showKey(user string, units ...string) string {
	scope := ""
	if user != "" {
		scope = user + "|systemctl --user "
	} else {
		scope = "systemctl "
	}
	return scope + "show --no-pager -p " + strings.Join(showProperties, ",") + " " + strings.Join(units, " ")
}

func fakeRunner() *runner.Fake {
	return &runner.Fake{Results: map[string]runner.Result{
		"systemctl --version":   {Stdout: []byte("systemd 255 (255.4-1ubuntu8)\n+PAM +AUDIT\n")},
		"systemctl " + listArgs: {Stdout: []byte(systemUnits)},
		showKey("", "cron.service", "backup.service", "getty@tty1.service"): {Stdout: []byte(systemShow)},
		"apps|systemctl --user " + listArgs:                                 {Stdout: []byte(userUnits)},
		showKey("apps", "myapp.service", "db.service"):                      {Stdout: []byte(userShow)},
		journalArgs: {Stdout: []byte(journal)},
	}}
}

func fixture(t *testing.T, extra map[string]string) hostfs.FS {
	files := map[string]string{
		"/run/systemd/system/.keep":                          "",
		"/sys/fs/cgroup/cgroup.controllers":                  "cpu memory pids\n",
		"/proc/meminfo":                                      "MemTotal:        1000000 kB\n",
		"/var/lib/systemd/linger/apps":                       "",
		"/run/user/1001/systemd/.keep":                       "",
		"/home/apps/.config/containers/systemd/db.container": "[Container]\n",
		"/sys/fs/cgroup/user.slice/user-1001.slice/user@1001.service/app.slice/db.service/memory.peak":   "536870000\n",
		"/sys/fs/cgroup/user.slice/user-1001.slice/user@1001.service/app.slice/db.service/memory.events": "low 0\nhigh 0\nmax 12\noom 1\noom_kill 1\noom_group_kill 0\n",
		"/sys/fs/cgroup/system.slice/cron.service/memory.max":                                            "max\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return hostfstest.New(t, files)
}

func newTestModule(fs hostfs.FS, euid int) *Module {
	m := New()
	m.fs, m.lookup, m.euid = fs, lookup, func() int { return euid }
	return m
}

func findUnit(t *testing.T, f *Facts, manager, unit string) Unit {
	t.Helper()
	for _, mg := range f.Managers {
		if mg.Name != manager {
			continue
		}
		for _, u := range mg.Units {
			if u.Name == unit {
				return u
			}
		}
	}
	t.Fatalf("unit %s:%s not found in %+v", manager, unit, f.Managers)
	return Unit{}
}

func TestCollectAuto(t *testing.T) {
	m := newTestModule(fixture(t, nil), 0)
	got, err := m.Collect(context.Background(), &module.Env{Runner: fakeRunner()})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.Version != 255 || !f.CgroupV2 || f.MemTotalBytes != 1000000*1024 || f.HistoryError != "" {
		t.Errorf("facts: %+v", f)
	}
	if len(f.Users) != 1 || f.Users[0].Name != "apps" || !f.Users[0].Linger || !f.Users[0].ManagerRunning || f.Users[0].EnabledUnits != 1 {
		t.Fatalf("users: %+v", f.Users)
	}
	if len(f.Managers) != 2 || f.Managers[0].Name != "system" || f.Managers[1].Name != "user:apps" {
		t.Fatalf("managers: %+v", f.Managers)
	}
	if strings.Join(f.Managers[0].Failed, ",") != "backup.service" {
		t.Errorf("failed: %v", f.Managers[0].Failed)
	}

	db := findUnit(t, f, "user:apps", "db.service")
	if db.MemoryPeakBytes == nil || *db.MemoryPeakBytes != 536870000 || *db.MemoryMaxBytes != 536870912 {
		t.Errorf("db memory (peak from cgroup): %+v", db)
	}
	if db.OOMKills == nil || *db.OOMKills != 1 || db.History == nil || db.History.OOMKills != 1 || db.History.LastOOMKill == "" {
		t.Errorf("db oom: %+v %+v", db.OOMKills, db.History)
	}
	if db.SourcePath != "/home/apps/.config/containers/systemd/db.container" || db.CPUUsageSeconds == nil || *db.CPUUsageSeconds != 9 {
		t.Errorf("db: %+v", db)
	}
	app := findUnit(t, f, "user:apps", "myapp.service")
	if app.MemoryMaxBytes != nil || *app.Restarts != 5 || app.History.Exits["exited/143"] != 2 || app.History.Restarts != 1 {
		t.Errorf("myapp: %+v %+v", app, app.History)
	}
	cron := findUnit(t, f, "system", "cron.service")
	if cron.MemoryMaxBytes != nil || *cron.MemoryPeakBytes != 2097152 || cron.MainExitCode != "" {
		t.Errorf("cron: %+v", cron)
	}
	backup := findUnit(t, f, "system", "backup.service")
	if backup.MainExitCode != "exited" || backup.MainExitStatus != 2 || backup.History.Failures["exit-code"] != 1 {
		t.Errorf("backup: %+v", backup)
	}
	old := findUnit(t, f, "system", "old.service")
	if old.History.OOMKills != 1 || old.Load != "" {
		t.Errorf("stopped unit history: %+v", old)
	}

	findings := m.Check(nil, f)
	got2 := map[string]model.Severity{}
	for _, x := range findings {
		got2[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"systemd.failed system:backup.service":      model.SeverityFail,
		"user.linger apps":                          model.SeverityOK,
		"unit.memory-peak user:apps:db.service":     model.SeverityWarn,
		"unit.oom-kills user:apps:db.service":       model.SeverityFail,
		"unit.oom-kills system:old.service":         model.SeverityFail,
		"unit.restarts user:apps:db.service":        model.SeverityOK,
		"unit.restarts user:apps:myapp.service":     model.SeverityWarn,
		"unit.memory-limit user:apps:myapp.service": model.SeverityInfo,
		"resources.overcommit MemoryMax":            model.SeverityOK,
	}
	for k, v := range want {
		if s, ok := got2[k]; !ok || s != v {
			t.Errorf("%s: %v (present %v), want %s", k, s, ok, v)
		}
	}
	if len(got2) != len(want) {
		t.Errorf("unexpected findings: %v", got2)
	}
	for _, x := range findings {
		if x.ID == "unit.restarts" && x.Subject == "user:apps:myapp.service" &&
			!strings.Contains(x.Message, "143 (SIGTERM: stop or deploy) ×2, 137 (SIGKILL: OOM or kill -9) ×1") {
			t.Errorf("restart reasons: %s", x.Message)
		}
	}
}

func TestExplicitUsersLingerAndNonRoot(t *testing.T) {
	fs := fixture(t, map[string]string{
		"/home/web/.config/systemd/user/default.target.wants/web.service": "",
	})
	m := newTestModule(fs, 1001) // running as apps
	c, err := config.Parse("[modules.systemd]\nusers = [\"apps\", \"1002\"]\nhistory = \"1d\"\nunits = [\"db.*\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	r := fakeRunner()
	r.Results[strings.Replace(journalArgs, "604800", "86400", 1)] = runner.Result{Stdout: []byte(journal)}
	r.Results[showKey("", "backup.service")] = runner.Result{Stdout: []byte(systemShow)}
	r.Results[showKey("apps", "db.service")] = runner.Result{Stdout: []byte(userShow)}

	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if len(f.Managers) != 3 || f.Managers[2].Name != "user:web" || f.Managers[2].Skipped == "" {
		t.Fatalf("managers: %+v", f.Managers)
	}
	var sev []string
	for _, x := range m.Check(nil, f) {
		if x.ID == "user.linger" {
			sev = append(sev, x.Subject+"="+x.Severity.String())
		}
	}
	if strings.Join(sev, ",") != "apps=ok,web=fail" {
		t.Errorf("linger: %v", sev)
	}

	for _, bad := range []string{`users = "all"`, `users = [1]`, `users = ["nobody-here"]`, `history = "soon"`, `units = ["["]`} {
		c, err := config.Parse("[modules.systemd]\n" + bad + "\n")
		if err != nil {
			t.Fatal(err)
		}
		if err := New().Configure(c.Decoder(Name)); err == nil && bad != `users = ["nobody-here"]` {
			t.Errorf("%s accepted", bad)
		}
		m := newTestModule(fs, 0)
		if err := m.Configure(c.Decoder(Name)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := newTestModule(fs, 0).SetUsers([]string{"ghost"}); err == nil {
		t.Error("unknown user accepted by SetUsers")
	}
}

func TestNonRootSkipsOtherUsers(t *testing.T) {
	m := newTestModule(fixture(t, nil), 1500)
	got, err := m.Collect(context.Background(), &module.Env{Runner: fakeRunner()})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.Managers[1].Skipped != "requires root" {
		t.Fatalf("user manager: %+v", f.Managers[1])
	}
	found := false
	for _, x := range m.Check(nil, f) {
		if x.ID == "systemd.version" && strings.Contains(x.Message, "user:apps not inspected: requires root") {
			found = true
		}
	}
	if !found {
		t.Error("missing info about the skipped manager")
	}
}

func TestErrorsAndSkip(t *testing.T) {
	if _, err := newTestModule(hostfstest.New(t, nil), 0).Collect(context.Background(), &module.Env{Runner: fakeRunner()}); !isSkip(err) {
		t.Errorf("no systemd: %v", err)
	}

	r := fakeRunner()
	r.Errors = map[string]error{
		"apps|systemctl --user " + listArgs: errors.New("Failed to connect to bus"),
		journalArgs:                         errors.New("permission denied"),
	}
	m := newTestModule(fixture(t, map[string]string{"/sys/fs/cgroup/cgroup.controllers": ""}), 0)
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if !strings.Contains(f.Managers[1].Error, "Failed to connect to bus") || !strings.Contains(f.HistoryError, "permission denied") {
		t.Errorf("errors: %+v %q", f.Managers[1], f.HistoryError)
	}
	var ids []string
	for _, x := range m.Check(nil, f) {
		if x.ID == "systemd.failed" && x.Subject == "user:apps" && x.Severity == model.SeverityWarn {
			ids = append(ids, "list-error")
		}
		if x.ID == "systemd.version" && strings.Contains(x.Message, "journal history not available") {
			ids = append(ids, "history")
		}
	}
	if strings.Join(ids, ",") != "list-error,history" {
		t.Errorf("findings: %v", ids)
	}
}

func isSkip(err error) bool {
	var s *module.SkipError
	return errors.As(err, &s)
}

func TestParsers(t *testing.T) {
	if parseVersion("systemd 252 (252.22-1~deb12u1)\n") != 252 || parseVersion("bash") != 0 {
		t.Error("parseVersion")
	}
	units := parseListUnits(hostfs.SplitLines([]byte(systemUnits)))
	if len(units) != 6 || units[1].Name != "backup.service" || units[1].Active != "failed" || units[5].Load != "not-found" {
		t.Errorf("list-units: %+v", units)
	}
	blocks := parseShow(hostfs.SplitLines([]byte("\n\n" + systemShow + "\n\n")))
	if len(blocks) != 3 || blocks[2]["Id"] != "getty@tty1.service" {
		t.Errorf("show blocks: %d", len(blocks))
	}
	for v, want := range map[string]bool{"[not set]": false, "infinity": false, notSet: false, "": false, "12": true, "x": false} {
		if (bytesValue(v) != nil) != want {
			t.Errorf("bytesValue(%q)", v)
		}
	}
	if describeExit("killed/9") != "SIGKILL" || describeExit("exited/1") != "exit 1" || describeExit("dumped/11") != "dumped 11" {
		t.Error("describeExit")
	}
}
