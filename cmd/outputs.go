package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
)

// outboxEnv is the variable that gives a session the path of its outbox, outboxRel the
// outbox in the checkout, or in the working folder of a run with no repository, and
// outboxExclude the line of the checkout's .git/info/exclude that keeps it out of a commit.
const (
	outboxEnv     = "QORY_OUTBOX"
	outboxRel     = ".qory/outbox"
	outboxExclude = "/.qory/outbox/"
)

// maxOutputBytes is the most bytes qory reads of the files of one output whose type sets no
// size_limit in the composed process.yaml.
const maxOutputBytes = 10 << 20

// The marks of [exports.ProcessType.Marks] that qory fills from the push of the target's
// repository, and the one that Qory Apiary sets.
const (
	fillCommits     = "pushed-commits"
	fillHead        = "pushed-head"
	fillBase        = "pushed-base"
	fillBranch      = "pushed-branch"
	fillBaseBranch  = "pushed-base-branch"
	fillCommitsLeft = "pushed-commits-left-out"
)

// The words of the outputs of a session. Line 1 is the fix, then the cause; a type is cut
// as [provisioner.ShownType] cuts it.
const (
	textOutputMissing   = "Write %s to the outbox: the stage requires it, and the session wrote none.\n  %s"
	textOutputHasMember = "Remove %s from the %s output: qory fills it at the push."
	textOutputBroken    = "Fix the %s output: it breaks its schema at %s."
	textOutputNotJSON   = "Write the %s output as one JSON object: the file is not one."
	textOutputUnread    = "Fix the %s output so that qory can read it: %s"
	textOutputNoPush    = "qory run: stored the reply output; the run has no repository to push."
	textOutputNoCommits = "qory run: stored no %s output: the run pushed no commits to %s."
)

// maxShownPlace is the most characters of a place in a schema that a line shows: with a type
// of 19 characters and "qory run: ", line 1 of [textOutputBroken] has at most 100.
const maxShownPlace = 29

// outboxPath is the outbox of the checkout, or working folder, at root.
func outboxPath(root string) string { return filepath.Join(root, filepath.FromSlash(outboxRel)) }

// outputPath is the outbox entry of the type typ, relative to the root that has the outbox: the
// file <typ>/<typ>.json for a structured type, the folder <typ> for any other.
func outputPath(typ string, structured bool) string {
	if structured {
		return path.Join(outboxRel, typ, typ+".json")
	}
	return path.Join(outboxRel, typ)
}

// readOutput reads the outbox entry of the type t under root, the checkout or working folder
// that has the outbox: for a type with a json_schema the one file <type>/<type>.json, and for
// any other type each regular file in the folder <type>. found is false when the entry is not
// there. The session wrote the entry, so the read stays inside root, whatever links it made: a
// link at any part of the path, or as a file, is an error, and so are files over the type's
// size_limit, or [maxOutputBytes] for a type with none.
func readOutput(root string, t exports.ProcessType) (files []runapi.PushFile, found bool, err error) {
	limit := t.SizeLimit
	if limit <= 0 {
		limit = maxOutputBytes
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	entry := outputPath(t.Name, t.Schema != nil)
	info, err := dir.Lstat(entry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var names []string
	switch {
	case t.Schema != nil:
		names = []string{entry}
	case info.IsDir():
		entries, err := fs.ReadDir(dir.FS(), entry)
		if err != nil {
			return nil, true, err
		}
		for _, e := range entries {
			names = append(names, path.Join(entry, e.Name()))
		}
	default:
		return nil, true, errors.New("the path is not a folder")
	}
	var total int64
	for _, name := range names {
		content, err := readRegular(dir, name, limit-total)
		if err != nil {
			return nil, true, err
		}
		total += int64(len(content))
		files = append(files, runapi.PushFile{Filename: path.Base(name), ContentType: contentType(content), Content: content})
	}
	if len(files) == 0 {
		return nil, false, nil
	}
	if t.Schema != nil {
		files[0].ContentType = "application/json"
	}
	return files, true, nil
}

// readRegular reads the regular file name of dir, of at most limit bytes.
func readRegular(dir *os.Root, name string, limit int64) ([]byte, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	// The path may be swapped for a pipe after the check: the open does not wait for a writer,
	// and the opened file is checked again.
	f, err := dir.OpenFile(name, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("the output has more than %d bytes", limit)
	}
	return content, nil
}

// pushRecord is what the push moved in the target's repository, which the marks of a
// delivered type are filled from: the repository on its system, the branch and its head, the
// commit the run started at and the default branch, and the commits from Base to Head, oldest
// first.
type pushRecord struct {
	System, Path     string
	Branch, Head     string
	Base, BaseBranch string
	Commits          []string
	Collection       string
}

// commitReference is the commit commit of rec as a primitive reference: the target's
// collection, and the commit's id as the path on the repository's system.
func (rec *pushRecord) commitReference(commit string) map[string]any {
	return map[string]any{"collection": rec.Collection, "address": map[string]any{"system": rec.System, "path": commit}}
}

// fillOutput is content, an outbox file of the type t, with target set to target when t is
// delivered and each member that t marks filled from rec. A file that has target or a marked
// member is an error that starts with its fix. rec is nil for a run that pushed nothing to
// the target; a marked member then is an error, as there is nothing to fill it from.
func fillOutput(content []byte, t exports.ProcessType, target *contracts.Target, rec *pushRecord) ([]byte, error) {
	shown := provisioner.ShownType(t.Name)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(content, &members); err != nil || members == nil {
		return nil, fmt.Errorf(textOutputNotJSON, shown)
	}
	if t.Send != "" {
		if _, ok := members["target"]; ok {
			return nil, fmt.Errorf(textOutputHasMember, "target", shown)
		}
	}
	marked := slices.Sorted(func(yield func(string) bool) {
		for name := range t.Marks {
			if !yield(name) {
				return
			}
		}
	})
	for _, name := range marked {
		if _, ok := members[name]; ok {
			return nil, fmt.Errorf(textOutputHasMember, shownText(name), shown)
		}
	}
	set := func(name string, v any) error {
		raw, err := json.Marshal(v)
		members[name] = raw
		return err
	}
	if t.Send != "" && target != nil {
		if err := set("target", target); err != nil {
			return nil, err
		}
	}
	for _, name := range marked {
		mark := t.Marks[name]
		if mark == fillCommitsLeft {
			continue
		}
		if rec == nil {
			return nil, fmt.Errorf(textOutputUnread, shown, "the run pushed no repository to fill "+shownText(name)+" from")
		}
		var v any
		switch mark {
		case fillCommits:
			commits := make([]any, 0, len(rec.Commits))
			for _, c := range rec.Commits {
				commits = append(commits, rec.commitReference(c))
			}
			v = commits
		case fillHead:
			v = rec.commitReference(rec.Head)
		case fillBase:
			v = rec.commitReference(rec.Base)
		case fillBranch:
			v = rec.Branch
		case fillBaseBranch:
			v = rec.BaseBranch
		default:
			continue
		}
		if err := set(name, v); err != nil {
			return nil, err
		}
	}
	return json.Marshal(members)
}

// checkOutput checks content, a filled output of the type t, against the type's schema. The
// error starts with its fix, and has the place of the first rule that content breaks.
func checkOutput(content []byte, t exports.ProcessType) error {
	shown := provisioner.ShownType(t.Name)
	schema, err := exports.CompileType(t.Schema)
	if err != nil {
		return fmt.Errorf(textOutputUnread, shown, shownError(err))
	}
	var doc any
	if err := json.Unmarshal(content, &doc); err != nil {
		return fmt.Errorf(textOutputNotJSON, shown)
	}
	if err := schema.Validate(doc); err != nil {
		place := []rune(shownText(exports.FirstBrokenPlace(err)))
		if len(place) > maxShownPlace {
			place = append(place[:maxShownPlace-1], '…')
		}
		return fmt.Errorf(textOutputBroken, shown, string(place))
	}
	return nil
}

// marksCommits reports whether t marks a member with [fillCommits]: such a type gets a version
// only when the run pushed commits to the target.
func marksCommits(t exports.ProcessType) bool {
	for _, mark := range t.Marks {
		if mark == fillCommits {
			return true
		}
	}
	return false
}

// outputName is the name of the artifact that a version of the type t goes to: for a delivered
// type t- and the first 32 lower-case hexadecimal digits of the SHA-256 of the target's
// collection, so each target has one artifact of the type; [defaultName] for any other.
func outputName(t exports.ProcessType, target *contracts.Target) string {
	if t.Send == "" || target == nil {
		return defaultName
	}
	sum := sha256.Sum256([]byte(target.Collection))
	return "t-" + hex.EncodeToString(sum[:])[:32]
}
