package timers

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

func lookup(n string) (runner.Account, error) {
	switch n {
	case "apps", "1001":
		return runner.Account{Name: "apps", UID: 1001, GID: 1001, Home: "/home/apps"}, nil
	case "web", "1002":
		return runner.Account{Name: "web", UID: 1002, GID: 1002, Home: "/home/web"}, nil
	}
	return runner.Account{}, errors.New("unknown user " + n)
}

var (
	timerShow   = "systemctl show --no-pager -p " + strings.Join(timerProperties, ",")
	serviceShow = "systemctl show --no-pager -p " + strings.Join(serviceProperties, ",")
	listTimers  = "systemctl list-units --type=timer --all --plain --no-legend --no-pager --full"
)

func ts(t time.Time) string { return t.In(time.Local).Format("Mon 2006-01-02 15:04:05 MST") }

func TestCollectAndCheck(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fs := hostfstest.New(t, map[string]string{
		"/run/systemd/system/.keep":    "",
		"/var/lib/systemd/linger/apps": "",
		"/run/user/1001/systemd/.keep": "",
		"/var/lib/systemd/linger/web":  "",
	})
	systemTimers := `backup.timer          loaded active   waiting Nightly backup
certbot.timer         loaded active   waiting Renew certificates
logrotate.timer       loaded inactive dead    Daily rotation
old.timer             loaded active   elapsed One-off
missing.timer         loaded active   waiting Starts nothing
fstrim.timer          loaded active   waiting Discard unused blocks
gone.timer            not-found inactive dead gone.timer
`
	timers := strings.Join([]string{
		"Id=backup.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\nUnit=backup.service\nTimersCalendar={ OnCalendar=*-*-* 03:00:00 ; next_elapse=" + ts(now.Add(10*time.Hour)) + " }\nTimersMonotonic=\nPersistent=yes\nLastTriggerUSec=" + ts(now.Add(-14*time.Hour)) + "\nNextElapseUSecRealtime=" + ts(now.Add(10*time.Hour)) + "\nActiveEnterTimestamp=" + ts(now.Add(-30*24*time.Hour)),
		"Id=certbot.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\nUnit=certbot.service\nTimersCalendar={ OnCalendar=*-*-* 00,12:00:00 ; next_elapse=n/a }\nTimersMonotonic=\nPersistent=yes\nLastTriggerUSec=n/a\nNextElapseUSecRealtime=" + ts(now.Add(2*time.Hour)) + "\nActiveEnterTimestamp=" + ts(now.Add(-5*24*time.Hour)),
		"Id=logrotate.timer\nLoadState=loaded\nActiveState=inactive\nUnitFileState=enabled\nUnit=logrotate.service\nTimersCalendar={ OnCalendar=daily ; next_elapse=n/a }\nPersistent=yes\nLastTriggerUSec=n/a\nNextElapseUSecRealtime=n/a\nActiveEnterTimestamp=",
		"Id=old.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=static\nUnit=old.service\nTimersCalendar={ OnCalendar=2020-01-01 00:00:00 ; next_elapse=n/a }\nPersistent=no\nLastTriggerUSec=n/a\nNextElapseUSecRealtime=n/a\nActiveEnterTimestamp=" + ts(now.Add(-time.Hour)),
		"Id=missing.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\nUnit=missing.service\nTimersCalendar={ OnCalendar=hourly ; next_elapse=n/a }\nPersistent=no\nLastTriggerUSec=n/a\nNextElapseUSecRealtime=" + ts(now.Add(time.Hour)) + "\nActiveEnterTimestamp=" + ts(now.Add(-time.Hour)),
		"Id=fstrim.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\nUnit=fstrim.service\nTimersCalendar=\nTimersMonotonic={ OnBootUSec=15min ; next_elapse=n/a }\nPersistent=no\nLastTriggerUSec=" + ts(now.Add(-2*time.Hour)) + "\nNextElapseUSecRealtime=n/a\nActiveEnterTimestamp=" + ts(now.Add(-3*time.Hour)),
	}, "\n\n") + "\n"
	services := `Id=backup.service
LoadState=loaded
ActiveState=failed
Result=exit-code
ExecMainStatus=3
InactiveEnterTimestamp=` + ts(now.Add(-13*time.Hour)) + `

Id=certbot.service
LoadState=loaded
ActiveState=inactive
Result=success
ExecMainStatus=0
InactiveEnterTimestamp=

Id=logrotate.service
LoadState=loaded
ActiveState=inactive
Result=success
ExecMainStatus=0

Id=old.service
LoadState=loaded
ActiveState=inactive
Result=success
ExecMainStatus=0

Id=missing.service
LoadState=not-found
ActiveState=inactive
Result=success
ExecMainStatus=0

Id=fstrim.service
LoadState=loaded
ActiveState=inactive
Result=success
ExecMainStatus=0
`
	userTimers := "sync.timer loaded active waiting Sync\n"
	userShow := "Id=sync.timer\nLoadState=loaded\nActiveState=active\nUnitFileState=enabled\nUnit=sync.service\nTimersMonotonic={ OnUnitActiveUSec=15min ; next_elapse=n/a }\nPersistent=no\nLastTriggerUSec=" + ts(now.Add(-5*time.Minute)) + "\nNextElapseUSecRealtime=\nActiveEnterTimestamp=" + ts(now.Add(-24*time.Hour)) + "\n"
	r := &runner.Fake{Results: map[string]runner.Result{
		listTimers: {Stdout: []byte(systemTimers)},
		timerShow + " backup.timer certbot.timer logrotate.timer old.timer missing.timer fstrim.timer":               {Stdout: []byte(timers)},
		serviceShow + " backup.service certbot.service logrotate.service old.service missing.service fstrim.service": {Stdout: []byte(services)},
		"apps|systemctl --user list-units --type=timer --all --plain --no-legend --no-pager --full":                  {Stdout: []byte(userTimers)},
		"apps|systemctl --user show --no-pager -p " + strings.Join(timerProperties, ",") + " sync.timer":             {Stdout: []byte(userShow)},
		"apps|systemctl --user show --no-pager -p " + strings.Join(serviceProperties, ",") + " sync.service":         {Stdout: []byte("Id=sync.service\nLoadState=loaded\nActiveState=inactive\nResult=success\n")},
	}}
	m := New()
	m.fs, m.lookup, m.euid, m.now = fs, lookup, func() int { return 0 }, func() time.Time { return now }
	c, _ := config.Parse("[modules.timers]\ntimers = [\"*.timer\"]\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if len(f.Managers) != 3 || f.Managers[2].Name != "user:web" || f.Managers[2].Skipped == "" {
		t.Fatalf("managers: %+v", f.Managers)
	}
	sys := f.Managers[0]
	if len(sys.Timers) != 6 || sys.Timers[0].Name != "backup.timer" {
		t.Fatalf("timers: %+v", sys.Timers)
	}
	b := sys.Timers[0]
	if !b.LastRun.Equal(now.Add(-14*time.Hour)) || !b.NextRun.Equal(now.Add(10*time.Hour)) || !b.Persistent ||
		strings.Join(b.Schedule, ",") != "OnCalendar=*-*-* 03:00:00" || b.Unit.Result != "exit-code" || b.Unit.ExitStatus != 3 {
		t.Errorf("backup.timer: %+v", b)
	}
	if u := f.Managers[1]; len(u.Timers) != 1 || strings.Join(u.Timers[0].Schedule, ",") != "OnUnitActiveSec=15min" {
		t.Errorf("user timers: %+v", u)
	}

	got2 := map[string]model.Severity{}
	for _, x := range m.Check(nil, f) {
		got2[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"timers.failed system:backup.timer":        model.SeverityFail,
		"timers.failed system:missing.timer":       model.SeverityFail,
		"timers.not-active system:logrotate.timer": model.SeverityWarn,
		"timers.no-next system:old.timer":          model.SeverityInfo,
		"timers.never-run system:certbot.timer":    model.SeverityInfo,
		"timers.failed user:apps":                  model.SeverityOK,
		"timers.scope user:web":                    model.SeverityInfo,
	}
	for k, w := range want {
		if s, ok := got2[k]; !ok || s != w {
			t.Errorf("%s: %v (present %v), want %v", k, s, ok, w)
		}
	}
	if len(got2) != len(want) {
		t.Errorf("findings: %v", got2)
	}
}

func TestSkipAndErrors(t *testing.T) {
	m := New()
	m.fs = hostfstest.New(t, map[string]string{"/etc/hostname": "x"})
	if _, err := m.Collect(context.Background(), &module.Env{Runner: &runner.Fake{}}); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("no systemd: %v", err)
	}
	if d := m.Detect(context.Background(), nil); d.Found {
		t.Errorf("detect: %+v", d)
	}
	m.fs = hostfstest.New(t, map[string]string{"/run/systemd/system/.keep": ""})
	m.lookup, m.euid = lookup, func() int { return 1001 }
	r := &runner.Fake{Results: map[string]runner.Result{listTimers: {ExitCode: 1, Stderr: []byte("Failed to connect to bus")}}}
	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("error: %v", err)
	}
	fnd := m.Check(nil, got)
	if len(fnd) != 1 || fnd[0].ID != "timers.scope" || fnd[0].Severity != model.SeverityWarn {
		t.Errorf("findings: %+v", fnd)
	}

	if err := m.SetUsers([]string{"nobody-here"}); err == nil {
		t.Error("unknown user")
	}
	c, _ := config.Parse("[modules.timers]\ntimers = [\"[\"]\n")
	if err := m.Configure(c.Decoder(Name)); err == nil {
		t.Error("invalid pattern")
	}
}

func TestParse(t *testing.T) {
	if got := parseTriggers("{ OnCalendar=Mon *-*-* 02:00:00 ; next_elapse=Mon 2026-09-28 02:00:00 UTC } { OnCalendar=Fri *-*-* 02:00:00 ; next_elapse=n/a }"); strings.Join(got, "|") != "OnCalendar=Mon *-*-* 02:00:00|OnCalendar=Fri *-*-* 02:00:00" {
		t.Errorf("triggers: %q", got)
	}
	if !parseTimestamp("n/a").IsZero() || !parseTimestamp("").IsZero() || !parseTimestamp("garbage").IsZero() {
		t.Error("empty timestamps")
	}
	want := time.Date(2026, 9, 28, 3, 0, 12, 0, time.UTC)
	if got := parseTimestamp("Mon 2026-09-28 03:00:12 UTC"); !got.Equal(want) {
		t.Errorf("timestamp: %v", got)
	}
}
