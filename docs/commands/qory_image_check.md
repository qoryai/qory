## qory image check

Check that an image has what the wall needs of it

### Synopsis

Check that an image has what the wall needs of it. With no image, check wall.image
of forager.yaml. A name of wall.images is read as that image's ref first, as
qory run reads --image.

From outside, it reads the image the engine holds: its platform, HOME in its
environment, whether the reference is pinned by digest, and every file, for one that is
setuid or setgid or has capabilities of its own. Then it starts the image the way the
wall starts the agent, as a user the image does not know, with no capability and no
network, and qory's Linux build checks from inside:

  - HOME is a directory that user writes in
  - the system's authorities are where the wall reads them
  - /bin/sh is there, for the runtime's hooks and the session's API-key approval
  - claude is the version Forager's descriptor is written against
  - git and gh run
  - dockerd, for a Docker of the agent's own, and what it runs

The Linux build is wall.helper, for the engine's architecture; on Linux, this binary
when wall.helper is not set. The docker command is wall.command, or docker.

The exit status is 0 when every image passes, and 1 when one fails.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#qorys-images

```
qory image check [image...] [flags]
```

### Examples

```
  qory image check                                # wall.image of forager.yaml
  qory image check qory-agent
  qory image check go-docker                      # an image wall.images defines
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

