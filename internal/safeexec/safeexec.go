// Package safeexec checks that a file terminus is about to run or read as trusted input (an
// external module, a custom fact) is one only a trusted user could have placed there. Without
// this, any local user able to create a file in the external modules or facts directory (a
// shared mount, a misconfigured permission somewhere else) could have terminus run arbitrary
// code, often as root, or feed it fabricated facts.
package safeexec

import (
	"fmt"
	"os"
	"syscall"
)

// Check verifies that path (resolved through any symlink) is owned by root or by the user running
// terminus, and is not writable by anyone else. It also checks dir, the directory path was found
// in, the same way: a directory anyone can write to lets anyone replace or add files in it
// regardless of their own permissions.
func Check(path, dir string) error {
	if err := checkOwnerAndPerms(dir); err != nil {
		return fmt.Errorf("directory %w", err)
	}
	return checkOwnerAndPerms(path)
}

func checkOwnerAndPerms(path string) error {
	// os.Stat follows symlinks: what is checked is what actually runs or is read, not the link.
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // not a platform this can be checked on
	}
	euid := uint32(os.Geteuid())
	if st.Uid != 0 && st.Uid != euid {
		return fmt.Errorf("%s: owned by uid %d, not root or the terminus user (uid %d)", path, st.Uid, euid)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: writable by group or other (mode %s)", path, info.Mode().Perm())
	}
	return nil
}
