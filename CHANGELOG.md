# Change Log
All notable changes to this project will be documented in this file.
This project adheres to [Semantic Versioning](http://semver.org/).

## [0.2.0] - unreleased

Start of terminus v2: facts plus checks on them, organized in modules (see `docs/design-v2.md`).

### New Features
- `terminus check`: evaluates the facts and reports findings (ok/info/warn/fail);
  exit code 0 ok, 1 warn, 2 fail, 3 terminus error. First check: `mem.available-low`.
- Output formats: `text` for humans (colors only on a terminal, `--color`, `NO_COLOR`) and
  `json` with a versioned schema (`schema_version`), per-module status, errors and timings.
- Modules run in parallel with a per-module timeout (`--timeout`); a failing or hanging module
  is reported instead of stopping the run. `--only` selects the modules.
- `terminus serve`: `POST /facts` as before, plus `GET /report`.
- New fact: `System.Memory.Available` (MemAvailable).

### Changed
- `terminus` without arguments prints the facts as text; use `terminus facts -o json` for JSON.
  In JSON the facts are under `.modules.<module>.facts`.
- Path queries match keys case-insensitively and fail with exit code 3 when the fact is missing.
- Static binaries (`CGO_ENABLED=0`) for linux/amd64 and linux/arm64; Go 1.24, no vendor directory.
- v1 flags (`-format`, `-http`, `-version`, ...) keep working.

### Fixed
- Memory and swap sizes honour the sysinfo memory unit.

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
