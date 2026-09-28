package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// ErrNeedRoot is returned when a command must run as another user and terminus is not root.
var ErrNeedRoot = errors.New("running commands as another user requires root")

// Account is a local user.
type Account struct {
	Name   string
	UID    uint32
	GID    uint32
	Groups []uint32
	Home   string
}

// RuntimeDir is the XDG runtime directory of the user, where its systemd manager and D-Bus live.
func (a Account) RuntimeDir() string { return fmt.Sprintf("/run/user/%d", a.UID) }

// LookupAccount finds a user by name or numeric UID. Without cgo only /etc/passwd and
// /etc/group are read: directory users (LDAP, SSSD) must be given by UID.
func LookupAccount(nameOrUID string) (Account, error) {
	var u *user.User
	var err error
	if _, numErr := strconv.ParseUint(nameOrUID, 10, 32); numErr == nil {
		u, err = user.LookupId(nameOrUID)
		if err != nil {
			// A UID without a passwd entry is still usable.
			uid, _ := strconv.ParseUint(nameOrUID, 10, 32)
			return Account{Name: nameOrUID, UID: uint32(uid), GID: uint32(uid), Home: "/"}, nil
		}
	} else {
		u, err = user.Lookup(nameOrUID)
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return Account{}, fmt.Errorf("unknown user %q (directory users, such as LDAP ones, can be given by UID)", nameOrUID)
		}
		if err != nil {
			return Account{}, fmt.Errorf("user %q: %w", nameOrUID, err)
		}
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	gid, _ := strconv.ParseUint(u.Gid, 10, 32)
	a := Account{Name: u.Username, UID: uint32(uid), GID: uint32(gid), Home: u.HomeDir}
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil {
				a.Groups = append(a.Groups, uint32(n))
			}
		}
	}
	return a, nil
}

// sessionEnv are the variables that point to the session of a user.
var sessionEnv = []string{"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

// asUser prepares cmd to run as the user: credentials (when it is not the current user) and
// the session environment, so that `systemctl --user`, `journalctl --user` and `podman` find
// the user's manager, bus and storage.
func asUser(cmd *exec.Cmd, name string) error {
	a, err := LookupAccount(name)
	if err != nil {
		return err
	}
	if uint32(os.Geteuid()) != a.UID {
		if os.Geteuid() != 0 {
			return fmt.Errorf("%s: %w", a.Name, ErrNeedRoot)
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: a.UID, Gid: a.GID, Groups: a.Groups},
		}
		// The working directory of root may not be readable by the user.
		cmd.Dir = "/"
	}
	env := make([]string, 0, len(os.Environ())+len(sessionEnv))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, s := range sessionEnv {
			if k == s {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env,
		"HOME="+a.Home,
		"USER="+a.Name,
		"LOGNAME="+a.Name,
		"XDG_RUNTIME_DIR="+a.RuntimeDir(),
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+a.RuntimeDir()+"/bus",
	)
	return nil
}
