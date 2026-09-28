# Configuration

terminus reads `/etc/terminus/terminus.toml` when it exists; `--config path` reads another file
(and then the file must exist). Without a file every default applies.

`terminus config example` prints a file with every module and threshold commented out at its
default; `terminus config validate` checks a file.

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
disable = ["net.public-listeners", "backup.*"]

# Thresholds: both warn and fail.
[checks.thresholds."disk.usage"]
warn = 0.80
fail = 0.90
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

Flags win over the file: `--timeout`, `--modules-dir`, `--external-facts-dir`.

# External modules

An external module is an executable in the modules directory (`/etc/terminus/modules.d`). It is
named after the file without extension (`backup.sh` is the `backup` module; lowercase letters,
digits, `-` and `_`) and runs by default: `enabled = false` in `[modules.backup]` disables it.

It must print on standard output, within the module timeout and exiting with code 0:

```json
{
  "facts": {"last_snapshot": "2026-09-26T03:00:00Z"},
  "findings": [
    {
      "id": "backup.age",
      "severity": "warn",
      "subject": "restic",
      "message": "last snapshot 54h ago",
      "hint": "check the restic timer",
      "evidence": {"age_hours": 54}
    }
  ]
}
```

- `facts` is any JSON value; it appears under `.modules.backup.facts`.
- Each finding needs `id` (prefixed with the module name), `severity` (`ok`, `info`, `warn`,
  `fail`) and `message`; `subject`, `hint` and `evidence` are optional.
- Unknown fields, invalid severities and a non-zero exit code are reported as module errors.

Findings of external modules can be disabled like the others (`disable = ["backup.*"]`); their
thresholds live in the module itself.

[contrib/modules/reboot.sh](../contrib/modules/reboot.sh) is a complete example.
