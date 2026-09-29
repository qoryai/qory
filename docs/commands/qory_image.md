## qory image

Check an image the wall runs the agent in

### Synopsis

Check an image the wall runs the agent in.

Four are defined under images/ in qory's repository, for linux/amd64 and linux/arm64:

  agent             Claude Code, git and gh
  agent-docker      agent, and a Docker daemon of the agent's own
  agent-go          agent, and Go
  agent-go-docker   agent-docker, and Go

Build them from the checkout of the release you run, build yours FROM one of them, and
check it with qory image check.

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

