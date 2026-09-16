## qory receive

Receive the runner's webhook deliveries and append the events to a file

### Synopsis

Receive the deliveries qory run posts to its webhook and append every event to a file,
one JSON object per line, in the order they arrived, each once. The configuration is
the file qory run reads, webhook.yaml in the configuration directory or the file
--webhook names: the receiver listens on the URL's host and port and answers on its
path, and verifies each delivery with the secret. --listen puts it on another address,
for a URL that names a host this machine is not. A delivery that does not verify is
refused with 401 and nothing of it is kept.

The file is --out, events.jsonl in the current directory when left out, and is appended
to when it exists; the ids in it are read first so a redelivery after a restart is not
stored twice. Ctrl-C stops the receiver.

--verbose adds nothing here.

```
qory receive [flags]
```

### Options

```
  -h, --help             help for receive
      --listen string    the address to listen on; the URL's host and port when left out
      --out string       the file the events are appended to (default "events.jsonl")
      --webhook string   the webhook configuration; webhook.yaml in the configuration directory when left out
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

