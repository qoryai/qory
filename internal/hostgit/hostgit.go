// Package hostgit is the one way qory runs git on this machine.
//
// Git on this machine reads a repository's configuration from inside it, so a key there
// can make git run a program: a hook, an fsmonitor, a filter, an ssh command. [Command]
// makes every git command qory runs:
//
//   - with the hooks directory and fsmonitor off, the ext:: transport off, submodules
//     left alone, and replace refs and the commit-graph not read, through -c flags that
//     win over every configuration file. A replace ref or a commit-graph can give a
//     commit other parents, so that git on this machine reads a branch's commits as on
//     its base;
//   - with the variables that would add configuration or choose another repository or
//     git's programs dropped from the environment, and the pager and the editors set to
//     programs that do nothing. GIT_SSH, GIT_SSH_COMMAND, GIT_ASKPASS,
//     GIT_PROXY_COMMAND, GIT_EXTERNAL_DIFF and XDG_CONFIG_HOME are kept from the
//     person's environment.
//
// [Unchecked] makes git for a command that reads no repository, such as git config
// --file, with the same flags and the same rules for the environment, applied to an
// environment its caller gives. Both run the first git on the PATH, by its path with its
// symbolic links resolved.
package hostgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// flags are the -c flags every git command gets. A -c flag is read after every
// configuration file, so each wins over a repository's setting of the same key, an
// included one too.
var flags = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "protocol.ext.allow=never",
	"-c", "diff.ignoreSubmodules=all",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "core.useReplaceRefs=false",
	"-c", "core.commitGraph=false",
}

// dropped are the variables taken out of git's environment: the ones that add
// configuration git would read after the checked files, the one that chooses git's
// programs, and the ones that choose another repository than the one checked. The
// variables that choose the global and the system configuration file stay: those
// files are the person's.
var dropped = []string{
	"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_EXEC_PATH",
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
}

// quiet are the variables set for every git command: no pager, and editors that do
// nothing.
var quiet = []string{"GIT_PAGER=cat", "GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"}

// Env is the environment of every git command [Command] makes: this process's, without
// the [dropped] variables and every GIT_CONFIG_KEY_<n> and GIT_CONFIG_VALUE_<n>, with
// the PATH's empty and relative entries taken out, /usr/bin:/bin when none is left or
// there is no PATH, and with the pager and the editors set to programs that do nothing.
// git finds its helpers and the programs it runs through the PATH, such as ssh: an
// empty or relative entry is taken from the working folder, which is the checkout, and
// a shell git starts with no PATH takes a default one, which may end in the working
// folder.
func Env() []string { return envFrom(os.Environ()) }

// baseKey is the key of the environment [WithBase] adds to a context.
type baseKey struct{}

// WithBase returns ctx with base as the environment that every git started with it
// starts from, in place of this process's: [Ready] and [Command] take base as [Env]
// takes this process's. A pool that fetches a run's code in its process sets the
// environment of its commands, whose PATH lists no entry in the folder walled runs
// write to.
func WithBase(ctx context.Context, base []string) context.Context {
	return context.WithValue(ctx, baseKey{}, slices.Clone(base))
}

// Base is the environment that a git started with ctx starts from: the one [WithBase]
// set, or this process's.
func Base(ctx context.Context) []string {
	if base, ok := ctx.Value(baseKey{}).([]string); ok {
		return base
	}
	return os.Environ()
}

// envOf is [Env] for a git started with ctx: [Base] of ctx in place of this process's
// environment.
func envOf(ctx context.Context) []string { return envFrom(Base(ctx)) }

// envFrom is [Env] with base in place of this process's environment.
func envFrom(base []string) []string {
	var env []string
	path := false
	for _, kv := range base {
		name, value, _ := strings.Cut(kv, "=")
		if drop(name) {
			continue
		}
		if name == "PATH" {
			kv, path = name+"="+absoluteEntries(value), true
		}
		env = append(env, kv)
	}
	if !path {
		env = append(env, "PATH="+fallbackPath)
	}
	return append(env, quiet...)
}

// absoluteEntries is the PATH value path with only its absolute entries, in order; with
// none, [fallbackPath], since a shell git starts may search its working folder for an
// empty PATH.
func absoluteEntries(path string) string {
	var kept []string
	for _, dir := range filepath.SplitList(path) {
		if filepath.IsAbs(dir) {
			kept = append(kept, dir)
		}
	}
	if len(kept) == 0 {
		return fallbackPath
	}
	return strings.Join(kept, string(filepath.ListSeparator))
}

// fallbackPath is the PATH git gets when the person's holds no absolute entry.
const fallbackPath = "/usr/bin:/bin"

func drop(name string) bool {
	if strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
		return true
	}
	for _, d := range dropped {
		if name == d {
			return true
		}
	}
	for _, q := range quiet {
		if n, _, _ := strings.Cut(q, "="); name == n {
			return true
		}
	}
	return false
}

// Command returns git with args, to run in dir. A caller sets the output, adds to Env and
// runs it. The error is that of [Ready]: no git, or one older than 2.36.
func Command(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	if err := Ready(ctx); err != nil {
		return nil, err
	}
	return command(ctx, dir, args...), nil
}

// command is git with the flags and environment of [Command], without [Ready]: the git
// [gitIn] finds on the PATH of [Env], made from [Base] of ctx, never one on that PATH
// that Env leaves out.
func command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return commandIn(ctx, dir, envOf(ctx), args...)
}

// commandIn is [command] with env, which [envFrom] made, in place of [Env]. Every git
// qory runs on this machine is made here.
func commandIn(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", append(append([]string{}, flags...), args...)...)
	c.Dir = dir
	c.Env = env
	c.Path, c.Err = gitIn(c.Env)
	if c.Err == nil {
		// Every git qory runs starts here: a git [Ready] found too old does not start.
		c.Err = known(c.Path)
	}
	return c
}

// Unchecked returns git with args, to run in dir, for a command that reads no
// repository, such as git config --file with GIT_CEILING_DIRECTORIES set so that dir is
// in none. Its environment is base, as [Env] takes this process's: the git it runs is
// the one the PATH of base finds after Env's rules, and it stops, as Command does, on a
// git older than 2.36.
func Unchecked(ctx context.Context, dir string, base []string, args ...string) (*exec.Cmd, error) {
	env := envFrom(base)
	if err := ready(ctx, env); err != nil {
		return nil, err
	}
	return commandIn(ctx, dir, env, args...), nil
}

// gitIn returns the git that the PATH of env finds: the first entry that holds a file
// named git that is regular and may be run, by its path with every symbolic link of the
// entry and of the file resolved, so git runs the file found: a symbolic link pointed
// elsewhere after the search is not followed again. With none, the error is an
// [*exec.Error] for [exec.ErrNotFound].
func gitIn(env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		file := filepath.Join(dir, "git")
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(file)
		if err != nil || !filepath.IsAbs(real) {
			continue
		}
		return real, nil
	}
	return "git", &exec.Error{Name: "git", Err: exec.ErrNotFound}
}
