// Package external is the core module that loads custom facts from a directory: executables
// printing JSON and static .json files (see docs/custom-facts.md).
package external

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/lib/facts"
)

// Name of the module.
const Name = "external"

// DefaultDir is the default external facts directory.
const DefaultDir = "/etc/terminus/facts.d"

// Module loads the external facts.
type Module struct {
	dir string
}

// New returns the external facts module reading from dir. The directory itself is configured in
// lib/facts (facts.Configure); dir is used to skip the module when the directory is missing.
func New(dir string) *Module { return &Module{dir: dir} }

// Name implements module.Module.
func (*Module) Name() string { return Name }

// Core implements module.Module.
func (*Module) Core() bool { return true }

// Collect implements module.Module.
func (m *Module) Collect(_ context.Context, _ *module.Env) (any, error) {
	if _, err := os.Stat(m.dir); errors.Is(err, fs.ErrNotExist) {
		return nil, module.Skip("directory %s does not exist", m.dir)
	} else if err != nil {
		return nil, err
	}
	return facts.External(), nil
}
