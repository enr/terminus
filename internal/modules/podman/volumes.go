package podman

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/users"
)

func (m *Module) volumeOwner(v *Volume, idm users.IDMap) {
	if v.Mountpoint == "" {
		return
	}
	uid, gid, err := m.fs.Owner(v.Mountpoint)
	if err != nil {
		return
	}
	cu, cg := idm.UID(uid), idm.GID(gid)
	v.OwnerUID, v.OwnerGID, v.ContainerUID, v.ContainerGID = &uid, &gid, &cu, &cg
}

var errBudget = errors.New("time budget exhausted")

// volumeSize sums the size and counts the files of a volume, until the deadline.
func volumeSize(hfs hostfs.FS, v *Volume, deadline time.Time) {
	if v.Mountpoint == "" || time.Now().After(deadline) {
		return
	}
	var size, files uint64
	err := filepath.WalkDir(hfs.Path(v.Mountpoint), func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errBudget
		}
		files++
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				size += uint64(info.Size())
			}
		}
		return nil
	})
	v.SizeBytes, v.Files = &size, &files
	v.SizePartial = errors.Is(err, errBudget)
}
