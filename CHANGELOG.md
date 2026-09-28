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
- Output formats: `text` for humans (colors only on a terminal, `--color`, `NO_COLOR`) and
  `json` with a versioned schema (`schema_version`), per-module status, errors and timings.
- Modules run in parallel with a per-module timeout (`--timeout`); a failing or hanging module
  is reported instead of stopping the run. `--only` selects the modules.
- `terminus serve`: `POST /facts` as before, plus `GET /report`.

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
