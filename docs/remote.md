# Remote machines

`terminus remote` runs terminus on other machines through ssh and brings the reports back, with
nothing to install on them beforehand.

```shell
$ terminus remote srv-01 apps@web-02 db-01:2222
$ terminus remote --sudo --remote-binary /usr/local/bin/terminus --hosts-file hosts.txt -- --only systemd,podman,quadlet
$ terminus remote --hosts-file hosts.txt --output-dir reports/ -o markdown > all.md
```

For each host (4 at a time, `--parallel`):

1. `ssh HOST 'uname -m; echo $HOME'`: the architecture and where to cache the binary.
2. The binary for that architecture is copied to `~/.cache/terminus/terminus-<hash>` through the
   ssh standard input (no scp or sftp needed), unless the same one is already there: the second
   run copies nothing.
3. `terminus report -o json` runs there (`sudo -n terminus ...` with `--sudo`), and the JSON comes
   back.

The output shows a section per host and a summary table:

```
HOST        RESULT                               FAIL  WARN  ARCH    COPIED
srv-01      warn                                 0     2     x86_64  no
web-02      ok                                   0     0     aarch64 yes
db-01:2222  error: connection: Connection refused
```

The exit code is the worst among the hosts: `0` ok, `1` warnings, `2` failures, `3` when a host
cannot be reached or terminus fails there.

## ssh

terminus uses the `ssh` client of the machine it runs on, so everything in `~/.ssh/config` applies
as it does for you: host aliases, `User`, `Port`, `ProxyJump`, `IdentityFile`, `ControlMaster`,
the agent, and the verification of `known_hosts`. ssh runs with `BatchMode=yes`: there is no
password or host key prompt, so the keys must be in the agent (or without passphrase) and the
hosts known beforehand (`ssh HOST true` once).

A host is anything ssh accepts, with an optional port: `srv-01`, `apps@web-02`, `web-03:2222`,
`[2001:db8::1]:2222`. More ssh options with `--ssh-option` (as `ssh -o`, repeatable), another
client with `--ssh`, another configuration file with `--ssh-config PATH` (as `ssh -F`: it replaces
`~/.ssh/config`, so a project can keep its own aliases and keys).

`--hosts-file` reads one host per line; blank lines and `#` comments are ignored.

## Privileges

Without `--sudo` terminus runs as the ssh user: it sees what that user sees (its own containers and
user units, not those of other users; no journal of other users). `--sudo` runs it with `sudo -n`
(non interactive): the ssh user needs a sudo rule without password for it.

`--sudo` requires `--remote-binary`: sudo must never be pointed at a path the ssh user can write,
such as the `~/.cache/terminus` directory terminus copies itself into, or that user could replace
the binary sudo runs as root with anything they like. Install terminus at a fixed, root-owned path
on the hosts and use that:

```shell
$ terminus remote --sudo --remote-binary /usr/local/bin/terminus --hosts-file hosts.txt
```

and in `/etc/sudoers.d/terminus`:

```
ops ALL=(root) NOPASSWD: /usr/local/bin/terminus
```

As root, terminus inspects the systemd user managers and the rootless containers of every user
with linger (see `users` in [configuration.md](configuration.md)).

## Other architectures

The local binary is copied when the host has the same architecture. For the other one put
`terminus-linux-amd64` or `terminus-linux-arm64` next to terminus or in `--binaries-dir`
(`.sdlc/build-dist` builds both). Supported: x86_64 and aarch64.

`--remote-binary /usr/local/bin/terminus` uses a terminus installed on the hosts instead and copies
nothing.

## Configuration and arguments

`--remote-config terminus.toml` copies a configuration (cached by hash like the binary) and runs
the remote terminus with `--config` on it; without it each host uses its own
`/etc/terminus/terminus.toml`, if any. The arguments after `--` go to the remote `terminus report`:
`-- --only memory,storage`, `-- --users apps`, `-- --modules podman,quadlet`.

## Saving and comparing

`--output-dir DIR` saves the JSON report of each host in `DIR/<host>.json` (`apps@web:2222`
becomes `apps_web_2222.json`), ready for `terminus diff`:

```shell
$ terminus remote --output-dir before/ srv-01
$ # deploy
$ terminus remote --output-dir after/ srv-01
$ terminus diff before/srv-01.json after/srv-01.json
```

`-o json` prints `{"hosts": [{"host", "report", "error", "arch", "copied"}, ...]}`.
