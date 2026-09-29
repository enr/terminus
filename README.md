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
terminus facts [path]     print the facts (all of them, or the value at path)
terminus check            evaluate the facts
terminus report           checks and facts in one document (markdown, html, json)
terminus diff A.json B.json  what changed between two reports
terminus remote HOST...   run terminus on other machines through ssh
terminus probe            stop a service on purpose to verify that the monitoring notices
terminus serve            serve facts and reports over HTTP
terminus modules list     which modules run and why
terminus modules detect   which optional modules fit this machine
terminus checks list      checks with their effective thresholds
terminus config example   a commented terminus.toml with every setting
terminus config validate  check the configuration
terminus version
```

Facts and checks are grouped in modules: the core ones (`system`, `cpu`, `memory`, `storage`,
`network`, `systemd`, `external` for custom facts) run by default, optional ones (`http`,
`journal`, `podman`, `quadlet`, `caddy`, `postgres`, `tls`, `backup`, `updates`, `timers`, `firewall`) are enabled in
[`/etc/terminus/terminus.toml`](docs/configuration.md), and [external modules](docs/configuration.md#external-modules)
are executables dropped in `/etc/terminus/modules.d`. `--only memory,storage` runs just some.
Terminus also supports [custom facts](docs/custom-facts.md) and a [HTTP API](docs/api.md).

### Check the machine

```shell
$ terminus check
srv-01 · 2026-09-28T10:00:00Z · 14ms
✖ 0 fail  ⚠ 2 warn  ℹ 1 info  ✔ 8 ok

Findings
  ⚠ warn  disk.usage            88% used [/var]
  ⚠ warn  mem.oom-kills         3 processes killed by the OOM killer since boot [oom killer]
  ℹ info  net.public-listeners  3 TCP ports listen on all addresses: 22 (sshd), 80 (caddy), 443 (caddy) [tcp]
  ✔ ok    cpu.load              15 min load 0.28 on 4 CPUs (0.07 per CPU) [load average]
  ✔ ok    disk.usage            41% used [/]
  ...

Modules  5 ok · 1 skipped
  external  skipped  directory /etc/terminus/facts.d does not exist
```

The exit code tells the outcome: `0` all good, `1` warnings, `2` failures, `3` terminus error.
A module that could not be fully collected counts as a warning even when its findings look fine.
`--problems` hides the findings that are fine, `-v` adds evidence and hints and lists every
module with its collection time (only the ones not ok otherwise).

The modules, their facts and their checks are described in [docs/modules.md](docs/modules.md).

### Other machines

```shell
$ terminus remote --sudo --remote-binary /usr/local/bin/terminus srv-01 apps@web-02 -- --only systemd,podman,quadlet
$ terminus remote --hosts-file hosts.txt --output-dir reports/
```

terminus copies itself to the hosts through ssh (once per version, `~/.ssh/config` and the agent
apply) and brings the reports back: see [docs/remote.md](docs/remote.md).

### Verify the monitoring

```shell
$ sudo terminus probe --unit myapp.service --user apps --http https://app.example.org/health \
    --check-cmd 'grep -q "myapp.*DOWN" /var/log/easeprobe.log' --wait 60s
```

stops the service for a while, checks that the endpoint and the monitoring notice, and starts it
again (also on Ctrl-C). The only command that changes the machine: see [docs/probe.md](docs/probe.md).

### Print a single fact

```shell
$ terminus memory.available_bytes
16210112512
$ terminus network.interfaces.eth0.addresses.0.ip
10.0.2.15
$ terminus storage.filesystems./.used_ratio
0.2301
```

A section is printed as text on a terminal (JSON when piped, `-o` to choose):

```shell
$ terminus facts storage.filesystems
MOUNT  TYPE       SIZE   USED  INODES  RO     SOURCE
/      ext4   48.0 GiB  23.0%    4.1%  false  /dev/vda1
/boot  ext4  975.9 MiB  31.2%    0.1%  false  /dev/vda2

tables show the main fields: -v shows them all, terminus facts storage.filesystems./ one record
```

Long output goes through a pager on a terminal (`$TERMINUS_PAGER`, `$PAGER` or `less`;
`--no-pager` to turn it off).

Using templates (Go field names):

```shell
$ terminus --format 'Machine ID is {{ .System.MachineID }}'
Machine ID is bab60d34057d4ed7a7f3699ee4d15d26
```

### Output formats

Also `jsonl`, `markdown`, `html` and `prometheus`: see [docs/output.md](docs/output.md).


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
$ terminus facts -o json | jq .modules.system.facts.kernel
{
  "name": "Linux",
  "release": "6.8.0",
  "version": "#1 SMP PREEMPT_DYNAMIC"
}
```

## Development

Use the scripts in the `.sdlc/` directory.

- Build a static binary in `bin/`: `.sdlc/build`
- Build distribution archives for linux/amd64 and linux/arm64 in `dist/`: `.sdlc/build-dist`
- Run format check, vet, staticcheck, tests (with the race detector when cgo and a C compiler are
  available) and the static build: `.sdlc/check`. On a workstation the checks that need a
  missing tool are skipped and listed at the end; in CI (`CI=true`) or with `SDLC_STRICT=1`
  they are errors.
- Update dependencies: `.sdlc/update`
