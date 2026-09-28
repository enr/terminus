# Probing the monitoring

A health check or an external monitor (easeprobe, uptime-kuma, blackbox_exporter) that was never
seen failing may watch the wrong URL, report to nowhere, or be answered by a cache.
`terminus probe` stops a service on purpose for a while to verify that the outage is noticed, then
starts it again.

It is the only terminus command that changes the machine: it asks for confirmation, and `--yes` is
required when it does not run on a terminal.

```shell
$ sudo terminus probe --unit myapp.service --user apps --container myapp \
    --http https://app.example.org/health \
    --check-cmd 'journalctl -u easeprobe --since -2min | grep -q "myapp.*down"' \
    --wait 60s
This stops the unit myapp.service of user apps for 1m0s, then starts it again. Continue? [y/N] y
probe: before: unit active, container running (healthy), http 200, check exit 1
probe: stopping the unit myapp.service of user apps
probe: stopped; waiting 1m0s
probe: while stopped: unit inactive, container absent, http 502, check exit 0
probe: starting the unit myapp.service of user apps
probe: recovered in 4s: unit active, container running (healthy), http 200, check exit 1

Findings
  ✔ ok    probe.detected-by-check  the monitoring noticed the outage within 1m0s [myapp.service]
  ✔ ok    probe.detected-by-http   stopped answering while the service was down, as expected
  ✔ ok    probe.recovered          back in 4s [myapp.service]
```

## What is stopped

- `--unit U`: `systemctl stop U`; with `--user apps`, a unit of the user manager of apps
  (`systemctl --user`, run as apps: terminus must run as root, or as apps itself).
- `--container C` without `--unit`: `podman stop C` (rootless with `--user`).
- `--container C` with `--unit`: the unit is stopped (a quadlet), the container is only watched.

## What is watched

| Flag | While the service is down | After the start |
|---|---|---|
| `--http URL` | must fail or answer 5xx | must answer below 500 |
| `--check-cmd CMD` | must exit 0: "the monitoring noticed" | — |
| `--unit` | — | must be `active` |
| `--container` | — | must be running and healthy (`podman healthcheck run` is used to avoid waiting for the interval) |

`--check-cmd` is run with `sh -c` after `--wait`: make it look where the monitoring reports, e.g.
the easeprobe log, the alertmanager API (`curl -s .../api/v2/alerts | grep -q myapp`), a
uptime-kuma status page. Set `--wait` longer than the probe interval of the monitor.

The service must be back within `--recover` (60s).

## Findings

| ID | Severity |
|---|---|
| `probe.detected-by-http` | fail when the URL still answered while the service was down (another instance or a cache answers for it); warn when it was already failing before |
| `probe.detected-by-check` | fail when the check command did not exit 0; info when neither `--http` nor `--check-cmd` is given |
| `probe.recovered` | fail when the service is not back after `--recover`: the hint says how to start it by hand |
| `probe.stopped` | fail when the stop failed (the service is started anyway) |
| `probe.interrupted` | warn when the probe was interrupted during the wait: the detection was not verified |

Exit code as for `terminus check`: 0 ok, 1 warn, 2 fail, 3 error. `-o json|markdown|html|...`
and `--output-file` work as for `check`; `--facts` adds the observations and the steps; `-q` hides
the progress lines on stderr.

## Safety

The service is started again in every case: when a check fails, when the stop itself fails (it
may be half stopped), and on Ctrl-C or SIGTERM, which cut the wait short. The start runs on its own
deadline (`--recover` plus 30 s) that further signals do not cancel. Only when terminus is killed
(`SIGKILL`) or the machine goes down the service stays stopped; in that case the progress lines
tell what was stopped.
