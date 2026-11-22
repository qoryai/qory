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
	"github.com/qoryai/qory/internal/preparation"
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
	textOutputMissing   = "Write %s to the outbox: the stage requires it and the session wrote none.\n  %s"
	textOutputHasMember = "Remove %s from the %s output: qory fills it at the push."
	textOutputBroken    = "Fix the %s output: it breaks its schema at %s."
	textOutputNotJSON   = "Write the %s output as one JSON object: the file is not one."
	textOutputUnread    = "Fix the %s output so that qory can read it.\n  %s"
	textOutputNoPush    = "qory run: stored the %s output; the run has no repository to push."
	textOutputNoCommits = "qory run: stored no %s output: the run pushed no commits to the target.\n  Target: %s"
	textSchemaUnread    = "Report the run to the instance admin of Qory Apiary: an output's schema does not convert.\n  The json_schema of artifact type %s is not a JSON Schema that qory can read: %s. The run code is used up."
	textDeliverNoType   = "Define %s in the harness's process.yaml: the run documents deliver it.\n  The composed harness defines no artifact type %s, so qory cannot check or fill its output. The run code is used up."
	textOutputTooBig    = "Cut the %s output to at most %d bytes: the next run starts from it.\n  With target and the members that qory fills, %s has %d bytes."
	textOutboxNotEmpty  = "Remove .qory/outbox, then start the run again: a session starts with no outbox.\n  Emptying the outbox in %s failed: %s. The run code is used up."
)

// maxShownPlace is the most characters of a place in a schema that a line shows: with a type
// of 19 characters and "qory run: ", line 1 of [textOutputBroken] has at most 100.
const maxShownPlace = 29

// outboxPath is the outbox of the checkout, or working folder, at root.
func outboxPath(root string) string { return filepath.Join(root, filepath.FromSlash(outboxRel)) }

// clearOutbox removes the outbox under root, the checkout or working folder, before a
// session starts, so that no output which an earlier session left is stored after this one.
// It removes nothing else: the removal stays inside root, .qory has to be a folder and no
// link, and an outbox that is a link is removed as the link. An error is [textOutboxNotEmpty].
func clearOutbox(root string) error {
	failed := func(err error) error { return fmt.Errorf(textOutboxNotEmpty, shownText(root), shownError(err)) }
	dir, err := os.OpenRoot(root)
	if err != nil {
		return failed(err)
	}
	defer dir.Close()
	info, err := dir.Lstat(".qory")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return failed(err)
	}
	if !info.IsDir() {
		return failed(errors.New(".qory is not a folder"))
	}
	if err := dir.RemoveAll(outboxRel); err != nil {
		return failed(err)
	}
	return nil
}

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
// size_limit, or [maxOutputBytes] for a type with none, and a folder of more than
// [runapi.MaxFiles] entries, which is an error before any file is read.
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
	// Each folder on the way is a folder and no link: os.Root follows a link that stays in root.
	for _, folder := range []string{".qory", outboxRel, path.Join(outboxRel, t.Name)} {
		info, err := dir.Lstat(folder)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
		if !info.IsDir() {
			return nil, true, fmt.Errorf("%s is not a folder", folder)
		}
	}
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
		// The count comes before any file is read: a version has at most [runapi.MaxFiles].
		if err := runapi.CheckVersion(t.Name, make([]int64, len(entries))); err != nil {
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
// the target; a member that qory fills from the push, see [pushFill], then is an error, as
// there is nothing to fill it from. A member with another mark is left as it is.
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
			return nil, fmt.Errorf(textOutputHasMember, provisioner.ShownType(shownText(name)), shown)
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
		if !pushFill(mark) {
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

// pushFill reports whether qory fills a member that has mark from the push: not for
// [fillCommitsLeft], which Qory Apiary sets, nor for a mark that qory does not know.
func pushFill(mark string) bool {
	switch mark {
	case fillCommits, fillHead, fillBase, fillBranch, fillBaseBranch:
		return true
	}
	return false
}

// marksPushFill reports whether t marks a member that qory fills from the push, see [pushFill].
func marksPushFill(t exports.ProcessType) bool {
	for _, mark := range t.Marks {
		if pushFill(mark) {
			return true
		}
	}
	return false
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

// runOutputs is what the push step needs to store the outputs of a session: the folder that
// has the outbox, the checkout or the working folder, the stage's outputs from the work item,
// the artifact types of the composed process.yaml, the documents' target, nil for none, and
// whether the run has no repository, which gets [textOutputNoPush] for each stored output.
type runOutputs struct {
	root         string
	stage        []runapi.Output
	types        []exports.ProcessType
	target       *contracts.Target
	noRepository bool
}

// readyOutput is an output of the outbox that passed its checks: the version to store, and the
// type's folder of the outbox, relative to the root that has the outbox, from which the
// version's files go once the version is stored.
type readyOutput struct {
	push   runapi.Push
	folder string
}

// typeOf is the artifact type typ of types, or a type with only its name when the composed
// process.yaml does not define it.
func typeOf(types []exports.ProcessType, typ string) exports.ProcessType {
	for _, t := range types {
		if t.Name == typ {
			return t
		}
	}
	return exports.ProcessType{Name: typ}
}

// recordOf is what this run pushed to the repository of target, from the branch's version
// after the push: nil for a run with no branch, a target with no address, and a repository
// that this run pushed nothing to. The commits are those from the run's start to the head of
// the branch, across all sessions.
func recordOf(branch *preparation.WorkBranch, target *contracts.Target) *pushRecord {
	if branch == nil {
		return nil
	}
	return recordIn(branch.Version, branch.Ref, target)
}

// recordIn is [recordOf] from version, a version of the branch ref.
func recordIn(version []preparation.Repository, ref string, target *contracts.Target) *pushRecord {
	if target == nil || target.Address == nil {
		return nil
	}
	for _, r := range version {
		if r.Base == nil || r.System != target.Address.System || !preparation.SameRepository(r.System, r.Path, target.Address.Path) {
			continue
		}
		rec := &pushRecord{
			System: r.System, Path: r.Path, Collection: target.Collection,
			Branch: ref, Head: r.Commit, Base: r.Base.Commit, BaseBranch: r.Base.Branch,
		}
		for _, c := range r.Commits {
			rec.Commits = append(rec.Commits, c.Commit)
		}
		return rec
	}
	return nil
}

// ready reads, fills and checks each output of the stage that the outbox has, before any
// version is stored, so an output that breaks its schema stores nothing. rec is what the
// push moved in the target's repository, see [recordOf]. A type that marks pushed-commits,
// or any member that qory fills from the push ([marksPushFill]), of a run that pushed nothing
// to the target gets no version, and a line on w. missing is each required output that the
// outbox does not have.
func (o *runOutputs) ready(w io.Writer, rec *pushRecord, note string) (out []readyOutput, missing []string, err error) {
	for _, stage := range o.stage {
		t := typeOf(o.types, stage.Type)
		shown := provisioner.ShownType(t.Name)
		files, found, err := readOutput(o.root, t)
		if err != nil {
			return nil, nil, fmt.Errorf(textOutputUnread, shown, shownError(err))
		}
		if !found {
			if stage.Required {
				missing = append(missing, stage.Type)
			}
			continue
		}
		if t.Schema != nil {
			if marksPushFill(t) && (rec == nil || marksCommits(t) && len(rec.Commits) == 0) {
				fmt.Fprintf(w, textOutputNoCommits+"\n", shown, targetShown(o.target))
				continue
			}
			content, err := fillOutput(files[0].Content, t, o.target, rec)
			if err != nil {
				return nil, nil, err
			}
			if err := checkOutput(content, t); err != nil {
				return nil, nil, err
			}
			files[0].Content = content
		}
		// The next run reads a delivered output's file to find its start, see [references].
		for _, f := range files {
			if t.Send != "" && len(f.Content) > maxStartFile {
				return nil, nil, fmt.Errorf(textOutputTooBig, shown, maxStartFile, shownText(f.Filename), len(f.Content))
			}
		}
		sizes := make([]int64, len(files))
		for i, f := range files {
			sizes[i] = int64(len(f.Content))
		}
		if err := runapi.CheckVersion(t.Name, sizes); err != nil {
			return nil, nil, fmt.Errorf(textOutputUnread, shown, shownError(err))
		}
		push := runapi.Push{Type: t.Name, Name: outputName(t, o.target), Note: note, Files: files}
		out = append(out, readyOutput{push: push, folder: path.Join(outboxRel, t.Name)})
	}
	return out, missing, nil
}

// targetShown is the target as a line shows it: its address, or its collection when it has no
// address.
func targetShown(target *contracts.Target) string {
	switch {
	case target == nil:
		return "the target"
	case target.Address != nil:
		return shownText(target.Address.System + "/" + target.Address.Path)
	}
	return shownText(target.Collection)
}

// removeStored removes the files of r's version from r's folder of the outbox under root,
// once the version is stored, and the folder when it is empty then. A file that the version
// does not have stays, and so does its folder.
func removeStored(root string, r readyOutput) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	for _, f := range r.push.Files {
		if err := dir.Remove(path.Join(r.folder, f.Filename)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	left, err := fs.ReadDir(dir.FS(), r.folder)
	if err != nil || len(left) > 0 {
		return err
	}
	return dir.Remove(r.folder)
}
