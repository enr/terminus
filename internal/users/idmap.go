package users

import (
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/runner"
)

// IDRange is a subordinate id range of /etc/subuid or /etc/subgid.
type IDRange struct {
	Start, Count uint64
}

// IDMap translates host ids into the ids a rootless container of the user sees: the user's own
// id is root (0), the subordinate range follows from 1. The rootful map is the identity.
type IDMap struct {
	identity       bool
	uid, gid       uint32
	subuid, subgid *IDRange
}

// Identity is the map of rootful containers.
func Identity() IDMap { return IDMap{identity: true} }

// NewIDMap reads /etc/subuid and /etc/subgid for the user.
func NewIDMap(fs hostfs.FS, a runner.Account) IDMap {
	subuid, _ := fs.Lines("/etc/subuid")
	subgid, _ := fs.Lines("/etc/subgid")
	return IDMap{uid: a.UID, gid: a.GID, subuid: FindRange(subuid, a), subgid: FindRange(subgid, a)}
}

// UID returns the container uid of a host uid, -1 when not mapped (seen as nobody).
func (m IDMap) UID(host uint32) int64 { return m.toContainer(host, m.uid, m.subuid) }

// GID returns the container gid of a host gid, -1 when not mapped.
func (m IDMap) GID(host uint32) int64 { return m.toContainer(host, m.gid, m.subgid) }

func (m IDMap) toContainer(host uint32, own uint32, sub *IDRange) int64 {
	if m.identity {
		return int64(host)
	}
	if host == own {
		return 0
	}
	if sub != nil && uint64(host) >= sub.Start && uint64(host) < sub.Start+sub.Count {
		return int64(uint64(host)-sub.Start) + 1
	}
	return -1
}

// FindRange finds the range of a user in /etc/subuid or /etc/subgid lines (by name or uid).
func FindRange(lines []string, a runner.Account) *IDRange {
	for _, l := range lines {
		f := strings.Split(strings.TrimSpace(l), ":")
		if len(f) != 3 || (f[0] != a.Name && f[0] != strconv.FormatUint(uint64(a.UID), 10)) {
			continue
		}
		start, err1 := strconv.ParseUint(f[1], 10, 32)
		count, err2 := strconv.ParseUint(f[2], 10, 32)
		if err1 == nil && err2 == nil {
			return &IDRange{Start: start, Count: count}
		}
	}
	return nil
}
