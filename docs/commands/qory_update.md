## qory update

Install the newest release of qory

### Synopsis

Install the newest release of qory from GitHub, the way this qory was installed:

  Homebrew        brew upgrade qory
  go install      go install github.com/qoryai/qory@latest
  release binary  download the archive, check it against the release's checksums,
                  and replace the binary in place

A build from a source checkout is not updated. Rebuild it, or install a release.

A build from main between releases is ahead of the newest release. update asks before it
installs the release over it. In a script, --release says yes.

Every command checks for a newer release when it runs at a terminal. When there is one,
it prints a notice after its output. It asks GitHub at most once an hour, and caches the
answer. A build from main gets a notice only when a release is ahead of it.
QORY_NO_UPDATE_CHECK=1 turns the check off. So does CI.

--verbose adds nothing here.

```
qory update [flags]
```

### Examples

```
  qory update           # install the newest release
  qory update --check   # only report whether there is one
```

### Options

```
      --check     report whether a newer release exists and install nothing
  -h, --help      help for update
      --release   install the newest release over a build that is ahead of it, without a confirmation prompt
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check

