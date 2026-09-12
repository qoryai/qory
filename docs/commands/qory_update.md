## qory update

Update qory to the newest release

### Synopsis

Update qory to the newest release.

The newest release is read from GitHub. How it is installed follows how this qory was
installed. A Homebrew install runs brew upgrade qory. A go install runs go install
github.com/qoryai/qory@latest. A release binary, the install script's or a downloaded
one, is replaced in place: the release's archive for this platform is downloaded, checked
against the release's checksums, and renamed over the running binary. A build from a
source checkout is not updated; rebuild it, or install a release.

--check reports whether a newer release exists and installs nothing.

A build from the main branch between releases carries a pseudo-version, which is ahead
of the newest release. Such a build is not updated on its own: on a terminal, update
offers to install the release over it and asks; in a script, --release says yes.

Every command looks for a newer release, when its error output is a terminal, and prints
a notice after its own output when the newest release is ahead of its version. GitHub is
asked at most once an hour; between asks the answer is read from a file under the user's
cache directory. A build from the main branch between releases carries a pseudo-version
and is told of a release only when it is behind one. QORY_NO_UPDATE_CHECK=1 turns the
look off, and so does CI being set.

--verbose adds nothing here.

```
qory update [flags]
```

### Options

```
      --check     report whether a newer release exists and install nothing
  -h, --help      help for update
      --release   install the newest release over a build that is ahead of it, without asking
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

