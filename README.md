# Terminus

Get facts about a Linux machine and check them. Parallel execution, structured output, remote API.

terminus v2 is being built: the design is in [docs/design-v2.md](docs/design-v2.md).

## Install

Download the archive for your architecture from the releases page: terminus is a single static
binary with no dependencies.

```shell
$ tar xzf terminus-*_linux_amd64.tar.gz
$ sudo install terminus-*_linux_amd64/terminus /usr/local/bin/
```

## Usage

```
terminus facts [path]   print the facts (all of them, or the value at path)
terminus check          evaluate the facts
terminus serve          serve facts and reports over HTTP
terminus version
```

Facts and checks are grouped in modules (`system`, `external`, ...). By default the core modules
run; `--only system` selects them explicitly. Terminus also supports [custom facts](docs/custom-facts.md)
and a [HTTP API](docs/api.md).

### Check the machine

```shell
$ terminus check -v
srv-01 · 2026-09-28T10:00:00Z · 14ms
✖ 0 fail  ⚠ 1 warn  ℹ 0 info  ✔ 0 ok

Findings
  ⚠ warn  mem.available-low  only 7.3% of memory available [memory]
          hint: check which processes or containers use the memory (ps, podman stats) and their limits
          available_bytes: 285.0 MiB (298844160)
          available_ratio: 0.0732
          total_bytes: 3.8 GiB (4080218112)

Modules
  external  skipped  directory /etc/terminus/facts.d does not exist
  system    ok       12ms
```

The exit code tells the outcome: `0` all good, `1` warnings, `2` failures, `3` terminus error.
`--problems` hides the findings that are fine.

### Print a single fact

```shell
$ terminus System.Network.Interfaces.eth0.IP6Addresses.0.IP
fe80::f816:3eff:fead:8549
```

Using templates:

```shell
$ terminus --format 'Machine ID is {{ .System.MachineID }}'
Machine ID is bab60d34057d4ed7a7f3699ee4d15d26
```

### Output formats

`-o text` (default) is meant for people: colors are used only on a terminal and can be controlled
with `--color auto|always|never` or `NO_COLOR`.

`-o json` is meant for programs: the complete report, raw values (bytes, milliseconds), a
`schema_version`, and the status of every module.

```shell
$ terminus check -o json | jq .summary
{
  "ok": 0,
  "info": 0,
  "warn": 1,
  "fail": 0
}
$ terminus facts -o json | jq .modules.system.facts.Kernel
{
  "Name": "Linux",
  "Release": "6.8.0",
  "Version": "#1 SMP PREEMPT_DYNAMIC"
}
```

## Development

Use the scripts in the `.sdlc/` directory.

- Build a static binary in `bin/`: `.sdlc/build`
- Build distribution archives for linux/amd64 and linux/arm64 in `dist/`: `.sdlc/build-dist`
- Run format check, vet, staticcheck (if installed), tests and the static build: `.sdlc/check`
- Update dependencies: `.sdlc/update`
