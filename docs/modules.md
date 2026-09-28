# Modules

Facts and checks are grouped in modules. Core modules run by default, optional modules must be
enabled in the [configuration](configuration.md), external modules are executables added to
`/etc/terminus/modules.d`. `terminus modules list` shows them all.

In JSON the facts of a module are under `.modules.<module>.facts`. Path queries start from the
module name (`terminus memory.available_bytes`); in lists a name selects the element
(`network.interfaces.eth0`, `storage.filesystems./srv`). Sizes are in bytes (`_bytes`), ratios
between 0 and 1 (`_ratio`), durations in seconds (`_seconds`); the text output makes them readable.

Findings have a severity: `ok`, `info`, `warn`, `fail`. The thresholds below are the defaults;
they can be changed in [`terminus.toml`](configuration.md#thresholds) and checks can be disabled.
`terminus checks list` shows the effective ones.

## system

Host name, kernel (uname), OS release (os-release), machine and boot id, uptime and boot time,
clock and time zone, hardware (DMI: vendor, product, BIOS; serial numbers need root) and
virtualization (`systemd-detect-virt`, or container/VM markers when it is missing).

No checks.

## cpu

Model, vendor, logical CPUs, cores, sockets, online CPUs, flags, load average and CPU pressure
(PSI, `/proc/pressure/cpu`).

| Check | Rule |
|---|---|
| `cpu.load` | 15 min load per logical CPU: warn ≥ 1, fail ≥ 2 |
| `cpu.pressure` | share of time tasks waited for a CPU (5 min): warn ≥ 20%, fail ≥ 50% |

## memory

RAM and swap from `/proc/meminfo` (the whole table in `meminfo`), processes killed by the OOM
killer since boot (`/proc/vmstat`, cgroup limits included) and memory pressure (PSI).

| Check | Rule |
|---|---|
| `mem.available` | MemAvailable / MemTotal: warn < 10%, fail < 5% |
| `mem.swap-used` | used swap: warn ≥ 50%, fail ≥ 80% |
| `mem.oom-kills` | warn when the OOM killer killed something since boot |
| `mem.pressure` | share of time all tasks stalled on memory (5 min): warn ≥ 5%, fail ≥ 20% |

## storage

Mounted filesystems (`/proc/self/mountinfo`) with usage and inodes (statfs, 3 s timeout each so
that a dead network share cannot hang the run), the `/etc/fstab` options of each mount point,
block devices (size, model, rotational, partitions, I/O counters) and swap areas.

Kernel filesystems, container image layers and container storage mounts are left out.

| Check | Rule |
|---|---|
| `disk.usage` | used space (as df): warn ≥ 85%, fail ≥ 95% |
| `disk.inodes` | used inodes: warn ≥ 85%, fail ≥ 95% |
| `disk.readonly` | fail when a filesystem is read-only but `/etc/fstab` mounts it read-write (the kernel remounts on errors) |
| `disk.unreachable` | fail when statfs fails or does not answer |

Read-only filesystems are not checked for usage; bind mounts are checked once; tmpfs is
reported only when it fills up.

## network

Interfaces (addresses, MTU, state, speed, counters), default routes (IPv4 and IPv6), DNS
configuration (with the upstream servers of systemd-resolved) and listening sockets (TCP in
LISTEN, bound UDP) with the owning process. Processes of other users are visible only as root.

| Check | Rule |
|---|---|
| `net.default-route` | warn without a default route |
| `net.dns` | fail without DNS servers |
| `net.public-listeners` | info: TCP ports listening on all addresses; with `public_ports = [22, 80, 443]` the other ports are warnings |

## systemd

Units of the system manager and of the user managers (`systemctl --user`): failed units, and for
the services matching `units` (all the running or failed ones by default) state, automatic
restarts, main process exit, memory current/peak/limit (`MemoryMax`, `MemoryHigh`), tasks, CPU
time, cgroup, unit file (`SourcePath` is the quadlet file for generated units). Missing values are
read from the cgroup v2 files (`memory.peak` before systemd 253); `memory.events` gives the OOM
kills of the current cgroup.

The journal adds the history of each unit over the `history` window, with one indexed query on the
systemd messages: OOM kills, failures by result, automatic restarts, main process exits by code
(137 = SIGKILL, often the OOM killer; 143 = SIGTERM, a stop or a deploy).

```toml
[modules.systemd]
users = "auto"          # "auto", [] or ["apps", "1001"]
units = ["*.service"]
history = "7d"
```

**Users.** No user is built in. With `users = "auto"` (the default) terminus inspects the users
that have linger enabled (`/var/lib/systemd/linger/<name>`) or a running user manager
(`/run/user/<uid>/systemd`); `users = []` inspects only the system manager; a list selects exactly
those users, by name or UID (users from LDAP/SSSD must be given by UID: the static binary reads
only `/etc/passwd`). `--users apps,web` overrides the setting for one run. An unknown user is a
configuration error.

As root terminus runs `systemctl --user` as each user, with their session environment
(`XDG_RUNTIME_DIR`, `DBUS_SESSION_BUS_ADDRESS`): no `sudo -u` needed. As another user only the
system manager and the user's own manager are inspected; the others are reported as skipped.
The module is skipped when systemd is not the init system (containers).

| Check | Rule |
|---|---|
| `systemd.failed` | fail for each failed unit (`system:backup.service`, `user:apps:myapp.service`) |
| `unit.memory-peak` | memory peak over MemoryMax: warn ≥ 0.9, fail ≥ 1.0 |
| `unit.memory-limit` | info: running user service without MemoryMax |
| `unit.oom-kills` | fail when the OOM killer hit the unit in the history window |
| `unit.restarts` | automatic restarts since the unit started: warn ≥ 3, fail ≥ 10, with the exit reasons |
| `user.linger` | fail for a user with enabled services or quadlets but without linger |
| `resources.overcommit` | sum of MemoryMax of running services over RAM: warn ≥ 1.0, fail ≥ 1.5 |
| `systemd.version` | info: facts not available (systemd < 253, cgroup v1, journal not readable, not root) |

## external

[Custom facts](custom-facts.md) from `/etc/terminus/facts.d` (`dir` in `[modules.external]`).
Skipped when the directory does not exist.

# Optional modules

## caddy

The configuration of Caddy, read from the admin API (`GET /config/`, also on a unix socket) or,
when the admin API is off, adapted from the Caddyfile with `caddy adapt` (locally or with
`podman exec` in the container where Caddy runs): servers, sites with their domains (also in
nested routes), handlers, reverse proxy upstreams, certificate issuers. Then each upstream is
dialed, the certificate Caddy serves for each domain is read connecting to Caddy itself with the
domain as SNI (no dependency on DNS), and each domain is resolved.

```toml
[modules.caddy]
enabled = true
admin = "http://localhost:2019"     # "unix//run/caddy/admin.sock", "" to skip
caddyfile = "/etc/caddy/Caddyfile"
container = ""                      # podman container running Caddy
user = ""                           # its rootless owner
tls_address = ""                    # default 127.0.0.1:443
public_ips = []                     # public IPs not on the interfaces (NAT)
```

| Check | Rule |
|---|---|
| `caddy.upstream` | fail: an upstream does not accept connections (the sites it serves are listed) |
| `caddy.tls-expiry` | days to expiry: warn < 14, fail < 7; short-lived certificates (internal CA, 6-day ACME) by share of lifetime: warn under 1/6, fail when expired |
| `caddy.dns` | warn: a domain does not resolve, or not to this machine (disable it behind a proxy/CDN) |

## postgres

PostgreSQL instances, through the Go driver (no psql needed on the host): version, settings in
effect (memory ones in bytes), sessions grouped by application, client address, state and
backend type, client connections over the usable ones, oldest transaction, longest idle in
transaction, database sizes. A role in `pg_monitor` sees every session:
`CREATE ROLE monitor LOGIN PASSWORD '...' IN ROLE pg_monitor;`.

```toml
[modules.postgres]
enabled = true
dsn = "postgres://monitor@127.0.0.1:5432/postgres?sslmode=disable"
password_file = "/etc/terminus/pg.pass"   # or PGPASSWORD, ~/.pgpass

[[modules.postgres.instances]]            # more instances
name = "app"
dsn = "postgres://monitor@127.0.0.1:5433/app"
```

| Check | Rule |
|---|---|
| `pg.reachable` | fail: cannot connect |
| `pg.connections` | client connections over max_connections minus the reserved ones: warn ≥ 0.8, fail ≥ 0.95; the top clients are in the evidence |
| `pg.idle-in-transaction` | longest session idle in a transaction: warn ≥ 300 s, fail ≥ 3600 s |

## podman

Containers of root (rootful) and of the selected users (rootless), read with the podman CLI run as
each user: state, exit code, OOM kill, health (status, failing streak, last output), restart policy
and count, published ports, networks, mounts, the systemd unit that runs the container
(`PODMAN_SYSTEMD_UNIT`), CPU/memory/PIDs (`podman stats`); volumes with the containers that mount
them, the owner of their directory on the host **and as the container sees it** (rootless user
namespace from `/etc/subuid`/`/etc/subgid`: the user is 0, the subordinate range starts at 1),
size and files (bounded in time); networks; `podman system df`.

```toml
[modules.podman]
enabled = true
users = "auto"          # as in [modules.systemd]; --users overrides it for all modules
rootful = true
volume_sizes = true
```

| Check | Rule |
|---|---|
| `podman.unhealthy` | fail: healthcheck failing |
| `podman.no-healthcheck` | info: running container without healthcheck |
| `podman.exited` | fail: container of a systemd unit not running; warn: other container exited with an error |
| `podman.oom-killed` | fail: last run killed by the OOM killer |
| `podman.restarts` | restarts done by podman: warn ≥ 3, fail ≥ 10 |
| `podman.volume-unused` | info: volume that no container mounts |
| `podman.storage` | info: reclaimable space |
| `podman.scope` | root or a user not inspected, or partial data |

## quadlet

The quadlet files of root (`/etc/containers/systemd`, `/usr/share/containers/systemd`) and of the
users (`~/.config/containers/systemd`, `/etc/containers/systemd/users[/<uid>]`): what they declare,
what **the quadlet generator installed on the machine** produces (`quadlet -dryrun [-user]` on
exactly those directories, run as the user), the state of the generated units, and the volumes
podman really has.

```toml
[modules.quadlet]
enabled = true
users = "auto"
rootful = true
binary = ""             # default /usr/libexec/podman/quadlet or /usr/lib/podman/quadlet
```

| Check | Rule |
|---|---|
| `quadlet.dryrun` | fail: the generator rejects a file (its unit does not exist) or fails |
| `quadlet.volume-not-used` | fail: `Volume=data:/x` while `data.volume` exists: podman mounts a volume named `data`, the unit (and its `VolumeName=`) is not used; confirmed by the dry run and by the volumes that exist |
| `quadlet.network-not-used` | fail: same for `Network=` and `.network` units |
| `quadlet.unit-not-loaded` | fail: the generated unit is not loaded (daemon-reload missing) |
| `quadlet.unit-never-active` | warn: a `.volume`/`.network` unit inactive (dead): no container requires it |
| `quadlet.changed-since-start` | warn: file changed after its service started: the change is not in effect |
| `quadlet.volume-owner` | warn: the real owner of the volume (as the container sees it) differs from `User=`/`Group=` |
| `quadlet.scope` | root or a user not inspected, or partial data |

## journal

Disk space of the journal files over the size of their filesystem, and log lines per unit in the
`window` (one pass on the journal, read as a stream: it is not a core module because on busy
machines it takes a while).

```toml
[modules.journal]
enabled = true
window = "24h"
```

| Check | Rule |
|---|---|
| `journal.disk-usage` | journal files over their filesystem: warn ≥ 0.1, fail ≥ 0.2 |
| `journal.noisy-unit` | lines per day of a unit: warn ≥ 50000, fail ≥ 500000 |

## http

Probes the endpoints listed in the configuration: status code, time to the response headers and
the TLS certificate served. Redirects are reported, not followed.

```toml
[modules.http]
enabled = true
timeout = "10s"

[[modules.http.endpoints]]
url = "https://example.org/"
status = 200   # optional: expected status, default any 2xx or 3xx
```

| Check | Rule |
|---|---|
| `http.status` | fail on errors, unexpected status, or 4xx/5xx without an expected status |
| `http.latency` | seconds to the response headers: warn ≥ 2, fail ≥ 5 |
| `http.tls-expiry` | days before the certificate expires: warn < 14, fail < 7 |

## tls

TLS certificates stored on the machine and served by endpoints. Files: the directories in
`paths` are searched recursively for `.pem`, `.crt` and `.cer` files (key files are not read);
each certificate is reported once with the files that hold it (`cert.pem` and `fullchain.pem`),
CA certificates are only used to verify the chains. Endpoints: a TLS connection to `address`
with `server_name` as SNI, reporting the chain sent, the protocol version and whether the
certificate covers the name. Chains are verified against the system roots and `ca_files`, at a
time when the certificate is valid (expiry is a separate check).

```toml
[modules.tls]
enabled = true
paths = ["/etc/letsencrypt/live"]   # default: certbot and the Caddy storage of caddy and root
ca_files = []                       # roots of private CAs
timeout = "5s"

[[modules.tls.endpoints]]
address = "mail.example.org:993"    # default port 443
server_name = ""                    # default: the host of address
```

Only implicit TLS is supported on endpoints (443, 465, 993, 995, ...), not STARTTLS.

| Check | Rule |
|---|---|
| `tls.expiry` | days to expiry: warn < 14, fail < 7; short-lived certificates by share of lifetime, as in `caddy.tls-expiry` |
| `tls.chain` | chain not verified: fail for endpoints (clients reject it: incomplete chain, self-signed), info for files (private CA, or intermediates stored elsewhere) |
| `tls.hostname` | fail: the endpoint certificate does not cover `server_name`; warn: certificate without subject alternative names |
| `tls.endpoint` | fail: no TLS connection |

## backup

The latest backup of each configured repository: a snapshot with
`restic snapshots --json --latest 1`, an archive with `borg list --json --last 1 --bypass-lock`,
a backup with `pgbackrest info --output=json`; its age, size when the tool reports it, and the
state of the systemd unit that makes the backups. Nothing is written to the repositories (no
locks either). Repositories are read in parallel; remote ones may need a longer `--timeout`.

```toml
[modules.backup]
enabled = true

[[modules.backup.repositories]]
name = "home"
type = "restic"                     # restic, borg, pgbackrest
repository = "sftp:backup@nas:/srv/restic"   # pgbackrest: the stanza
password_file = "/etc/restic/password"       # restic and borg
env_file = "/etc/restic/env"        # KEY=value lines for the tool: S3/B2 credentials, BORG_RSH, ...
host = ""                           # restic: only the snapshots of this host (shared repositories)
user = ""                           # run the tool as this user (root only; pgbackrest: postgres)
unit = "restic-backup.service"      # systemd unit that makes the backups
max_age = "26h"                     # warn after max_age, fail after twice; default backup.age
```

Borg never prompts: a repository moved or unencrypted and never seen before is an error.

| Check | Rule |
|---|---|
| `backup.age` | hours since the latest backup: warn ≥ 26, fail ≥ 50 (or `max_age` and twice it); warn at least when pgBackRest marks it as ended with an error |
| `backup.repository` | fail: the repository cannot be read (the last line of the tool error is reported), or it holds no backup |
| `backup.job` | fail: the last run of `unit` failed |

## updates

Pending package updates with the package manager of the machine, computed from the package
index it already has (nothing is downloaded, no lock is taken):
`apt-get -s dist-upgrade` (security updates come from a `-security` suite), `dnf`/`yum`
`--cacheonly check-update` and `updateinfo list --security`, `apk version -l '<'` (no security
information). Then the age of the package index (the counts are only as recent as the last
`apt update`/`dnf makecache`), and whether a reboot is pending: `/run/reboot-required` (with the
packages that asked for it), `needs-restarting -r` when installed, a kernel newer than the
running one in `/lib/modules`, or the modules of the running kernel removed.

No settings: `enabled = true` in `[modules.updates]`.

| Check | Rule |
|---|---|
| `updates.security` | pending security updates: warn ≥ 1, fail ≥ 20 |
| `updates.pending` | info: pending updates, with their names |
| `updates.reboot` | warn: a reboot is pending, with the reasons |
| `updates.index-age` | days since the package index was refreshed: warn ≥ 7, fail ≥ 30 |
