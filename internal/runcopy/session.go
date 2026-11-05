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
// the message of the commit it makes, "" for [commitMessage]. When HEAD of the copy is past
// the base commit, the bundle has HEAD, so an agent's commits on another branch keep their
// messages. When HEAD is at the base commit and branch is past it, or when HEAD has no commit or
// does not descend from the base commit, the bundle has branch as it is and the script makes no
// commit, since the working tree is at HEAD: a commit would undo branch's files. Work that the
// bundle does not have writes n.left, which [Dir.LeftBehind] reads: a change in such a working
// tree; a commit in a ref the clone did not write, such as a branch, a tag or refs/stash; HEAD, a
// commit made with HEAD detached, or, with nothing past the base commit, any commit a reflog has,
// unless a remote-tracking ref has it; a submodule off its gitlink or with a change; a repository
// the run made; or a line of git add for what it could not add. The person's personal ignore
// rules stay in effect through the info/exclude of that git folder.
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
		refs := filepath.Join(d.Bundle(), "refs-"+strconv.Itoa(n))
		if err := writeNew(refs, repos[n].refs); err != nil {
			return GitSession{}, fileErr(refs, err)
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
// repositories. For the repository number n it sources list-n, see [sessionList], reads refs-n,
// see [Repository], and uses exclude-n when there is one. It writes n.bundle, or n.none when nothing is past the base
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
	GIT_NO_LAZY_FETCH=1 GIT_ALLOW_PROTOCOL=none LC_ALL=C
mkdir "$HOME"
g() {
	git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false -c core.safecrlf=false "$@"
}
nl='
'
cr=$(printf '\r')
# kind is exec for a file with the owner's execute bit, as git and SetupPath.State read it, and
# file for any other. [ -x ] tests what the process may do, which differs for root or for another
# user. $execs is what one find listed, since a process per file takes minutes for 10000 paths.
kind() {
	case $execs in
	*"$nl./$1$nl"*) printf exec ;;
	*) printf file ;;
	esac
}
# copygit is git with the copy's .git as git dir, so packed refs and reftable both work. It reads
# the copy's configuration, inside the wall.
copygit() {
	g --git-dir="$T/.git" "$@"
}
# past reports whether a commit descends from the base commit and is not that commit.
past() {
	[ -n "$1" ] && [ "$1" != "$base" ] && copygit merge-base --is-ancestor "$base" "$1" 2>/dev/null
}
# lacking prints the lines of the file $1 that the file $2 does not have, and every line when grep
# fails, so a failure keeps the copy.
lacking() {
	set +e
	grep -Fxv -f "$2" "$1" 2>/dev/null
	lacked=$?
	set -e
	if [ "$lacked" -gt 1 ]; then cat "$1"; fi
}
# printable is $1 with no control character, for a line of this script.
printable() {
	printf '%s' "$1" | tr -d '\000-\037\177' 2>/dev/null || printf 'a folder'
}
# submodule_at reports whether the submodule at the folder $1 of $T has the commit $2 at HEAD and
# no change. Its git reads the submodule's configuration, inside the wall.
submodule_at() (
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	cd "$T/$1"
	now=$(g --git-dir="$T/$1/.git" rev-parse --verify --quiet "HEAD^{commit}") || exit 1
	[ "$now" = "$2" ] || exit 1
	changes=$(g --git-dir="$T/$1/.git" --work-tree=. status --porcelain --ignore-submodules=none) || exit 1
	[ -z "$changes" ]
)
# state prints what the repository at $T has at a path, in the form of SetupPath.State.
state() {
	f=$T/$1
	if [ -h "$f" ]; then
		t=$(readlink "$f")
		printf 'link:%s' "$(printf '%s' "$t" | g hash-object --no-filters --stdin)"
	elif [ -f "$f" ]; then
		printf '%s:%s' "$(kind "$1")" "$(g hash-object --no-filters -- "$f")"
	elif [ -e "$f" ]; then
		printf other
	else
		printf missing
	fi
}
n=0
while [ "$n" -lt "$count" ]; do
	. "$B/list-$n"
	if [ "$rel" = . ]; then T=$W shown="the copy"; else T=$W/$rel shown="the copy of $rel"; fi
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

	# head is what the bundle has: HEAD when it is past the base commit, so commits on another
	# branch come back with their messages, or HEAD at the base commit with the branch not past it.
	# In any other case it is the branch, and moved is set: the working tree is at HEAD, a commit
	# from there would undo the branch's files, so the session makes none, and a change stays behind.
	at=$(copygit rev-parse --verify --quiet "HEAD^{commit}") || at=
	on=$(copygit rev-parse --verify --quiet "refs/heads/$branch^{commit}") || on=
	head=$at
	moved=
	if ! past "$at" && { [ "$at" != "$base" ] || past "$on"; }; then
		head=$on
		moved=1
	fi
	case $head in
	'' | *[!0-9a-f]*)
		echo "qory: neither HEAD nor $branch in $shown is a commit, so the session commits nothing" >&2
		exit 1
		;;
	esac
	if [ ${#head} -ne ${#base} ]; then
		echo "qory: git read a commit id of ${#head} characters in $shown, and the base commit has ${#base}, so the session commits nothing" >&2
		exit 1
	fi
	# A commit that the bundle does not have stays behind: one in a ref the clone did not write,
	# such as a branch, a tag or refs/stash; HEAD; or one that HEAD's reflog has and no branch's
	# reflog has, which is a commit made with HEAD detached. A rebase's steps do not count, since
	# the rebase's result replaces them. A commit in a remote-tracking ref is at its remote.
	copygit for-each-ref --no-merged "$head" --format='%(objectname) %(refname)' > "$G/tips-now" 2>/dev/null || : > "$B/$n.left"
	lacking "$G/tips-now" "$B/refs-$n" > "$G/new"
	while read -r tip ref; do
		printf '%s\n' "$tip"
	done < "$G/new" > "$G/tips"
	if [ -n "$moved" ] && [ -n "$at" ]; then
		printf '%s\n' "$at" >> "$G/tips"
	fi
	copygit for-each-ref --format='%(refname)' refs/heads/ > "$G/names" 2>/dev/null || :
	: > "$G/on-branch"
	while IFS= read -r ref; do
		copygit rev-list -g "$ref" >> "$G/on-branch" 2>/dev/null || :
	done < "$G/names"
	{ copygit log -g --format='%H %gs' HEAD 2>/dev/null || :; } > "$G/reflog"
	while read -r tip what; do
		case $what in
		rebase*) ;;
		*) printf '%s\n' "$tip" ;;
		esac
	done < "$G/reflog" > "$G/moves"
	lacking "$G/moves" "$G/on-branch" >> "$G/tips"
	behind=$(copygit rev-list -1 --ignore-missing --stdin --not "$head" "$base" --remotes < "$G/tips" 2>/dev/null) || behind=1
	if [ -n "$behind" ]; then
		: > "$B/$n.left"
	fi
	export GIT_DIR="$G" GIT_WORK_TREE="$T" GIT_INDEX_FILE="$G/index"
	g cat-file -e "$head^{commit}"
	g update-ref "refs/heads/$branch" "$head"
	# The tree the working tree is compared with: HEAD, or an empty tree when HEAD has no commit.
	from=$at
	if [ -z "$from" ]; then
		from=$(g hash-object -t tree /dev/null)
	fi
	g read-tree "$from"
	# With moved set, the paths the commit sets back stay as HEAD has them, so they differ in nothing.
	back=$base
	if [ -n "$moved" ]; then
		back=$from
	fi

	# Add every change but .qory, each folder with a .git, and the paths left out.
	P=$G/add
	R=$G/restore
	printf ':/\0:(exclude,top,literal,icase).qory\0' > "$P"
	printf ':(top,literal,icase).qory\0' > "$R"
	: > "$G/folders"
	{ find . -path ./.git -prune -o -path ./.qory -prune -o -name .git -exec sh -c '
		list=$1
		shift
		for p do
			d=${p%/.git}
			printf ":(exclude,top,literal)%s\0" "${d#./}"
			printf "%s\n" "${d#./}" >> "$list"
		done' sh "$G/folders" {} + 2>/dev/null || :; } >> "$P"
	i=0
	while [ "$i" -lt "$links" ]; do
		printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
		printf ':(top,literal)%s\0' "$1" >> "$R"
		shift
		i=$((i + 1))
	done
	execs=
	if [ "$setup" -gt 0 ]; then
		execs=$nl$(find . -path ./.git -prune -o -type f -perm -100 -print 2>/dev/null || :)$nl
	fi
	# One git hashes the setup's regular files, since a process per file takes minutes for
	# 10000 paths. git strips a line end from each path it reads, so a path with a line break
	# or a final carriage return takes the slow way through state.
	i=0
	for a do
		if [ $((i % 2)) -eq 0 ] && [ ! -h "$T/$a" ] && [ -f "$T/$a" ]; then
			case $a in
			*"$nl"* | *"$cr") ;;
			*) printf '%s\n' "$T/$a" ;;
			esac
		fi
		i=$((i + 1))
	done > "$G/regular"
	g hash-object --no-filters --stdin-paths < "$G/regular" > "$G/ids"
	exec 3< "$G/ids"
	i=0
	known=$nl
	while [ "$i" -lt "$setup" ]; do
		f=$T/$1
		have=
		if [ ! -h "$f" ] && [ -f "$f" ]; then
			case $1 in
			*"$nl"* | *"$cr") ;;
			*)
				read -r id <&3
				have=$(kind "$1"):$id
				;;
			esac
		fi
		if [ -z "$have" ]; then
			have=$(state "$1")
		fi
		if [ "$have" = "$2" ]; then
			printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
			if [ "$2" = other ]; then
				known=$known$1$nl
			fi
		fi
		shift 2
		i=$((i + 1))
	done
	exec 3<&-
	# Each folder with a .git stays out of the commit. A submodule stays behind when it is not at
	# the commit of its gitlink or has a change. Any other such folder stays behind when the setup
	# did not make it and no ignore rule covers it: the clone makes no other.
	while IFS= read -r d; do
		entry=$(g ls-files -s -- ":(top,literal)$d" 2>/dev/null) || entry=
		shows=$(printable "$d")
		case $entry in
		"160000 "*)
			link=${entry#160000 }
			link=${link%% *}
			if ! submodule_at "$d" "$link"; then
				printf 'qory: the submodule %s in %s has commits or changes that the commit leaves out\n' "$shows" "$shown" >&2
				: > "$B/$n.left"
			fi
			;;
		*)
			case $known in
			*"$nl$d$nl"*) continue ;;
			esac
			if g check-ignore -q -- "./$d" 2>/dev/null; then
				continue
			fi
			printf 'qory: %s has a git repository at %s that the run made, and the commit leaves it out\n' "$shown" "$shows" >&2
			: > "$B/$n.left"
			;;
		esac
	done < "$G/folders"
	# git add exits 1 when a path left out here is also ignored, and it adds the rest all the
	# same. Any other failure exits 128.
	set +e
	g add -A --pathspec-from-file="$P" --pathspec-file-nul 2> "$G/add-errors"
	added=$?
	set -e
	case $added in
	0 | 1) ;;
	*)
		cat "$G/add-errors" >&2
		exit "$added"
		;;
	esac
	# Any other line is what git add could not add, such as a folder it cannot read, or an
	# embedded repository that the list above does not have. That work stays behind.
	block=
	while IFS= read -r line || [ -n "$line" ]; do
		case $block:$line in
		':The following paths are ignored by one of your .gitignore files:') block=paths ;;
		'paths:hint: '* | 'paths:Use -f'*) block=hint ;;
		paths:*) ;;
		'hint:hint: '*) ;;
		*)
			block=
			printf 'qory: git add left this out of the commit in %s: %s\n' "$shown" "$line" >&2
			: > "$B/$n.left"
			;;
		esac
	done < "$G/add-errors"
	g reset -q "$back" --pathspec-from-file="$R" --pathspec-file-nul

	set +e
	g diff --cached --quiet --no-ext-diff --ignore-submodules=none "$from" --
	changed=$?
	set -e
	case $changed:$moved in
	0:*) ;;
	1:?*) : > "$B/$n.left" ;;
	1:)
		GIT_AUTHOR_NAME=` + commitName + ` GIT_AUTHOR_EMAIL=` + commitEmail + ` \
		GIT_COMMITTER_NAME=` + commitName + ` GIT_COMMITTER_EMAIL=` + commitEmail + ` \
			g commit -q --no-verify --no-gpg-sign -m "$message"
		;;
	*) exit "$changed" ;;
	esac

	# The bundle: the branch without base and the commits before it.
	if [ "$(g rev-list --count "refs/heads/$branch" "^$base")" = 0 ]; then
		: > "$B/$n.none"
		# With nothing past the base commit, a commit that any reflog has stays behind too, such as
		# one that a reset took off the branch.
		gone=$(copygit rev-list -1 --reflog --not "$base" --remotes 2>/dev/null) || gone=1
		if [ -n "$gone" ]; then
			: > "$B/$n.left"
		fi
	else
		g bundle create -q "$B/$n.bundle.part" "refs/heads/$branch" "^$base"
		mv "$B/$n.bundle.part" "$B/$n.bundle"
	fi
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	n=$((n + 1))
done
`
