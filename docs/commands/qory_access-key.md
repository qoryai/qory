## qory access-key

Make this machine's access key for its server

### Synopsis

Make the access key every run signs its requests to the server with.

The key is an Ed25519 key. Its secret stays on this machine, in access-key-secret beside
runner.yaml in ~/.config/qory, and the server keeps only its public key. A key is
never rotated: a new one is enrolled, and the old one revoked.

  enrol   enrol a new key with a code from the server

With --print, enrol writes no key or setting on this machine and prints the key for a
CI's settings instead.

More: https://github.com/qoryai/qory/blob/main/docs/run.md

### Options

```
  -h, --help   help for access-key
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory access-key enrol](qory_access-key_enrol.md)	 - Enrol a new access key with a code from the server

