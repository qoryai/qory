package prebuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/devcontainer"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the seed step, inside the container. textRan and textNoCommand name a
// lifecycle command's member.
const (
	textRan          = prefix + "ran %s in %s"
	textNoCommand    = prefix + "%s is not set; nothing to run"
	textStored       = prefix + "moved " + Workspace + " to " + SeedPath + " in %s"
	textManaged      = "the image has something at " + ManagedSettingsPath + ", where Claude Code reads managed settings above every other source; no image is made with anything there"
	textHomeShared   = prefix + "HOME, %s, is not a folder the wall user writes in; the commands run with it all the same"
	textHomeRelative = "HOME, %s, is not an absolute path; the commands do not run with it"
	textHomeInTree   = "HOME, %s, is in " + Workspace + " or " + SeedPath + ", whose files the image keeps as the repository's tree; the commands do not run with it"
	textHooksTaken   = "the image has something at " + HooksPath + ", where a run mounts its hook socket's folder; nothing may be there"
	textSeedTaken    = SeedPath + " is not an empty folder; the seed step stores the tree there"
	textNoConfig     = Workspace + " holds no development container configuration"
	textNotMoved     = "%s could not be moved to " + SeedPath + ": %s"
	textOddInSeed    = "%s is neither a file, a folder nor a link, and is not moved to " + SeedPath
	containerEnvKey  = "containerEnv"
	remoteEnvKey     = "remoteEnv"
)

// ManagedSettingsPath is where Claude Code reads managed settings on Linux, above every
// other source of its settings. The image has nothing there, in any form.
const ManagedSettingsPath = "/etc/claude-code"

// SeedPaths are where the seed step works: [Workspace], [SeedPath], [HooksPath] and
// [ManagedSettingsPath] in the container. A test sets others.
type SeedPaths struct {
	Workspace, Seed, Hooks, ManagedSettings string
}

// ContainerPaths are the seed step's paths in the image's container.
var ContainerPaths = SeedPaths{Workspace: Workspace, Seed: SeedPath, Hooks: HooksPath, ManagedSettings: ManagedSettingsPath}

// Seed is the step the build runs inside a container of the image, as the wall
// user: it checks nothing is at the hooks' path or at the managed settings' path,
// before the commands and again after them, as [checkFree] has it, reads the configuration the repository
// copied to the workspace holds, runs its onCreateCommand, then its
// updateContentCommand, in the workspace, and moves every entry of the workspace into
// the seed folder, which leaves the workspace empty. It prints a line on out for each
// command and for the move, and the commands' output goes to log.
//
// The commands' environment is env, the container's, with containerEnv, then
// remoteEnv, added, each value's variables replaced as [devcontainer.Vars] has them:
// ${containerWorkspaceFolder} is the workspace, and ${containerEnv:NAME} reads env and
// what is added before it. A failed command is named, never its text. Their HOME is
// the last that environment sets: the image's, or one containerEnv or remoteEnv sets.
// A relative HOME, which the commands would read from the workspace, and a HOME in the
// workspace or the seed folder are refused before they run: their caches would become
// the repository's tree. A HOME the wall user cannot write in is a line, and the commands
// run all the same.
func Seed(ctx context.Context, p SeedPaths, env []string, out, log io.Writer) error {
	if err := checkFree(p); err != nil {
		return err
	}
	if entries, err := os.ReadDir(p.Seed); err != nil || len(entries) > 0 {
		return errors.New(textSeedTaken)
	}
	cfg, err := devcontainer.Load(p.Workspace)
	if err != nil {
		return err
	}
	if cfg == nil {
		return errors.New(textNoConfig)
	}
	vars := devcontainer.Vars{WorkspaceFolder: Workspace, WorkspaceFolderBasename: filepath.Base(Workspace), Env: slices.Clone(env)}
	for _, m := range []struct {
		member string
		values map[string]string
	}{{containerEnvKey, cfg.ContainerEnv}, {remoteEnvKey, cfg.RemoteEnv}} {
		added, err := expandEnv(vars, m.member, m.values)
		if err != nil {
			return err
		}
		vars.Env = append(vars.Env, added...)
	}
	if home, ok := lookupEnv(vars.Env, "HOME"); ok && home != "" {
		if !path.IsAbs(home) {
			return fmt.Errorf(textHomeRelative, integration.Printable(exports.Value(home, false)))
		}
		if inTree(home, p) {
			return fmt.Errorf(textHomeInTree, integration.Printable(exports.Value(home, false)))
		}
		if !writable(home) {
			fmt.Fprintf(out, textHomeShared+"\n", integration.Printable(exports.Value(home, false)))
		}
	}
	for _, c := range []devcontainer.Command{cfg.OnCreate, cfg.UpdateContent} {
		if c.Empty() {
			fmt.Fprintf(out, textNoCommand+"\n", c.Member)
			continue
		}
		expanded, err := c.Expand(vars)
		if err != nil {
			return err
		}
		took, err := devcontainer.Run(ctx, expanded, p.Workspace, vars.Env, log, log)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, textRan+"\n", c.Member, seconds(took))
	}
	start := time.Now()
	if err := moveEntries(p.Workspace, p.Seed); err != nil {
		return err
	}
	if err := checkFree(p); err != nil {
		return err
	}
	fmt.Fprintf(out, textStored+"\n", seconds(time.Since(start)))
	return nil
}

// checkFree refuses a container with anything at the hooks' path or at the managed
// settings' path: a folder, a file or a link, even one that leads nowhere, and a path
// the wall user cannot look at. The container is what is committed as the image, so
// the step checks before the commands and again after them, as the image will be.
func checkFree(p SeedPaths) error {
	if _, err := os.Lstat(p.Hooks); !errors.Is(err, fs.ErrNotExist) {
		return errors.New(textHooksTaken)
	}
	if _, err := os.Lstat(p.ManagedSettings); !errors.Is(err, fs.ErrNotExist) {
		return errors.New(textManaged)
	}
	return nil
}

// inTree reports whether home, an absolute path, is the workspace or the seed folder,
// or in one of them.
func inTree(home string, p SeedPaths) bool {
	home = path.Clean(home)
	for _, dir := range []string{p.Workspace, p.Seed} {
		if home == dir || strings.HasPrefix(home, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
}

// lookupEnv is the value env sets name to, the last when it sets it more than once.
func lookupEnv(env []string, name string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value, found = v, true
		}
	}
	return value, found
}

// writable reports whether this process can make a file in the folder dir: it makes
// one and removes it.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".qory-prebuild-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	return os.Remove(name) == nil
}

// expandEnv is values as NAME=value entries, in the order of their names, each value's
// variables replaced; member names the object for an error.
func expandEnv(vars devcontainer.Vars, member string, values map[string]string) ([]string, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []string
	for _, name := range names {
		v, err := vars.Expand(member+"."+integration.Printable(accesskey.ShownName(name, false)), values[name])
		if err != nil {
			return nil, err
		}
		out = append(out, name+"="+v)
	}
	return out, nil
}

// moveEntries moves every entry of from into to, which is empty. An entry is renamed;
// one the file system cannot rename across its layers, such as a folder of the image's
// below the container's layer, is copied, then removed.
func moveEntries(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src, dst := filepath.Join(from, e.Name()), filepath.Join(to, e.Name())
		err := rename(src, dst)
		if errors.Is(err, syscall.EXDEV) {
			err = copyTree(src, dst)
			if err == nil {
				err = removeTree(src)
			}
		}
		if err != nil {
			return fmt.Errorf(textNotMoved, integration.Printable(exports.HidePath(e.Name())), integration.Printable(reason(err)))
		}
	}
	return nil
}

// rename renames src to dst. A folder without write permission cannot be moved to
// another folder, since its .. entry changes, as a command such as go mod download
// leaves one: it is made writable for the rename, and gets its bits back after.
func rename(src, dst string) error {
	err := os.Rename(src, dst)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	info, lerr := os.Lstat(src)
	if lerr != nil || !info.IsDir() || info.Mode().Perm()&0o200 != 0 {
		return err
	}
	if os.Chmod(src, info.Mode().Perm()|0o200) != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Chmod(src, info.Mode().Perm())
		return err
	}
	return os.Chmod(dst, info.Mode().Perm())
}

// reason is err without the path a [fs.PathError] or [os.LinkError] names.
func reason(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		return pe.Err.Error()
	case errors.As(err, &le):
		return le.Err.Error()
	}
	return err.Error()
}

// copyTree copies the folder, file or link at src to dst, keeping permission bits; a
// folder is made writable while it is filled and gets its bits after.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.Mode().IsRegular():
		return copyRegular(src, dst, info.Mode().Perm())
	case info.IsDir():
		if err := os.Mkdir(dst, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())
	}
	return fmt.Errorf(textOddInSeed, integration.Printable(exports.HidePath(filepath.Base(src))))
}

// copyRegular copies the regular file src to dst, a new file of mode perm.
func copyRegular(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// removeTree removes path and what it holds, making each folder writable first, since
// a command may leave folders without write permission, as Go's module cache does.
func removeTree(path string) error {
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// seconds is d as a line has it: seconds, with one decimal.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}
