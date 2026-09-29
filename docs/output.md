# Output formats

Every command that prints a report takes `-o`/`--output` and `--output-file`:

| Format | For | Content |
|---|---|---|
| `text` | people at a terminal (default of `facts` and `check`) | summary, findings (`-v`: evidence and hints, `--problems`: only warn and fail), modules, facts (`facts`: lists of records as tables of their main fields fitted to the terminal, `-v` every field); colors only on a terminal (`--color`, `NO_COLOR`) |
| `json` | programs, `jq`, `terminus diff` | the complete report with `schema_version`, raw values (bytes, seconds, milliseconds), module statuses and errors |
| `jsonl` | log shippers (Loki, Vector, Fluent Bit) | one line per finding, with host and time |
| `markdown` | tickets, pull requests, wikis (default of `report`) | summary, findings and modules as tables, facts in collapsible JSON blocks |
| `html` | archives, attachments | a single self-contained page (no script, no external resource), light and dark theme |
| `prometheus` | alerting | text exposition format for the node_exporter textfile collector |

On a terminal, output longer than the screen goes through a pager: `$TERMINUS_PAGER`, `$PAGER` or
`less` (with `LESS=FRX` unless `LESS` is set); an empty value or `cat` turns it off, as does
`--no-pager`.

`--output-file path` writes to a temporary file and renames it at the end: readers never see a
half-written file.

## Reports and comparisons

```shell
terminus report > srv-01.md                      # checks and facts in one document
terminus report -o html --output-file /tmp/srv-01.html

terminus report -o json --output-file before.json
# deploy, configuration change, incident ...
terminus report -o json --output-file after.json
terminus diff before.json after.json --facts
```

`terminus diff` lists the new, resolved and changed findings and, with `--facts`, the facts that
changed. Values that change at every run (time, uptime, counters, current usage) are left out;
`--ignore '*.mtu'` leaves out more, `--no-default-ignore` shows everything. The exit code is 0
when nothing got worse, 1 for new warnings, 2 for new failures: usable after a deploy.

## Prometheus

```shell
# /etc/cron.d/terminus: every 5 minutes
*/5 * * * * root terminus check -o prometheus --output-file /var/lib/node_exporter/textfile/terminus.prom
```

Metrics:

| Metric | Labels | Value |
|---|---|---|
| `terminus_finding_severity` | `id`, `module`, `subject` | 0 ok, 1 info, 2 warn, 3 fail |
| `terminus_findings` | `severity` | number of findings |
| `terminus_module_up` | `module`, `status` | 1 when the module collected its facts |
| `terminus_module_duration_seconds` | `module` | collection time |
| `terminus_exit_code` | | 0 ok, 1 warn, 2 fail |
| `terminus_last_run_timestamp_seconds` | | time of the run |
| `terminus_run_duration_seconds` | | duration of the run |

Example alerts:

```yaml
- alert: TerminusFailure
  expr: terminus_finding_severity >= 3
  labels: {severity: critical}
  annotations: {summary: "{{ $labels.id }} on {{ $labels.subject }}"}
- alert: TerminusStale
  expr: time() - terminus_last_run_timestamp_seconds > 900
```
