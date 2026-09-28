# terminus report: srv-01

2026-09-28T10:00:00Z · terminus 0.2.0-test · 123ms

**1 fail · 1 warn · 1 info · 1 ok**

## Findings

| Severity | Check | Subject | Message | Hint |
|---|---|---|---|---|
| ✖ fail | `unit.failed` | backup.service | unit is failed |  |
| ⚠ warn | `mem.available-low` | memory | only 7.3% of memory available | check memory usage |
| ℹ info | `ext.escape` | a\|b &lt;x&gt; "q" \ z | line1 line2 &lt;b&gt;&amp;&lt;/b&gt; |  |
| ✔ ok | `disk.usage` | / | 42% used |  |

## Modules

| Module | Status | Duration | Notes |
|---|---|---|---|
| external | skipped | 0ms | directory /etc/terminus/facts.d does not exist |
| podman | partial | 1.5s | volume inspect: permission denied |
| system | ok | 12ms |  |

## Facts

<details>
<summary>podman</summary>

```json
{}
```

</details>

<details>
<summary>system</summary>

```json
{
  "disks": [
    {
      "device": "sda"
    }
  ],
  "flags": [
    "fpu",
    "sse"
  ],
  "hostname": "srv-01",
  "long": [
    1,
    2,
    3,
    4,
    5,
    6,
    7,
    8,
    9,
    10,
    11,
    12,
    13
  ],
  "memory": {
    "available_ratio": 0.0732,
    "total_bytes": 4294967296
  },
  "uptime_seconds": 187200.5
}
```

</details>

