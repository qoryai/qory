// Package reviewcopy makes the copy of a pushed commit that a reviewer reads: the
// commit's files in a folder with no .git, checked against the commit's file list, and the
// diff from the base commit to the pushed one beside them, both read-only.
//
// [Folder.Session] writes the script of a bare session that runs in the copy container,
// with the source repository's git folder and the copy's folder mounted, as the git
// session of internal/runcopy runs. The script reads the source only through its objects
// folder, as the alternates of a new git folder with an empty configuration, no hooks and
// no attribute file, so no setting, hook, fsmonitor or filter of the source's
// configuration runs, and no file that the source's working tree has and the commit does
// not reaches the copy. It checks that the commit's bytes have the commit's id, that the
// commit's file list builds the commit's tree, and that each file of the copy has the
// bytes and the mode that the list has. It checks the bytes and the file list of the base
// commit and of the project base the same way, and that each file that the diff reads from
// an object, of the base or a link of the commit, has the bytes of its object id. It lists
// the paths of the runtime's project files that the change from the project base adds,
// changes or removes, see [Folder.Changed]. A link or a submodule of the commit stays out
// of the copy, so no read of the copy follows a link; [Folder.Result] lists them. After
// the session, [Folder.Result] reads what the script wrote.
package reviewcopy

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/qoryai/qory/internal/reportfile"
)

// Names of the files and folders below a copy's folder.
const (
	commitName  = "commit"
	diffName    = "commit.diff"
	stateName   = "state"
	changedName = "changed"
	// ScriptName is the file of the copy's folder that has the session's script. The host
	// writes it for the session and never reads it back.
	ScriptName = "qory-review-copy.sh"
)

// Stop is an error of the copy that stops the review before it starts. Cause is the cause
// without the words "so the review did not start", and Detail the line below it. The
// caller of the join makes the stop's text from both.
type Stop struct {
	Cause, Detail string
	Err           error
}

// Error is the cause, then the detail after a colon.
func (e *Stop) Error() string {
	if e.Detail == "" {
		return e.Cause
	}
	return e.Cause + ": " + e.Detail
}

// Unwrap is the error that Detail shows, nil for none.
func (e *Stop) Unwrap() error { return e.Err }

// stop is a [*Stop] whose detail shows err, kept for [errors.Is] and [errors.As].
func stop(cause string, err error) *Stop {
	detail := err.Error()
	var file *reportfile.Error
	if errors.As(err, &file) {
		// The text of err starts with a fix for a report, which does not fit this stop.
		detail = strconv.Quote(file.Path) + " " + file.What()
	}
	return &Stop{Cause: cause, Detail: detail, Err: err}
}

// maxStateFile is the most bytes of a file of the state folder that [Folder.Result] reads.
const maxStateFile = 4 << 20

// sessionPath is the PATH of the script: folders of the copy container's image, as the git
// session of internal/runcopy has them.
var sessionPath = "/usr/local/bin:/usr/bin:/bin"

// commitID is the form of a commit id: 40 or 64 lower-case hexadecimal digits.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Folder is the folder of one reviewer copy: the commit's files in commit, the diff in
// commit.diff, and the script's work files in state, which no reviewer mounts.
type Folder struct{ root string }

// New makes the folder of a reviewer copy at root, an absolute and clean path whose folder
// does not exist yet, and returns it. The caller resolves root once, as it makes the
// folder: [Folder.Result] reads the script's files below root with reportfile.Read, which
// follows no link from root down.
func New(root string) (Folder, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\n\r\x00") {
		return Folder{}, &Stop{Cause: "The path of the review's copy is not an absolute, clean path on one line", Detail: "The path is " + strconv.Quote(root)}
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Folder{}, &Stop{Cause: "The folder of the review's copy exists already", Detail: strconv.Quote(root) + " exists"}
		}
		return Folder{}, stop("Making the folder of the commit's copy failed", err)
	}
	return Folder{root: root}, nil
}

// Root is the copy's folder.
func (f Folder) Root() string { return f.root }

// Commit is the folder that has the commit's files: the value commit of qory.review.
func (f Folder) Commit() string { return filepath.Join(f.root, commitName) }

// Diff is the file that has the diff from the base commit to the commit: the value diff of
// qory.review.
func (f Folder) Diff() string { return filepath.Join(f.root, diffName) }

// state is the script's folder, which no reviewer mounts.
func (f Folder) state() string { return filepath.Join(f.root, stateName) }

// Session is the bare session that makes the copy: the program, its arguments and its
// environment, for a session with no runtime in the copy container. The session mounts the
// source's git folder, read-only where it can, and the copy's folder, writable.
type Session struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// Session writes the script of the session that copies commit from source, the git folder
// of a repository that has it, such as the .git of a run's copy or a bare repository, and
// writes the diff from base to commit beside it. base is "" for a commit with no base: the
// diff then has every file. projectBase is the commit that the list of changed project
// files starts at, "" for none: the list then has each project file of the commit. Each id
// is a full commit id, and all have one length. project are the paths of the runtime's
// project files, see [render.CommandGuard.ProjectPaths]: each a clean, relative path with
// forward slashes on one line, with no * ? [ or \.
func (f Folder) Session(source, commit, base, projectBase string, project []string) (Session, error) {
	if !commitID.MatchString(commit) {
		return Session{}, &Stop{Cause: "The commit of the review's copy is not a full commit id", Detail: "The commit is " + strconv.Quote(commit)}
	}
	if base != "" && (!commitID.MatchString(base) || len(base) != len(commit)) {
		return Session{}, &Stop{Cause: "The base of the review's copy is not a full commit id of the commit's length", Detail: "The base is " + strconv.Quote(base)}
	}
	if projectBase != "" && (!commitID.MatchString(projectBase) || len(projectBase) != len(commit)) {
		return Session{}, &Stop{Cause: "The project base of the review is not a full commit id of the commit's length", Detail: "The project base is " + strconv.Quote(projectBase)}
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || strings.ContainsAny(source, "\n\r\x00") {
		return Session{}, &Stop{Cause: "The git folder of the review's copy is not an absolute, clean path", Detail: "The path is " + strconv.Quote(source)}
	}
	args := []string{"", sessionPath, source, f.root, commit, base, projectBase}
	for _, p := range project {
		if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") ||
			strings.ContainsAny(p, "*?[\\\n\r\x00") {
			return Session{}, &Stop{Cause: "A path of the runtime's project files is not a clean, relative path", Detail: "The path is " + strconv.Quote(p)}
		}
		// The path at the root or in any folder below it, the file or link at it and
		// everything under the folder at it; icase: a path in another case matches too, as
		// on a file system that ignores case. The check above leaves no wildcard in p.
		args = append(args, ":(glob,icase)**/"+p, ":(glob,icase)**/"+p+"/**")
	}
	args[0] = filepath.Join(f.root, ScriptName)
	file, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Session{}, stop("qory could not write the script that copies the commit", err)
	}
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		return Session{}, stop("qory could not write the script that copies the commit", err)
	}
	if err := file.Close(); err != nil {
		return Session{}, stop("qory could not write the script that copies the commit", err)
	}
	return Session{
		Command: "/bin/sh",
		Args:    args,
		Env: []string{
			"PATH=" + sessionPath, "HOME=" + filepath.Join(f.state(), "home"),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		},
		Dir: f.root,
	}, nil
}

// Entry is a path of the commit that the copy leaves out, with its mode: 120000 for a link
// and 160000 for a submodule.
type Entry struct {
	Mode string
	Path string
}

// Result reads what the session wrote, once it ended: the paths of the commit that the copy
// leaves out. The error is a [*Stop] when the session stopped, ended before its end, or
// left no copy or no diff.
func (f Folder) Result() ([]Entry, error) {
	if data, err := reportfile.Read(f.root, filepath.Join(stateName, "failed"), maxStateFile); err == nil {
		return nil, &Stop{Cause: "A check of the commit's copy failed", Detail: strings.TrimSpace(string(data))}
	}
	if _, err := reportfile.Read(f.root, filepath.Join(stateName, "done"), maxStateFile); err != nil {
		return nil, &Stop{Cause: "The session that copies the commit ended before the copy was complete"}
	}
	if info, err := os.Lstat(f.Commit()); err != nil || !info.IsDir() {
		return nil, &Stop{Cause: "The copy of the commit has no folder " + commitName}
	}
	if info, err := os.Lstat(f.Diff()); err != nil || !info.Mode().IsRegular() {
		return nil, &Stop{Cause: "The copy of the commit has no file " + diffName}
	}
	data, err := reportfile.Read(f.root, filepath.Join(stateName, "left-out"), maxStateFile)
	if err != nil {
		return nil, stop("The list of paths that the copy left out could not be read", err)
	}
	var out []Entry
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(line, "\n")
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, &Stop{Cause: "The list of paths that the copy left out has a line qory cannot read", Detail: strconv.Quote(line)}
		}
		mode, _, _ := strings.Cut(meta, " ")
		out = append(out, Entry{Mode: mode, Path: path})
	}
	return out, nil
}

// Changed reads the paths that the session listed in state/changed, once [Folder.Result]
// returned no error: each path of the runtime's project files, as Session got them, that
// the change from the project base adds, changes or removes, none when the change has
// none. With no project base, each such path of the commit is in the list. The error is a
// [*Stop].
func (f Folder) Changed() ([]string, error) {
	data, err := reportfile.Read(f.root, filepath.Join(stateName, changedName), maxStateFile)
	if err != nil {
		return nil, stop("The list of the project files that the change touches could not be read", err)
	}
	var out []string
	for p := range strings.SplitSeq(string(data), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// FreshObjects is POSIX shell text that defines two functions for a script that reads
// objects from a git folder that a program it does not trust could write, such as a run's
// checkout:
//
//   - g runs git with the settings of internal/runcopy's git session: no hooks, fsmonitor,
//     replace refs, commit graph, attribute or exclude file, signing program or protocol,
//     and no line-end conversion.
//   - fresh_objects TARGET ID... makes the git folder TARGET, with no alternates, and
//     copies into it each ID, a commit, with every tree and blob of its tree, not its
//     parents: git rev-list --objects --no-walk lists them in the folder in GIT_DIR, git
//     pack-objects packs them, and git index-pack, which hashes every object, writes them
//     into TARGET. An object whose bytes do not have its id so lands under another id. It
//     returns non-zero when a git fails, or when TARGET lacks a listed object under its id
//     or an ID as a commit. IDs of 64 characters make a SHA-256 folder. It needs git 2.25
//     or later, for rev-list --no-object-names.
//
// A caller sets GIT_DIR to the folder to read, runs fresh_objects, then sets GIT_DIR to
// TARGET for every later read. The script of [Folder.Session] uses it so.
const FreshObjects = `
# g is git with the settings of internal/runcopy's git session, and no line-end
# conversion.
g() {
	git --no-pager -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false -c core.safecrlf=false \
		-c log.showSignature=false -c gpg.program=false -c gpg.openpgp.program=false \
		-c gpg.x509.program=false -c gpg.ssh.program=false \
		-c core.autocrlf=false -c core.symlinks=false "$@"
}
# fresh_objects TARGET ID... copies each ID, a commit, with its trees and blobs, from the
# folder in GIT_DIR into the new folder TARGET through pack-objects and index-pack, and
# returns non-zero when TARGET lacks one of the objects that it listed. Each step is
# checked on its own: set -e does not hold in a function that a || calls.
fresh_objects() (
	target=$1
	shift
	[ $# -gt 0 ] && [ ! -e "$target" ] || exit 1
	mkdir "$target" "$target/objects" "$target/objects/info" "$target/objects/pack" \
		"$target/refs" "$target/refs/heads" "$target/info" || exit 1
	: > "$target/config" || exit 1
	if [ ${#1} -eq 64 ]; then
		printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$target/config" || exit 1
	fi
	printf 'ref: refs/heads/main\n' > "$target/HEAD" || exit 1
	printf '%s\n' "$@" > "$target/ids" || exit 1
	g rev-list --objects --no-object-names --no-walk --stdin < "$target/ids" > "$target/list" || exit 1
	# No deltas: an object whose bytes are another's could make two deltas of each other.
	g pack-objects --stdout --quiet --no-reuse-delta --window=0 --depth=0 < "$target/list" > "$target/pack" ||
		exit 1
	GIT_DIR=$target g index-pack --stdin < "$target/pack" > /dev/null || exit 1
	: > "$target/pack"
	# Each listed object is in TARGET under its id: an object whose bytes have another id
	# is not. A commit among them has its parents' ids, which are not listed.
	GIT_DIR=$target g cat-file --batch-check='%(objectname) %(objecttype)' < "$target/list" > "$target/found" || exit 1
	# The list has ids alone, so only a missing object ends a line with " missing".
	if grep -q ' missing$' "$target/found"; then
		exit 1
	fi
	for id in "$@"; do
		[ "$(GIT_DIR=$target g cat-file -t "$id" 2>/dev/null)" = commit ] || exit 1
	done
)
`

// script is the session's script. Its arguments are the PATH, the source's git folder, the
// copy's folder, the commit, the base commit, "" for none, the project base, "" for none,
// and the pathspecs of the paths of the runtime's project files. It writes the files of
// the commit to commit, the diff to commit.diff, and in state: failed, with the reason,
// when it stops at a check; left-out, the list of the links and submodules of the commit
// as ls-tree lists them; changed, the paths that the pathspecs match and the change from
// the project base adds, changes or removes, each followed by a NUL; and done, last. The
// tools it runs besides git are the git session's.
var script = `set -eu
PATH=$1 S=$2 R=$3 commit=$4 base=$5 pbase=$6
shift 6
export PATH
unset XDG_CONFIG_HOME GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_CONFIG_SYSTEM \
	GIT_EXEC_PATH GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_NAMESPACE GIT_TEMPLATE_DIR GIT_CEILING_DIRECTORIES GIT_SSH GIT_SSH_COMMAND \
	GIT_ASKPASS SSH_ASKPASS GIT_EXTERNAL_DIFF GIT_PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR \
	GIT_REPLACE_REF_BASE GIT_PROXY_COMMAND GIT_ATTR_SOURCE GIT_DIFF_OPTS \
	GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
	GIT_LITERAL_PATHSPECS GIT_GLOB_PATHSPECS GIT_NOGLOB_PATHSPECS GIT_ICASE_PATHSPECS
T=$R/state
G=$T/git
W=$R/commit
export HOME="$T/home" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
	GIT_NO_REPLACE_OBJECTS=1 GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 \
	GIT_NO_LAZY_FETCH=1 GIT_ALLOW_PROTOCOL=none LC_ALL=C
umask 022
mkdir "$T" "$HOME"
# fail writes the reason to state/failed and stops.
fail() {
	printf '%s\n' "$1" > "$T/failed"
	printf 'qory review copy: %s\n' "$1" >&2
	exit 1
}
` + FreshObjects + `nl='
'
tab=$(printf '\t')
one=$(printf '\001')
if [ -h "$S" ] || [ -h "$S/objects" ] || [ ! -d "$S/objects" ]; then
	fail "the source has no objects folder, or it is a link"
fi
# A view of the source: a new git folder with an empty configuration, no hooks, no
# attribute file, and the source's objects as its alternates, so no file of the source but
# its objects is read.
V=$T/source
mkdir "$V" "$V/objects" "$V/objects/info" "$V/refs" "$V/refs/heads" "$W"
: > "$V/config"
if [ ${#commit} -eq 64 ]; then
	printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$V/config"
fi
printf '%s\n' "$S/objects" > "$V/objects/info/alternates"
printf 'ref: refs/heads/main\n' > "$V/HEAD"
export GIT_DIR="$V"
# Each id names a commit of the source. Its bytes are checked in the fresh folder below.
[ "$(g cat-file -t "$commit" 2>/dev/null)" = commit ] ||
	fail "the source has no commit $commit with the bytes of that id"
ids=$commit
if [ -n "$base" ]; then
	[ "$(g cat-file -t "$base" 2>/dev/null)" = commit ] ||
		fail "the source has no base commit $base with the bytes of that id"
	ids="$ids $base"
fi
if [ -n "$pbase" ]; then
	[ "$(g cat-file -t "$pbase" 2>/dev/null)" = commit ] ||
		fail "the source has no project base commit $pbase with the bytes of that id"
	ids="$ids $pbase"
fi
# Every check and diff below reads the fresh folder G, which has no alternates: index-pack
# hashed each object into it, so no pack or loose file of the source can show other bytes
# for an id to one read than to another. The ids are hex, so the split is safe.
# shellcheck disable=SC2086
fresh_objects "$G" $ids ||
	fail "the objects of the commit, the base and the project base could not be copied by their ids"
export GIT_DIR="$G" GIT_INDEX_FILE="$G/index"
empty=$(g hash-object -t tree /dev/null)
# git 2.40 and later read attributes from the empty tree alone, so no .gitattributes of the
# commit changes a file of the copy or the diff. An earlier git reads them; the check of each
# file below then writes the file again from its object.
export GIT_ATTR_SOURCE="$empty"
# checked reports whether the object $1 is a commit whose bytes have the id $1, and leaves its
# bytes in state/object.
checked() {
	[ "$(g cat-file -t "$1" 2>/dev/null)" = commit ] || return 1
	g cat-file commit "$1" > "$T/object" || return 1
	[ "$(g hash-object --no-filters -t commit "$T/object")" = "$1" ]
}
if ! checked "$commit"; then
	fail "the source has no commit $commit with the bytes of that id"
fi
read -r word tree < "$T/object"
[ "$word" = tree ] || fail "the commit $commit has no tree"
from=$empty
if [ -n "$base" ]; then
	checked "$base" || fail "the source has no base commit $base with the bytes of that id"
	read -r word basetree < "$T/object"
	[ "$word" = tree ] || fail "the base commit $base has no tree"
	from=$base
fi
pfrom=$empty
if [ -n "$pbase" ]; then
	checked "$pbase" || fail "the source has no project base commit $pbase with the bytes of that id"
	read -r word ptree < "$T/object"
	[ "$word" = tree ] || fail "the project base commit $pbase has no tree"
	pfrom=$pbase
fi
# The file list: ls-tree's lines, with a NUL in place of each line end. A path with a line
# break, or with 0x01, which stands for one, stops the copy.
g ls-tree -r -z --full-tree "$commit" > "$T/tree.z"
tr '\n\000' '\001\n' < "$T/tree.z" > "$T/tree"
if grep "$one" "$T/tree" > /dev/null; then
	fail "the commit has a path with a line break or the character 0x01"
fi
# The list builds the commit's tree, so it has every path, mode and object of the commit.
g read-tree --empty
g update-index -z --index-info < "$T/tree.z"
[ "$(g write-tree)" = "$tree" ] || fail "the file list of the commit does not build its tree"
# want is the regular files of the list; left-out is its links and submodules, which the
# copy leaves out of the index, so no checkout writes them.
while IFS= read -r line; do
	meta=${line%%"$tab"*}
	case ${meta%% *} in
	100644 | 100755) printf '%s\n' "$line" ;;
	*) printf '%s\n' "$line" >&3 ;;
	esac
done < "$T/tree" > "$T/want" 3> "$T/left-out"
while IFS= read -r line; do
	printf '%s\n' "${line#*"$tab"}"
done < "$T/left-out" | tr '\n' '\000' > "$T/left-out.z"
if [ -s "$T/left-out.z" ]; then
	g update-index --force-remove -z --stdin < "$T/left-out.z"
fi
(
	export GIT_WORK_TREE="$W"
	g checkout-index -a
)
# listing writes to state/got a line per file of the copy in the form of ls-tree, with the
# file's object id as git hashes its bytes, and stops at a link or a special file.
listing() {
	cd "$W"
	find . ! -type d ! -type f -print > "$T/other"
	[ ! -s "$T/other" ] || fail "the copy has a link or a special file"
	find . -type f -print > "$T/have"
	execs=$nl$(find . -type f -perm -100 -print)$nl
	g hash-object --no-filters --stdin-paths < "$T/have" > "$T/ids"
	while IFS= read -r p <&3 && IFS= read -r id <&4; do
		case $execs in
		*"$nl$p$nl"*) mode=100755 ;;
		*) mode=100644 ;;
		esac
		printf '%s blob %s\t%s\n' "$mode" "$id" "${p#./}"
	done 3< "$T/have" 4< "$T/ids" > "$T/got"
	cd "$R"
	sort "$T/want" > "$T/want.s"
	sort "$T/got" > "$T/got.s"
}
listing
# A file that attributes changed at the checkout is written again from its object.
comm -23 "$T/want.s" "$T/got.s" > "$T/again"
while IFS= read -r line; do
	meta=${line%%"$tab"*} p=${line#*"$tab"}
	id=${meta##* }
	case $p in
	*/*) mkdir -p "$W/${p%/*}" ;;
	esac
	g cat-file blob "$id" > "$W/$p"
	case ${meta%% *} in
	100755) chmod u+x,go+x "$W/$p" ;;
	*) chmod a-x "$W/$p" ;;
	esac
done < "$T/again"
listing
if [ -n "$(comm -3 "$T/want.s" "$T/got.s")" ]; then
	fail "the files of the copy do not match the file list of the commit"
fi
# tree_built reports whether the file list of the commit $1 builds its tree $2, in the
# index file $3. git stops at a tree whose bytes do not have its id, and a tree that the
# list does not build has another id.
tree_built() (
	export GIT_INDEX_FILE="$G/$3"
	g ls-tree -r -z --full-tree "$1" > "$T/$3.z" && g read-tree --empty &&
		g update-index -z --index-info < "$T/$3.z" && [ "$(g write-tree)" = "$2" ]
)
if [ -n "$base" ]; then
	tree_built "$base" "$basetree" base-index || fail "the file list of the base commit does not build its tree"
fi
# The project base's trees, which the list of changed project files reads; a project base
# that is the base had its check.
if [ -n "$pbase" ] && [ "$pbase" != "$base" ]; then
	tree_built "$pbase" "$ptree" pbase-index ||
		fail "the file list of the project base commit does not build its tree"
fi
# The diff reads from its object each file of the base that it lists, and each link of the
# commit; the check of the copy covers the commit's other files. Each has the bytes of its
# object id. The mode 000000 stands for no file, and a submodule has no object here.
g diff --raw --no-renames --no-abbrev "$from" "$commit" > "$T/raw"
blob_checked() {
	case $1 in 000000 | 160000) return 0 ;; esac
	[ "$(g cat-file blob "$2" | g hash-object --no-filters --stdin)" = "$2" ]
}
while read -r frommode tomode fromid toid rest; do
	case $tomode in 120000) ;; *) tomode=000000 ;; esac
	blob_checked "${frommode#:}" "$fromid" && blob_checked "$tomode" "$toid" ||
		fail "the diff has a file whose bytes do not have its object id"
done < "$T/raw"
# The paths of the project files that the change from the project base adds, changes or
# removes, or none when the runtime has no project files: a diff with no pathspec would
# list every path.
# --no-renames lists both paths of a move.
: > "$T/changed"
if [ $# -gt 0 ]; then
	g diff --name-only -z --no-renames "$pfrom" "$commit" -- "$@" > "$T/changed"
fi
# --text: no attribute marks a file binary and leaves its change out of the diff, also in git
# before 2.40, which does not read GIT_ATTR_SOURCE.
g diff --text --no-ext-diff --no-textconv --no-color "$from" "$commit" > "$R/commit.diff"
chmod -R a-w "$W"
chmod a-w "$R/commit.diff"
printf 'done\n' > "$T/done"
`
