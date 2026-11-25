package hostgit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Maker makes one git of [AddAll] with args in dir, with env added to its environment, as
// [Unchecked] does. The index of that git is the one the files are added to.
type Maker func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error)

// AddError is a git of [AddAll] that failed.
type AddError struct {
	// Subcommand is git's subcommand, and Stderr is what git printed on its error output.
	Subcommand, Stderr string
	// Err is the error of the run of git, an [*exec.ExitError] for a git that exited.
	Err error
}

// Error is git's first line, or the error of the run of a git that printed none.
func (e *AddError) Error() string {
	line, _, _ := strings.Cut(strings.TrimSpace(e.Stderr), "\n")
	if line == "" {
		line = e.Err.Error()
	}
	return fmt.Sprintf("git %s failed: %s", e.Subcommand, line)
}

func (e *AddError) Unwrap() error { return e.Err }

// AddAll runs git add in the repository at dir, whose working folder a run can write, and
// returns each path that the add made a gitlink, an entry of mode 160000, in the order of the
// index. paths are the paths to add, each from the top of the repository and with no pattern
// in it; none is the whole repository. With an error, the index must not be used again.
//
// A path that the index has goes to git add -u, and any other path to git add -A: git add -A
// exits 1 on a listed path below a folder that git ignores, also on a tracked one, and git add
// -u exits 128 on a path that the index does not have. A new file below an ignored folder stays
// out, since git status does not list it.
//
// The add leaves out each gitlink that it would reach, since git add runs git status in the
// folder of a gitlink, with the configuration of the repository there. With an exclusion that no
// path reaches, git add can leave out other paths, so there is none for those. A gitlink with a
// link on the way to it, for which git add removes the entry, goes out of the index before the
// add. The gitlinks and paths go to git through its input, so their number has no limit, and no
// add runs without them. A new gitlink is for the caller to take out of the index, or to fail on.
// The index must be one that no process of a run can write while AddAll runs.
//
// Each git has no protocol and no fetch of a missing object, see [noProtocol], also with a
// command that dropped the variables of [Unchecked].
func AddAll(ctx context.Context, command Maker, dir string, env, paths []string) ([]string, error) {
	tracked, before, err := entries(ctx, command, dir, env)
	if err != nil {
		return nil, err
	}
	added := map[string]bool{"": len(paths) == 0}
	var updated, all []string
	if len(paths) == 0 {
		all = []string{""}
	}
	for _, path := range paths {
		path = strings.TrimSuffix(path, "/")
		added[path] = true
		if tracked[path] {
			updated = append(updated, path)
		} else {
			all = append(all, path)
		}
	}
	var gone strings.Builder
	var links []string
	had := map[string]bool{}
	for _, path := range before {
		if reached, behindLink := reached(dir, added, path); reached && behindLink {
			gone.WriteString(path + "\x00")
			continue
		}
		had[path] = true
		links = append(links, path)
	}
	if gone.Len() > 0 {
		if _, err := runAdd(ctx, command, dir, env, gone.String(), "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			return nil, err
		}
	}
	for _, add := range []struct {
		flag  string
		paths []string
	}{{"-u", updated}, {"-A", all}} {
		if len(add.paths) == 0 {
			continue
		}
		if _, err := runAdd(ctx, command, dir, env, addSpecs(dir, add.paths, links), "add", add.flag, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return nil, err
		}
	}
	_, after, err := entries(ctx, command, dir, env)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(after, func(path string) bool { return had[path] }), nil
}

// addSpecs is the input of one git add of paths, "" for the whole repository: each path, and an
// exclusion for each gitlink of links that the add reaches.
func addSpecs(dir string, paths, links []string) string {
	var specs strings.Builder
	added := map[string]bool{}
	for _, path := range paths {
		added[path] = true
		if path == "" {
			// Without a path, git would read the exclusions as everything in dir.
			specs.WriteString(":(top)\x00")
			continue
		}
		specs.WriteString(":(top,literal)" + path + "\x00")
	}
	for _, link := range links {
		if reached, _ := reached(dir, added, link); reached {
			specs.WriteString(":(exclude,top,glob)" + escaped(link) + "\x00")
		}
	}
	return specs.String()
}

// escaped is path as a glob that matches path alone. Its first byte has a backslash too, so git
// takes no part of the glob as a plain prefix: git add exits 1 on a pathspec, also one that
// excludes, whose plain prefix is below a folder that git ignores.
func escaped(path string) string {
	var glob strings.Builder
	for i := range len(path) {
		if i == 0 || strings.IndexByte(`*?[\`, path[i]) >= 0 {
			glob.WriteByte('\\')
		}
		glob.WriteByte(path[i])
	}
	return glob.String()
}

// reached reports whether an add of the paths in added reaches the gitlink path, which is when
// added has it or a path above it, "" for the whole repository, and whether a folder on the way to
// it is a link.
func reached(dir string, added map[string]bool, path string) (reached, behindLink bool) {
	reached = added[""] || added[path]
	for i := range len(path) {
		if path[i] != '/' {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path[:i])))
		behindLink = behindLink || err == nil && info.Mode()&os.ModeSymlink != 0
		reached = reached || added[path[:i]]
	}
	return reached, behindLink
}

// entries are the paths of the index, and the path of each gitlink in it, in the order of the
// index.
func entries(ctx context.Context, command Maker, dir string, env []string) (map[string]bool, []string, error) {
	staged, err := runAdd(ctx, command, dir, env, "", "ls-files", "-z", "--stage")
	if err != nil {
		return nil, nil, err
	}
	paths := map[string]bool{}
	var gitlinks []string
	for entry := range strings.SplitSeq(staged, "\x00") {
		// A path can have a tab itself.
		about, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		paths[path] = true
		if strings.HasPrefix(about, "160000 ") {
			gitlinks = append(gitlinks, path)
		}
	}
	return paths, gitlinks, nil
}

// runAdd runs the git of [AddAll] that command makes, with input on its stdin, and returns what
// it printed on stdout.
func runAdd(ctx context.Context, command Maker, dir string, env []string, input string, args ...string) (string, error) {
	cmd, err := command(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	cmd.Env = append(cmd.Environ(), noProtocol...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(input), &stdout, &stderr
	// A process left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	Group(cmd)
	err = cmd.Run()
	StopGroup(cmd)
	if err != nil {
		return "", &AddError{Subcommand: args[0], Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}
