package suite

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/proxygit"
)

// commitID is the form of a commit id: 40 or 64 lower-case hexadecimal digits.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidCommit reports whether commit has the form of a full commit id.
func ValidCommit(commit string) bool { return commitID.MatchString(commit) }

// localOnly lets git reach a repository on this machine by its path alone.
var localOnly = []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always"}

// noMaintenance keeps git from starting maintenance that would outlive the call.
var noMaintenance = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// checkoutLF writes line endings as the commit has them.
var checkoutLF = []string{"-c", "core.autocrlf=false", "-c", "core.eol=lf"}

// cloneWait is the longest the clone of one commit may take.
const cloneWait = 25 * time.Second

// Clone writes the files of commit, a commit of the repository at src, into dest, a
// folder that does not exist yet, with git as [hostgit] runs it: hooks and the file system
// monitor off, no configuration file of the person's, no transport but a path on this
// machine. The folder contains a repository whose HEAD is commit, so that nothing
// in it points back to src. Submodules are not checked out. A failure leaves nothing
// of dest.
func Clone(ctx context.Context, src, commit, dest string) (err error) {
	if !ValidCommit(commit) {
		return fmt.Errorf("%q is not a full commit id", commit)
	}
	ctx, cancel := context.WithTimeout(ctx, cloneWait)
	defer cancel()
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	steps := [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", src, commit},
		slices.Concat(checkoutLF, []string{"checkout", "--quiet", "--force", "--detach", commit}),
	}
	for _, step := range steps {
		if err := git(ctx, dest, step...); err != nil {
			return err
		}
	}
	return nil
}

// git runs git with args in dir and returns the first line git printed on stderr as the
// error of a failure.
func git(ctx context.Context, dir string, args ...string) error {
	full := slices.Concat(proxygit.Hardened, localOnly, noMaintenance, args)
	cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), full...)
	if err != nil {
		return err
	}
	cmd.Env = append(proxygit.Env(cmd.Env, &proxygit.Proxy{}), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if line == "" {
			line = err.Error()
		}
		return fmt.Errorf("git %s: %s", args[subcommand(args)], line)
	}
	return nil
}

// subcommand is the index of the first argument of args that is git's subcommand.
func subcommand(args []string) int {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case !strings.HasPrefix(args[i], "-"):
			return i
		}
	}
	return 0
}
