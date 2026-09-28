# Change Log
All notable changes to this project will be documented in this file.
This project adheres to [Semantic Versioning](http://semver.org/).

## [0.2.0] - unreleased

Start of terminus v2: facts plus checks on them, organized in modules (see `docs/design-v2.md`).

### New Features
- `terminus check`: evaluates the facts and reports findings (ok/info/warn/fail);
  exit code 0 ok, 1 warn, 2 fail, 3 terminus error.
- Core modules `system`, `cpu`, `memory`, `storage`, `network`, `external` (docs/modules.md) with
  checks on load, CPU and memory pressure, available memory, swap, OOM kills, disk space and
  inodes, filesystems remounted read-only, unreachable mounts, default route, DNS.
- New facts: MemAvailable and the whole meminfo, OOM kills, PSI, virtualization, time zone,
  filesystem usage and inodes, fstab options, swap areas, interface state and counters,
  default routes, DNS (systemd-resolved aware), listening sockets with their process.
- Path queries select list elements by name: `network.interfaces.eth0`, `storage.filesystems./var`.
- Configuration file, decoded strictly: enable or disable modules, module settings, disabled
  checks, checks excluded by subject glob (`checks.exclude`), thresholds
  (docs/configuration.md). Merged from three layers, system (`/etc/terminus/terminus.toml`),
  user (`~/.config/terminus/terminus.toml`) and per-run (`./terminus.toml`); `--config` reads a
  single file instead.
- `modules list`, `modules detect`, `checks list`, `config validate`, `config example`, `config show` commands;
  `--modules`, `--no-modules`, `--only` and `--modules-dir` flags.
- External modules: executables in `/etc/terminus/modules.d` printing facts and findings as JSON.
- Optional `http` module: status, latency and TLS certificate expiry of configured endpoints.
- Core `systemd` module: units of the system and user managers (users "auto", listed, or
  `--users`), memory peaks and limits, cgroup OOM counters, journal history of OOM kills,
  failures, restarts and exit codes, linger; checks systemd.failed, unit.memory-peak,
  unit.memory-limit, unit.oom-kills, unit.restarts, user.linger, resources.overcommit.
- Optional `journal` module: journal disk usage and log lines per unit.
- Optional `podman` module: containers of root and of the users (state, health, OOM, restarts,
  ports, mounts, unit, stats), volumes with their owner as seen in the container, networks,
  storage; checks podman.unhealthy, podman.exited, podman.oom-killed, podman.restarts, ...
- Optional `quadlet` module: quadlet files, dry run with the generator of the machine, generated
  unit states, volumes really used; checks quadlet.volume-not-used (a .volume unit not used because
  the .container names the volume without the suffix), quadlet.network-not-used, quadlet.dryrun,
  quadlet.unit-not-loaded, quadlet.unit-never-active, quadlet.changed-since-start,
  quadlet.volume-owner.
- `--users` applies to systemd, podman, quadlet and timers.
- Optional `caddy` module: domains served, upstreams, served certificates, DNS of the domains.
- Optional `postgres` module: connections by client, settings, sizes, stuck transactions.
- `public_ports` in `[modules.network]`: unexpected public ports become warnings.
- Output formats `jsonl`, `markdown`, `html`, `prometheus`; `--output-file` writes atomically.
- `terminus report` (checks and facts in one document) and `terminus diff` (compare two reports).
- Commands run as another user (root only) with the session environment of that user.
- Output formats: `text` for humans (colors only on a terminal, `--color`, `NO_COLOR`) and
  `json` with a versioned schema (`schema_version`), per-module status, errors and timings.
- Text output of the facts: lists of records (interfaces, filesystems, units, containers ...)
  are tables of their main fields, fitted to the terminal width; `-v` shows every field, with
  list elements labelled by name. Durations in milliseconds and microseconds are humanized.
- Less noise in the text output: sizes without the exact byte count (`-v` shows it), groups of
  counters all at zero shown as `all 0`, minor records folded into one line (memory and
  squashfs filesystems, loop devices, virtual interfaces down), only the modules that are not
  ok listed under a count by status, zero counts of the summary in grey.
- `modules list`, `modules detect` and `checks list` fit the terminal: the description wraps
  beside the other columns or below the row.
- `terminus facts <section>` (e.g. `facts storage`, `facts network.interfaces.eth0`) prints the
  section as text on a terminal, JSON otherwise (`-o text`/`-o json` to choose).
- Long output on a terminal goes through a pager (`$TERMINUS_PAGER`, `$PAGER`, `less`);
  `--no-pager` turns it off.
- Modules run in parallel with a per-module timeout (`--timeout`); a failing or hanging module
  is reported instead of stopping the run. `--only` selects the modules.
- `terminus serve`: `POST /facts` as before, plus `GET /report`.
- `terminus remote`: runs terminus on other machines through the system ssh client (the binary is
  copied once per version and architecture), shows the reports together, saves them with
  `--output-dir` for `terminus diff` (docs/remote.md).
- `terminus probe`: stops a unit or a container on purpose to verify that the endpoint and the
  monitoring notice the outage, and always starts it again (docs/probe.md).
- Optional `tls` module: certificates in files (certbot, Caddy storage, configured paths) and
  served by endpoints: expiry, chain verification, host names.
- Optional `backup` module: latest backup of restic, borg and pgBackRest repositories, its age,
  and the result of the systemd unit that makes it.
- Optional `updates` module: pending updates and security updates (apt, dnf, yum, apk) from the
  package index already on the machine, its age, pending reboot (marker, needs-restarting, newer
  kernel installed).
- Optional `timers` module: systemd timers of the system and user managers, schedule, last and
  next run, result of the last run.
- Optional `firewall` module: nftables (firewalld, ufw and iptables-nft rules included) and
  iptables-legacy rules evaluated for new connections from anywhere: whether closed ports are
  filtered, for IPv4 and IPv6, and which listening ports are reachable from other machines.

### Changed
- `terminus` without arguments prints the facts as text; use `terminus facts -o json` for JSON.
  In JSON the facts are under `.modules.<module>.facts`.
- Facts are split by module and use snake_case names with units (`memory.total_bytes`,
  `system.kernel.release`). Queries are case-insensitive, so `System.Kernel.Release` still works,
  but paths such as `System.Memory.Total` or `System.Network.Interfaces.eth0` moved to
  `memory.total_bytes` and `network.interfaces.eth0`. Templates use the Go field names
  (`{{ .System.Kernel.Release }}`).
- Custom facts are named after the file without extension (`mysql.sh` provides `mysql`), run with
  a 30 s timeout, and their errors are reported instead of being logged.
- Path queries match keys case-insensitively and fail with exit code 3 when the fact is missing.
- Static binaries (`CGO_ENABLED=0`) for linux/amd64 and linux/arm64; Go 1.24, no vendor directory.
- v1 flags (`-format`, `-http`, `-version`, ...) keep working.

### Fixed
- Memory and swap sizes were wrong on systems where sysinfo reports them in units larger than a byte.
- Block device I/O counters were always zero on kernels with 15/17-field `stat` files.
- A missing or unreadable DMI file (serial numbers as non-root) emptied all the DMI facts.
- The last processor of `/proc/cpuinfo` was lost when the file did not end with a blank line.
- `PassNo` of filesystems was read from the wrong field; mounts come from mountinfo, not /etc/mtab.
- A load average field was named `Ten` instead of 15 minutes.

### Removed Features
- Windows and macOS support.

## [0.1.0] - 2015-08-27

### Removed Features
- Text Template: Removed the ability to format output from the HTTP API with text/template. It now accepts the same dotted-notation as the command-line. Because of the entire text/template removal, the version is being bumped to 0.1.0.

## [0.0.4] - 2015-08-22

### New Features
- Run conservatively: Facts will only be obtained when requested

### Removed Features
- Text Templates: The ability to format results as a text template has been removed.

### Changed
- The internal structure of the code has been modified to accommodate more than just the Linux platform.

## [0.0.3] - 2015-08-17

### New Features
- Debug mode: only errors are printed in debug mode
- Specify a fact to query on the command-line

### New Facts
- Processor
- DMI
- Block Devices

### Changed
- Add LSB Codename facts to OSRelease
- Add timezone and offset to Date
- Fixed load average calculation

## [0.0.2] - 2015-04-30

### Changed
- Add Ip4Addresses and Ip6Addresses facts to interfaces
