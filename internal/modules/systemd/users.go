package systemd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/runner"
)

func geteuid() int { return os.Geteuid() }

// userSpec is the users setting: "auto" or a list of names/UIDs.
type userSpec struct {
	Auto  bool
	Names []string
}

// UnmarshalTOML accepts users = "auto" or users = ["apps", "1001"].
func (s *userSpec) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		if t != "auto" {
			return fmt.Errorf(`must be "auto" or a list of users, not %q`, t)
		}
		*s = userSpec{Auto: true}
	case []any:
		out := userSpec{Names: []string{}}
		for _, e := range t {
			n, ok := e.(string)
			if !ok || n == "" {
				return fmt.Errorf("users must be names or UIDs as strings, not %v", e)
			}
			out.Names = append(out.Names, n)
		}
		*s = out
	default:
		return fmt.Errorf(`must be "auto" or a list of users`)
	}
	return nil
}

// selectUsers resolves the users setting: explicit names, or with "auto" the users that have
// linger enabled or a running user manager.
func (m *Module) selectUsers() ([]User, error) {
	var names []string
	var errs []error
	if m.users.Auto {
		seen := map[string]bool{}
		if entries, err := m.fs.ReadDir("/var/lib/systemd/linger"); err == nil {
			for _, e := range entries {
				seen[e.Name()] = true
			}
		}
		if entries, err := m.fs.ReadDir("/run/user"); err == nil {
			for _, e := range entries {
				if _, err := strconv.Atoi(e.Name()); err == nil && m.fs.Exists("/run/user/"+e.Name()+"/systemd") {
					seen[e.Name()] = true
				}
			}
		}
		for n := range seen {
			names = append(names, n)
		}
	} else {
		names = m.users.Names
	}

	byUID := map[uint32]User{}
	for _, n := range names {
		a, err := m.lookup(n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		byUID[a.UID] = m.userInfo(a)
	}
	users := make([]User, 0, len(byUID))
	for _, u := range byUID {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UID < users[j].UID })
	return users, errors.Join(errs...)
}

func (m *Module) userInfo(a runner.Account) User {
	u := User{
		Name:           a.Name,
		UID:            a.UID,
		Linger:         m.fs.Exists("/var/lib/systemd/linger/" + a.Name),
		ManagerRunning: m.fs.Exists(a.RuntimeDir() + "/systemd"),
	}
	u.EnabledUnits = m.countEnabled(a.Home)
	return u
}

// countEnabled counts the units a user enabled (symlinks in *.wants directories) and the quadlet
// files, which generate units started at boot.
func (m *Module) countEnabled(home string) int {
	if home == "" || home == "/" {
		return 0
	}
	n := 0
	dir := home + "/.config/systemd/user"
	if entries, err := m.fs.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasSuffix(e.Name(), ".wants") {
				if wants, err := m.fs.ReadDir(dir + "/" + e.Name()); err == nil {
					n += len(wants)
				}
			}
		}
	}
	if entries, err := m.fs.ReadDir(home + "/.config/containers/systemd"); err == nil {
		for _, e := range entries {
			switch {
			case strings.HasSuffix(e.Name(), ".container"), strings.HasSuffix(e.Name(), ".pod"), strings.HasSuffix(e.Name(), ".kube"):
				n++
			}
		}
	}
	return n
}
