// Package reviewcopy makes the copy of a pushed commit that a reviewer reads: the
// commit's files in a folder with no .git, checked against the commit's file list, and the
// diff from the base commit to the pushed one beside them, both read-only.
//
// [Folder.Session] writes the script of a bare session that runs in the copy container,
// with the source repository's git folder and the copy's folder mounted, as the git session
// of internal/runcopy runs. The script reads the source only through its objects folder,
// as the alternates of a new git folder with an empty configuration, no hooks and no
// attribute file, so no setting, hook, fsmonitor or filter of the source's configuration
// runs, and no file that the source's working tree has and the commit does not reaches the
// copy. It checks that the commit's bytes have the commit's id, that the commit's file list
// builds the commit's tree, and that each file of the copy has the bytes and the mode that
// the list has. It checks the base commit's bytes and the base's file list the same way,
// and that each file that the diff reads from an object, of the base or a link of the
// commit, has the bytes of its object id. A link or a submodule of the commit stays out of
// the copy, so no read of the copy follows a link; [Folder.Result] lists them. After the
// session, [Folder.Result] reads what the script wrote.
package reviewcopy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/qoryai/qory/internal/reportfile"
)

// Names of the files and folders below a copy's folder.
const (
	commitName = "commit"
	diffName   = "commit.diff"
	stateName  = "state"
	// ScriptName is the file of the copy's folder that has the session's script. The host
	// writes it for the session and never reads it back.
	ScriptName = "qory-review-copy.sh"
)

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
// does not exist yet, and returns it. [Folder.Result] reads the script's files with
// reportfile.Read, so root has no link in it.
func New(root string) (Folder, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\n\r\x00") {
		return Folder{}, fmt.Errorf("Give the reviewer copy an absolute, clean path on one line.\nThe path is %s.", strconv.Quote(root))
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Folder{}, fmt.Errorf("Give the reviewer copy a folder that does not exist yet.\n%s exists.", strconv.Quote(root))
		}
		return Folder{}, fmt.Errorf("Make the folder of the reviewer copy writable.\n%s: %w", strconv.Quote(root), err)
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
// diff then has every file. Each id is a full commit id, and both have one length.
func (f Folder) Session(source, commit, base string) (Session, error) {
	if !commitID.MatchString(commit) {
		return Session{}, fmt.Errorf("Give the reviewer copy a full commit id.\nThe commit is %s.", strconv.Quote(commit))
	}
	if base != "" && (!commitID.MatchString(base) || len(base) != len(commit)) {
		return Session{}, fmt.Errorf("Give the reviewer copy a full base commit id of the commit's length.\nThe base is %s.", strconv.Quote(base))
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || strings.ContainsAny(source, "\n\r\x00") {
		return Session{}, fmt.Errorf("Give the reviewer copy the absolute, clean path of a git folder.\nThe path is %s.", strconv.Quote(source))
	}
	path := filepath.Join(f.root, ScriptName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Session{}, fmt.Errorf("Give the reviewer copy a new folder: the script could not be written.\n%w", err)
	}
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		return Session{}, fmt.Errorf("Give the reviewer copy a folder with free space: the script could not be written.\n%w", err)
	}
	if err := file.Close(); err != nil {
		return Session{}, err
	}
	return Session{
		Command: "/bin/sh",
		Args:    []string{path, sessionPath, source, f.root, commit, base},
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
// leaves out. The error starts with the fix: the session stopped, ended before its end, or
// left no copy or no diff.
func (f Folder) Result() ([]Entry, error) {
	if data, err := reportfile.Read(filepath.Join(f.state(), "failed"), maxStateFile); err == nil {
		return nil, fmt.Errorf("Escalate the round: the reviewer copy stopped at a check.\n%s", strings.TrimSpace(string(data)))
	}
	if _, err := reportfile.Read(filepath.Join(f.state(), "done"), maxStateFile); err != nil {
		return nil, errors.New("Run the copy session again: it ended before the reviewer copy was complete.")
	}
	if info, err := os.Lstat(f.Commit()); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Run the copy session again: the reviewer copy has no folder %s.", commitName)
	}
	if info, err := os.Lstat(f.Diff()); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Run the copy session again: the reviewer copy has no file %s.", diffName)
	}
	data, err := reportfile.Read(filepath.Join(f.state(), "left-out"), maxStateFile)
	if err != nil {
		return nil, fmt.Errorf("Run the copy session again: its list of the paths it left out cannot be read.\n%w", err)
	}
	var out []Entry
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(line, "\n")
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("Run the copy session again: its list of the paths it left out has a bad line.\n%s", strconv.Quote(line))
		}
		mode, _, _ := strings.Cut(meta, " ")
		out = append(out, Entry{Mode: mode, Path: path})
	}
	return out, nil
}

// script is the session's script. Its arguments are the PATH, the source's git folder, the
// copy's folder, the commit and the base commit, "" for none. It writes the files of the
// commit to commit, the diff to commit.diff, and in state: failed, with the reason, when it
// stops at a check; left-out, the list of the links and submodules of the commit as ls-tree
// lists them; and done, last. The tools it runs besides git are the git session's.
var script = `set -eu
PATH=$1 S=$2 R=$3 commit=$4 base=$5
export PATH
unset XDG_CONFIG_HOME GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_CONFIG_SYSTEM \
	GIT_EXEC_PATH GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_NAMESPACE GIT_TEMPLATE_DIR GIT_CEILING_DIRECTORIES GIT_SSH GIT_SSH_COMMAND \
	GIT_ASKPASS SSH_ASKPASS GIT_EXTERNAL_DIFF GIT_PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR \
	GIT_REPLACE_REF_BASE GIT_PROXY_COMMAND GIT_ATTR_SOURCE GIT_DIFF_OPTS \
	GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
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
# g is git with the settings of internal/runcopy's git session, and no line-end conversion.
g() {
	git --no-pager -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.useReplaceRefs=false \
		-c core.commitGraph=false -c core.attributesFile=/dev/null -c core.excludesFile=/dev/null \
		-c core.untrackedCache=false -c gc.auto=0 -c maintenance.auto=false \
		-c protocol.allow=never -c commit.gpgSign=false -c core.safecrlf=false \
		-c log.showSignature=false -c gpg.program=false -c gpg.openpgp.program=false \
		-c gpg.x509.program=false -c gpg.ssh.program=false \
		-c core.autocrlf=false -c core.symlinks=false "$@"
}
nl='
'
tab=$(printf '\t')
one=$(printf '\001')
if [ -h "$S" ] || [ -h "$S/objects" ] || [ ! -d "$S/objects" ]; then
	fail "the source has no objects folder, or it is a link"
fi
# A new git folder with an empty configuration, no hooks, no attribute file, and the source's
# objects as its alternates: no file of the source but its objects is read.
mkdir "$G" "$G/objects" "$G/objects/info" "$G/refs" "$G/refs/heads" "$G/info" "$W"
: > "$G/config"
if [ ${#commit} -eq 64 ]; then
	printf '[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = sha256\n' > "$G/config"
fi
printf '%s\n' "$S/objects" > "$G/objects/info/alternates"
printf 'ref: refs/heads/main\n' > "$G/HEAD"
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
if [ -n "$base" ]; then
	# The base's file list builds the base's tree, in the index file base-index. git stops
	# at a tree whose bytes do not have its id.
	(
		export GIT_INDEX_FILE="$G/base-index"
		g ls-tree -r -z --full-tree "$base" > "$T/base.z" && g read-tree --empty &&
			g update-index -z --index-info < "$T/base.z" && [ "$(g write-tree)" = "$basetree" ]
	) || fail "the file list of the base commit does not build its tree"
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
g diff --no-ext-diff --no-textconv --no-color "$from" "$commit" > "$R/commit.diff"
chmod -R a-w "$W"
chmod a-w "$R/commit.diff"
printf 'done\n' > "$T/done"
`
