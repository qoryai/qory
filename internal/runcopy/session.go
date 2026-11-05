package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/wall"
)

// SessionLimit is the longest the git session may run; the caller that starts the
// session stops it then. A program the agent planted in the copy can run again in that
// session, under the run's egress policy, so the session ends after this time.
const SessionLimit time.Duration = 5 * time.Minute

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

// LeftOut is what the git session leaves out of the commit it makes in one repository,
// besides .qory and every folder with a .git in it. Each path is relative to the top of
// that repository, with forward slashes.
type LeftOut struct {
	// Links are the links qory wrote, as render.OwnLink finds them. The commit also
	// sets each back to what the base commit has, as the push step does.
	Links []string
	// Setup are the paths the setup changed, as [Dir.SetupChanges] lists them, each with what
	// the setup left there, as [Dir.SetupStates] reads it. As in the push step, a path
	// stays out only while the copy still has what the setup left; a path the agent
	// changed after the setup goes into the commit.
	Setup []SetupPath
}

// SetupPath is a path the setup changed, relative to the top of its repository with
// forward slashes, and what the setup left there.
type SetupPath struct {
	Path string
	// State is what the setup left at Path: missing; other, for a folder or a file that
	// is neither regular nor a link; or file:, exec: or link: and the git blob id of the
	// content or of the link's target. exec: is a regular file with the owner's execute
	// bit.
	State string
}

// stateForm is the form of [SetupPath.State].
var stateForm = regexp.MustCompile(`^(?:missing|other|(?:file|exec|link):(?:[0-9a-f]{40}|[0-9a-f]{64}))$`)

// GitSession is the bare session that runs after the agent: the program, its arguments
// and its environment, for a Forager SessionSpec with no Runtime. The arguments and the
// files in the bundle folder contain every path and value the script reads, so the script
// does not depend on the environment the session passes.
type GitSession struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// GitSession is the session that commits what the agent left in each repository of
// repos, as [Dir.Clone] returned them, on branch, and writes branch, without the
// repository's base commit and the commits before it, to a bundle per repository that
// [Dir.Quarantine] reads. out is what each repository leaves out, by
// [Repository.Path]. GitSession makes [Dir.Bundle], an empty folder that the session
// alone binds.
//
// For each repository, the script makes a git folder in the bundle folder, with an
// empty configuration, no hooks and the copy's objects as its alternates, and runs git
// with that folder as GIT_DIR and the repository as GIT_WORK_TREE. So no setting of the
// copy's .git applies to the add, the commit or the bundle, and a filter that
// .gitattributes lists has no driver. One git reads the branch's commit with the
// copy's .git, and its configuration, as git dir: inside the wall that git has no more
// access than the agent had. The script starts the index from that commit, adds every
// change but [LeftOut]'s, and commits when anything changed. With no commit past the
// base commit, it writes a file that records that, in place of the bundle. message is
// the message of the commit it makes, "" for [commitMessage]. When HEAD of the copy descends
// from the base commit, the bundle has HEAD, so an agent's commits on another branch keep
// their messages; otherwise it has branch. The person's personal ignore rules stay in effect
// through the info/exclude of that git folder.
//
// The script runs no filter: a file a filter changed at the checkout, such as a Git LFS
// file, stays out while the copy has what the checkout wrote, and one the agent changed
// goes into the commit as its content in the copy.
func (d Dir) GitSession(ctx context.Context, branch, message string, repos []Repository, out map[string]LeftOut) (GitSession, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return GitSession{}, err
	}
	if message == "" {
		message = commitMessage
	}
	args := []string{"-c", sessionScript, "qory-git-session", sessionPath, d.Work(), d.Bundle(), branch, message, strconv.Itoa(len(repos))}
	lists := make([]string, len(repos))
	for n, r := range repos {
		list, err := sessionList(r, out[r.Path])
		if err != nil {
			return GitSession{}, err
		}
		lists[n] = list
	}
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return GitSession{}, fmt.Errorf(textExists, shown(d.Bundle()))
		}
		return GitSession{}, fileErr(d.Bundle(), err)
	}
	for n, list := range lists {
		p := filepath.Join(d.Bundle(), "list-"+strconv.Itoa(n))
		if err := writeNew(p, list); err != nil {
			return GitSession{}, fileErr(p, err)
		}
		if len(repos[n].excludes) > 0 {
			p := filepath.Join(d.Bundle(), "exclude-"+strconv.Itoa(n))
			if err := writeNew(p, string(repos[n].excludes)); err != nil {
				return GitSession{}, fileErr(p, err)
			}
		}
	}
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

// sessionList is the file the script sources for the repository r: shell assignments of its
// folder, its base commit and the counts, then set -- with the links and each setup path
// followed by its state. A file keeps 10,000 paths out of the arguments of sh, which the
// system limits, and a shell builtin takes any number. Each value is in single quotes.
func sessionList(r Repository, o LeftOut) (string, error) {
	rel, err := repositoryRel(r)
	if err != nil {
		return "", err
	}
	if !objectID.MatchString(r.Base) {
		return "", fmt.Errorf(textBase, shown(rel), shown(r.Base))
	}
	setup := slices.Concat(o.Setup, r.filtered)
	var b strings.Builder
	fmt.Fprintf(&b, "rel=%s base=%s links=%d setup=%d\nset --", shellQuote(rel), r.Base, len(o.Links), len(setup))
	for _, p := range o.Links {
		if !cleanRel(p) {
			return "", fmt.Errorf(textLeftOut, shown(p))
		}
		b.WriteString(" " + shellQuote(p))
	}
	for _, s := range setup {
		p := strings.TrimSuffix(s.Path, "/")
		if !cleanRel(p) {
			return "", fmt.Errorf(textLeftOut, shown(s.Path))
		}
		if !stateForm.MatchString(s.State) {
			return "", fmt.Errorf(textState, shown(s.State))
		}
		b.WriteString(" " + shellQuote(p) + " " + s.State)
	}
	b.WriteString("\n")
	return b.String(), nil
}

// shellQuote is value in single quotes, for sh.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// repositoryRel is the folder of r relative to the copy, as the script takes it: . for
// the person's repository, and a path under .qory/repositories for a further one.
func repositoryRel(r Repository) (string, error) {
	if r.Path == "" {
		return ".", nil
	}
	if !cleanRel(r.Path) || !strings.HasPrefix(r.Path, repositories+"/") {
		return "", fmt.Errorf(textRepoPath, shown(r.Path))
	}
	return r.Path, nil
}

// SessionMounts are the git session's binds: the copy and the bundle folder, both
// read-write, and nothing else.
func (d Dir) SessionMounts() []wall.Mount {
	return []wall.Mount{{Path: d.Work()}, {Path: d.Bundle()}}
}

// sessionScript is the git session's script, run with sh -c. Its arguments are the
// PATH, the copy, the bundle folder, the branch, the commit's message and the number of
// repositories. For the repository number n it sources list-n, see [sessionList], and uses
// exclude-n when there is one. It writes n.bundle, or n.none when nothing is past the base
// commit. It needs git 2.25 or later, for --pathspec-from-file.
const sessionScript = `set -eu
PATH=$1 W=$2 B=$3 branch=$4 message=$5 count=$6
export PATH
unset XDG_CONFIG_HOME GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_CONFIG_SYSTEM \
	GIT_EXEC_PATH GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_NAMESPACE GIT_TEMPLATE_DIR GIT_CEILING_DIRECTORIES GIT_SSH GIT_SSH_COMMAND \
	GIT_ASKPASS SSH_ASKPASS GIT_EXTERNAL_DIFF GIT_PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR \
	GIT_REPLACE_REF_BASE GIT_PROXY_COMMAND GIT_ATTR_SOURCE \
	GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
# GIT_ALLOW_PROTOCOL beats protocol.ext.allow in the copy's configuration, which beats -c
# protocol.allow=never. GIT_NO_LAZY_FETCH keeps git from fetching a missing object.
export HOME="$B/home" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
	GIT_NO_REPLACE_OBJECTS=1 GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 \
	GIT_NO_LAZY_FETCH=1 GIT_ALLOW_PROTOCOL=none
mkdir "$HOME"
g() {
	git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false "$@"
}
nl='
'
# state prints what the repository at $T has at a path, in the form of SetupPath.State.
state() {
	f=$T/$1
	if [ -h "$f" ]; then
		t=$(readlink "$f")
		printf 'link:%s' "$(printf '%s' "$t" | g hash-object --no-filters --stdin)"
	elif [ -f "$f" ]; then
		k=file
		if [ -x "$f" ]; then
			k=exec
		fi
		printf '%s:%s' "$k" "$(g hash-object --no-filters -- "$f")"
	elif [ -e "$f" ]; then
		printf other
	else
		printf missing
	fi
}
n=0
while [ "$n" -lt "$count" ]; do
	. "$B/list-$n"
	if [ "$rel" = . ]; then T=$W; else T=$W/$rel; fi
	G=$B/git-$n
	cd "$T"
	mkdir "$G" "$G/objects" "$G/objects/info" "$G/refs" "$G/refs/heads" "$G/info"
	if [ -f "$B/exclude-$n" ]; then
		cp "$B/exclude-$n" "$G/info/exclude"
	fi
	: > "$G/config"
	if [ ${#base} -eq 64 ]; then
		printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$G/config"
	fi
	printf '%s\n' "$T/.git/objects" > "$G/objects/info/alternates"
	printf 'ref: refs/heads/%s\n' "$branch" > "$G/HEAD"

	# HEAD when it descends from the base commit, so commits on another branch come back with
	# their messages; otherwise the branch. Read by git with the copy's .git, so packed refs and
	# reftable both work. This git reads the copy's configuration, inside the wall.
	head=$(g --git-dir="$T/.git" rev-parse --verify --quiet "HEAD^{commit}") || head=
	if [ -z "$head" ] || ! g --git-dir="$T/.git" merge-base --is-ancestor "$base" "$head" 2>/dev/null; then
		head=$(g --git-dir="$T/.git" rev-parse --verify --quiet "refs/heads/$branch^{commit}") || head=
	fi
	case $head in
	'' | *[!0-9a-f]*)
		echo "qory: check out $branch or a branch from it in the copy of $rel: HEAD and $branch are not at a commit past the base commit" >&2
		exit 1
		;;
	esac
	if [ ${#head} -ne ${#base} ]; then
		echo "qory: set the branch $branch of the copy of $rel to a commit id: git read something else there" >&2
		exit 1
	fi
	export GIT_DIR="$G" GIT_WORK_TREE="$T" GIT_INDEX_FILE="$G/index"
	g cat-file -e "$head^{commit}"
	g update-ref "refs/heads/$branch" "$head"
	g read-tree "refs/heads/$branch"

	# Add every change but .qory, each folder with a .git, and the paths left out.
	P=$G/add
	R=$G/restore
	printf ':/\0:(exclude,top,literal,icase).qory\0' > "$P"
	printf ':(top,literal,icase).qory\0' > "$R"
	{ find . -path ./.git -prune -o -path ./.qory -prune -o -name .git -exec sh -c '
		for p do
			d=${p%/.git}
			printf ":(exclude,top,literal)%s\0" "${d#./}"
		done' sh {} + 2>/dev/null || :; } >> "$P"
	i=0
	while [ "$i" -lt "$links" ]; do
		printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
		printf ':(top,literal)%s\0' "$1" >> "$R"
		shift
		i=$((i + 1))
	done
	# One git hashes the setup's regular files, since a process per file takes minutes for
	# 10000 paths. A path with a line break takes the slow way through state.
	i=0
	for a do
		if [ $((i % 2)) -eq 0 ] && [ ! -h "$T/$a" ] && [ -f "$T/$a" ]; then
			case $a in
			*"$nl"*) ;;
			*) printf '%s\n' "$T/$a" ;;
			esac
		fi
		i=$((i + 1))
	done > "$G/regular"
	g hash-object --no-filters --stdin-paths < "$G/regular" > "$G/ids"
	exec 3< "$G/ids"
	i=0
	while [ "$i" -lt "$setup" ]; do
		f=$T/$1
		have=
		if [ ! -h "$f" ] && [ -f "$f" ]; then
			case $1 in
			*"$nl"*) ;;
			*)
				read -r id <&3
				k=file
				if [ -x "$f" ]; then
					k=exec
				fi
				have=$k:$id
				;;
			esac
		fi
		if [ -z "$have" ]; then
			have=$(state "$1")
		fi
		if [ "$have" = "$2" ]; then
			printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
		fi
		shift 2
		i=$((i + 1))
	done
	exec 3<&-
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
			g commit -q --no-verify --no-gpg-sign -m "$message"
		;;
	*) exit "$changed" ;;
	esac

	# The bundle: the branch without base and the commits before it.
	if [ "$(g rev-list --count "refs/heads/$branch" "^$base")" = 0 ]; then
		: > "$B/$n.none"
	else
		g bundle create -q "$B/$n.bundle.part" "refs/heads/$branch" "^$base"
		mv "$B/$n.bundle.part" "$B/$n.bundle"
	fi
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	n=$((n + 1))
done
`
