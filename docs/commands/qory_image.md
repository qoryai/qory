## qory image

Check an image the wall runs the agent in

### Synopsis

Check an image the wall runs the agent in.

Qory publishes four with each release, for linux/amd64 and linux/arm64:

  ghcr.io/qoryai/agent             Claude Code, git and gh
  ghcr.io/qoryai/agent-docker      the same, and a Docker daemon of the agent's own
  ghcr.io/qoryai/agent-go          agent, and Go
  ghcr.io/qoryai/agent-go-docker   agent-docker, and Go

Build yours FROM one of them, and check it with qory image check.

### Options

```
  -h, --help   help for image
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory image check](qory_image_check.md)	 - Check that an image has what the wall needs of it

