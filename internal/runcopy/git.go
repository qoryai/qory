package runcopy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/proxygit"
)

// localOnly lets git reach a repository on this machine by its path alone: every
// network transport is off.
var localOnly = []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always"}

// noMaintenance keeps git from starting maintenance in the background, which would
// outlive the call.
var noMaintenance = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// gitError is a git command that failed: the subcommand and the first line git printed
// on stderr, or the error that kept git from running.
type gitError struct {
	subcommand string
	line       string
}

func (e *gitError) Error() string { return "git " + e.subcommand + ": " + e.line }

// git runs git with args in dir on this machine, as the push step runs it: the flags of
// [hostgit], [proxygit.Hardened], the environment of [proxygit.Env] with no proxy, so
// git reads no system or global configuration file and no file of the person's home,
// and no transport but a local path. It returns what git printed on stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitIn(ctx, dir, nil, args...)
}

// gitIn is [git] with env added to git's environment.
func gitIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return gitWith(ctx, dir, env, nil, args...)
}

// gitWith is [gitIn] with stdin as git's input; nil is none.
func gitWith(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	full := slices.Concat(proxygit.Hardened, localOnly, noMaintenance, args)
	cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), full...)
	if err != nil {
		return "", &gitError{subcommand: args[0], line: err.Error()}
	}
	cmd.Env = append(proxygit.Env(cmd.Env, &proxygit.Proxy{}), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A helper left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		line := firstLine(stderr.String())
		if line == "" {
			line = err.Error()
		}
		return "", &gitError{subcommand: args[0], line: line}
	}
	return stdout.String(), nil
}

// firstLine is the first line of text that is not empty, as a line prints it.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return shown(line)
		}
	}
	return ""
}

// objectID is a commit id in full: SHA-1 or SHA-256, in lower-case hex.
var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// commitOf is the commit that rev resolves to in the repository at dir, in full.
func commitOf(ctx context.Context, dir, rev string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !objectID.MatchString(id) {
		return "", fmt.Errorf("git rev-parse printed %s for %s, which is no commit id", shown(id), shown(rev))
	}
	return id, nil
}

// checkBranch returns an error when branch is not a branch name git takes, such as one
// with a space, a .. or a leading dash.
func checkBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("give a branch name that does not start with a dash: %s is no branch name for the run", shown(branch))
	}
	if _, err := git(ctx, "/", "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("give a branch name that git check-ref-format takes: %s is no branch name for the run", shown(branch))
	}
	return nil
}

// checkRef returns an error when ref is not a full ref name under refs/ that git takes.
func checkRef(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("give a full ref name that starts with refs/: %s is not one", shown(ref))
	}
	if _, err := git(ctx, "/", "check-ref-format", ref); err != nil {
		return fmt.Errorf("give a ref name that git check-ref-format takes: %s is not one", shown(ref))
	}
	return nil
}

// blobID is the git blob id of what r has, by git hash-object --stdin --no-filters in
// the repository at dir, which sets the object format.
func blobID(ctx context.Context, dir string, r io.Reader) (string, error) {
	out, err := gitWith(ctx, dir, nil, r, "hash-object", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !objectID.MatchString(id) {
		return "", fmt.Errorf("git hash-object printed %s, which is no object id", shown(id))
	}
	return id, nil
}
