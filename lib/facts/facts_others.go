// +build !windows

package facts

import (
	"os"
)

func isExecutable(fi os.FileInfo) bool {
	if m := fi.Mode(); !m.IsDir() && m&0111 != 0 {
		return true
	}
	return false
}
