package runcopy

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
)

// localOnly lets git reach a repository on this machine by its path alone: every
// network transport is off.
var localOnly = []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always"}

// noMaintenance keeps git from starting maintenance in the background, which would
// outlive the call.
var noMaintenance = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// gitError is a git command that failed: the subcommand, the first line git printed on
// stderr, or the error that kept git from running, and git's exit status, -1 when git
// did not exit. uncut is whether line is git's line as [firstLine] keeps it.
type gitError struct {
	subcommand string
	line       string
	exit       int
	uncut      bool
}

func (e *gitError) Error() string { return "git " + e.subcommand + ": " + e.line }

// ShownError is err as a line prints it when [integration.Printable] does not run on all
// of its text: each line of git in it that [firstLine] keeps uncut is cut and has each
// character that does not print as ?, as [preparation.ShownPath] prints it. The result
// unwraps to err.
func ShownError(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	cut := text
	for _, ge := range gitErrors(err) {
		if ge.uncut {
			cut = strings.ReplaceAll(cut, ge.Error(), "git "+ge.subcommand+": "+shown(ge.line))
		}
	}
	if cut == text {
		return err
	}
	return &cutError{cut, err}
}

// gitErrors is each [*gitError] in the tree of err.
func gitErrors(err error) []*gitError {
	switch e := err.(type) {
	case *gitError:
		return []*gitError{e}
	case interface{ Unwrap() []error }:
		var out []*gitError
		for _, inner := range e.Unwrap() {
			out = append(out, gitErrors(inner)...)
		}
		return out
	case interface{ Unwrap() error }:
		return gitErrors(e.Unwrap())
	}
	return nil
}

// cutError is the error of [ShownError]: text, with err under it.
type cutError struct {
	text string
	err  error
}

func (e *cutError) Error() string { return e.text }

func (e *cutError) Unwrap() error { return e.err }

// exitOf is git's exit status in err, -1 when err is not a [*gitError] or git did not
// exit.
func exitOf(err error) int {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.exit
	}
	return -1
}

// subcommand is the first argument of args that is git's subcommand: not an option,
// and not the value of a -c.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return "git"
}

// git runs git with args in dir on this machine, as the push step runs it: the flags of
// [hostgit], [proxygit.Hardened], the environment of [proxygit.Env] with no proxy, so
// git reads no system or global configuration file and no file of the person's home,
// and no transport but a local path. It returns what git printed on stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitWith(ctx, dir, nil, nil, args...)
}

// gitWith is [git] with env added to git's environment and stdin as its input; nil is
// none.
func gitWith(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	full := slices.Concat(proxygit.Hardened, localOnly, noMaintenance, args)
	cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), full...)
	if err != nil {
		return "", &gitError{subcommand: subcommand(args), line: hidden(err.Error()), exit: -1}
	}
	cmd.Env = append(proxygit.Env(cmd.Env, &proxygit.Proxy{}), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	cmd.Env = append(cmd.Env, env...)
	return run(cmd, stdin, args)
}

// personGit runs git with args in dir with the person's git environment: the person's
// HOME and global configuration, so a filter they set up, such as Git LFS, applies,
// with the flags of [hostgit] and hooks off. Only the checkout of a copy that qory just
// made, before the run starts, runs this way. env goes on git's environment.
func personGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := slices.Concat(noMaintenance, args)
	cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), full...)
	if err != nil {
		return "", &gitError{subcommand: subcommand(args), line: hidden(err.Error()), exit: -1}
	}
	cmd.Env = append(append(cmd.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir), "GIT_TERMINAL_PROMPT=0"), env...)
	return run(cmd, nil, args)
}

// run runs cmd with stdin and returns what it printed on stdout.
func run(cmd *exec.Cmd, stdin io.Reader, args []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A helper left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		// A filter or a credential helper can print a URL with its password here. The hide reads
		// all of it, since [firstLine] drops the later lines.
		line := firstLine(hidden(stderr.String()))
		uncut := line != ""
		if !uncut {
			line = hidden(err.Error())
		}
		exit := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
		return "", &gitError{subcommand: subcommand(args), line: line, exit: exit, uncut: uncut}
	}
	return stdout.String(), nil
}

// hidden is text as [hideHTTP] and then [exports.HideURLError] print it.
func hidden(text string) string {
	return exports.HideURLError(errors.New(hideHTTP(text))).Error()
}

// firstLine is the first line of text with a character other than white space, trimmed
// of the white space at its ends, as [exports.Value] prints it bare: a line that contains
// an access key secret, a run key, a run code or an enrolment code is the phrase in its
// place. It is "" when text has no such line. firstLine does not cut the line or replace
// a character that does not print: before the cut of [integration.Printable], a caller
// takes each path of the copy out of the line, or [ShownError] cuts it.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return exports.Value(line, false)
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
		return "", &gitError{subcommand: "rev-parse", line: "printed " + shown(id) + ", which is not a commit id", exit: -1}
	}
	return id, nil
}

// checkBranch returns an error when branch is not a branch name, by the check the
// preparation runs on a remote's default branch.
func checkBranch(ctx context.Context, branch string) error {
	ok, err := preparation.BranchName(ctx, "/", branch)
	if err != nil {
		return fmt.Errorf(textGitFailed, err)
	}
	if !ok || branch == "" {
		return fmt.Errorf(textBranch, shown(branch))
	}
	return nil
}

// checkRef returns an error when ref is not a full ref name under refs/ that git
// check-ref-format takes.
func checkRef(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf(textRef, shown(ref))
	}
	if _, err := git(ctx, "/", "check-ref-format", ref); err != nil {
		// check-ref-format exits 1 for a name it takes as no ref; any other failure is
		// git's.
		if exitOf(err) == 1 {
			return fmt.Errorf(textRef, shown(ref))
		}
		return fmt.Errorf(textGitFailed, err)
	}
	return nil
}

// objectFormat is the object format of the repository at dir: sha1 or sha256.
func objectFormat(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-object-format")
	return strings.TrimSpace(out), err
}

// maxHashBytes is the most bytes [hashBlob] reads of one file. A setup can leave a sparse file
// of any size.
var maxHashBytes int64 = maxCopyBackBytes

// hashBlob is the git blob id of what r has, size bytes, in format: the hash of blob <size>,
// a zero byte and the content, as git hash-object --no-filters computes it. It reads no more
// than size bytes, and fails when r has fewer. No git runs, so a file costs no process.
func hashBlob(ctx context.Context, format string, r io.Reader, size int64) (string, error) {
	var h hash.Hash
	switch format {
	case "sha1":
		h = sha1.New()
	case "sha256":
		h = sha256.New()
	default:
		return "", &gitError{subcommand: "rev-parse", line: "printed " + shown(format) + ", which is not an object format", exit: -1}
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	n, err := io.Copy(h, io.LimitReader(ctxReader{ctx, r}, size))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// bundleFetchChecks reports whether a fetch from a bundle checks the objects it
// receives with transfer.fsckObjects, as git does from 2.48 on. A test sets it.
var bundleFetchChecks = func(ctx context.Context) bool {
	gitVersion.once.Do(func() {
		out, err := git(ctx, "/", "version")
		gitVersion.atLeast248 = err == nil && atLeast(out, 2, 48)
	})
	return gitVersion.atLeast248
}

// gitVersion is what [bundleFetchChecks] found, once per process.
var gitVersion struct {
	once       sync.Once
	atLeast248 bool
}

// atLeast reports whether the first line git version printed, such as "git version
// 2.39.5 (Apple Git-154)", has a version of major.minor or later.
func atLeast(out string, major, minor int) bool {
	line, _, _ := strings.Cut(out, "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "git version ")
	if !ok {
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	parts := strings.SplitN(fields[0], ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && (ma > major || ma == major && mi >= minor)
}

// httpURL is the start of a URL with the scheme http, https, ftp or ftps, in any case, after any
// text: a colour code or a word can come right before it.
var httpURL = regexp.MustCompile(`(?i)(?:https?|ftps?)://`)

// hideHTTP is text with the first such URL and all that follows it, later lines too, replaced by
// textHiddenURL, when an @, a ? or a # follows the URL's start in text. A credential can have a
// space, a quote, a / or a line break, so no rule can tell where it ends, and nothing after the URL's
// start stays. With no such character, text stays as it is.
func hideHTTP(text string) string {
	start := httpURL.FindStringIndex(text)
	if start == nil || !strings.ContainsAny(text[start[0]:], "@?#") {
		return text
	}
	return text[:start[0]] + textHiddenURL
}
