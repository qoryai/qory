// Package hostgit is the one way qory runs git on this machine.
//
// A walled run's container has the run's checkout, and every read-write mount, open for
// writing. Git on this machine reads a repository's configuration from inside it, so a
// run could set a key there that makes git on this machine run a program: a hook, an
// fsmonitor, a filter, an ssh command. [Command] makes every git command qory runs:
//
//   - with the hooks directory and fsmonitor off, the ext:: transport off, submodules
//     left alone, and replace refs and the commit-graph not read, through -c flags that
//     win over every configuration file. A run could write a replace ref or a
//     commit-graph that gives a commit other parents, so that git on this machine reads
//     a branch's commits as on its base.
//     core.hooksPath covers only the hooks directory: a hook a configuration defines,
//     hook.<name>.command with hook.<name>.event, runs whatever it says, and the
//     settings check refuses those keys in a place a walled run used, as it refuses
//     every key qory and git do not write;
//   - with the variables that would add configuration or choose another repository or
//     git's programs dropped from the environment, and the pager and the editors set to
//     programs that do nothing. GIT_SSH, GIT_SSH_COMMAND, GIT_ASKPASS,
//     GIT_PROXY_COMMAND, GIT_EXTERNAL_DIFF and XDG_CONFIG_HOME are kept from the
//     person's environment, which is sound only while nothing loads qory's environment
//     from a file a run wrote;
//   - after the settings check, when the repository's configuration lies in a place a
//     walled run could write, as package places records them, or a .git or commondir
//     in such a place leads to it.
//
// [Unchecked] makes git for a command that reads no repository, such as git config
// --file, with the same flags and the same rules for the environment, applied to an
// environment its caller gives, and without the settings check. Both run the first git
// on the PATH whose path, with its symbolic links resolved, lies in no recorded place,
// by that resolved path; a hard link to a file in a place is not seen.
//
// The check reads the configuration files without letting git act on them, and accepts
// the keys qory and git write, with any value, and the settings the place held when it
// was first recorded. A remote's URL is accepted only as the fingerprint holds it, and
// a remote's other keys only when the configuration gives it a URL. Any other key is a
// [*RefusedError], and qory runs no git there. So is a configuration git cannot read,
// and a .git that points somewhere other than it did when the place was recorded. An
// include is accepted only when it was there then and names a file outside every
// recorded place.
//
// The check runs before every command. A repository whose files are byte for byte what
// the last full check in this process read is not read again. The places the check
// finds no walled run using stay held until git has run, so that a walled run that
// starts in one waits for it.
package hostgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/places"
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
// the PATH's empty and relative entries taken out, and every entry that lies in a
// recorded place, as written or with its links resolved, /usr/bin:/bin when none is left
// or there is no PATH, and with the pager and the editors set to programs that do
// nothing. git finds its helpers and the programs it runs through the PATH, such as ssh:
// an empty or relative entry is taken from the working folder, which is the checkout, an
// entry in a recorded place holds what a walled run could have written, and a shell git
// starts with no PATH takes a default one, which may end in the working folder. When
// qory cannot read the records, the PATH is /usr/bin:/bin.
func Env() []string { return envFrom(os.Environ()) }

// envFrom is [Env] with base in place of this process's environment.
func envFrom(base []string) []string {
	recs, err := places.Cached()
	var env []string
	path := false
	for _, kv := range base {
		name, value, _ := strings.Cut(kv, "=")
		if drop(name) {
			continue
		}
		if name == "PATH" {
			kept := fallbackPath
			if err == nil {
				kept = absoluteEntries(value, recs)
			}
			kv, path = name+"="+kept, true
		}
		env = append(env, kv)
	}
	if !path {
		env = append(env, "PATH="+fallbackPath)
	}
	return append(env, quiet...)
}

// absoluteEntries is the PATH value path with only its absolute entries that lie in no
// place of recs, as written or with their links resolved, in order; with none,
// [fallbackPath], since a shell git starts may search its working folder for an empty
// PATH.
func absoluteEntries(path string, recs []places.Record) string {
	var kept []string
	for _, dir := range filepath.SplitList(path) {
		if filepath.IsAbs(dir) && len(places.Containing(recs, dir)) == 0 {
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

// Command returns git with args, to run in dir, after the settings check of dir's
// repository. A caller sets the output, adds to Env and runs it, by [Cmd.Run],
// [Cmd.Output] or [Cmd.CombinedOutput], or releases it unrun by [Cmd.Release]. The
// error is a [*RefusedError] when the check refuses, a state directory qory cannot take
// among the refusals, or the error that kept the check from reading a record of the
// places.
func Command(ctx context.Context, dir string, args ...string) (*Cmd, error) {
	if err := Ready(ctx); err != nil {
		return nil, err
	}
	h, err := check(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &Cmd{Cmd: command(ctx, dir, args...), hold: h}, nil
}

// Cmd is git that [Command] returns, with the places its check found no walled run
// using held until git has run, or the Cmd is released.
type Cmd struct {
	*exec.Cmd
	hold *places.Hold
}

// Run runs git as [exec.Cmd.Run] does, and then releases the places.
func (c *Cmd) Run() error {
	defer c.Release()
	return c.Cmd.Run()
}

// Output runs git as [exec.Cmd.Output] does, and then releases the places.
func (c *Cmd) Output() ([]byte, error) {
	defer c.Release()
	return c.Cmd.Output()
}

// CombinedOutput runs git as [exec.Cmd.CombinedOutput] does, and then releases the
// places.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	defer c.Release()
	return c.Cmd.CombinedOutput()
}

// Release releases the places the check holds for git, for a Cmd that is not run.
func (c *Cmd) Release() { c.hold.Release() }

// command is git with the flags and environment of [Command], without the check: the
// git [gitIn] finds on the PATH of [Env], never one on this process's PATH that Env
// leaves out.
func command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return commandIn(ctx, dir, Env(), args...)
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
// in none: [Command] without the settings check. Its environment is base, as [Env]
// takes this process's: the git it runs is the one the PATH of base finds after Env's
// rules, and it stops, as Command does, on a git older than 2.36.
func Unchecked(ctx context.Context, dir string, base []string, args ...string) (*exec.Cmd, error) {
	env := envFrom(base)
	if err := ready(ctx, env); err != nil {
		return nil, err
	}
	return commandIn(ctx, dir, env, args...), nil
}

// gitIn returns the git that the PATH of env finds: the first entry that holds a file
// named git that is regular and may be run, and whose path, with every symbolic link of
// the entry and of the file resolved, lies in no recorded place. It returns that
// resolved path, so git runs the file it checked: a symbolic link a walled run could
// point elsewhere after the check is not followed again. A git that resolves into a
// recorded place runs what a walled run could have written there, and is passed over;
// when the PATH holds no other git, the entries of [fallbackPath] are searched the same
// way, as [Env] gives git that PATH when it leaves out every entry. A hard link is not
// seen. When qory cannot read the records, no git resolves to a place: [Env] then gives
// git only [fallbackPath]. With none, the error is an [*exec.Error] for
// [exec.ErrNotFound].
func gitIn(env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = value
		}
	}
	recs, _ := places.Cached()
	skipped := false
	find := func(path string) string {
		for _, dir := range filepath.SplitList(path) {
			file := filepath.Join(dir, "git")
			info, err := os.Stat(file)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
				continue
			}
			real, err := filepath.EvalSymlinks(file)
			if err != nil || !filepath.IsAbs(real) || len(places.Containing(recs, real)) > 0 {
				skipped = true
				continue
			}
			return real
		}
		return ""
	}
	if file := find(path); file != "" {
		return file, nil
	}
	if skipped {
		if file := find(fallbackPath); file != "" {
			return file, nil
		}
	}
	return "git", &exec.Error{Name: "git", Err: exec.ErrNotFound}
}
