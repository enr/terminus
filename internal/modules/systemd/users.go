package systemd

import (
	"strings"

	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// selectUsers resolves the users setting.
func (m *Module) selectUsers() ([]User, error) {
	accounts, err := m.resolver().Select(m.users)
	users := make([]User, 0, len(accounts))
	for _, a := range accounts {
		users = append(users, m.userInfo(a))
	}
	return users, err
}

func (m *Module) resolver() users.Resolver {
	return users.Resolver{FS: m.fs, Lookup: m.lookup, Euid: m.euid}
}

func (m *Module) userInfo(a runner.Account) User {
	r := m.resolver()
	u := User{Name: a.Name, UID: a.UID, Linger: r.Linger(a), ManagerRunning: r.ManagerRunning(a)}
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
