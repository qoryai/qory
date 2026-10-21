package hostgit

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// GitVersionError is a git older than 2.36 on this machine: qory reads git worktree
// list --porcelain -z, which git prints from 2.36 on.
type GitVersionError struct {
	// Version is the version as git version printed it, such as 2.34.1.
	Version string
}

func (e *GitVersionError) Error() string {
	return "Install git 2.36 or later. qory reads git's worktree list in a form that git " + e.Version + " does not print."
}

// versions are the results of [Ready] by the path of the git program, so that git
// version runs once per program and process.
var versions struct {
	sync.Mutex
	of map[string]error
}

// Ready returns a [*GitVersionError] when the git that [Command] runs is older than
// 2.36. It returns nil when there is no git, which [Command] then reports, and when git
// version fails or prints a line qory cannot read. [Command], [Hold], [Record],
// [Fingerprint] and [Changes] call Ready first, and [Unchecked] runs the same check on
// the git that its environment finds. [Resolve] does not: after Ready found git older
// than 2.36, Resolve starts no git and returns false. A caller that reads that false as
// a folder outside a working tree calls Ready first, so a git failure is never read
// that way.
func Ready(ctx context.Context) error { return ready(ctx, Env()) }

// ready is [Ready] for the git that the PATH of env finds.
func ready(ctx context.Context, env []string) error {
	path, err := gitIn(env)
	if err != nil {
		return nil
	}
	versions.Lock()
	defer versions.Unlock()
	if err, ok := versions.of[path]; ok {
		return err
	}
	c := exec.CommandContext(ctx, path, "version")
	c.Env, c.Dir = env, "/"
	out, err := c.Output()
	var exit *exec.ExitError
	if ctx.Err() != nil || err != nil && !(errors.As(err, &exit) && exit.Exited()) {
		// git did not run to the end, as when ctx ended or a signal stopped it: nothing
		// is kept, and the next call runs git version again.
		return nil
	}
	var result error
	if err == nil {
		result = tooOld(string(out))
	}
	if versions.of == nil {
		versions.of = map[string]error{}
	}
	versions.of[path] = result
	return result
}

// known is what [Ready] found for the git at path, nil when it found nothing or has not
// run for it.
func known(path string) error {
	versions.Lock()
	defer versions.Unlock()
	return versions.of[path]
}

// tooOld reads the first line git version prints, such as "git version 2.39.5 (Apple
// Git-154)" or "git version 2.36.0.windows.1", and returns a [*GitVersionError] for a
// version older than 2.36, nil for a later one or a line it cannot read.
func tooOld(out string) error {
	line, _, _ := strings.Cut(out, "\n")
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "git version ")
	if !ok {
		return nil
	}
	version, _, _ := strings.Cut(rest, " ")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return nil
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	if major > 2 || major == 2 && minor >= 36 {
		return nil
	}
	return &GitVersionError{Version: version}
}
