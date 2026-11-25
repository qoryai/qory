package preparation

import "context"

// SafeAfterRun is the check of the push step for a repository that qory fetched and an agent had
// open for writing, [safeAfterRun], for the hook runner: git on this machine reads the checkout's
// objects for a hook's program, and adds its files to an index for a snapshot, after the check.
func SafeAfterRun(ctx context.Context, dir string) error { return safeAfterRun(ctx, dir) }

// PrivateGitDir fills the empty folder private as a git directory of the checkout at dir, with
// HEAD at commit, for the hook runner: [privateGitDir] for a repository that an agent had open
// for writing. A git with GIT_DIR set to private reads the checked copies of the configuration,
// so a key that a process writes into dir's .git after this call has no effect.
func PrivateGitDir(ctx context.Context, dir, private, commit string) error {
	return privateGitDir(ctx, dir, dir, private, commit, true)
}
