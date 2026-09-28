# Custom Facts

There are two types of custom facts: executable and static. Custom facts are stored in the external facts directory, which defaults to `/etc/terminus/facts.d`. Use the `--external-facts-dir` flag to specify a different location.

Custom facts belong to the `external` module. Each file provides one fact, named after the file
without its extension: `docker.json` and `docker.sh` both provide `docker` (having both is an
error). Hidden files and files that are neither executable nor `.json` are ignored.

A file that fails (invalid JSON, non-zero exit code, timeout) is reported in the module errors;
the other facts are still collected. Query custom facts with their name (`terminus docker.ServerAPIVersion`)
or through the module (`terminus external.docker.ServerAPIVersion`).

## Executable Facts

Executable facts can be written in any language and reside under the external facts directory with the executable bit set.
They must print JSON on standard output and exit with code 0 within 30 seconds; they run in parallel.

### Example

```shell
sudo vim /etc/terminus/facts.d/date
```

```
#!/bin/bash

echo "{\"Now\": \"$(date)\"}"
exit 0
```

```shell
$ sudo chmod +x /etc/terminus/facts.d/date
$ terminus date.Now
Sat Apr 11 13:38:26 PDT 2015
```

## Static Facts

Static facts must be in the JSON format and reside under the external facts directory with a `.json` file extension.

### Example

```shell
$ sudo vim /etc/terminus/facts.d/docker.json
```

```json
{
  "ClientAPIVersion": "1.16",
  "ClientOSArch": "linux/amd64",
  "ClientVersion": "1.5.0",
  "ServerAPIVersion": "1.16",
  "ServerOSArch": "linux/amd64",
  "ServerVersion": "1.5.0"
}
```

```shell
$ terminus docker.ServerAPIVersion
1.16
```
