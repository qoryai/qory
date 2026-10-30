## qory apiary enrol

Enrol a diagnostics key with a code from Qory Apiary

### Synopsis

Make a diagnostics key on this computer and enrol it with the Qory Apiary server at
<server>. <code> is a diagnostics enrolment code, qecd_..., which an instance admin
issues under Instance settings > Diagnostics keys in Qory Apiary. A code is valid for
15 minutes and works once.

qory makes the key in ~/.config/qory/apiary/<host> and sends Qory Apiary the public
key, with a proof that this computer has the secret. The response must be signed with
the server key whose fingerprint is in the code. qory then writes access-key-secret,
apiary.json with the server, the key id and that server key, and instance-id, which
qory apiary run diagnose reads. forager.yaml and its directory stay as they are.

If <host> has a diagnostics key already, enrol stops: remove the directory first.

If the command stops after Qory Apiary enrolled the key, run the same command again: qory
finishes the enrolment from the response it kept in enrolment-answer, without sending
the code again. If no response came, the same command within the code's 15 minutes
retries with the same key.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/apiary.md

```
qory apiary enrol <server> <code> [flags]
```

### Examples

```
  qory apiary enrol https://apiary.example qecd_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
```

### Options

```
  -h, --help   help for enrol
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory apiary](qory_apiary.md)	 - Debug runs on a Qory Apiary server

