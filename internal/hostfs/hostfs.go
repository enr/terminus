// Package hostfs reads the files that describe the host (/proc, /sys, /etc) under a root
// directory, so that collectors can be tested against fixture trees.
package hostfs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// FS reads host files under Root.
type FS struct {
	Root string
}

// Host is the FS of the running machine.
var Host = FS{Root: "/"}

// Path returns the real path of an absolute host path.
func (f FS) Path(p string) string {
	if f.Root == "" || f.Root == "/" {
		return p
	}
	return filepath.Join(f.Root, p)
}

// ReadFile reads a whole file.
func (f FS) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(f.Path(p))
}

// ReadString reads a file and trims the surrounding white space.
func (f FS) ReadString(p string) (string, error) {
	b, err := f.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadUint reads a file holding one unsigned integer.
func (f FS) ReadUint(p string) (uint64, error) {
	s, err := f.ReadString(p)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", p, err)
	}
	return n, nil
}

// Lines reads a file split in lines.
func (f FS) Lines(p string) ([]string, error) {
	b, err := f.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return SplitLines(b), nil
}

// ReadDir lists a directory.
func (f FS) ReadDir(p string) ([]os.DirEntry, error) {
	return os.ReadDir(f.Path(p))
}

// Readlink reads a symbolic link.
func (f FS) Readlink(p string) (string, error) {
	return os.Readlink(f.Path(p))
}

// Owner returns the owner uid and gid of a path (without following a final symlink).
func (f FS) Owner(p string) (uid, gid uint32, err error) {
	info, err := os.Lstat(f.Path(p))
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("%s: no owner information", p)
	}
	return st.Uid, st.Gid, nil
}

// Exists reports whether a path exists.
func (f FS) Exists(p string) bool {
	_, err := os.Stat(f.Path(p))
	return err == nil
}

// SplitLines splits text in lines, dropping the trailing empty line.
func SplitLines(b []byte) []string {
	var lines []string
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines
}

// IsNotExist reports whether err means that a file does not exist.
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// IsIgnorable reports whether err means that a file is not there or not readable by the current
// user: optional information that is simply left out.
func IsIgnorable(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

// ParseKeyValue parses KEY=value lines (os-release, environment files): quotes are removed,
// comments and blank lines skipped.
func ParseKeyValue(lines []string) map[string]string {
	m := map[string]string{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		} else if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = unescapeShell.Replace(v[1 : len(v)-1])
		}
		m[strings.TrimSpace(k)] = v
	}
	return m
}

// unescapeShell undoes the escapes allowed in double-quoted os-release values.
var unescapeShell = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`")

// PSI holds the pressure stall information of a resource (/proc/pressure/*).
type PSI struct {
	Some *PSILine `json:"some,omitempty"`
	Full *PSILine `json:"full,omitempty"`
}

// PSILine is one line of a PSI file: share of time (percent) in which tasks were stalled.
type PSILine struct {
	Avg10       float64 `json:"avg10"`
	Avg60       float64 `json:"avg60"`
	Avg300      float64 `json:"avg300"`
	TotalMicros uint64  `json:"total_us"`
}

// ParsePSI parses a /proc/pressure file.
func ParsePSI(lines []string) (PSI, error) {
	var p PSI
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		var line PSILine
		for _, f := range fields[1:] {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				return p, fmt.Errorf("invalid PSI field %q", f)
			}
			var err error
			switch k {
			case "avg10":
				line.Avg10, err = strconv.ParseFloat(v, 64)
			case "avg60":
				line.Avg60, err = strconv.ParseFloat(v, 64)
			case "avg300":
				line.Avg300, err = strconv.ParseFloat(v, 64)
			case "total":
				line.TotalMicros, err = strconv.ParseUint(v, 10, 64)
			}
			if err != nil {
				return p, fmt.Errorf("invalid PSI field %q: %w", f, err)
			}
		}
		switch fields[0] {
		case "some":
			p.Some = &line
		case "full":
			p.Full = &line
		}
	}
	return p, nil
}

// ReadPSI reads /proc/pressure/<resource>. It returns nil when the kernel has no PSI support.
func (f FS) ReadPSI(resource string) (*PSI, error) {
	lines, err := f.Lines("/proc/pressure/" + resource)
	if err != nil {
		// EOPNOTSUPP: PSI compiled in but disabled (psi=0).
		if IsIgnorable(err) || errors.Is(err, syscall.EOPNOTSUPP) {
			return nil, nil
		}
		return nil, err
	}
	p, err := ParsePSI(lines)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
