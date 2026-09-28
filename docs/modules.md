# Modules

Facts and checks are grouped in modules. The core modules below run by default; `--only`
selects some of them (`terminus check --only memory,storage`).

In JSON the facts of a module are under `.modules.<module>.facts`. Path queries start from the
module name (`terminus memory.available_bytes`); in lists a name selects the element
(`network.interfaces.eth0`, `storage.filesystems./srv`). Sizes are in bytes (`_bytes`), ratios
between 0 and 1 (`_ratio`), durations in seconds (`_seconds`); the text output makes them readable.

Findings have a severity: `ok`, `info`, `warn`, `fail`. Thresholds are fixed for now; they will
become configurable in `terminus.toml` (see [design-v2.md](design-v2.md)).

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

## external

[Custom facts](custom-facts.md) from `/etc/terminus/facts.d`. Skipped when the directory does
not exist.
