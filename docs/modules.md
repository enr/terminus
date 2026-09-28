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
| `net.public-listeners` | info: TCP ports listening on all addresses |

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
