# Terminus API

To enable the terminus HTTP API use `terminus serve --http` (the v1 form `terminus -http` still works).

## Usage

### Server

```shell
$ terminus serve --http ":8080"
```

The server runs the core modules on every request.

### Client

#### Get all facts

```shell
$ curl http://$SERVER_IP:8080/facts
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
