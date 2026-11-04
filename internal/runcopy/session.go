package runcopy

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/qoryai/forager/wall"
)

// SessionLimit is the longest the git session may run. A program the agent planted in
// the copy can run again in that session, under the run's egress policy, so the session
// ends after this time.
const SessionLimit = 5 * time.Minute

// sessionPath is the PATH of the git session's script: folders of the run's image,
// none of them in the copy, where git is found. A test sets it to the folder of this
// machine's git.
var sessionPath = "/usr/local/bin:/usr/bin:/bin"

// Commit author and committer of the commit the git session makes, and its message.
const (
	commitName    = "qory"
	commitEmail   = "qory@localhost"
	commitMessage = "Add the work the agent left uncommitted"
)

// LeftOut is what the git session leaves out of the commit it makes, besides .qory and
// every folder with a .git in it. Each path is relative to the top of the copy, with
// forward slashes.
type LeftOut struct {
	// Links are the links qory wrote, as render.OwnLink finds them. The commit also
	// sets each back to what the base commit has, as the push step does.
	Links []string
	// Setup are the paths the setup changed, as RecordSetup lists them. The git session
	// cannot compare a path with the setup's digest of it, so a path listed here stays
	// out even when the agent changed it after the setup.
	Setup []string
}

// GitSession is the bare session that runs after the agent: the program, its arguments
// and its environment, for a Forager SessionSpec with no Runtime. The arguments contain
// every path and value the script reads, so the script does not depend on the
// environment the session passes.
type GitSession struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// GitSession is the session that commits what the agent left in the copy on branch and
// writes branch, without base and the commits before it, to the bundle that
// [Dir.Quarantine] reads. base is the commit [Dir.Clone] returned for the person's
// repository. GitSession makes [Dir.Bundle], an empty folder that the session alone
// binds.
//
// The script makes a git folder in the bundle folder, with an empty configuration, no
// hooks and the copy's objects as its alternates, and runs every git with that folder
// as GIT_DIR and the copy as GIT_WORK_TREE. So no setting of the copy's .git applies,
// and a filter that .gitattributes lists has no driver. It reads the branch's commit
// from the copy's ref files with no git, starts the index from it, adds every change
// but [LeftOut]'s, and commits when anything changed. With no commit past base, it
// writes no bundle.
func (d Dir) GitSession(ctx context.Context, branch, base string, out LeftOut) (GitSession, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return GitSession{}, err
	}
	if !objectID.MatchString(base) {
		return GitSession{}, fmt.Errorf("give the base commit in full: %s is no commit id", shown(base))
	}
	for _, p := range append(append([]string{}, out.Links...), out.Setup...) {
		if !cleanRel(p) {
			return GitSession{}, fmt.Errorf("give each path left out of the run's commit relative to the top of the copy: %s is not such a path", shown(p))
		}
	}
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		return GitSession{}, err
	}
	args := []string{"-c", sessionScript, "qory-git-session", sessionPath, d.Work(), d.Bundle(), branch, base}
	args = append(append(append(args, out.Links...), "--"), out.Setup...)
	return GitSession{
		Command: "/bin/sh",
		Args:    args,
		Env: []string{
			"PATH=" + sessionPath, "HOME=" + d.Bundle() + "/home",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		},
		Dir: d.Work(),
	}, nil
}

// SessionMounts are the git session's binds: the copy and the bundle folder, both
// read-write, and nothing else.
func (d Dir) SessionMounts() []wall.Mount {
	return []wall.Mount{{Path: d.Work()}, {Path: d.Bundle()}}
}

// cleanRel reports whether p is a clean relative path with forward slashes that stays
// in the folder it is relative to.
func cleanRel(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && path.Clean(p) == p &&
		p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsRune(p, 0)
}

// sessionScript is the git session's script, run with sh -c. Its arguments are the
// PATH, the copy, the bundle folder, the branch, the base commit, the links qory wrote,
// --, and the paths the setup changed.
const sessionScript = `set -eu
PATH=$1 W=$2 B=$3 branch=$4 base=$5
shift 5
export PATH
cd "$W"
unset XDG_CONFIG_HOME GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_CONFIG_SYSTEM \
	GIT_EXEC_PATH GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_NAMESPACE GIT_TEMPLATE_DIR GIT_CEILING_DIRECTORIES GIT_SSH GIT_SSH_COMMAND \
	GIT_ASKPASS SSH_ASKPASS GIT_EXTERNAL_DIFF GIT_PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR \
	GIT_REPLACE_REF_BASE GIT_PROXY_COMMAND GIT_ATTR_SOURCE
G=$B/git
export GIT_DIR="$G" GIT_WORK_TREE="$W" GIT_INDEX_FILE="$G/index" HOME="$B/home" \
	GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_NO_REPLACE_OBJECTS=1 \
	GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0
g() {
	git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false "$@"
}
mkdir "$G" "$HOME" "$G/objects" "$G/objects/info" "$G/refs" "$G/refs/heads"
: > "$G/config"
if [ ${#base} -eq 64 ]; then
	printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$G/config"
fi
printf '%s\n' "$W/.git/objects" > "$G/objects/info/alternates"
printf 'ref: refs/heads/%s\n' "$branch" > "$G/HEAD"

# The branch's commit, from the copy's ref files: no git opens the copy's .git.
if [ ! -d "$W/.git" ] || [ -h "$W/.git" ]; then
	echo "qory: the copy has no .git folder" >&2
	exit 1
fi
head=
ref=$W/.git/refs/heads/$branch
if [ -f "$ref" ] && [ ! -h "$ref" ]; then
	head=$(cat "$ref")
elif [ -f "$W/.git/packed-refs" ] && [ ! -h "$W/.git/packed-refs" ]; then
	head=$(awk -v r="refs/heads/$branch" '$2 == r { print $1; exit }' "$W/.git/packed-refs")
fi
case $head in
'' | *[!0-9a-f]*)
	echo "qory: the copy has no commit on branch $branch" >&2
	exit 1
	;;
esac
if [ ${#head} -ne ${#base} ]; then
	echo "qory: the copy's branch $branch is set to no commit id" >&2
	exit 1
fi
g cat-file -e "$head^{commit}"
g update-ref "refs/heads/$branch" "$head"
g read-tree "refs/heads/$branch"

# Add every change but .qory, each folder with a .git, and the paths left out.
P=$G/add
R=$G/restore
printf ':/\0:(exclude,top,literal).qory\0' > "$P"
printf ':(top,literal).qory\0' > "$R"
(cd "$W" && find . -path ./.git -prune -o -path ./.qory -prune -o -name .git -exec sh -c '
	for p do
		d=${p%/.git}
		printf ":(exclude,top,literal)%s\0" "${d#./}"
	done' sh {} +) >> "$P"
links=1
for p do
	if [ "$links" = 1 ] && [ "$p" = -- ]; then
		links=0
		continue
	fi
	printf ':(exclude,top,literal)%s\0' "$p" >> "$P"
	if [ "$links" = 1 ]; then
		printf ':(top,literal)%s\0' "$p" >> "$R"
	fi
done
g add -A --pathspec-from-file="$P" --pathspec-file-nul
g reset -q "$base" --pathspec-from-file="$R" --pathspec-file-nul

set +e
g diff --cached --quiet --no-ext-diff --ignore-submodules=none "refs/heads/$branch" --
changed=$?
set -e
case $changed in
0) ;;
1)
	GIT_AUTHOR_NAME=` + commitName + ` GIT_AUTHOR_EMAIL=` + commitEmail + ` \
	GIT_COMMITTER_NAME=` + commitName + ` GIT_COMMITTER_EMAIL=` + commitEmail + ` \
		g commit -q --no-verify --no-gpg-sign -m "` + commitMessage + `"
	;;
*) exit "$changed" ;;
esac

# The bundle: the branch without base and the commits before it.
if [ "$(g rev-list --count "refs/heads/$branch" "^$base")" = 0 ]; then
	exit 0
fi
g bundle create -q "$B/` + bundleFile + `.part" "refs/heads/$branch" "^$base"
mv "$B/` + bundleFile + `.part" "$B/` + bundleFile + `"
`
