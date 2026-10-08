## qory access-key enrol

Enrol a new access key with a code from the server

### Synopsis

Enrol a new access key for this machine with an enrolment code an owner or
administrator of the server created. The code is valid for 15 minutes and used once.

qory makes the key, keeps its secret in access-key-secret.new, prints its fingerprint
and sends the server the public key. The server's signed answer gives the key its id:
qory moves the secret to access-key-secret and writes the id into the server section of
runner.yaml, with the server's URL and its public key where the section has none yet.
The key is active from that answer on: runs can start. qory keeps the answer in
enrolment-answer until the enrolment is finished: should the command stop after the
answer came, the same command finishes it on this machine, at any time, without asking
the server again. A refused enrolment moves aside only access-key-secret.new, never
access-key-secret.

When access-key-secret exists, enrol refuses, so it never replaces this machine's key
unasked: use --replace to move this machine to a new key, or --print for a key kept
elsewhere. The one exception is a retry: run the same command again within the code's
15 minutes, while access-key-secret still holds the key made for it, and it retries
with that key.

--replace moves this machine to a new key, and the old key stays in use until the new
one is active. qory makes the new key in access-key-secret.new and enrols it, leaving
access-key-secret and runner.yaml as they are, so an enrolment that fails leaves the
old key working. Once the server's signed answer has come, the new secret takes the old
one's place, runner.yaml names the new key, and the old secret is removed. The old
key still works on the server until an owner or administrator revokes it on the node's
page, unless it is revoked already. Before the server's answer, the same command within
the code's 15 minutes retries; after it, the same command finishes the replacement on
this machine, at any time. On a machine without a key, --replace enrols as the command
does without it.

The key's name is instance.name of runner.yaml, else this machine's host name.

--print writes no key or setting and prints QORY_ACCESS_KEY_ID, QORY_ACCESS_KEY_SECRET
and QORY_APIARY_PUBLIC_KEY for a CI's settings. Only the secret belongs in its secret
store. The key is for another machine, so the server and the pin of runner.yaml do not
apply; the code is checked against QORY_APIARY_PUBLIC_KEY when it is set.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md

```
qory access-key enrol <server> <code> [flags]
```

### Examples

```
  qory access-key enrol https://apiary.example qec_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
  qory access-key enrol --replace https://apiary.example "$code"   # move this machine to a new key
  qory access-key enrol --print https://apiary.example "$code"   # for a CI's settings
```

### Options

```
  -h, --help      help for enrol
      --print     write no key or setting; print the key's three settings for a CI
      --replace   move this machine to a new key; the old key stays in use until the new one is active
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory access-key](qory_access-key.md)	 - Make this machine's access key for its server

