package hookrun

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
)

// SetAfterCheck sets the function that runs once a private git directory is made, before git
// runs, and returns the function that sets the one before.
func SetAfterCheck(f func(dir string)) func() {
	was := afterCheck
	afterCheck = f
	return func() { afterCheck = was }
}

// SetAfterWalk sets the function that runs after the walk for repositories, before git add runs,
// and returns the function that sets the one before.
func SetAfterWalk(f func(dir string)) func() {
	was := afterWalk
	afterWalk = f
	return func() { afterWalk = was }
}

// SetChmod sets the function that sets the mode of a path in a restore, and returns the function
// that sets the one before.
func SetChmod(f func(root *os.Root, name string, mode fs.FileMode) error) func() {
	was := chmodAt
	chmodAt = f
	return func() { chmodAt = was }
}

// Same reports whether the checkout's tree is the snapshot's, as a restore checks it, with a new
// private git directory.
func (s *Snapshot) Same(ctx context.Context) (bool, error) {
	env, done, err := s.private(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	return s.same(ctx, env)
}

// AddAll is [addAll] in the repository at dir, with its .git and its index.
func AddAll(ctx context.Context, dir string) error { return addAll(ctx, gitCommand, dir, nil) }

// GitCommand is [gitCommand] with no variables added.
func GitCommand(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	return gitCommand(ctx, dir, nil, args...)
}

// RunScript is [runGit] in dir with args, where sh runs script in place of git.
func RunScript(ctx context.Context, dir, script string, args ...string) error {
	_, err := runGit(ctx, func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = dir
		return cmd, nil
	}, dir, nil, args...)
	return err
}

// FolderState is [folderState].
func FolderState(ctx context.Context, dir, name string) (string, error) {
	return folderState(ctx, dir, name)
}

// AddFile is [addFile] with the sum thrown away.
func AddFile(ctx context.Context, dir, path string, info fs.FileInfo) error {
	return addFile(ctx, io.Discard, dir, path, info)
}
