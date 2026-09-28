# Configuration

terminus merges up to three layers, from weakest to strongest:

1. system: `/etc/terminus/terminus.toml`
2. user: `$XDG_CONFIG_HOME/terminus/terminus.toml` (`~/.config/terminus/terminus.toml` by default)
3. per-run: `./terminus.toml`, in the current directory

A layer that doesn't exist is skipped, and the defaults apply when none exist. Tables merge key
by key, so a lower layer's `[modules.http]` still applies to keys a higher layer doesn't repeat;
anything else, including arrays like `checks.disable`, is replaced wholesale by the highest layer
that sets it (not concatenated). `[checks.exclude]` is a table of arrays, so it merges per check
ID like any other table: a higher layer adding patterns for a different check ID does not drop a
lower layer's patterns for another one, but it replaces the whole array for the same ID.

`--config path` bypasses all three layers and reads exactly that file instead (and then the file
must exist).

`terminus config example` prints a file with every module and threshold commented out at its
default; `terminus config validate` checks the effective (merged) configuration, or a single file
with `--config`. `terminus config show` lists the files considered (found or not) and prints the
merged configuration, syntax highlighted on a terminal; it does not validate it, and its output
is itself a valid `terminus.toml`.

```toml
# Maximum time for each module.
timeout = "30s"

# Directory of the external modules.
modules_dir = "/etc/terminus/modules.d"

# One section per module: "enabled" plus the module's own settings.
[modules.http]
enabled = true

[[modules.http.endpoints]]
url = "https://example.org/"

[modules.network]
enabled = false

[checks]
# Checks whose findings are dropped: IDs or prefixes.
disable = ["net.public-listeners", "rsync.*"]

# Thresholds: both warn and fail.
[checks.thresholds."disk.usage"]
warn = 0.80
fail = 0.90

# Findings dropped by subject: a check ID (or prefix) mapped to glob patterns matched against
# the finding's Subject. Unlike "disable", the check still runs for every other subject.
[checks.exclude]
"unit.memory-limit" = ["*:app-*.service"]
```

## Validation

The file is decoded strictly: unknown keys, unknown modules, checks that do not exist, thresholds
on checks that have none, and thresholds in the wrong order are errors. terminus stops with exit
code 3 and lists every problem, so that a typo cannot silently disable a module or a check.

## Which modules run

From the strongest to the weakest:

1. `--only a,b` runs exactly those modules;
2. `--no-modules a` and `--modules b` disable or enable modules for one run;
3. `enabled = true|false` in `[modules.<name>]`;
4. the default: core modules and external modules run, optional modules do not.

`terminus modules list` shows every module, whether it runs and why.
`terminus modules detect` tells which optional modules fit the machine and prints the sections
to add; it never changes the configuration.

## Thresholds

`terminus checks list` shows every check with its effective threshold. A threshold keeps the
direction of the check: for `mem.available` and `http.tls-expiry` lower values are worse, so
`warn` must be above `fail`.

| Unit | Meaning |
|---|---|
| ratio | between 0 and 1 (0.85 = 85%) |
| percent | between 0 and 100 (pressure stall information) |
| seconds, days | durations |
| load per cpu | load average divided by the logical CPUs |

## Command line

Flags win over the file: `--timeout`, `--modules-dir`, `--external-facts-dir`, `--users`.

# External modules

An external module is an executable in the modules directory (`/etc/terminus/modules.d`). It is
named after the file without extension (`rsync.sh` is the `rsync` module; lowercase letters,
digits, `-` and `_`) and runs by default: `enabled = false` in `[modules.rsync]` disables it.

The file (and the directory itself) must be owned by root or by the user running terminus, and not
writable by anyone else: terminus runs it as trusted code, so one it cannot vouch for is refused
and reported as an error instead of run.

It must print on standard output, within the module timeout and exiting with code 0:

```json
{
  "facts": {"last_sync": "2026-09-26T03:00:00Z"},
  "findings": [
    {
      "id": "rsync.age",
      "severity": "warn",
      "subject": "nas",
      "message": "last sync 54h ago",
      "hint": "check the rsync timer",
      "evidence": {"age_hours": 54}
    }
  ]
}
```

- `facts` is any JSON value; it appears under `.modules.rsync.facts`.
- Each finding needs `id` (prefixed with the module name), `severity` (`ok`, `info`, `warn`,
  `fail`) and `message`; `subject`, `hint` and `evidence` are optional.
- Unknown fields, invalid severities and a non-zero exit code are reported as module errors.

Findings of external modules can be disabled like the others (`disable = ["rsync.*"]`); their
thresholds live in the module itself.

[contrib/modules/reboot.sh](../contrib/modules/reboot.sh) is a complete example.
