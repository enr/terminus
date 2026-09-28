# Terminus API

To enable the terminus HTTP API use `terminus serve --http` (the v1 form `terminus -http` still works).

## Usage

### Server

```shell
$ terminus serve --http ":8080"
```

A collection (every enabled module: external scripts, backups, TLS dials, ...) is reused for
`--cache-ttl` (default 5s) across requests, so the server answers a burst of requests with one
collection instead of one each; `--cache-ttl 0` collects on every request. There is no
authentication: bind to a loopback or private address, or put the server behind a reverse proxy
that authenticates the caller.

### Client

#### Get all facts

```shell
$ curl -X POST http://$SERVER_IP:8080/facts
```

#### Get a single fact

```shell
$ curl http://$SERVER_IP:8080/facts -d 'System.MachineID'
```

A path that does not exist returns `404`.

#### Get the complete report

Facts, module statuses and the findings of the checks, in the same JSON format as
`terminus check -o json`:

```shell
$ curl http://$SERVER_IP:8080/report | jq .summary
```
