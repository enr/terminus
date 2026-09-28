# terminus v2 — stato completo di una macchina Linux (fatti + controlli)

## Context

Oggi `terminus` (Go, 2015, go 1.13 + vendor/) raccoglie fatti base in parallelo (uname, os-release,
memoria, interfacce, mtab, DMI, block device, cpuinfo), supporta fatti esterni (`/etc/terminus/facts.d`,
eseguibili o `.json`), query per path (`System.Network...`), template Go e una API HTTP.

Serve un tool che dia una **fotografia precisa e completa** di un server, inclusi domini specifici
(systemd, podman, quadlet, journald, cgroup, rete, postgres) come nella procedura manuale
`srv-01-verifica-stato.md` — e che **valuti** quei dati (findings pass/warn/fail con spiegazione),
non solo li elenchi. Decisioni prese: fatti + controlli, binario locale + wrapper SSH, riscrittura
in questo repo.

## Linguaggio: Go (raccomandato)

| | Go | Rust | TS via Bun |
|---|---|---|---|
| Binario statico reale | `CGO_ENABLED=0`, ~10-15 MB | musl, ~3-6 MB | `bun build --compile` embedda il runtime: ~60-90 MB, glibc-dipendente |
| Cross-compile amd64/arm64 | banale | buono (cross/zig) | limitato |
| Librerie per il dominio | `godbus/dbus` (systemd via D-Bus, pure Go), `prometheus/procfs`, `vishvananda/netlink`, `jackc/pgx`, `x/crypto/ssh` | `zbus`, `procfs`, `sqlx` — ok ma più lavoro | quasi tutto via shell-out |
| Ecosistema container/systemd | podman, quadlet, systemd tooling sono scritti in Go | — | — |
| Codice esistente riusabile | sì (collector di `lib/facts`) | no | no |

Scelta: **Go**. Rust solo se la dimensione del binario fosse un vincolo forte; Bun non rispetta davvero
"nessun runtime" (lo impacchetta) ed è debole su syscall/procfs/netlink.

Vincolo di progetto conseguente: **zero cgo**. Quindi niente `go-systemd/sdjournal` (usa libsystemd)
e niente binding ufficiali podman (cgo + gpgme/btrfs): journald via `journalctl -o json`, podman via
API REST sul socket unix (`$XDG_RUNTIME_DIR/podman/podman.sock`) con fallback su
`podman ... --format json`.

## Architettura

```
cmd/terminus/            CLI (cobra o flag std), sottocomandi
internal/model/          Fact tree, Finding{ID,Severity,Subject,Message,Evidence,Hint}
internal/exec/           Runner comandi con timeout/ctx, esecuzione "come utente" (vedi sotto)
internal/modules/<nome>/ un package per dominio (core e opzionali), interfaccia comune:
    type Collector interface {
        Name() string                     // "system", "systemd", "podman", ...
        Available(ctx, Env) bool          // es. podman presente? bus utente raggiungibile?
        Collect(ctx, Env) (any, []error)  // errori parziali non bloccanti
    }
internal/checks/         regole: func(Facts) []Finding, registrate per dominio, soglie configurabili
internal/output/         renderer: text (rich/tty), json, jsonl, markdown, html, prometheus, template
internal/remote/         wrapper ssh: copia binario (arch-detect), esegue, raccoglie JSON
```

Principi:
- **Parallelismo con `errgroup` + context/timeout** per collector (riusa l'idea di `getSystemFacts`,
  `lib/facts/facts_all.go:206`, ma con errori raccolti nell'output invece di `log.Println` persi).
- **Errori nell'output**: ogni dominio ha `_errors`/`_skipped` → si distingue "zero container" da
  "podman non raggiungibile".
- **Scope utente**: `--user apps` (o auto-discovery degli utenti con linger + container) — da root il
  tool usa il bus `/run/user/<uid>/bus` e il socket podman dell'utente, oppure esegue i comandi con
  `setpriv/runuser` + `XDG_RUNTIME_DIR` (sezione 0 del doc).
- **Solo lettura di default**. I controlli attivi (sez. 11: stop del servizio per verificare la sonda)
  solo dietro sottocomando esplicito `probe --disruptive` con conferma.
- Fatti esterni (`facts.d`) mantenuti per compatibilità (`lib/facts/facts.go:34`), più "check esterni"
  che restituiscono findings in JSON.

## Moduli: core vs opzionali

Tutto è organizzato in **moduli** (collector + check dello stesso dominio). Due livelli:

- **core** — sempre attivo, utile a chiunque: system, cpu/mem, storage, network, users/sessions,
  time/packages, servizi systemd *di sistema* (solo unit failed/stato generale).
- **opzionali** — compilati nel binario ma **disabilitati di default**; si abilitano per host.

Abilitazione:
```toml
# /etc/terminus/terminus.toml
[modules.podman]
enabled = true
users   = ["apps"]

[modules.quadlet]
enabled = true
users   = ["apps"]

[modules.caddy]
enabled = true
admin   = "http://localhost:2019"

[modules.postgres]
enabled = true
dsn     = "postgres://monitor@127.0.0.1:5432/postgres"

[[modules.http.endpoints]]
url    = "https://example.org/"
expect = 200

[checks]
disable = ["net.public-bind"]

[checks.thresholds]
"unit.memory-peak-near-max" = 0.9
"tls.expiry-warn"           = "14d"
```

- CLI: `--modules podman,caddy` / `--no-modules quadlet` sovrascrivono la config.
- `terminus modules detect`: rileva cosa c'è sulla macchina (binario podman, socket caddy admin, porta
  5432, ...) e **propone** un terminus.toml — non abilita nulla da solo.
- `terminus modules list`: moduli disponibili, stato (enabled/disabled/unavailable) e perché.
- Interfaccia Go unica (`Module{Name, Detect, Collect, Checks, ConfigSchema}`) registrata in un
  registry: aggiungere un modulo = un package in `internal/modules/<nome>`.
- Estensione senza ricompilare: moduli esterni in `modules.d/` (eseguibili che stampano
  `{"facts":…, "findings":[…]}`), evoluzione dei fatti esterni attuali.
- Opzionale: build tag per produrre binari "slim" senza certi moduli (es. `-tags nopostgres`).

**Formato config: TOML** (non YAML). Motivi per questo caso d'uso:
- file scritto a mano su un server, con vi, da chi è in mezzo a un incidente: in TOML non ci sono
  errori di indentazione che cambiano il significato; YAML ha tipizzazione implicita traditrice
  (`no`/`off` → bool in YAML 1.1, `0644` ottale, `1.10` → float, orari `12:30` sessagesimali);
- struttura del config piatta (2-3 livelli: modulo → chiave), che è esattamente il punto forte di
  TOML; la debolezza di TOML (strutture profonde) qui non si presenta;
- coerenza con l'ecosistema del dominio: `containers.conf`, `storage.conf`, `registries.conf` di
  podman sono TOML; systemd/quadlet sono INI, a cui TOML somiglia;
- librerie Go pure, mature e stabili (`BurntSushi/toml`, `pelletier/go-toml/v2`); la storica
  `gopkg.in/yaml.v3` è stata archiviata, la manutenzione è passata a un fork (`go.yaml.in/yaml/v3`);
- decodifica stretta: chiavi sconosciute → errore (`MetaData.Undecoded()`), così un typo in un nome
  di modulo non lo disabilita in silenzio. Durate come stringhe (`"14d"`) parse-ate dal tool.

YAML avrebbe senso solo se il config dovesse essere generato/consumato soprattutto da tool YAML-centrici
(Ansible, k8s); anche lì Ansible scrive TOML via template senza problemi. Output del tool resta JSON
(macchina) / table / markdown — il formato config non influisce.

## Collector core (fatti)

Base (porting/riscrittura dell'esistente, correggendo bug noti: `PassNo` letto da `fields[4]`,
early-return in DMI/blockdev che svuota tutto al primo file mancante, `/etc/mtab`→`/proc/self/mountinfo`):
- **system**: host, kernel, os-release, uptime, boot id, machine id, DMI, virtualizzazione (systemd-detect-virt / cpuid)
- **cpu/mem**: nproc, modello, load, `/proc/meminfo` completo (MemAvailable!), swap, PSI (`/proc/pressure/*`)
- **storage**: mount (mountinfo), `statfs` per usage/inode, block device, LVM/raid se presenti
- **network**: interfacce/indirizzi (netlink), route, DNS (resolv.conf/resolved), **socket in ascolto**
  con processo e bind address (`/proc/net/tcp*` + mapping inode→pid) — sez. 10
- **users/sessions**: `loginctl` via D-Bus logind: utenti, **Linger** — sez. 0
- **packages/time**: versioni chiave (podman, systemd, kernel), NTP sync, timezone

## Moduli opzionali — prima tornata (servono a te)

- **systemd-units** (D-Bus, system + user bus per utente): unit failed, stato di ogni unit, e per le unit
  di interesse `MemoryCurrent/MemoryPeak/MemoryMax/TasksCurrent/TasksMax/CPUUsageNSec`, restart count
  (`NRestarts`), `ExecMainStatus`, `Result` — sez. 3. Fallback lettura diretta cgroup v2
  (`memory.peak`, `memory.max`, `memory.events` → contatori `oom`/`oom_kill`).
- **journald**: `--disk-usage`, per unit: righe ultime 24h, eventi OOM / exit status 137 vs 143
  negli ultimi N giorni (`journalctl -o json --since`) — sez. 4, 5. Anche kernel `oom-kill` da `dmesg`/journal.
- **podman** (per utente): container (nome, pid, stato, health, restart policy, immagine+digest,
  porte pubblicate), `stats` una tantum, volumi (nome, mountpoint, **owner uid/gid reale**, size,
  inode), reti, versione — sez. 2, 7, 11.
- **quadlet**: file sorgente in `~/.config/containers/systemd` e `/etc/containers/systemd`; path del
  binario quadlet (`/usr/lib/podman/quadlet` o `/usr/libexec/...`); output `-dryrun` parsato
  (unit generate, `Requires=`/`After=`, volumi/reti referenziate); correlazione con unit caricate — sez. 8, 9.
- **postgres** (opzionale, attivo se configurato DSN o porta rilevata): `pg_stat_activity`
  aggregato, `max_connections`, `shared_buffers`, versione — sez. 6. Via `pgx` (pure Go).
- **http endpoints** (URL da config): status code e latenza per URL pubblici — sez. 10.
- **caddy**: via admin API (`GET /config/` su `localhost:2019` o socket unix) → per ogni server:
  listen, **domini serviti** (matcher `host` delle route), upstream dei `reverse_proxy`, policy TLS;
  fallback se admin API disabilitata: `caddy adapt --config Caddyfile` → stesso JSON. Se caddy gira
  in container, lo trova tramite il modulo podman. Check: dominio che non risolve all'IP pubblico
  dell'host, **certificato in scadenza** (dial TLS su ogni dominio, `NotAfter`), upstream che punta a
  una porta dove nessuno ascolta (incrocio con socket in ascolto / porte pubblicate podman),
  risposta HTTP non 2xx/3xx.

## Moduli opzionali — catalogo suggerito (fasi successive)

| Modulo | Fatti | Check tipici |
|---|---|---|
| **tls-certs** | cert da file (`/etc/letsencrypt`, storage caddy) e da endpoint | scadenza < 14/7 gg, catena incompleta, SAN mancante |
| **nginx / traefik / haproxy** | vhost/router, upstream (`nginx -T`, API traefik, socket haproxy) | come caddy: domini, upstream morti |
| **docker** | come podman (API socket) | stessi check container |
| **mysql/mariadb, redis** | connessioni, max, memoria, replica | vicino ai limiti, replica ferma, redis senza `maxmemory` |
| **backup** (restic / borg / pgbackrest) | ultimo snapshot, dimensione, repo | backup più vecchio di N ore, ultimo job fallito |
| **timers/cron** | systemd timer: ultima/prossima esecuzione, esito | timer fallito, mai eseguito |
| **firewall** (nftables / firewalld / ufw) | regole attive, policy default | porta in ascolto pubblica non filtrata, firewall spento |
| **ssh-hardening** | `sshd -T` | root login / password auth attivi |
| **updates** (apt / dnf / apk) | aggiornamenti pendenti, security, reboot richiesto | patch di sicurezza pendenti da > N gg, kernel in uso ≠ installato |
| **security-agents** (fail2ban / crowdsec) | jail/ban attivi | servizio giù, jail sshd assente |
| **disk-health** (SMART, `smartctl -j`), **zfs/btrfs/mdraid** | stato pool/array, errori | degraded, scrub vecchio, errori SMART |
| **vpn** (wireguard / tailscale) | peer, ultimo handshake | peer silente da > N min |
| **monitoring** (easeprobe, node_exporter, uptime-kuma) | sonde configurate, ultimo esito | agent giù, servizio senza sonda — sez. 11 |
| **dns** | resolver, record dei domini serviti (da caddy/nginx) | A/AAAA non puntano a quest'host, IPv6 pubblicato ma non in ascolto |
| **mail** (postfix) | coda | coda > N messaggi |
| **k3s/kubernetes** | nodi, pod non Ready | pod in CrashLoop |

Priorità suggerita dopo i tuoi: tls-certs, backup, updates, timers, firewall (valgono per quasi ogni
server esposto su internet).

## Controlli (findings) — mappati sul doc srv-01

| ID | Regola | Sev |
|---|---|---|
| `host.size-mismatch` | RAM/CPU/disco diversi da quanto dichiarato in config (`expect:`) | warn |
| `user.linger-off` | utente che possiede container senza Linger | fail |
| `unit.memory-peak-near-max` | `MemoryPeak ≥ 90% MemoryMax` | warn/fail |
| `unit.no-memory-limit` | unit di container senza `MemoryMax` | warn |
| `unit.oom-killed` | `memory.events oom_kill>0` o exit 137 nel journal | fail |
| `unit.restart-loop` | `NRestarts` alto, distinguendo 143 (deploy) da 137 (OOM) | warn |
| `unit.failed` | unit in stato failed | fail |
| `resources.overcommit` | somma `MemoryMax` > RAM fisica | warn |
| `journal.noisy-unit` | righe/giorno sopra soglia; disk-usage journal alto | warn |
| `quadlet.volume-not-used` | esistono sia `VolumeName=` sia `systemd-<nome>` → unit .volume ignorata | fail |
| `quadlet.unit-never-active` | unit -volume/-network `inactive (dead)` | warn |
| `volume.ownership-mismatch` | uid/gid reale ≠ `UID=`/`User=` dichiarato | warn |
| `net.public-bind` | porta di servizio interno su `0.0.0.0`/`::` | warn |
| `container.unhealthy` / `no-healthcheck` | health status / healthcheck assente | fail/warn |
| `pg.connections-near-max` | connessioni ≥ 80% `max_connections` | warn |
| `disk.usage` / `disk.inodes` | soglie su filesystem | warn/fail |
| `version.feature-gap` | podman < 4.9 / systemd < 253 → MemoryPeak non disponibile, avvisa | info |

Soglie e "attese" in un file di config opzionale (`/etc/terminus/terminus.toml` o `--config`):
unit/porte attese, DSN postgres, URL da provare, soglie, check disabilitati.

## Formati di output

Principio: **un solo modello dati** (`Report{Meta, Modules{Facts, Errors, Skipped}, Findings}`),
più renderer indipendenti. I collector producono solo valori grezzi (byte, secondi, ns, timestamp
RFC3339); la formattazione "umana" (3.8 GiB, "2g 4h", colori) sta solo nei renderer.

| `-o` / `--output` | Per chi | Contenuto |
|---|---|---|
| `text` (default) | umano al terminale | sezioni per modulo, riepilogo in testa (`2 fail · 5 warn · 41 ok`), tabelle allineate, simboli ✔ ⚠ ✖, unità leggibili; `-v` mostra evidence + hint per ogni finding, `--problems` solo warn/fail |
| `json` | macchine, jq, API, `diff`, `remote` | schema stabile e versionato (`schema_version`), valori grezzi, errori per modulo; è il formato "sorgente" di `diff`/`remote` |
| `jsonl` | ingestion log (Loki, Vector, ...) | un finding per riga, con host e timestamp |
| `markdown` | ticket, wiki, doc tipo srv-01 | report completo con tabelle, incollabile in PR/issue |
| `html` | condivisione/archivio | file singolo self-contained (CSS inline), stesso contenuto del markdown |
| `prometheus` | monitoring | formato textfile collector di node_exporter: findings e metriche chiave come gauge → allarmi senza agent aggiuntivo |
| `template` | casi custom | `--format '{{ .System.Hostname }}'` / `--format-file`, come oggi |
| path query | scripting | `terminus facts System.Memory.Total` stampa il valore nudo, come oggi |

Comportamento "rich text":
- colori/simboli solo se stdout è un TTY; `--color auto|always|never`, rispetta `NO_COLOR`;
  se non TTY il `text` degrada a testo semplice (niente ANSI nei file/log);
- larghezza tabelle adattata al terminale, niente TUI interattiva (serve funzionare anche via ssh
  semplice e in cron);
- libreria: `charmbracelet/lipgloss` (+ `lipgloss/table`), pure Go.

Exit code (tutti i formati): `0` ok, `1` warn, `2` fail, `3` errore del tool → usabile in cron,
CI, script di deploy (`terminus check -o text || rollback`).

## CLI

```
terminus facts [path]            # albero fatti, path query compatibile con l'attuale (System.Memory.Total)
terminus facts --only podman,systemd --user apps
terminus check                   # esegue collector necessari + regole; exit code 0/1/2 (ok/warn/fail)
terminus check -o text|json|jsonl|markdown|html|prometheus
terminus report                  # markdown/HTML "fotografia" completa (prima/dopo deploy, incidente)
terminus diff a.json b.json      # confronto tra due snapshot (prima/dopo)
terminus remote user@host check  # scp binario per arch → run → output locale; più host in parallelo
terminus serve --http :6060      # API esistente, mantenuta
terminus probe --disruptive unit # sez. 11, esplicito
```

## Fasi di implementazione

1. ✅ **Fondamenta**: go.mod a Go 1.23+, rimozione `vendor/`, layout `internal/`, modello Fact/Finding,
   runner comandi, renderer `text` e `json` (gli altri in fase 6), build statica (`CGO_ENABLED=0`, ldflags come `.sdlc/build`),
   goreleaser per amd64/arm64.
2. ✅ **Porting collector base** da `lib/facts/facts_all.go` e `facts_linux.go` con i bugfix; drop
   darwin/windows (o stub) — focus Linux.
3. ✅ **Sistema moduli**: registry, terminus.toml, `modules list/detect`, flag enable/disable, moduli esterni.
4. ✅ **systemd-units + cgroup + journald** e relativi check (valore più alto: sez. 3-5).
5. ✅ **podman + quadlet + volumi** e check (sez. 2, 7-9).
6. **network sockets, caddy (+tls), postgres, http**, `report`, `diff`, renderer markdown/html/jsonl/prometheus.
7. **remote via ssh** (`x/crypto/ssh`, usa ssh-agent/known_hosts) e `probe`.
8. Moduli del catalogo, uno alla volta, secondo priorità.

## Verifica

- Unit test per parser (mountinfo, meminfo, cgroup files, quadlet dryrun, journal json) con fixture
  in `testdata/` catturate da un server reale (srv-01).
- Test d'integrazione in container Fedora/Debian con systemd + podman rootless (podman in podman /
  VM), con quadlet di esempio che riproduce il bug volume (`hop-quadlet-volume-unit.md`) → atteso
  finding `quadlet.volume-not-used`.
- `file terminus` → "statically linked"; esecuzione su distro diverse (Debian, Fedora, Alpine).
- Confronto manuale: output di `terminus check --user apps` su srv-01 vs comandi del doc srv-01.
- `.sdlc/check` (vet, staticcheck, test) verde.
