package system

import (
	"golang.org/x/sys/unix"
)

// uname returns the kernel identification, architecture, host name and NIS domain name.
func uname() (Kernel, string, string, string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return Kernel{}, "", "", "", err
	}
	k := Kernel{
		Name:    unix.ByteSliceToString(u.Sysname[:]),
		Release: unix.ByteSliceToString(u.Release[:]),
		Version: unix.ByteSliceToString(u.Version[:]),
	}
	return k, unix.ByteSliceToString(u.Machine[:]), unix.ByteSliceToString(u.Nodename[:]), unix.ByteSliceToString(u.Domainname[:]), nil
}
