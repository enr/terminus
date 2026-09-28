package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/engine"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/modules/cpu"
	"github.com/enr/terminus/internal/modules/external"
	"github.com/enr/terminus/internal/modules/extmod"
	"github.com/enr/terminus/internal/modules/http"
	"github.com/enr/terminus/internal/modules/memory"
	"github.com/enr/terminus/internal/modules/network"
	"github.com/enr/terminus/internal/modules/storage"
	"github.com/enr/terminus/internal/modules/system"
	"github.com/enr/terminus/internal/runner"
)

// app holds what the commands share once the flags and the configuration are loaded.
type app struct {
	cfg     *config.Config
	reg     *module.Registry
	env     *module.Env
	sel     module.Selection
	timeout time.Duration
}

// builtinModules returns a fresh instance of every built-in module.
func builtinModules(externalFactsDir string) []module.Module {
	return []module.Module{
		system.New(),
		cpu.New(),
		memory.New(),
		storage.New(),
		network.New(),
		external.New(externalFactsDir),
		http.New(),
	}
}

// newApp loads the configuration, the external modules and the registry. Command line flags win
// over the configuration. An invalid configuration is an error: nothing runs half-configured.
func newApp(g *globalOptions, stderr io.Writer) (*app, error) {
	changed := g.changed
	if changed == nil {
		changed = func(string) bool { return false }
	}
	level := slog.LevelWarn
	if g.debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(g.configPath, changed("config"))
	if err != nil {
		return nil, err
	}

	modulesDir := config.DefaultModulesDir
	switch {
	case changed("modules-dir"):
		modulesDir = g.modulesDir
	case cfg.ModulesDir != "":
		modulesDir = cfg.ModulesDir
	}
	mods := builtinModules(g.externalFactsDir)
	ext, err := extmod.Load(modulesDir)
	if err != nil {
		log.Warn("some external modules were not loaded", "err", err)
	}
	for _, m := range ext {
		mods = append(mods, m)
	}
	reg, err := module.NewRegistry(mods...)
	if err != nil {
		return nil, fmt.Errorf("%w (an external module in %s has the name of a built-in one)", err, modulesDir)
	}

	if err := cfg.Configure(reg); err != nil {
		return nil, configError(cfg, err)
	}
	if changed("external-facts-dir") {
		if m, ok := reg.Get(external.Name); ok {
			m.(*external.Module).SetDir(g.externalFactsDir)
		}
	}
	if err := cfg.Validate(reg); err != nil {
		return nil, configError(cfg, err)
	}

	timeout := engine.DefaultTimeout
	switch {
	case changed("timeout"):
		timeout = g.timeout
	case cfg.Timeout > 0:
		timeout = cfg.Timeout
	}

	a := &app{
		cfg: cfg,
		reg: reg,
		env: &module.Env{
			Runner: runner.Exec{},
			Debug:  g.debug,
			Log:    log,
			Checks: &cfg.Checks,
		},
		sel: module.Selection{
			Only:    g.only,
			Enable:  g.enable,
			Disable: g.disable,
			Config:  cfg.Enabled,
		},
		timeout: timeout,
	}
	if _, err := reg.Resolve(a.sel); err != nil {
		return nil, err
	}
	return a, nil
}

func configError(cfg *config.Config, err error) error {
	if cfg.Path != "" {
		return fmt.Errorf("invalid configuration %s:\n%w", cfg.Path, err)
	}
	return fmt.Errorf("invalid configuration:\n%w", err)
}

// collect runs the enabled modules.
func (a *app) collect(ctx context.Context, checks bool) (*model.Report, error) {
	mods, err := a.reg.Enabled(a.sel)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return engine.Run(ctx, mods, a.env, engine.Options{Timeout: a.timeout, Checks: checks}), nil
}
