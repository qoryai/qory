package preparation

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/devcontainer"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the start commands. textStart is a line of [devcontainer], which names
// the configuration or the command, never a command's text or a value.
const (
	textStart = "qory run prepare: %s"
	textRan   = "qory run prepare: ran %s in %s"
	// textEnvVariable is a variable of containerEnv or remoteEnv, where a failure to
	// replace the variables in its value is.
	textEnvVariable = "%s's variable %s"
	// textEnvName is a member, containerEnv or remoteEnv, with a name no environment
	// variable can have.
	textEnvName = "qory run prepare: %s has a variable whose name is empty or holds = or a NUL byte, which no environment variable's name can"
	// textUntracked is a configuration in the working folder that the run's commit
	// does not hold, one the image's copy left among its untracked or ignored files.
	textUntracked = "qory run prepare: %s is not in the run's commit; no start command runs from it"
	// The refusals of a configuration the run's commit holds, in [devcontainer]'s words
	// for a file of the working tree.
	textTrackedLink    = "qory run prepare: %s is a symbolic link; the development container configuration is read only from files and folders of the repository"
	textTrackedNotDir  = "qory run prepare: %s is not a folder; the development container configuration is read only from files and folders of the repository"
	textTrackedNotFile = "qory run prepare: %s is not a regular file; the development container configuration is read only from a regular file"
	textTrackedTooBig  = "qory run prepare: %s is more than 1 MiB; a development container configuration is a few kilobytes"
)

// Start runs the start commands of the development container configuration of the
// run's commit, the commit checked out in dir, as [trackedConfig] reads it:
// postCreateCommand, then postStartCommand, the two a prebuilt image's build leaves to
// the run. With no configuration, or neither command, it runs nothing.
//
// Each command runs in dir, with standard input /dev/null, and output as both its
// stdout and its stderr, handed to the command's processes as it is when it is a file.
// The caller passes its stderr: a service a postStartCommand leaves running writes
// there, and never into the stdout that carries what the agent prints. Its
// variables are those of [devcontainer.Vars]: ${containerWorkspaceFolder} is dir,
// ${containerWorkspaceFolderBasename} its base name, and ${containerEnv:NAME} reads
// environ, then containerEnv. The commands' environment is environ, then containerEnv,
// then remoteEnv, each setting a variable over the one before, and a remoteEnv
// variable whose value is null unset; the variables in their values are replaced the
// same way, but ${containerEnv:NAME} in containerEnv reads environ alone. A name that
// is empty or holds = or a NUL byte is refused, naming the member. Both commands'
// variables are replaced before either runs.
//
// The first command that fails stops the preparation: its error names it, and never
// its text. When the commands ran, Start prints on output the line that says which,
// and in how long together. When ctx ends, the commands stop as [devcontainer.Run]
// stops them.
func Start(ctx context.Context, dir string, environ []string, output io.Writer) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return local(err)
	}
	cfg, err := trackedConfig(ctx, dir, output)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	vars := devcontainer.Vars{WorkspaceFolder: dir, WorkspaceFolderBasename: filepath.Base(dir), Env: environ}
	containerEnv, err := expandEnv("containerEnv", cfg.ContainerEnv, vars)
	if err != nil {
		return err
	}
	vars.Env = setEnv(environ, containerEnv)
	remoteEnv, err := expandEnv("remoteEnv", cfg.RemoteEnv, vars)
	if err != nil {
		return err
	}
	if err := checkNames("remoteEnv", cfg.RemoteUnset); err != nil {
		return err
	}
	env := slices.DeleteFunc(setEnv(vars.Env, remoteEnv), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(cfg.RemoteUnset, name)
	})
	var commands []devcontainer.Command
	for _, c := range []devcontainer.Command{cfg.PostCreate, cfg.PostStart} {
		if c.Empty() {
			continue
		}
		expanded, err := c.Expand(vars)
		if err != nil {
			return fmt.Errorf(textStart, err)
		}
		commands = append(commands, expanded)
	}
	if len(commands) == 0 {
		return nil
	}
	var took time.Duration
	var ran []string
	for _, c := range commands {
		d, err := devcontainer.Run(ctx, c, dir, env, output, output)
		took += d
		if err != nil {
			return fmt.Errorf(textStart, err)
		}
		ran = append(ran, c.Member)
	}
	fmt.Fprintf(output, textRan+"\n", strings.Join(ran, " and "), seconds(took))
	return nil
}

// expandEnv is the variables of vars replaced in each value of env, the member member,
// as NAME=value entries in the order of their names, once [checkNames] took the names.
func expandEnv(member string, env map[string]string, vars devcontainer.Vars) ([]string, error) {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)
	if err := checkNames(member, names); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		value, err := vars.Expand(fmt.Sprintf(textEnvVariable, member, integration.Printable(accesskey.ShownName(name, false))), env[name])
		if err != nil {
			return nil, fmt.Errorf(textStart, err)
		}
		out = append(out, name+"="+value)
	}
	return out, nil
}

// setEnv is env without the entries of each NAME that set sets, then set's NAME=value
// entries, in set's order.
func setEnv(env, set []string) []string {
	out := slices.Clone(env)
	for _, kv := range set {
		name, _, _ := strings.Cut(kv, "=")
		out = slices.DeleteFunc(out, func(e string) bool {
			n, _, _ := strings.Cut(e, "=")
			return n == name
		})
		out = append(out, kv)
	}
	return out
}

// trackedConfig is the configuration of the repository in dir as its commit HEAD holds
// it, read with git and parsed as [devcontainer.Parse] parses it, or nil when the
// commit holds none. The places are [devcontainer.Places], in their order, and the
// first the commit holds is read. A place the commit does not hold but the working tree
// has, an untracked or ignored file the image's copy left, is never read: trackedConfig
// prints [textUntracked] for it on output. As [devcontainer.Find] refuses them, a place
// with a symbolic link or a file that is not a folder on the way, one that is not a
// regular file, and one of more than [devcontainer.MaxSize] bytes are refused.
func trackedConfig(ctx context.Context, dir string, output io.Writer) (*devcontainer.Config, error) {
	for _, place := range devcontainer.Places {
		oid, err := trackedFile(ctx, dir, place)
		if err != nil {
			return nil, err
		}
		if oid == "" {
			if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(place))); err == nil {
				fmt.Fprintf(output, textUntracked+"\n", place)
			}
			continue
		}
		size, err := git(ctx, dir, "cat-file", "-s", oid)
		if err != nil {
			return nil, gitFailed(err)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil || n > devcontainer.MaxSize {
			return nil, fmt.Errorf(textTrackedTooBig, place)
		}
		data, err := git(ctx, dir, "cat-file", "blob", oid)
		if err != nil {
			return nil, gitFailed(err)
		}
		cfg, err := devcontainer.Parse(place, []byte(data))
		if err != nil {
			return nil, fmt.Errorf(textStart, err)
		}
		return cfg, nil
	}
	return nil, nil
}

// trackedFile is the object name of the regular file at place, slash-separated, in the
// commit HEAD of the repository in dir, or "" when the commit holds nothing there. It
// walks place segment by segment, and refuses a symbolic link, a segment before the
// last that is not a folder, and a last that is not a regular file.
func trackedFile(ctx context.Context, dir, place string) (string, error) {
	segments := strings.Split(place, "/")
	for i := range segments {
		at := strings.Join(segments[:i+1], "/")
		out, err := git(ctx, dir, "ls-tree", "-z", "HEAD", "--", at)
		if err != nil {
			return "", gitFailed(err)
		}
		mode, kind, oid, found := "", "", "", false
		for _, entry := range strings.Split(out, "\x00") {
			info, path, ok := strings.Cut(entry, "\t")
			fields := strings.Fields(info)
			if ok && path == at && len(fields) == 3 {
				mode, kind, oid, found = fields[0], fields[1], fields[2], true
			}
		}
		last := i == len(segments)-1
		switch {
		case !found:
			return "", nil
		case mode == "120000":
			return "", fmt.Errorf(textTrackedLink, at)
		case !last && kind != "tree":
			return "", fmt.Errorf(textTrackedNotDir, at)
		case last && (kind != "blob" || (mode != "100644" && mode != "100755")):
			return "", fmt.Errorf(textTrackedNotFile, at)
		case last:
			return oid, nil
		}
	}
	return "", nil
}

// checkNames refuses a name of names, variables of the member member, that is empty or
// holds = or a NUL byte, naming the member alone.
func checkNames(member string, names []string) error {
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf(textEnvName, member)
		}
	}
	return nil
}
