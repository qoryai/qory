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

	"github.com/qoryai/forager/wall"
)

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
	// Links are the links into the copy's .qory folder, as render.OwnLink finds them. The commit also
	// sets each back to what the base commit has, as the push step does.
	Links []string
	// Setup are the paths the setup changed, as [Dir.SetupChanges] lists them, each with what
	// the setup left there, as [Dir.SetupStates] reads it. As in the push step, a path
	// stays out only while the copy still has what the setup left; a path the agent
	// changed after the setup goes into the commit.
	Setup []SetupPath
	// Submodules are the files the setup left in a submodule, as [Dir.SubmoduleSetup] lists them.
	// A change in a submodule that is one of these keeps nothing behind.
	Submodules []SetupPath
	// SubmodulesPartial is whether [Dir.SubmoduleSetup] left a folder of a submodule out of
	// Submodules, so the setup can have written a .gitignore there that Submodules does not list.
	SubmodulesPartial bool
	// SetupEnd is the second of the setup's end, as [Dir.Recorded] read it: a path that an ignore
	// rule of the setup covers and whose change time is past it keeps the copy. 0 is a run whose
	// setup can have written no ignore rules, or with no record, and -1 a setup whose end the
	// record session could not read.
	SetupEnd int64
}

// SetupPath is a path the setup changed, relative to the top of its repository with
// forward slashes, and what the setup left there.
type SetupPath struct {
	Path string
	// State is what the setup left at Path: missing; other, for a folder or a file that
	// is neither regular nor a link; file:, exec: or link: and the git blob id of the
	// content or of the link's target; or repo: and the blob id of a list of the HEAD,
	// refs, reflogs, .gitignore files and git status of a repository that the setup made,
	// as [Dir.RecordSession] reads it. exec: is a regular file with the owner's execute bit.
	State string
}

// stateForm is the form of [SetupPath.State].
var stateForm = regexp.MustCompile(`^(?:missing|other|(?:file|exec|link|repo):(?:[0-9a-f]{40}|[0-9a-f]{64}))$`)

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
// [Dir.Quarantine] reads, with the run's other refs under [Dir.RefPrefix]. out is what each
// repository leaves out, by [Repository.Path]. GitSession makes [Dir.Bundle], an empty folder
// that the session alone binds.
//
// For each repository, the script makes a git folder in the bundle folder, with an
// empty configuration, no hooks and the copy's objects as its alternates, and runs git
// with that folder as GIT_DIR and the repository as GIT_WORK_TREE. So no setting of the
// copy's .git applies to the add, the commit or the bundle, and a filter that
// .gitattributes lists has no driver. One git reads the branch's commit with the
// copy's .git, and its configuration, as git dir: inside the wall that git has no more
// access than the agent had. The script gives the session's user read access to each file
// and folder that user owns, starts the index from that commit, adds every change but
// [LeftOut]'s, and commits when anything changed. With nothing past the base commit, it writes
// a file that records that, in place of the bundle. message is the message of the commit it
// makes, "" for [commitMessage]. When HEAD of the copy is past the base commit, the bundle has
// HEAD, so an agent's commits on another branch keep their messages. When HEAD is at the base
// commit and branch is past it, or when HEAD has no commit or does not descend from the base
// commit, the bundle has branch as it is and the script commits the working tree on HEAD, as
// the ref uncommitted under the prefix, since a commit on branch would undo branch's files. The
// same goes for a sparse checkout in the copy, where each path the checkout took out and the
// working tree does not have stays as HEAD has it. HEAD and branch at one commit that the base commit does not have, such as an
// amended base commit, get a commit as usual.
//
// Under the prefix the bundle also has each branch, tag and refs/stash that the run made or
// moved, and a detached HEAD, when it has a commit that neither the base commit nor a ref at the end of
// the clone has, and lost/<n> for commits that only a reflog or another ref
// reaches. A new or moved branch or tag at a commit with no such commit, such as a tag of the base
// commit, is a line in n.known, its commit id and its name under the prefix, as a bundle leaves out
// a ref whose commits it excludes; an annotated one is in the bundle, with its tag object. Each
// submodule that the clone copied gets the same refs, with its changes as
// uncommitted, in the bundle n-k, where k is the line of the submodule in subs-n. A repository
// the run made stays behind and writes n.nested. Any other work that stays behind writes n.left;
// [Dir.Left] reads both: a change in a submodule that the clone did not copy, or in a repository
// inside a submodule, such as a submodule of a submodule with a change in its working tree; a file
// that git ls-files still lists after the add, such as one the session's user cannot read, which
// includes a path left out here that a commit of the run changed, when the copy has other bytes
// than that commit; a folder the session cannot read; a file at a link of [LeftOut.Links] that is no
// longer a link; a change in a repository the setup made; or any other path that the commit leaves
// out, which a last check lists after the commit. The person's personal ignore rules stay in effect
// through the info/exclude of that git folder. A path that an ignore rule covers counts as left out
// unless the person's rules cover it too: those rules and the .gitignore files of the base commit,
// with each .gitignore that the setup changed as the setup left it. A repository the setup made
// stays out, and stays behind when what repo_state reads there differs from its [SetupPath.State],
// which [Dir.RecordSession] read after the setup. A path that the setup's rules ignore, also those
// of a repository the setup made, and whose change time is past [LeftOut.SetupEnd], stays behind
// too, and n.ignored lists it, unless it is in a folder of [generatedFolders] or is the .git/index
// of a repository that has what HEAD there has. A folder the session cannot enter is one such
// path, with a final /, and a name with a line break writes n.left alone, as does a path with a
// line break in a list that git writes whose pieces other paths could cover, such as the ignored
// paths.
//
// The add leaves out each regular file of more than [MaxFile] bytes that it would add, in the
// repository and in each submodule, and lists its path, relative to the copy, in n.large, which
// [Dir.Left] reads. A commit the agent made keeps such a file: no commit is rewritten.
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
	script := filepath.Join(d.Bundle(), SessionScriptName)
	prefix := d.RefPrefix()
	if err := checkRef(ctx, prefix); err != nil {
		return GitSession{}, err
	}
	args := []string{script, sessionPath, d.Work(), d.Bundle(), branch, message, strconv.Itoa(len(repos)), prefix}
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
	if err := writeNew(script, sessionScript); err != nil {
		return GitSession{}, fileErr(script, err)
	}
	for n, list := range lists {
		p := filepath.Join(d.Bundle(), "list-"+strconv.Itoa(n))
		if err := writeNew(p, list); err != nil {
			return GitSession{}, fileErr(p, err)
		}
		subs := filepath.Join(d.Bundle(), "sub-"+strconv.Itoa(n))
		if err := writeNew(subs, submoduleList(out[repos[n].Path].Submodules)); err != nil {
			return GitSession{}, fileErr(subs, err)
		}
		refs := filepath.Join(d.Bundle(), "refs-"+strconv.Itoa(n))
		if err := writeNew(refs, repos[n].refs); err != nil {
			return GitSession{}, fileErr(refs, err)
		}
		cloned := filepath.Join(d.Bundle(), "subs-"+strconv.Itoa(n))
		if err := writeNew(cloned, clonedList(repos[n].submodules)); err != nil {
			return GitSession{}, fileErr(cloned, err)
		}
		for k, s := range repos[n].submodules {
			p := filepath.Join(d.Bundle(), "subrefs-"+strconv.Itoa(n)+"-"+strconv.Itoa(k))
			if err := writeNew(p, s.refs); err != nil {
				return GitSession{}, fileErr(p, err)
			}
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

// submoduleList is the file sub-n of the script: a state and a path per line.
func submoduleList(paths []SetupPath) string {
	var b strings.Builder
	for _, s := range paths {
		b.WriteString(s.State + " " + s.Path + "\n")
	}
	return b.String()
}

// clonedList is the file subs-n of the script: for each submodule the clone copied, its number,
// the commit the clone checked out, and its path, on one line. A path with a line break is left
// out, so the script finds no such submodule and keeps the copy for a change in it.
func clonedList(subs []submodule) string {
	var b strings.Builder
	for k, s := range subs {
		if strings.ContainsAny(s.path, "\n\r") || !objectID.MatchString(s.link) {
			continue
		}
		fmt.Fprintf(&b, "%d %s %s\n", k, s.link, s.path)
	}
	return b.String()
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
	fmt.Fprintf(&b, "rel=%s base=%s icase=%t links=%d setup=%d since=%d generated=%s\nset --",
		shellQuote(rel), r.Base, r.ignorecase, len(o.Links), len(setup), max(o.SetupEnd, -1), shellQuote(generatedNames()))
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

// SessionScriptName is the file in the bundle folder that has [sessionScript]. The host writes
// it for the session and never reads it back.
const SessionScriptName = "qory-git-session.sh"

// sessionScript is the git session's script, run with sh from a file, so that the session's
// line and its record have the file's path and no script text. Its arguments are the
// PATH, the copy, the bundle folder, the branch, the commit's message, the number of
// repositories and [Dir.RefPrefix]. For the repository number n it sources list-n, see
// [sessionList], reads refs-n, see [Repository], sub-n, see [submoduleList], subs-n, see
// [clonedList], and subrefs-n-k for its submodule number k, and uses exclude-n when there is
// one. It writes n.bundle, or n.none when nothing is past the base commit, and n-k.bundle or
// n-k.none for each submodule in subs-n, n.large for files it leaves out by their size, n.ignored for
// changes under the setup's ignore rules, and n.known or n-k.known for the run's refs at a commit the
// person has. Its
// find takes a size in bytes, with c, which busybox and GNU find both read: [MaxFile] bytes. It needs git 2.25 or later, for --pathspec-from-file.
const sessionScript = scriptFunctions + sessionMain

// scriptFunctions is the start of [sessionScript] and of [recordScript]: their settings and
// functions, with the same arguments.
const scriptFunctions = `set -eu
PATH=$1 W=$2 B=$3 branch=$4 message=$5 count=$6 prefix=$7
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
# g is git with settings that beat the copy's configuration and a submodule's, which the agent
# wrote: no pager, fsmonitor, replace ref, signature check, signing program or hook in a hooks
# folder runs. git 2.55 still runs a hook that such a configuration sets with
# hook.<name>.command, and has a key that turns hooks off for one event only, not for all. So
# each command run with such a configuration is a read that fires no hook event.
g() {
	git --no-pager -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false -c core.safecrlf=false \
		-c log.showSignature=false -c gpg.program=false -c gpg.openpgp.program=false \
		-c gpg.x509.program=false -c gpg.ssh.program=false "$@"
}
nl='
'
cr=$(printf '\r')
tab=$(printf '\t')
st=0
# kind is exec for a file with the owner's execute bit, as git and SetupPath.State read it, and
# file for any other. [ -x ] tests what the process may do, which differs for root or for another
# user. $execs is what one find listed, since a process per file takes minutes for 10000 paths.
kind() {
	case $execs in
	*"$nl./$1$nl"*) printf exec ;;
	*) printf file ;;
	esac
}
# copygit is git with the copy's git folder $C as git dir, so packed refs and reftable both work.
# It reads the copy's configuration, inside the wall, and the settings of g beat each key there
# that would run a program for the commands below, but hook.<name>.command: each command below
# reads refs, logs or the index and fires no hook event.
copygit() {
	g --git-dir="$C" "$@"
}
# past reports whether a commit descends from the base commit and is not that commit.
past() {
	[ -n "$1" ] && [ "$1" != "$base" ] && copygit merge-base --is-ancestor "$base" "$1" 2>/dev/null
}
# c_quoted prints $1 as a line of an alternates file that a line break in $1 does not split.
c_quoted() {
	rest=$1 line=
	while [ -n "$rest" ]; do
		c=${rest%"${rest#?}"}
		rest=${rest#?}
		case $c in
		\\ | \") line=$line\\$c ;;
		"$nl") line=$line\\n ;;
		*) line=$line$c ;;
		esac
	done
	printf '"%s"\n' "$line"
}
# broken reports whether the file $1, a list of paths each with a NUL, has a path with a line
# break, which the lines read from the list split into paths that other checks may cover.
broken() {
	tr -cd '\n' < "$1" > "$1-breaks" 2>/dev/null || return 0
	[ -s "$1-breaks" ]
}
# split_hidden reports whether a path of the NUL list $1 has a line break and a piece of it, as
# the lines read from $1 split it, is a line of the file $2, with or without a final /, or is below
# a folder of the file ${3-}: that piece counts as kept out on purpose, and the path with it. A path
# that $2-z, the NUL list of the paths with a line break in $2, has whole is kept out as itself. A
# \001 in a path counts as a line break too, and a failure returns 0.
split_hidden() {
	tr '\n\000' '\001\n' < "$1" > "$1-lines" 2>/dev/null || return 0
	{ grep "$(printf '\001')" "$1-lines" || :; } > "$1-broken" 2>/dev/null || return 0
	if [ ! -s "$1-broken" ]; then
		return 1
	fi
	: > "$1-whole"
	if [ -f "$2-z" ]; then
		tr '\n\000' '\001\n' < "$2-z" > "$1-whole" 2>/dev/null || return 0
	fi
	lacking "$1-broken" "$1-whole" | tr '\001' '\n' > "$1-split" 2>/dev/null || return 0
	while IFS= read -r h_p; do
		printf '%s\n' "$h_p" "${h_p%/}"
	done < "$1-split" > "$1-pieces"
	if [ ! -s "$1-pieces" ]; then
		return 1
	fi
	if [ -n "$(both "$1-pieces" "$2")" ]; then
		return 0
	fi
	if [ -n "${3-}" ]; then
		outside "$1-pieces" "$3" > "$1-outside"
		[ -n "$(lacking "$1-pieces" "$1-outside")" ] && return 0
	fi
	return 1
}
# lacking prints the lines of the file $1 that the file $2 does not have, and every line of $1 when
# sort or comm fails, so a failure keeps the copy. LC_ALL=C is what both sort by.
lacking() {
	if ! { sort -u "$1" > "$G/one" && sort -u "$2" > "$G/two" && comm -23 "$G/one" "$G/two"; } 2>/dev/null; then
		cat "$1"
	fi
}
# both prints the lines that the files $1 and $2 share, and every line of $1 when sort or comm fails,
# so a failure keeps the copy.
both() {
	if ! { sort -u "$1" > "$G/one" && sort -u "$2" > "$G/two" && comm -12 "$G/one" "$G/two"; } 2>/dev/null; then
		cat "$1"
	fi
}
# printable is $1 with no control character, for a line of this script.
printable() {
	printf '%s' "$1" | tr -d '\000-\037\177' 2>/dev/null || printf 'a folder'
}
# readable adds read access for the session's user to each file and folder below the current
# folder that lacks it, but the top .git and .qory, so git add reads them. chmod changes only what that
# user owns; any other stays as it is.
readable() {
	find . -path ./.git -prune -o -path ./.qory -prune -o -type d ! -perm -500 -exec chmod u+rx {} \; \
		-o ! -type d ! -type l ! -perm -400 -exec chmod u+r {} + 2>/dev/null || :
}
# submodule_status writes the changes of the submodule at the folder $1 of $T to $G/status, through
# the git folder $G/status-$st, $st being a number the caller raises for each call: see tree_status,
# which takes $2 and $3 as its fifth and sixth arguments.
submodule_status() (
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	tree_status "$T/$1" "$G/status" "$G/status-$st" 1 "${2-}" "${3-}"
)
# through_link reports whether the path $2 below the folder $1, or a folder on the way to it, is a
# link.
through_link() {
	l_at=$1 l_rest=$2
	while :; do
		l_at=$l_at/${l_rest%%/*}
		if [ -h "$l_at" ]; then
			return 0
		fi
		case $l_rest in
		*/*) l_rest=${l_rest#*/} ;;
		*) return 1 ;;
		esac
	done
}
# filled reports whether $1 is anything but an empty folder that the session can list: a folder
# with an entry or one it cannot list, or any other file. find lists the folder, as the image may
# have no ls, and ends at its next write after the read of the first entry. A find that fails
# prints a line in place of an entry, so the folder counts as not empty. With no find, the shell
# lists every entry.
filled() {
	if [ ! -d "$1" ] || [ ! -r "$1" ] || [ ! -x "$1" ]; then
		return 0
	fi
	if command -v find > /dev/null 2>&1; then
		if { find "$1/." ! -name . -prune -print 2>/dev/null || echo; } | { IFS= read -r f_e; }; then
			return 0
		fi
		return 1
	fi
	for f_e in "$1"/* "$1"/.[!.]* "$1"/..?*; do
		if [ -e "$f_e" ] || [ -h "$f_e" ]; then
			return 0
		fi
	done
	return 1
}
# tree_status writes to the file $2 the changes of the repository at the folder $1, which is $4
# submodules deep: what its working tree changed since its HEAD. As for the top repository, git
# runs through a new git folder $3 that gitfolder makes, with an empty configuration, the
# repository's objects as alternates, HEAD at the repository's commit and an index of its tree, so
# no program, hook, fsmonitor or filter that the agent wrote in the repository's configuration runs.
# A submodule inside it at another commit is a change. One at its commit with a .git gets the same
# status, in the git folder status-<k> of $3, k being its number: when that status has a change or
# fails, the submodule is a change in $2, and n.left is written. These gitlinks cannot be checked,
# so each is a change, and n.left is written: one whose path ls-files quotes; one whose path is a
# link or goes through one, since a link to . would repeat the status without end; one with no
# .git and a folder that is not empty or that the session cannot list, such as a nested
# submodule that no update populated; and one more than 8 submodules deep. With $5 set, each path
# that an ignore rule covers, which the .gitignore files of HEAD do not cover, is a change too, as
# ?? and its path: the run wrote or changed that rule. Such a repository and each one inside it
# get this check. With $6 set, a submodule with a change, in place of that change in $2 and n.left,
# adds its entries to $2, each with the submodule's path in front of its path: repo_state then
# compares them with what the setup left. A call in a condition ignores set -e, so each step that
# can fail exits 1 on its failure.
tree_status() (
	t_at=$1 t_out=$2 G=$3 t_depth=$4 t_rules=${5-} t_deep=${6-}
	cd "$t_at" || exit 1
	objects=$(g --git-dir=.git rev-parse --git-path objects) || exit 1
	case $objects in
	/*) ;;
	*) objects=$t_at/$objects ;;
	esac
	C=${objects%/objects}
	now=$(g --git-dir="$C" rev-parse --verify --quiet "HEAD^{commit}") || now=
	if [ -n "$now" ]; then
		base=$now
	fi
	gitfolder /dev/null || exit 1
	export GIT_DIR="$G" GIT_WORK_TREE="$t_at" GIT_INDEX_FILE="$G/index"
	if [ -n "$now" ]; then
		printf '%s\n' "$now" > "$G/HEAD" || exit 1
		g read-tree "$now" || exit 1
	fi
	g status --porcelain -z --untracked-files=all --ignore-submodules=dirty > "$t_out" || exit 1
	if [ -n "$t_rules" ]; then
		rules || exit 1
		g ls-files -z -o -i --exclude-standard > "$G/ignored-z" || exit 1
		if broken "$G/ignored-z"; then
			: > "$B/$n.left"
		fi
		tr '\000' '\n' < "$G/ignored-z" > "$G/ignored" || exit 1
		{ grep -v -e '^:' -e '^$' "$G/ignored" || :; } | tr '\n' '\000' > "$G/asked-z" || exit 1
		person_ignored "$G/asked-z" "$G/person-z"
		tr '\000' '\n' < "$G/person-z" > "$G/person" || exit 1
		lacking "$G/ignored" "$G/person" > "$G/hidden" || exit 1
		while IFS= read -r t_p; do
			printf '?? %s\0' "$t_p"
		done < "$G/hidden" >> "$t_out" || exit 1
	fi
	g -c core.quotePath=false ls-files -s > "$G/entries" || exit 1
	t_k=0
	while IFS= read -r t_line; do
		case $t_line in
		"160000 "*) ;;
		*) continue ;;
		esac
		t_p=${t_line#*"$tab"}
		case $t_p in
		\"*)
			: > "$B/$n.left"
			continue
			;;
		esac
		if through_link "$t_at" "$t_p"; then
			printf ' M %s\0' "$t_p" >> "$t_out" || exit 1
			: > "$B/$n.left"
			continue
		fi
		if [ ! -e "$t_at/$t_p/.git" ] && [ ! -h "$t_at/$t_p/.git" ]; then
			if [ -e "$t_at/$t_p" ] && filled "$t_at/$t_p"; then
				printf ' M %s\0' "$t_p" >> "$t_out" || exit 1
				: > "$B/$n.left"
			fi
			continue
		fi
		if [ "$t_depth" -ge 8 ]; then
			printf ' M %s\0' "$t_p" >> "$t_out" || exit 1
			: > "$B/$n.left"
			continue
		fi
		t_k=$((t_k + 1))
		if ! tree_status "$t_at/$t_p" "$G/status-$t_k.out" "$G/status-$t_k" $((t_depth + 1)) "$t_rules" "$t_deep"; then
			printf ' M %s\0' "$t_p" >> "$t_out" || exit 1
			: > "$B/$n.left"
		elif [ -s "$G/status-$t_k.out" ] && [ -n "$t_deep" ]; then
			if broken "$G/status-$t_k.out"; then
				: > "$B/$n.left"
			fi
			tr '\000' '\n' < "$G/status-$t_k.out" > "$G/status-$t_k.lines" || exit 1
			while IFS= read -r t_e; do
				printf '%s %s/%s\0' "${t_e%"${t_e#??}"}" "$t_p" "${t_e#???}"
			done < "$G/status-$t_k.lines" >> "$t_out" || exit 1
		elif [ -s "$G/status-$t_k.out" ]; then
			printf ' M %s\0' "$t_p" >> "$t_out" || exit 1
			: > "$B/$n.left"
		fi
	done < "$G/entries"
)
# submodule_at reports whether the submodule at the folder $1 of $T has the commit $2 at HEAD, and
# writes its changes to $G/status.
submodule_at() (
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	now=$(g --git-dir="$T/$1/.git" rev-parse --verify --quiet "HEAD^{commit}") || exit 1
	[ "$now" = "$2" ] || exit 1
	submodule_status "$1" rules
)
# from_setup reports whether each change in $G/status of the submodule at the folder $1 is what the
# setup left there, as sub-n has it, and writes the state and path of each change to $G/now. One
# git hashes the regular files, as for the setup's paths: git strips a final carriage return from
# each path it reads, so such a path goes through state. A status entry with a line break returns 1,
# since the lines read below could each match sub-n. sub-n has no such path.
from_setup() {
	: > "$G/now"
	tr -cd '\n' < "$G/status" > "$G/breaks"
	if [ -s "$G/breaks" ]; then
		return 1
	fi
	: > "$G/hash"
	tr '\000' '\n' < "$G/status" > "$G/changes"
	while IFS= read -r line; do
		p=$1/${line#???}
		if [ ! -h "$T/$p" ] && [ -f "$T/$p" ] && [ "$p" = "${p%"$cr"}" ]; then
			printf '%s\n' "./$p" >> "$G/hash"
		else
			printf '%s %s\n' "$(state "$p")" "$p" >> "$G/now"
		fi
	done < "$G/changes"
	g hash-object --no-filters --stdin-paths < "$G/hash" > "$G/ids" 2>/dev/null || return 1
	while IFS= read -r p && read -r id <&4; do
		p=${p#./}
		printf '%s:%s %s\n' "$(kind "$p")" "$id" "$p" >> "$G/now"
	done < "$G/hash" 4< "$G/ids"
	[ -z "$(lacking "$G/now" "$B/sub-$n")" ]
}
# below_gitlink reports whether a folder above the folder $1 is a submodule of the tree $from.
below_gitlink() {
	p=$1
	while :; do
		case $p in
		*/*) p=${p%/*} ;;
		*) return 1 ;;
		esac
		case $(g ls-tree "$from" -- ":(top,literal)$p" 2>/dev/null) in
		"160000 "*) return 0 ;;
		esac
	done
}
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
# large leaves out of the add each regular file of more than 100 MiB that git add would add: the
# pathspec file $2 gets an exclude for it, the file $3 of the paths that stay out gets its path, and
# n.large gets the path after the folder $1. find skips ./.git and $4. The other arguments are the
# pathspecs that the add excludes. $G/large is the list. A path with a line break
# goes out unchecked, as its pieces would match no line of that list, with ? for each line break in
# n.large. A commit the agent made keeps its files.
large() {
	l_at=$1 l_add=$2 l_out=$3 l_skip=$4
	shift 4
	: > "$G/large-found"
	: > "$G/large"
	find . -path ./.git -prune -o -path "$l_skip" -prune -o -type f -size +104857600c -exec sh -c '
		found=$1 add=$2 out=$3 list=$4 at=$5
		shift 5
		for p do
			p=${p#./}
			case $p in
			*"
"*)
				printf ":(exclude,top,literal)%s\0" "$p" >> "$add"
				printf "%s\n" "$p" >> "$out"
				printf "%s\0" "$p" >> "$out-z"
				printf "%s%s" "$at" "$p" | tr "\n" "?" >> "$list"
				printf "\n" >> "$list"
				;;
			*) printf "%s\n" "$p" >> "$found" ;;
			esac
		done' sh "$G/large-found" "$l_add" "$l_out" "$B/$n.large" "$l_at" {} + 2>/dev/null || :
	if [ ! -s "$G/large-found" ]; then
		return 0
	fi
	# ls-files hashes only the files found, to find the changed ones. A failed ls-files leaves out every
	# file found, which keeps more in the copy, not less.
	while IFS= read -r p; do
		set -- "$@" ":(top,literal)$p"
	done < "$G/large-found"
	if g ls-files -z -m -o --exclude-standard -- "$@" > "$G/large-z" 2>/dev/null; then
		if broken "$G/large-z"; then
			: > "$B/$n.left"
		fi
		tr '\000' '\n' < "$G/large-z" > "$G/large-added"
	else
		cp "$G/large-found" "$G/large-added"
	fi
	both "$G/large-found" "$G/large-added" > "$G/large-both"
	lacking "$G/large-both" "$l_out" > "$G/large"
	while IFS= read -r p; do
		printf ':(exclude,top,literal)%s\0' "$p" >> "$l_add"
		printf '%s\n' "$p" >> "$l_out"
		printf '%s%s\n' "$l_at" "$p" >> "$B/$n.large"
	done < "$G/large"
}
# add_lines prints the lines of git add in $G/add-errors, for $1, but its list of ignored paths.
# The lines are for the person to read: no check of the script reads them.
add_lines() {
	block=
	while IFS= read -r line || [ -n "$line" ]; do
		case $block:$line in
		':The following paths are ignored by one of your .gitignore files:') block=paths ;;
		'paths:hint: '* | 'paths:Use -f'*) block=hint ;;
		paths:*) ;;
		'hint:hint: '*) ;;
		*)
			block=
			printf 'qory: git add printed in %s: %s\n' "$1" "$line" >&2
			;;
		esac
	done < "$G/add-errors"
}
# add runs git add -A with the pathspecs in the file $1, and prints its lines for $2. git add exits
# 1 when it could not add a file, or when a path left out here is also ignored, and it adds the
# rest all the same. Any other failure exits 128.
add() {
	set +e
	g add -A --ignore-errors --pathspec-from-file="$1" --pathspec-file-nul 2> "$G/add-errors"
	added=$?
	set -e
	case $added in
	0 | 1) ;;
	*)
		cat "$G/add-errors" >&2
		exit "$added"
		;;
	esac
	add_lines "$2"
}
# rules writes each regular .gitignore of the commit $base to the same path below the folder
# $G/rules, through the index $G/rules-index. These files and $G/info/exclude are the person's
# ignore rules, which person_ignored reads. A failure leaves fewer files there, so that more paths
# count as left out.
rules() {
	mkdir "$G/rules"
	(
		export GIT_INDEX_FILE="$G/rules-index"
		g read-tree "$base" && g -c core.quotePath=false ls-files -s > "$G/rules-all" || exit 0
		{ grep -e "$tab\.gitignore\$" -e '/\.gitignore$' "$G/rules-all" || :; } > "$G/rules-found"
		while IFS= read -r line; do
			case $line in
			100644\ * | 100755\ *) ;;
			*) continue ;;
			esac
			case ${line#*"$tab"} in
			\"*) ;;
			*) printf '%s\0' "${line#*"$tab"}" ;;
			esac
		done < "$G/rules-found" > "$G/rules-paths"
		g checkout-index -q -z --stdin --prefix="$G/rules/" < "$G/rules-paths" 2>/dev/null || :
	)
}
# person_ignored writes to the file $2 each path of the file $1, a path and a NUL each, that the
# person's ignore rules cover, as rules wrote them. A path with a final / is a folder. check-ignore
# reads a path that starts with : as a pathspec, so the caller leaves such a path out. A failure
# writes nothing, so that each path counts as left out, and sets p_status past 1.
person_ignored() {
	: > "$2"
	p_status=0
	(
		cd "$G/rules" && export GIT_WORK_TREE="$G/rules" &&
			g check-ignore --no-index -z --stdin < "$1" > "$2" 2>/dev/null
	) || p_status=$?
	if [ "$p_status" -gt 1 ]; then
		: > "$2"
	fi
}
# setup_rules puts in $G/rules each .gitignore of the file $1, a list of the setup's paths that the
# work tree at $T still has as the setup left them: a regular file replaces the one of $base, and
# any other, which git reads no rules from, empties it. So where the setup changed a .gitignore, the
# person's rules are the setup's.
setup_rules() {
	while IFS= read -r s_p; do
		case $s_p in
		.gitignore | */.gitignore) ;;
		*) continue ;;
		esac
		if [ -f "$T/$s_p" ] && ! through_link "$T" "$s_p"; then
			case $s_p in
			*/*) mkdir -p "$G/rules/${s_p%/*}" 2>/dev/null || : ;;
			esac
			cp "$T/$s_p" "$G/rules/$s_p" 2>/dev/null || : > "$G/rules/$s_p" 2>/dev/null || :
		elif [ -f "$G/rules/$s_p" ]; then
			: > "$G/rules/$s_p"
		fi
	done < "$1"
}
# stat_args sets s_opt, s_time and s_line to the arguments of stat that print a change time in seconds,
# alone or before a path: busybox and GNU stat take -c, and BSD stat -f. It returns 1 with neither.
stat_args() {
	if stat -c %Z . > /dev/null 2>&1; then
		s_opt=-c s_time=%Z s_line='%Z %n'
	elif stat -f %c . > /dev/null 2>&1; then
		s_opt=-f s_time=%c s_line='%c %N'
	else
		return 1
	fi
}
# changed writes to the file $1 each path whose change time is past $since, the setup's end, or every
# path with $since below 0. A write sets the change time from the clock, and touch -d cannot set it.
# It skips each folder named in $generated and each .git/index that index_clean finds with no
# work, such as one that git status rewrote, and lists a folder, with a final /, only when find
# cannot enter it. The list has a path a line, so a name with a line break, which find stats as x,
# writes n.left.
changed() {
	c_out=$1
	: > "$c_out"
	if [ "$since" -eq 0 ]; then
		return 0
	fi
	if ! stat_args; then
		: > "$B/$n.left"
		return 0
	fi
	set --
	for c_n in $generated; do
		set -- "$@" -o -name "$c_n"
	done
	if [ $# -gt 0 ]; then
		shift
	fi
	find . \( -path ./.git -o -path ./.qory -o -type d \( "$@" \) \) -prune -o -name "*$nl*" -exec stat "$s_opt" x {} + \
		-o -type d ! -perm -500 -prune -exec stat "$s_opt" "$s_line/" {} + \
		-o ! -type d -exec stat "$s_opt" "$s_line" {} + > "$c_out-times" 2>/dev/null || : > "$B/$n.left"
	c_status=0
	grep -q -x x "$c_out-times" 2>/dev/null || c_status=$?
	if [ "$c_status" -ne 1 ]; then
		: > "$B/$n.left"
	fi
	if ! sort -n -r "$c_out-times" > "$c_out-sorted" 2>/dev/null; then
		: > "$B/$n.left"
		return 0
	fi
	while IFS= read -r c_line; do
		c_t=${c_line%% *}
		case $c_t in
		'' | *[!0-9]*) continue ;;
		esac
		if [ ${#c_t} -gt 15 ]; then
			: > "$B/$n.left"
			continue
		fi
		if [ "$c_t" -le "$since" ]; then
			break
		fi
		c_p=${c_line#* }
		c_p=${c_p#./}
		case $c_p in
		*/.git/index)
			if index_clean "$c_p"; then
				continue
			fi
			;;
		esac
		if [ -e "$c_p" ] || [ -h "$c_p" ]; then
			printf '%s\n' "$c_p"
		else
			: > "$B/$n.left"
		fi
	done < "$c_out-sorted" > "$c_out"
}
# index_clean reports whether the file $1, a .git/index, holds no work: git reads a copy of it, with
# the folder above it as git folder, and finds what HEAD there has, as after a git status. A folder
# that is no git folder, or a failed git, returns 1.
index_clean() (
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	cp "$1" "$c_out-index" 2>/dev/null || exit 1
	export GIT_INDEX_FILE="$c_out-index"
	g --git-dir="${1%/index}" diff-index --cached --quiet --no-ext-diff HEAD -- > /dev/null 2>&1
)
# new_ignored writes to $G/new-ignored-z each path of $G/new, as ./path, that the work tree's rules
# ignore, and to $G/new-person-z those that the person's rules ignore. check-ignore stops at a path in
# a submodule, so such a path is not asked: the checks of submodules cover it.
new_ignored() {
	: > "$G/new-ignored-z"
	: > "$G/new-person-z"
	if [ ! -s "$G/new" ]; then
		return 0
	fi
	if ! g -c core.quotePath=false ls-files -s > "$G/new-index" 2>/dev/null; then
		: > "$B/$n.left"
		return 0
	fi
	while IFS= read -r line; do
		case $line in
		"160000 "*) printf '%s\n' "${line#*"$tab"}" ;;
		esac
	done < "$G/new-index" > "$G/new-links"
	outside "$G/new" "$G/new-links" > "$G/new-asked"
	while IFS= read -r p; do
		printf './%s\0' "$p"
	done < "$G/new-asked" > "$G/new-asked-z"
	n_status=0
	g check-ignore -z --stdin < "$G/new-asked-z" > "$G/new-ignored-z" 2>/dev/null || n_status=$?
	if [ "$n_status" -gt 1 ]; then
		: > "$G/new-ignored-z"
		: > "$B/$n.left"
		return 0
	fi
	person_ignored "$G/new-ignored-z" "$G/new-person-z"
}
# setup_ignored writes n.left, and with $since past 0 each path after $1 to n.ignored, for each path
# of $G/new-ignored-z that the person's rules leave and either the rules after setup_rules ignore or
# a repository of $setup_repos holds: check-ignore read that repository's rules. When person_ignored
# fails on the rules after setup_rules, it writes n.left alone.
setup_ignored() {
	if [ ! -s "$G/new-ignored-z" ]; then
		return 0
	fi
	person_ignored "$G/new-ignored-z" "$G/new-setup-z"
	if [ "$p_status" -gt 1 ]; then
		: > "$B/$n.left"
	fi
	tr '\000' '\n' < "$G/new-ignored-z" > "$G/new-ignored"
	tr '\000' '\n' < "$G/new-setup-z" > "$G/new-setup"
	tr '\000' '\n' < "$G/new-person-z" > "$G/new-person"
	while IFS= read -r p; do
		up=${p#./}
		while :; do
			case $setup_repos in
			*"$nl$up$nl"*)
				printf '%s\n' "$p"
				break
				;;
			esac
			case $up in
			*/*) up=${up%/*} ;;
			*) break ;;
			esac
		done
	done < "$G/new-ignored" >> "$G/new-setup"
	lacking "$G/new-setup" "$G/new-person" > "$G/new-left"
	if [ ! -s "$G/new-left" ]; then
		return 0
	fi
	: > "$B/$n.left"
	if [ "$since" -gt 0 ]; then
		while IFS= read -r p; do
			printf '%s%s\n' "$1" "${p#./}"
		done < "$G/new-left" >> "$B/$n.ignored"
	fi
}
# repo_git adds to the files $r_f and $r_l the regular files and the links of HEAD, packed-refs,
# refs, reftable and logs of the git folder $1, relative to the current folder, for repo_state.
repo_git() {
	for r_p in "$1/HEAD" "$1/packed-refs" "$1/refs" "$1/reftable" "$1/logs"; do
		if [ -h "$r_p" ]; then
			printf './%s\n' "$r_p" >> "$r_l"
		elif [ -f "$r_p" ]; then
			printf './%s\n' "$r_p" >> "$r_f"
		elif [ -d "$r_p" ]; then
			find "./$r_p" -type f -print >> "$r_f" || return 1
			find "./$r_p" -type l -print >> "$r_l" || return 1
		fi
	done
}
# repo_state prints the state of the repository that the setup made at the folder $1 of $T, as
# SetupPath.State has it: repo: and the blob id of a list, sorted, of a line per file or change. The
# files are HEAD, packed-refs and each file below refs, reftable and logs of each git folder: each
# folder named .git below $1, and each folder below its modules and worktrees with a regular file
# HEAD and a folder refs or a regular file commondir, such as the git folder of a submodule. They
# have the refs and reflogs of the repository and of its submodules and worktrees. The other files
# are each .gitignore below $1 but in a folder named .git. Each such line is kind:id and the path
# below $1, kind being file, with the blob id of the content, or link, with that of its target. The
# changes are the entries of the file $2, as tree_status writes it with $6 set: status, the path's
# state, and the path. It exits 1 when a find, a git or a readlink fails, or a path has a carriage
# return. The record session and the git session both run it, so the two lists match while the
# repository stays as the setup left it.
repo_state() (
	cd "$T/$1" || exit 1
	r_f=$G/repo-files r_l=$G/repo-links
	find . -name .git -prune -o -name .gitignore -type f -print > "$r_f" || exit 1
	find . -name .git -prune -o -name .gitignore -type l -print > "$r_l" || exit 1
	find . -name .git -prune -type d -print > "$G/repo-gits" || exit 1
	while IFS= read -r r_g; do
		r_g=${r_g#./}
		repo_git "$r_g" || exit 1
		for r_top in "$r_g/modules" "$r_g/worktrees"; do
			# A link there is a line too, as the link's target.
			if [ -h "$r_top" ]; then
				printf './%s\n' "$r_top" >> "$r_l"
			elif [ -d "$r_top" ]; then
				find "./$r_top" -type d -print > "$G/repo-dirs" || exit 1
				while IFS= read -r r_d; do
					r_d=${r_d#./}
					if [ -f "$r_d/HEAD" ] && [ ! -h "$r_d/HEAD" ]; then
						if { [ -d "$r_d/refs" ] && [ ! -h "$r_d/refs" ]; } ||
							{ [ -f "$r_d/commondir" ] && [ ! -h "$r_d/commondir" ]; }; then
							repo_git "$r_d" || exit 1
						fi
					fi
				done < "$G/repo-dirs"
			fi
		done
	done < "$G/repo-gits"
	if [ -n "$(cat "$r_f" "$r_l" | tr -cd '\r')" ]; then
		exit 1
	fi
	while IFS= read -r r_p; do
		printf '%s/%s\n' "$T/$1" "${r_p#./}"
	done < "$r_f" > "$G/repo-paths" || exit 1
	g hash-object --no-filters --stdin-paths < "$G/repo-paths" > "$G/repo-ids" || exit 1
	if broken "$2"; then
		exit 1
	fi
	tr '\000' '\n' < "$2" > "$G/repo-status" || exit 1
	{
		while IFS= read -r r_p && IFS= read -r r_id <&4; do
			printf 'file:%s %s\n' "$r_id" "${r_p#./}"
		done < "$r_f" 4< "$G/repo-ids"
		while IFS= read -r r_p; do
			r_t=$(readlink "$r_p") || exit 1
			r_id=$(printf '%s' "$r_t" | g hash-object --no-filters --stdin) || exit 1
			printf 'link:%s %s\n' "$r_id" "${r_p#./}"
		done < "$r_l"
		while IFS= read -r r_line; do
			r_p=${r_line#???}
			r_p=${r_p%/}
			printf 'status %s %s\n' "$(state "$1/$r_p")" "$r_p"
		done < "$G/repo-status"
	} > "$G/repo-lines" || exit 1
	sort "$G/repo-lines" > "$G/repo-sorted" || exit 1
	r_id=$(g hash-object --no-filters --stdin < "$G/repo-sorted") || exit 1
	printf 'repo:%s' "$r_id"
)
# submodule_of reports whether the folder $2 of the repository at the folder $1 of $T is a submodule
# of it: a gitlink of its HEAD, or a folder whose .git file points below the folder modules of its
# .git, as for a submodule of a submodule or one that no commit has yet.
submodule_of() (
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	case $(g --git-dir="$T/$1/.git" ls-tree HEAD -- ":(top,literal)$2" 2>/dev/null) in
	"160000 "*) exit 0 ;;
	esac
	s_git=$T/$1/$2/.git
	if [ -h "$s_git" ] || [ ! -f "$s_git" ]; then
		exit 1
	fi
	s_line=
	IFS= read -r s_line < "$s_git" || [ -n "$s_line" ] || exit 1
	case $s_line in
	"gitdir: "*) s_at=${s_line#gitdir: } ;;
	*) exit 1 ;;
	esac
	case $s_at in
	/*) ;;
	*) s_at=$T/$1/$2/$s_at ;;
	esac
	# A link at .git or modules could point at any folder, such as one that the repository ignores,
	# and repo_state reads no git folder through one. After cd -P, PWD is the folder with no link
	# on its path, so a git folder whose path there is below modules has no link below modules.
	if [ -h "$T/$1/.git" ] || [ -h "$T/$1/.git/modules" ]; then
		exit 1
	fi
	s_modules=$(cd -P "$T/$1/.git/modules" 2>/dev/null && printf '%s' "$PWD") || exit 1
	s_at=$(cd -P "$s_at" 2>/dev/null && printf '%s' "$PWD") || exit 1
	case $s_at in
	"$s_modules"/*) exit 0 ;;
	esac
	exit 1
)
# kept_out reports whether both the ignore rules of the work tree and the person's cover the folder
# $1 of $T.
kept_out() {
	g check-ignore -q -- "./$1" 2>/dev/null || return 1
	case $1 in
	:*) return 1 ;;
	esac
	printf '%s/\0' "$1" > "$G/asked-z"
	person_ignored "$G/asked-z" "$G/person-z"
	[ -s "$G/person-z" ]
}
# outside prints each line of the file $1 that is neither a folder of the file $2 nor below one. sort
# puts a folder and the paths below it in one run of lines, as each line is a path, a /, a tab and 0
# for a folder or 1 for a path, so one pass reads each line once. A failed sort prints every line of
# $1, so that a failure keeps the copy.
outside() {
	{
		while IFS= read -r o_p; do
			printf '%s/\t0\n' "$o_p"
		done < "$2"
		while IFS= read -r o_p; do
			printf '%s/\t1\n' "$o_p"
		done < "$1"
	} > "$G/marks"
	if ! sort "$G/marks" > "$G/marks-sorted" 2>/dev/null; then
		cat "$1"
		return 0
	fi
	o_in=
	while IFS= read -r o_line; do
		o_key=${o_line%"$tab"*}
		if [ -n "$o_in" ]; then
			case $o_key in
			"$o_in"*) continue ;;
			esac
		fi
		case $o_line in
		*"$tab"0) o_in=$o_key ;;
		*) printf '%s\n' "${o_key%/}" ;;
		esac
	done < "$G/marks-sorted"
}
# leftover is the check under the others, once the index is what the commit has: it writes n.left
# when the work tree at $T has a path that the commit leaves out. One git ls-files lists each path
# that differs from the index and each path the index does not have, but an ignored one, with no
# exclude of the folders with a .git: it lists an untracked repository as its folder, and no path in
# a gitlink. A second one lists each ignored path, and a path that the person's ignore rules do not
# cover counts too: the run wrote or changed the rule that covers it. A path in the file $1, which
# stays out on purpose with its line, such as a file of more than 100 MiB, a path in a folder of the
# file $2, a repository the run made with its line, and a gitlink of $from, which the checks of
# submodules cover, count for nothing. The other arguments are the pathspecs that ls-files
# excludes. Each path that counts goes to $G/leftover. A gitlink that the commit has and $from does
# not is a repository that git add put in as a link, and a gitlink with no .git at a folder that is
# not empty has files that no ls-files lists: each writes n.left too. A failed git keeps the copy.
leftover() {
	lo_out=$1 lo_made=$2
	shift 2
	: > "$G/leftover"
	if ! g ls-files -z -m -o --exclude-standard -- ':/' "$@" > "$G/after-z" 2>/dev/null ||
		! g ls-files -z -o -i --exclude-standard -- ':/' "$@" > "$G/ignored-z" 2>/dev/null ||
		! g -c core.quotePath=false ls-tree -r "$from" > "$G/from-tree" 2>/dev/null ||
		! g -c core.quotePath=false ls-files -s > "$G/index-tree" 2>/dev/null; then
		: > "$B/$n.left"
		return 0
	fi
	if broken "$G/ignored-z"; then
		: > "$B/$n.left"
	fi
	tr '\000' '\n' < "$G/after-z" > "$G/after"
	tr '\000' '\n' < "$G/ignored-z" > "$G/ignored"
	{ grep -v -e '^:' -e '^$' "$G/ignored" || :; } | tr '\n' '\000' > "$G/asked-z"
	person_ignored "$G/asked-z" "$G/person-z"
	tr '\000' '\n' < "$G/person-z" > "$G/person"
	lacking "$G/ignored" "$G/person" >> "$G/after"
	for lo_tree in from-tree index-tree; do
		while IFS= read -r line; do
			case $line in
			"160000 "*) printf '%s\n' "${line#*"$tab"}" ;;
			esac
		done < "$G/$lo_tree" > "$G/$lo_tree-links"
	done
	if [ -n "$(lacking "$G/index-tree-links" "$G/from-tree-links")" ]; then
		: > "$B/$n.left"
	fi
	cat "$lo_out" "$G/from-tree-links" > "$G/after-kept"
	cat "$lo_out-z" > "$G/after-kept-z" 2>/dev/null || :
	if split_hidden "$G/after-z" "$G/after-kept" "$lo_made"; then
		: > "$B/$n.left"
	fi
	lacking "$G/after" "$lo_out" > "$G/after-out"
	while IFS= read -r p; do
		printf '%s\n' "${p%/}"
	done < "$G/after-out" > "$G/after-paths"
	lacking "$G/after-paths" "$G/from-tree-links" > "$G/after-unlinked"
	outside "$G/after-unlinked" "$lo_made" > "$G/leftover"
	while IFS= read -r p; do
		case $p in
		\"*)
			: > "$B/$n.left"
			continue
			;;
		esac
		if through_link "$T" "$p" || [ -e "$T/$p/.git" ] || [ -h "$T/$p/.git" ]; then
			continue
		fi
		if [ -e "$T/$p" ] && filled "$T/$p"; then
			: > "$B/$n.left"
		fi
	done < "$G/index-tree-links"
	if [ -s "$G/leftover" ]; then
		: > "$B/$n.left"
	fi
}
# uncommitted writes the index as a commit whose parent is HEAD, at $at, to $prefix/uncommitted, for
# a working tree the session makes no commit of on the run's branch.
uncommitted() {
	tree=$(g write-tree)
	if [ -n "$at" ]; then
		set -- -p "$at"
	else
		set --
	fi
	made=$(GIT_AUTHOR_NAME=` + commitName + ` GIT_AUTHOR_EMAIL=` + commitEmail + ` \
		GIT_COMMITTER_NAME=` + commitName + ` GIT_COMMITTER_EMAIL=` + commitEmail + ` \
		g commit-tree "$tree" "$@" -m "$message")
	g update-ref "$prefix/uncommitted" "$made"
}
# fresh prints a commit when the commit $1 has one that $G/old does not reach: the base commit or a
# ref at the end of the clone, and $1 itself when that check fails. A ref that the agent wrote, such
# as a remote-tracking ref it moved, does not count, since the person may not have its commits.
fresh() {
	printf '%s\n' "$1" | cat - "$G/old" | g rev-list -1 --stdin --ignore-missing || printf '%s\n' "$1"
}
# known_ref takes a new or moved branch or tag $3 at the object $1, whose commit $2 the person has. A
# bundle leaves out a ref whose commits it excludes, so a ref at the commit itself goes to the file
# $known, as its object and its name under $prefix, for the host to make. An annotated tag is an
# object the person does not have, and goes under $prefix in the bundle. The run's branch, $mainref,
# and any other ref stay out.
known_ref() {
	case $3 in
	"$mainref") return 0 ;;
	refs/heads/?* | refs/tags/?*) ;;
	*) return 0 ;;
	esac
	if [ "$1" = "$2" ]; then
		printf '%s %s\n' "$1" "$prefix/${3#refs/}" >> "$known"
	else
		g update-ref "$prefix/${3#refs/}" "$1"
	fi
}
# extra_refs writes each new or moved ref of the copy's repository at $C that has a fresh commit to
# $prefix in the git folder $G: a branch as heads/<name>, a tag as tags/<name>, and refs/stash as
# stash. $mainref, the run's branch, counts only when the commit $tip does not have it. HEAD,
# detached at a fresh commit that $tip does not have, goes to HEAD. $clonerefs are the refs of the
# clone. Then lost/1, lost/2 and so on are the tips of the fresh commits that no ref here reaches,
# of these: a commit of another new or moved ref, a commit of HEAD's reflog that no branch's reflog
# has, other than a rebase's step, which is a commit made with HEAD detached, an older stash, and,
# with $reflogs set, any commit a reflog has. A branch or tag at a commit with no fresh commit goes
# through known_ref.
extra_refs() {
	{
		printf '^%s\n' "$base"
		while read -r e_id e_ref; do
			printf '^%s\n' "$e_id"
		done < "$clonerefs"
	} > "$G/old"
	copygit for-each-ref --format='%(objectname) %(refname)' > "$G/refs-now"
	lacking "$G/refs-now" "$clonerefs" > "$G/moved-refs"
	: > "$G/others"
	while read -r e_id e_ref; do
		e_commit=$(g rev-parse --verify --quiet "$e_id^{commit}") || continue
		if [ -z "$(fresh "$e_commit")" ]; then
			known_ref "$e_id" "$e_commit" "$e_ref"
			continue
		fi
		case $e_ref in
		refs/remotes/*) continue ;;
		"$mainref")
			if [ -n "$tip" ] && g merge-base --is-ancestor "$e_commit" "$tip"; then
				continue
			fi
			e_name=${e_ref#refs/}
			;;
		refs/heads/?* | refs/tags/?*) e_name=${e_ref#refs/} ;;
		refs/stash) e_name=stash ;;
		*)
			printf '%s\n' "$e_commit" >> "$G/others"
			continue
			;;
		esac
		g update-ref "$prefix/$e_name" "$e_id"
	done < "$G/moved-refs"
	if [ -n "$at" ] && ! copygit symbolic-ref -q HEAD > /dev/null 2>&1 && [ -n "$(fresh "$at")" ]; then
		if [ -z "$tip" ] || ! g merge-base --is-ancestor "$at" "$tip"; then
			g update-ref "$prefix/HEAD" "$at"
		fi
	fi
	if [ -n "$reflogs" ]; then
		{ copygit rev-list --no-walk --reflog 2>/dev/null || :; } >> "$G/others"
	else
		copygit for-each-ref --format='%(refname)' refs/heads/ > "$G/names"
		while IFS= read -r e_ref; do
			copygit rev-list -g "$e_ref" 2>/dev/null || :
		done < "$G/names" > "$G/on-branch"
		{ copygit log -g --format='%H %gs' HEAD 2>/dev/null || :; } > "$G/reflog"
		while read -r e_id e_what; do
			case $e_what in
			rebase*) ;;
			*) printf '%s\n' "$e_id" ;;
			esac
		done < "$G/reflog" > "$G/moves"
		lacking "$G/moves" "$G/on-branch" >> "$G/others"
		{ copygit rev-list -g refs/stash 2>/dev/null || :; } >> "$G/others"
	fi
	{
		cat "$G/others" "$G/old"
		if [ -n "$tip" ]; then
			printf '^%s\n' "$tip"
		fi
		g for-each-ref --format='^%(objectname)' "$prefix/"
	} | g rev-list --parents --stdin --ignore-missing > "$G/lost"
	while read -r e_id e_parents; do
		printf '%s\n' "$e_id"
	done < "$G/lost" > "$G/lost-ids"
	while read -r e_id e_parents; do
		for e_parent in $e_parents; do
			printf '%s\n' "$e_parent"
		done
	done < "$G/lost" > "$G/lost-parents"
	lacking "$G/lost-ids" "$G/lost-parents" > "$G/lost-tips"
	e_n=0
	while read -r e_id; do
		e_n=$((e_n + 1))
		g update-ref "$prefix/lost/$e_n" "$e_id"
	done < "$G/lost-tips"
}
# bundle writes $mainref, when it is past the base commit, and each ref under $prefix in $G to the
# bundle $1.bundle, without the base commit and the commits before it. With none of them, it writes
# $1.none.
bundle() {
	b_out=$1
	set --
	if [ -n "$mainref" ] && [ "$(g rev-list --count "$mainref" "^$base")" != 0 ]; then
		set -- "$mainref"
	fi
	g for-each-ref --format='%(refname)' "$prefix/" > "$G/extra"
	while IFS= read -r b_ref; do
		set -- "$@" "$b_ref"
	done < "$G/extra"
	if [ $# -eq 0 ]; then
		: > "$b_out.none"
	else
		g bundle create -q "$b_out.bundle.part" "$@" "^$base"
		mv "$b_out.bundle.part" "$b_out.bundle"
	fi
}
# gitfolder makes the git folder $G, with an empty configuration, the person's ignore rules in the
# file $1 when it exists, the objects of the copy's git folder $C as its alternates, and HEAD on the
# run's branch, which has no commit there yet.
gitfolder() {
	mkdir "$G" "$G/objects" "$G/objects/info" "$G/refs" "$G/refs/heads" "$G/info"
	if [ -f "$1" ]; then
		cp "$1" "$G/info/exclude"
	fi
	: > "$G/config"
	if [ ${#base} -eq 64 ]; then
		printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$G/config"
	fi
	c_quoted "$C/objects" > "$G/objects/info/alternates"
	if [ "$icase" = true ]; then
		printf '[core]\n\tignorecase = true\n' >> "$G/config"
	fi
	printf 'ref: refs/heads/%s\n' "$branch" > "$G/HEAD"
}
# submodule_bundle commits what the agent left in the submodule at the folder $1 of $T as a commit
# on its HEAD, and writes each new or moved ref of the submodule, with that commit, to the bundle
# n-$2.bundle, without the commit $3 that the clone checked out there and the commits before it. A
# file the setup left, as $G/now and sub-n both have it, and each folder with a .git below the
# submodule stay out of the commit. A change in such a folder stays behind, and so does a
# repository the run made in a folder that the submodule's HEAD has, as n.nested.
submodule_bundle() (
	d=$1 k=$2 base=$3
	shows=$(printable "$d")
	shows="the submodule $shows in $shown"
	PG=$G
	G=$G/sub-$k
	T=$T/$d
	C=$T/.git
	cd "$T"
	gitfolder /dev/null
	export GIT_DIR="$G" GIT_WORK_TREE="$T" GIT_INDEX_FILE="$G/index"
	at=$(copygit rev-parse --verify --quiet "HEAD^{commit}") || at=
	from=$at
	if [ -z "$from" ]; then
		from=$(g hash-object -t tree /dev/null)
	fi
	g read-tree "$from"
	rules
	while IFS= read -r p; do
		case $p in
		"$d"/?*) printf '%s\n' "${p#"$d"/}" ;;
		esac
	done < "$PG/new" > "$G/new"
	new_ignored
	printf ':/\0' > "$G/add"
	# A folder with a .git below another one stays out with it: git stops at a pathspec inside a
	# submodule of the submodule.
	all=$nl
	: > "$G/all"
	while IFS= read -r p; do
		case $p in
		"$d"/*)
			all=$all${p#"$d"/}$nl
			printf '%s\n' "${p#"$d"/}" >> "$G/all"
			;;
		esac
	done < "$PG/folders"
	# A folder with a .git that $from has as a folder is a repository the run made there, as git
	# init makes. The status lists each change in it by its file, never the folder, and the add
	# leaves the changes out, so it stays behind as such a repository does in the top repository.
	# No ignore rule covers such a folder: git ignores no folder that has a file of the index.
	: > "$G/made"
	while IFS= read -r p; do
		entry=$(g ls-tree "$from" -- ":(top,literal)$p" 2>/dev/null) || entry=
		case $entry in
		"040000 "*) ;;
		*) continue ;;
		esac
		printf 'qory: %s has a git repository at %s that the run made, and the commit leaves it out\n' "$shows" "$(printable "$p")" >&2
		: > "$B/$n.nested"
		printf '%s\n' "$p" >> "$G/made"
	done < "$G/all"
	: > "$G/nested"
	while IFS= read -r p; do
		case $p in
		"$d"/*) ;;
		*) continue ;;
		esac
		p=${p#"$d"/} up=$p
		while :; do
			case $up in
			*/*) up=${up%/*} ;;
			*) break ;;
			esac
			case $all in
			*"$nl$up$nl"*) continue 2 ;;
			esac
		done
		printf '%s\n' "$p" >> "$G/nested"
		printf ':(exclude,top,literal)%s\0' "$p" >> "$G/add"
	done < "$PG/folders"
	# A failure here leaves the setup's files in the commit, which then has more, not less.
	if ! { sort -u "$PG/now" > "$G/one" && sort -u "$B/sub-$n" > "$G/two" && comm -12 "$G/one" "$G/two" > "$G/setup"; } 2>/dev/null; then
		: > "$G/setup"
	fi
	: > "$G/setup-paths"
	: > "$G/setup-paths-z"
	while IFS= read -r line; do
		p=${line#* }
		p=${p#"$d"/}
		printf '%s\n' "$p" >> "$G/setup-paths"
		printf ':(exclude,top,literal)%s\0' "$p" >> "$G/add"
	done < "$G/setup"
	setup_rules "$G/setup-paths"
	setup_repos=$nl
	setup_ignored "$top$d/"
	tr '\000' '\n' < "$PG/status" > "$G/changes"
	while IFS= read -r line; do
		p=${line#???}
		printf '%s\n' "${p%/}"
	done < "$G/changes" > "$G/changed"
	# Each folder with a .git, not only the first of a chain, since the status lists a repository
	# inside the folder of another by its path, such as a/b/ in the folder a.
	told=
	if [ -s "$G/all" ] && [ -n "$(both "$G/all" "$G/changed")" ]; then
		printf 'qory: %s has a git repository with commits or changes that the commit leaves out\n' "$shows" >&2
		: > "$B/$n.left"
		told=1
	fi
	set -- ':/'
	while IFS= read -r p; do
		set -- "$@" ":(exclude,top,literal)$p"
	done < "$G/nested"
	shift
	large "$top$d/" "$G/add" "$G/setup-paths" ./.git "$@"
	set -- ':/' "$@"
	add "$G/add" "$shows"
	g ls-files -z -m -o --exclude-standard -- "$@" > "$G/unadded-z" 2>/dev/null || : > "$B/$n.left"
	tr '\000' '\n' < "$G/unadded-z" > "$G/unadded"
	if [ -n "$(lacking "$G/unadded" "$G/setup-paths")" ] || split_hidden "$G/unadded-z" "$G/setup-paths"; then
		: > "$B/$n.left"
	fi
	set +e
	g diff --cached --quiet --no-ext-diff --ignore-submodules=none "$from" --
	changed=$?
	set -e
	case $changed in
	0) ;;
	1) uncommitted ;;
	*) exit "$changed" ;;
	esac
	# A path that the check under the others finds in a folder with a .git gets the line of such a
	# folder.
	leftover "$G/setup-paths" "$G/made"
	outside "$G/leftover" "$G/all" > "$G/leftover-outside"
	if [ -z "$told" ] && [ -n "$(lacking "$G/leftover" "$G/leftover-outside")" ]; then
		printf 'qory: %s has a git repository with commits or changes that the commit leaves out\n' "$shows" >&2
	fi
	mainref= tip= clonerefs=$B/subrefs-$n-$k reflogs=1 known=$B/$n-$k.known
	extra_refs
	bundle "$B/$n-$k"
)
`

// sessionMain is the part of [sessionScript] that commits and writes the bundles.
const sessionMain = `# changed runs for each repository before the session changes a file in any, such as with chmod in
# readable.
n=0
while [ "$n" -lt "$count" ]; do
	. "$B/list-$n"
	if [ "$rel" = . ]; then cd "$W"; else cd "$W/$rel"; fi
	changed "$B/new-$n"
	n=$((n + 1))
done
n=0
while [ "$n" -lt "$count" ]; do
	. "$B/list-$n"
	if [ "$rel" = . ]; then T=$W shown="the copy" top=; else T=$W/$rel shown="the copy of $rel" top=$rel/; fi
	G=$B/git-$n C=$T/.git
	cd "$T"
	gitfolder "$B/exclude-$n"

	# head is what the bundle has: HEAD when it is past the base commit, so commits on another
	# branch come back with their messages, or HEAD at the base commit with the branch not past it.
	# In any other case it is the branch, and moved is set: the working tree is at HEAD, a commit
	# from there would undo the branch's files, so the session makes none on the branch, and a change
	# comes back as uncommitted.
	at=$(copygit rev-parse --verify --quiet "HEAD^{commit}") || at=
	on=$(copygit rev-parse --verify --quiet "refs/heads/$branch^{commit}") || on=
	head=$at
	moved=
	if ! past "$at" && { [ "$at" != "$base" ] || past "$on"; }; then
		head=$on
		# HEAD and the branch at one commit that the base commit does not have, such as an amended
		# base commit: the working tree is at the branch, so a commit undoes nothing.
		if [ -z "$at" ] || [ "$at" != "$on" ] || copygit merge-base --is-ancestor "$at" "$base" 2>/dev/null; then
			moved=1
		fi
	fi
	# A sparse checkout took files out of the working tree, and a commit would delete them: the
	# session makes none on the branch, and the paths the checkout took out stay as HEAD has them.
	sparse=
	if copygit ls-files -t 2>/dev/null | grep -q '^S '; then
		moved=1 sparse=1
		copygit ls-files -z -t > "$G/sparse-z"
	fi
	case $head in
	'' | *[!0-9a-f]*)
		echo "qory: neither HEAD nor $branch in $shown is at or past the base commit, so the session commits nothing" >&2
		exit 1
		;;
	esac
	if [ ${#head} -ne ${#base} ]; then
		echo "qory: git read a commit id of ${#head} characters in $shown, and the base commit has ${#base}, so the session commits nothing" >&2
		exit 1
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
	: > "$G/out-paths"
	: > "$G/out-paths-z"
	: > "$G/out-folders"
	: > "$G/made"
	: > "$G/setup-kept"
	if ! command -v find > /dev/null 2>&1; then
		: > "$B/$n.left"
	fi
	mv "$B/new-$n" "$G/new"
	readable
	# Each path that the sparse checkout took out, and that the working tree does not have, stays out.
	# A path that the working tree has goes into the commit as any other, such as a file the agent
	# changed and then marked with git update-index --skip-worktree. A path with a line break would
	# read as other paths here, so with one the session makes no commit of the working tree.
	nocommit=
	if [ -n "$sparse" ]; then
		tr -cd '\n' < "$G/sparse-z" > "$G/breaks"
		if [ -s "$G/breaks" ]; then
			nocommit=1
			: > "$B/$n.left"
		fi
		tr '\000' '\n' < "$G/sparse-z" > "$G/sparse"
		while IFS= read -r line; do
			case $line in
			'S '*)
				if [ ! -e "$T/${line#S }" ] && [ ! -h "$T/${line#S }" ]; then
					printf '%s\n' "${line#S }"
				fi
				;;
			esac
		done < "$G/sparse" > "$G/sparse-paths"
		while IFS= read -r p; do
			printf ':(exclude,top,literal)%s\0' "$p"
		done < "$G/sparse-paths" >> "$P"
		cat "$G/sparse-paths" >> "$G/out-paths"
	fi
	# A path with a line break is written whole here, and its pieces in the list match nothing. git
	# add stops at an exclude inside a submodule, so such a path below a gitlink of $from gets no
	# exclude and writes n.left.
	find . -path ./.git -prune -o -path ./.qory -prune -o -name .git -exec sh -c '
		list=$1
		add=$2
		from=$3
		left=$4
		shift 4
		for p do
			d=${p%/.git}
			d=${d#./}
			printf "%s\n" "$d" >> "$list"
			case $d in
			*"
"*)
				up=$d
				while :; do
					case $up in
					*/*) up=${up%/*} ;;
					*) break ;;
					esac
					case $(git --no-pager ls-tree "$from" -- ":(top,literal)$up" 2>/dev/null) in
					"160000 "*)
						: > "$left"
						continue 2
						;;
					esac
				done
				printf ":(exclude,top,literal)%s\0" "$d" >> "$add"
				;;
			esac
		done' sh "$G/folders" "$P" "$from" "$B/$n.left" {} + 2>/dev/null || :
	# A link into .qory stays out and the commit sets it back. While it is a link or is gone, it stays
	# out with no line. Any other file there, such as a regular file that replaced the link, may have
	# the run's text from .qory, so it stays out of the commit too, but in the copy: the checks after
	# the add list it when it differs from the commit, and write n.left.
	i=0
	while [ "$i" -lt "$links" ]; do
		printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
		printf ':(top,literal)%s\0' "$1" >> "$R"
		if [ -h "$T/$1" ] || [ ! -e "$T/$1" ]; then
			printf '%s\n' "$1" >> "$G/out-paths"
			case $1 in
			*"$nl"*) printf '%s\0' "$1" >> "$G/out-paths-z" ;;
			esac
		fi
		shift
		i=$((i + 1))
	done
	execs=
	if [ "$setup" -gt 0 ] || [ -s "$B/sub-$n" ]; then
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
			*) printf '%s\n' "./$a" ;;
			esac
		fi
		i=$((i + 1))
	done > "$G/regular"
	g hash-object --no-filters --stdin-paths < "$G/regular" > "$G/ids"
	exec 3< "$G/ids"
	i=0
	known=$nl setup_repos=$nl
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
		case $2 in
		repo:*)
			# A repository the setup made stays out whole, and keeps the copy when its HEAD, refs,
			# reflogs, .gitignore files or git status, with that of its submodules, differ from what
			# the record session read after the setup.
			if [ -d "$f" ] && [ ! -h "$f" ] && [ -d "$f/.git" ] && [ ! -h "$f/.git" ]; then
				printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
				known=$known$1$nl
				setup_repos=$setup_repos$1$nl
				printf '%s\n' "$1" >> "$G/out-folders"
				st=$((st + 1))
				if ! submodule_status "$1" "" deep || [ "$(repo_state "$1" "$G/status" || :)" != "$2" ]; then
					: > "$B/$n.left"
				fi
			fi
			;;
		"$have")
			printf ':(exclude,top,literal)%s\0' "$1" >> "$P"
			if [ "$2" = other ]; then
				known=$known$1$nl
				printf '%s\n' "$1" >> "$G/out-folders"
				# Such a folder stays out whole, and no record shows what it had after the setup, so
				# it keeps the copy: a repository whose state the setup could not record, or another
				# folder, such as one too deep.
				if [ -d "$f" ] && [ ! -h "$f" ]; then
					: > "$B/$n.left"
				fi
			else
				printf '%s\n' "$1" >> "$G/out-paths"
				case $1 in
				*"$nl"*) printf '%s\0' "$1" >> "$G/out-paths-z" ;;
				esac
				printf '%s\n' "$1" >> "$G/setup-kept"
			fi
			;;
		esac
		shift 2
		i=$((i + 1))
	done
	exec 3<&-
	rules
	new_ignored
	setup_rules "$G/setup-kept"
	setup_ignored "$top"
	# The positional parameters are now what stays out besides .qory and $G/out-paths, for ls-files.
	while IFS= read -r d; do
		set -- "$@" ":(exclude,top,literal)$d"
	done < "$G/out-folders"
	# Each folder with a .git stays out of the commit. A .git below a submodule belongs to that
	# submodule. A submodule that the clone copied comes back in the bundle n-k, with the
	# commits and changes the setup did not leave there. Any other submodule stays behind when it is
	# not at the commit of its gitlink, or has a change that the setup did not leave. Any other such
	# folder stays behind when the setup did not make it and no ignore rule covers it: the clone
	# makes no other.
	while IFS= read -r d; do
		if below_gitlink "$d"; then
			continue
		fi
		printf ':(exclude,top,literal)%s\0' "$d" >> "$P"
		set -- "$@" ":(exclude,top,literal)$d"
		entry=$(g ls-tree "$from" -- ":(top,literal)$d" 2>/dev/null) || entry=
		shows=$(printable "$d")
		case $entry in
		"160000 "*)
			link=${entry#160000 commit }
			link=${link%%"$(printf '\t')"*}
			k= cloned=
			while IFS= read -r line; do
				rest=${line#* }
				if [ "${rest#* }" = "$d" ]; then
					k=${line%% *} cloned=${rest%% *}
					break
				fi
			done < "$B/subs-$n"
			st=$((st + 1))
			if [ -n "$k" ]; then
				submodule_status "$d" rules
				from_setup "$d" || :
				submodule_bundle "$d" "$k" "$cloned"
			elif ! submodule_at "$d" "$link" || ! from_setup "$d"; then
				printf 'qory: the submodule %s in %s has commits or changes that the commit leaves out\n' "$shows" "$shown" >&2
				: > "$B/$n.left"
			fi
			;;
		*)
			# A folder of the setup's is no repository the run made, and neither is a repository in
			# one that keeps the copy, nor a submodule of a repository the setup made, whose state
			# has that of its submodules. Any other repository in a repository the setup made, such
			# as a clone in a folder it ignores, is one the run made: the setup's paths list each
			# such repository that the setup made.
			k_up=$d
			while :; do
				case $known in
				*"$nl$k_up$nl"*)
					case $setup_repos in
					*"$nl$k_up$nl"*)
						if [ "$k_up" = "$d" ] || submodule_of "$k_up" "${d#"$k_up"/}"; then
							continue 2
						fi
						break
						;;
					esac
					continue 2
					;;
				esac
				case $k_up in
				*/*) k_up=${k_up%/*} ;;
				*) break ;;
				esac
			done
			if kept_out "$d"; then
				continue
			fi
			printf 'qory: %s has a git repository at %s that the run made, and the commit leaves it out\n' "$shown" "$shows" >&2
			: > "$B/$n.nested"
			printf '%s\n' "$d" >> "$G/made"
			;;
		esac
	done < "$G/folders"
	large "$top" "$P" "$G/out-paths" ./.qory ':(exclude,top,literal,icase).qory' "$@"
	add "$P" "$shown"
	# What stays behind is what the add left: a file that ls-files still lists, or a folder this
	# process cannot read, which no git lists, unless an ignore rule covers it.
	g ls-files -z -m -o --exclude-standard -- ':/' ':(exclude,top,literal,icase).qory' "$@" > "$G/unadded-z" 2>/dev/null || : > "$B/$n.left"
	tr '\000' '\n' < "$G/unadded-z" > "$G/unadded"
	lacking "$G/unadded" "$G/out-paths" > "$G/missed"
	if [ -s "$G/missed" ] || split_hidden "$G/unadded-z" "$G/out-paths"; then
		: > "$B/$n.left"
	fi
	# A path that stays out, that a commit of the run changed, and that here has other bytes than
	# that commit: the undo of the commit's change is in no commit.
	if [ -n "$at" ] && [ "$at" != "$base" ]; then
		g diff-tree -r --no-renames --name-only -z "$base" "$at" > "$G/changed-z" 2>/dev/null || : > "$B/$n.left"
		tr '\000' '\n' < "$G/changed-z" > "$G/changed"
		both "$G/unadded" "$G/out-paths" > "$G/kept"
		# A changed path with a line break reads as other paths, which the kept ones could match.
		if [ -s "$G/kept" ] && broken "$G/changed-z"; then
			: > "$B/$n.left"
		fi
		both "$G/kept" "$G/changed" > "$G/kept-changed"
		# A file left out as too large has its line in n.large.
		lacking "$G/kept-changed" "$G/large" > "$G/undone"
		if [ -s "$G/undone" ]; then
			: > "$B/$n.left"
		fi
	fi
	find . -path ./.git -prune -o -path ./.qory -prune -o -type d -exec sh -c '
		for p do
			test -r "$p" && test -x "$p" || printf "%s\n" "$p"
		done' sh {} + > "$G/closed" 2>/dev/null || :
	while IFS= read -r d; do
		if ! kept_out "${d#./}"; then
			: > "$B/$n.left"
		fi
	done < "$G/closed"
	g reset -q "$back" --pathspec-from-file="$R" --pathspec-file-nul

	# With moved set, the changes go to $prefix/uncommitted, on HEAD.
	set +e
	g diff --cached --quiet --no-ext-diff --ignore-submodules=none "$from" --
	changed=$?
	set -e
	case $changed:$moved:$nocommit in
	0:*) ;;
	1:?*:) uncommitted ;;
	1:?*:?*) ;;
	1::*)
		GIT_AUTHOR_NAME=` + commitName + ` GIT_AUTHOR_EMAIL=` + commitEmail + ` \
		GIT_COMMITTER_NAME=` + commitName + ` GIT_COMMITTER_EMAIL=` + commitEmail + ` \
			g commit -q --no-verify --no-gpg-sign -m "$message"
		;;
	*) exit "$changed" ;;
	esac
	# With nocommit set, n.left is already written and the index is in no commit.
	if [ -z "$nocommit" ]; then
		set -- ':(exclude,top,literal,icase).qory'
		while IFS= read -r d; do
			set -- "$@" ":(exclude,top,literal)$d"
		done < "$G/out-folders"
		leftover "$G/out-paths" "$G/made" "$@"
	fi

	# The bundle: the branch and the run's other refs, without base and the commits before it. With
	# nothing past the base commit on the branch, a commit that any reflog has comes back too, such
	# as one that a reset took off the branch.
	tip=$(g rev-parse --verify "refs/heads/$branch^{commit}")
	mainref=refs/heads/$branch clonerefs=$B/refs-$n reflogs= known=$B/$n.known
	if [ "$(g rev-list --count "$mainref" "^$base")" = 0 ]; then
		reflogs=1
	fi
	extra_refs
	bundle "$B/$n"
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
	n=$((n + 1))
done
`
