## qory access-key create

Make an access key whose public key an administrator pastes into the server

### Synopsis

Make a new access key for this machine and print its public key and fingerprint.

An owner or administrator of the server pastes the public key into an existing node or
node pool, where it is approved at once. Its page then shows the lines for the server
section of runner.yaml: server.url, server.access_key_id and server.apiary_public_key.

The secret goes into access-key-secret beside runner.yaml. When that file exists,
create refuses: move it aside yourself first.

--print writes no file and prints QORY_ACCESS_KEY_SECRET for a CI's secret store.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md

```
qory access-key create [flags]
```

### Examples

```
  qory access-key create
  qory access-key create --print   # for a CI's secret store
```

### Options

```
  -h, --help    help for create
      --print   write no file; print the key's secret for a CI
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory access-key](qory_access-key.md)	 - Make this machine's access key for its server

