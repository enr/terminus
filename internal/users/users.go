// Package users selects the users whose services and containers terminus inspects: a setting
// shared by the systemd, podman and quadlet modules (users = "auto" | ["apps", "1001"]).
package users

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/runner"
)

// Spec is the users setting: "auto" or a list of names/UIDs.
type Spec struct {
	Auto  bool
	Names []string
}

// AutoSpec is the default: the users with linger or a running user manager.
var AutoSpec = Spec{Auto: true}

// UnmarshalTOML accepts users = "auto" or users = ["apps", "1001"].
func (s *Spec) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		if t != "auto" {
			return fmt.Errorf(`must be "auto" or a list of users, not %q`, t)
		}
		*s = AutoSpec
	case []any:
		out := Spec{Names: []string{}}
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

// Resolver turns a Spec into accounts.
type Resolver struct {
	FS     hostfs.FS
	Lookup func(string) (runner.Account, error)
	Euid   func() int
}

// Host is the resolver of the running machine.
func Host() Resolver {
	return Resolver{FS: hostfs.Host, Lookup: runner.LookupAccount, Euid: os.Geteuid}
}

// Validate checks that the users named by the spec exist.
func (r Resolver) Validate(s Spec) error {
	var errs []error
	for _, n := range s.Names {
		if _, err := r.Lookup(n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Select returns the accounts of the spec, sorted by UID: the named ones, or with "auto" the
// users that have linger enabled or a running user manager.
func (r Resolver) Select(s Spec) ([]runner.Account, error) {
	names := s.Names
	if s.Auto {
		names = r.discover()
	}
	var errs []error
	byUID := map[uint32]runner.Account{}
	for _, n := range names {
		a, err := r.Lookup(n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		byUID[a.UID] = a
	}
	out := make([]runner.Account, 0, len(byUID))
	for _, a := range byUID {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, errors.Join(errs...)
}

func (r Resolver) discover() []string {
	seen := map[string]bool{}
	if entries, err := r.FS.ReadDir("/var/lib/systemd/linger"); err == nil {
		for _, e := range entries {
			seen[e.Name()] = true
		}
	}
	if entries, err := r.FS.ReadDir("/run/user"); err == nil {
		for _, e := range entries {
			if _, err := strconv.Atoi(e.Name()); err == nil && r.FS.Exists("/run/user/"+e.Name()+"/systemd") {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Linger tells whether the user has linger enabled.
func (r Resolver) Linger(a runner.Account) bool {
	return r.FS.Exists("/var/lib/systemd/linger/" + a.Name)
}

// ManagerRunning tells whether the systemd user manager of the user runs.
func (r Resolver) ManagerRunning(a runner.Account) bool {
	return r.FS.Exists(a.RuntimeDir() + "/systemd")
}

// CanRunAs tells whether commands can run as the user: as root, or as the user itself.
func (r Resolver) CanRunAs(a runner.Account) bool {
	euid := r.Euid()
	return euid == 0 || uint32(euid) == a.UID
}

// Scope is where containers or quadlet files live: the rootful one ("root") or a user's.
type Scope struct {
	// Name is "root" or the user name.
	Name    string
	Account runner.Account
	// Rootless is false for the rootful scope.
	Rootless bool
	// User is the user to run commands as; empty for the rootful scope (commands run as root).
	User string
	// Skipped says why the scope is not inspected.
	Skipped string
}

// Scopes returns the rootful scope (when requested) followed by the users of the spec.
func (r Resolver) Scopes(s Spec, rootful bool) ([]Scope, error) {
	var out []Scope
	if rootful {
		sc := Scope{Name: "root", Account: runner.Account{Name: "root", UID: 0, Home: "/root"}}
		if r.Euid() != 0 {
			sc.Skipped = "requires root"
		}
		out = append(out, sc)
	}
	accounts, err := r.Select(s)
	for _, a := range accounts {
		if a.UID == 0 {
			continue // root is the rootful scope
		}
		sc := Scope{Name: a.Name, Account: a, Rootless: true, User: a.Name}
		if !r.CanRunAs(a) {
			sc.Skipped = "requires root"
		}
		out = append(out, sc)
	}
	return out, err
}
