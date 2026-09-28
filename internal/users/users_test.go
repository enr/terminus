package users

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/runner"
)

var accounts = map[string]runner.Account{
	"apps": {Name: "apps", UID: 1001, GID: 1001},
	"web":  {Name: "web", UID: 1002, GID: 1002},
	"root": {Name: "root", UID: 0},
}

func lookup(n string) (runner.Account, error) {
	for _, a := range accounts {
		if a.Name == n || n == strconv.FormatUint(uint64(a.UID), 10) {
			return a, nil
		}
	}
	return runner.Account{}, errors.New("unknown user " + n)
}

func TestSpecAndScopes(t *testing.T) {
	var s struct {
		Users Spec `toml:"users"`
	}
	for in, ok := range map[string]bool{`users = "auto"`: true, `users = ["apps"]`: true, `users = "all"`: false, `users = [1]`: false, `users = 1`: false} {
		c, err := config.Parse("[modules.x]\n" + in + "\n")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Decoder("x")(&s); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}

	fs := hostfstest.New(t, map[string]string{
		"/var/lib/systemd/linger/apps": "",
		"/run/user/1002/systemd/x":     "",
		"/run/user/0/systemd/x":        "",
	})
	r := Resolver{FS: fs, Lookup: lookup, Euid: func() int { return 1001 }}
	scopes, err := r.Scopes(AutoSpec, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range scopes {
		got = append(got, s.Name+"/"+s.Skipped)
	}
	if strings.Join(got, ",") != "root/requires root,apps/,web/requires root" {
		t.Errorf("scopes: %v", got)
	}
	if !r.Linger(accounts["apps"]) || r.Linger(accounts["web"]) || !r.ManagerRunning(accounts["web"]) {
		t.Error("linger/manager")
	}
	if err := r.Validate(Spec{Names: []string{"apps", "ghost"}}); err == nil {
		t.Error("unknown user accepted")
	}
	if _, err := r.Select(Spec{Names: []string{"ghost"}}); err == nil {
		t.Error("select unknown user")
	}
}

func TestIDMap(t *testing.T) {
	a := accounts["apps"]
	fs := hostfstest.New(t, map[string]string{"/etc/subuid": "# x\nweb:200000:65536\n1001:100000:65536\n", "/etc/subgid": "apps:300000:10\n"})
	m := NewIDMap(fs, a)
	for host, want := range map[uint32]int64{1001: 0, 100000: 1, 100998: 999, 165535: 65536, 165536: -1, 0: -1} {
		if got := m.UID(host); got != want {
			t.Errorf("uid %d: %d, want %d", host, got, want)
		}
	}
	if m.GID(300009) != 10 || m.GID(300010) != -1 {
		t.Error("gid")
	}
	if Identity().UID(999) != 999 {
		t.Error("identity")
	}
}
