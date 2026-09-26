# Security

## Reporting a vulnerability

Write to **info@8wonders.de**. Do not open a public issue or pull request for it.

Say what you found, the version (`qory version`), and how to see it happen: the
configuration, the command, and what happened that should not have. Take any secret out
of what you send.

You get an answer within three working days. We tell you what we found, fix what is a
vulnerability in a new release, and publish an advisory that credits you unless you
prefer otherwise.

## Supported versions

The latest release. Below 1.0 a fix is a new release, and an earlier release gets no
fix.

## What is a vulnerability here

- A repository decides what only the machine may: anything in a checkout, its
  `qory.yaml`, a module, a composed harness, changes what `~/.config/qory/runner.yaml`
  defines for egress, the server, credentials, integrations or the wall, or a run's
  `--policy` widens the machine's.
- `qory run` hands a session something it must not receive: the server's secret, a
  credential, a variable of your environment that was not listed for a walled run.
- Composing a harness writes outside the checkout and the home it was set to use.
- `qory update` installs an archive that does not match the release's checksums.

The wall, the proxy, credentials kept outside the enclosure and the signed delivery are
the [runner](https://github.com/qoryai/runner)'s, and so is their
[security policy](https://github.com/qoryai/runner/blob/main/SECURITY.md): what they
guarantee, and the limits that are how they work and not a flaw. Reports about them
come to the same address.

If you are not sure whether something counts, write anyway.
