## qory image check

Check that an image has what the wall needs of it

### Synopsis

Check that an image has what the wall needs of it. With no image, check wall.image
of runner.yaml.

From outside, it reads the image the engine holds: its platform, HOME in its
environment, and whether the reference is pinned by digest. Then it starts the image the
way the wall starts the agent, as a user the image does not know, with no capability and
no network, and qory's Linux build checks from inside:

  - HOME is a directory that user writes in
  - the system's authorities are where the wall reads them
  - /bin/sh is there, for the runtime's hooks
  - claude is the version the runner's descriptor is written against
  - git and gh run
  - no file is setuid or setgid
  - dockerd, for a Docker of the agent's own, and what it needs

The Linux build is this binary on Linux, and wall.helper elsewhere. The docker command
is wall.command, or docker.

The exit status is 0 when every image passes, and 1 when one fails.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#the-agents-image

```
qory image check [image...] [flags]
```

### Examples

```
  qory image check                                # wall.image of runner.yaml
  qory image check ghcr.io/qoryai/agent:0.13.0
  qory image check my-agent:1 my-agent-docker:1
```

### Options

```
  -h, --help   help for check
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory image](qory_image.md)	 - Check an image the wall runs the agent in

