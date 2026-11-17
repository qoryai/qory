package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
)

// textNotChecked is the cause in [textNotStored] of a version of the push step that did
// not pass its push check.
const textNotChecked = "the version did not pass the check for %s"

// The texts of qory run for its push checks. The load's errors start with their fix, alone
// in the first line, and the cause follows on an indented line;
// textCheckNotFinished is the status line of a push whose check returned an error.
const (
	textChecksNoWall     = "Set wall.adapter to docker, and run again.\n  The push checks of %s run in containers of the docker wall alone."
	textChecksNoRunAPI   = "Open the run through a gateway that sends the run API address, and run again.\n  The push checks read the artifacts through the run API, which failed: %s."
	textChecksBadRunAPI  = "Update the run's gateway, and run again.\n  The run API address that the gateway sent is not one the push checks can use: %s."
	textChecksEngine     = "Switch the engine back to the one the run started on, and run again.\n  The push checks would run on the engine %s, and the run's containers are on the engine %s."
	textChecksNoFolder   = "Check the folder of the push checks' files, and run again.\n  The folder is %s, and making it failed: %s."
	textChecksBadImage   = "Set wall.image or --image to an image reference, and run again.\n  Push checks cannot start from %s. A reference starts with a letter or a digit and has only letters, digits, and . _ / : @ -."
	textChecksNoImage    = "Pull the wall's default image onto this machine, and run again.\n  Push checks run in it, and the engine does not have the image %s."
	textChecksNotSet     = "Run again.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: %s."
	textCheckNotFinished = "the check before a push did not finish, and the push was not stored (503 " + runapi.CodePushCheckUnavailable + "): %s"
)

// The errors of [runPushChecks.load] and [runPushChecks.set] for a run from a run code. The
// code is used by then, so the run starts again only from the new run code that taking the
// work item in again makes. That run opens on the engine selected at its start, so switching
// the engine back does not help this one.
const (
	textChecksNoWallFromCode    = "Set wall.adapter to docker, then take the work item in again in Qory Apiary.\n  The push checks of %s run in containers of the docker wall alone" + codeUsed
	textChecksNoRunAPIFromCode  = "Take the work item in again in Qory Apiary, through a gateway that sends the run API address.\n  The push checks read the artifacts through the run API, which failed: %s" + codeUsed
	textChecksBadRunAPIFromCode = "Update the run's gateway, then take the work item in again in Qory Apiary.\n  The run API address that the gateway sent is not one the push checks can use: %s" + codeUsed
	textChecksEngineFromCode    = "Take the work item in again in Qory Apiary, and do not switch the engine while that run starts.\n  The push checks would run on the engine %s, and the run's containers are on the engine %s" + codeUsed
	textChecksNoFolderFromCode  = "Check the folder of the push checks' files, then take the work item in again in Qory Apiary.\n  The folder is %s, and making it failed: %s" + codeUsed
	textChecksBadImageFromCode  = "Set wall.image or --image to an image reference, then take the work item in again in Qory Apiary.\n  Push checks cannot start from %s" + codeUsed
	textChecksNoDefaultFromCode = "Set wall.image in forager.yaml, then take the work item in again in Qory Apiary.\n  Push checks run in the wall's default image" + codeUsed
	textChecksNoImageFromCode   = "Pull the wall's default image, then take the work item in again in Qory Apiary.\n  Push checks run in it, and the engine does not have the image %s" + codeUsed
	textChecksNotSetFromCode    = "Take the work item in again in Qory Apiary.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: %s" + codeUsed
	textChecksUnreadFromCode    = "Take the work item in again in Qory Apiary, so that the new run composes the harness again.\n  The push checks in %s could not be read: %s" + codeUsed
)

// checksStop is the error of a line that stops a run with push checks, with args in it:
// text, or textFromCode for a run from a run code, fromCode.
func checksStop(fromCode bool, text, textFromCode string, args ...any) error {
	if fromCode {
		text = textFromCode
	}
	return fmt.Errorf(text, args...)
}

// The folder in the run directory that holds the temporary folders of the run's push
// checks, and the label, NAME=value, that the wall removes the run's containers by,
// followed by the run's id, as Forager's wall has it.
const (
	pushChecksDir = "push-checks"
	runLabel      = "dev.qory.run="
)

// engineSelectWait is how long the selection of the push checks' engine may take.
const engineSelectWait = 30 * time.Second

// loadPushChecks reads the push checks of a home; tests replace it.
var loadPushChecks = pushcheck.Load

// runPushChecks is the push checks of a walled run, from the home qory run composed for
// it: [runPushChecks.runAPICheck] checks each POST of the run API from behind the wall,
// once [runPushChecks.set] turned it on, and [runPushChecks.check] the push step's
// version. In a run whose home has no push check, nothing is checked.
type runPushChecks struct {
	mu      sync.Mutex
	checker *pushcheck.Checker
	// report writes one status line of qory run, masked; nil writes none.
	report func(string)
	// fromCode is whether the run comes from a run code: its load and set errors are the
	// lines of [checksStop] for such a run.
	fromCode bool
	// inputs is the path of the run's inputs.json, which the check gets, see
	// [pushcheck.Checker.Inputs]; "" for a run with none. inputsData is the bytes qory
	// wrote there.
	inputs     string
	inputsData []byte
}

// current is the checker, nil before the load or for a home with no push check.
func (p *runPushChecks) current() *pushcheck.Checker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checker
}

// set turns on the check of each POST of the run API from behind the wall of run, with
// [runPushChecks.runAPICheck], when the load found push checks; with none, it sets no
// check. Call it after the load and before the run's setup and join.
func (p *runPushChecks) set(run openedRun) error {
	if p.current() == nil {
		return nil
	}
	if err := run.SetRunAPICheck(p.runAPICheck); err != nil {
		return checksStop(p.fromCode, textChecksNotSet, textChecksNotSetFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	return nil
}

// runAPICheck is [pushcheck.Checker.RunAPICheck] of the run's checker, for
// [session.Run.SetRunAPICheck]. Forager writes no line for its error, so qory run
// writes it as a status line.
func (p *runPushChecks) runAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	c := p.current()
	if c == nil {
		return nil, nil
	}
	refusal, err := c.RunAPICheck(ctx, req)
	// report is qory run's status line, which masks the run's secrets.
	if err != nil && p.report != nil {
		p.report(fmt.Sprintf(textCheckNotFinished, shownError(err)))
	}
	return refusal, err
}

// check is [pushcheck.Checker.Check] of the run's checker.
func (p *runPushChecks) check(ctx context.Context, artifactType string, files []pushcheck.File) (*pushcheck.Failure, error) {
	c := p.current()
	if c == nil {
		return nil, nil
	}
	return c.Check(ctx, artifactType, files)
}

// load reads the push checks of home, the home qory run composed for the run runID,
// which the wall of spec mounts read-only at that path. The checks run in a container
// from the wall's default image, see [checkImage], on the engine of the wall, as the
// run's user, with the wall's limits and the run's label, and read the artifacts through
// the run's run API on this machine. A run whose engine does not have the wall's default
// image stops here, before the setup and the agent. Their temporary folders are in a
// folder of the run directory, which done removes. A home with no push check, and a run
// with no control plane, controlPlane false, load nothing. An error starts with its fix.
func (p *runPushChecks) load(ctx context.Context, run openedRun, home, runID string, spec *runSpec, controlPlane bool) (done func(), err error) {
	checker, err := pushChecker(ctx, run, home, runID, spec, controlPlane, p.fromCode)
	if err != nil || checker == nil {
		return func() {}, err
	}
	checker.Inputs, checker.InputsData = p.inputs, p.inputsData
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checker = checker
	return func() { os.RemoveAll(checker.Dir) }, nil
}

// pushChecker is the checker of [runPushChecks.load], nil for a home with no push check
// and for a run with no control plane, which has no run API and takes no push. fromCode
// is a run from a run code, see [checksStop].
func pushChecker(ctx context.Context, run openedRun, home, runID string, spec *runSpec, controlPlane, fromCode bool) (*pushcheck.Checker, error) {
	checks, err := loadPushChecks(home)
	var unread *pushcheck.UnreadError
	if fromCode && errors.As(err, &unread) {
		return nil, fmt.Errorf(textChecksUnreadFromCode, unread.File, unread.Cause)
	}
	if err != nil || len(checks) == 0 || !controlPlane {
		return nil, err
	}
	d, ok := spec.Wall.(*wall.Docker)
	if !ok {
		return nil, checksStop(fromCode, textChecksNoWall, textChecksNoWallFromCode, exports.Value(home, false))
	}
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, checksStop(fromCode, textChecksNoRunAPI, textChecksNoRunAPIFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, checksStop(fromCode, textChecksBadRunAPI, textChecksBadRunAPIFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	engine, err := checkEngine(ctx, d, fromCode)
	if err != nil {
		return nil, err
	}
	user := d.User
	if user == "" {
		// The wall's default: the uid:gid of this process.
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	// A run from a run code runs its checks in the wall's default image alone: its
	// policy, from the server, may select an image built from the repository.
	if fromCode && spec.Image == "" {
		return nil, errors.New(textChecksNoDefaultFromCode)
	}
	// The image goes on the engine's command line: a value of another shape than a
	// reference, such as one that starts with -, stops the run before any command gets it.
	image := checkImage(run, spec)
	if !config.IsImageRef(image) {
		return nil, checksStop(fromCode, textChecksBadImage, textChecksBadImageFromCode, exports.Value(image, true))
	}
	checker := &pushcheck.Checker{
		Home: home, Checks: checks, Image: image, User: user,
		Engine: engine,
		Fetch:  artifactsForCheck(client),
		Dir:    filepath.Join(run.Dir(), pushChecksDir),
		Limits: wall.Limits{CPUs: spec.Limits.CPUs, Memory: spec.Limits.Memory, PIDs: spec.Limits.PIDs},
		Label:  runLabel + runID,
		Mask:   secretmask.Default().Replace,
	}
	// The run's image, for a run with no default image, comes in the run's image step,
	// after this load. An inspect that fails for another cause than a missing image lets
	// the run go on: a check that cannot start stops its push with 503, and the push
	// stores nothing.
	if spec.Image != "" {
		if has, err := checker.HasImage(ctx); err == nil && !has {
			return nil, checksStop(fromCode, textChecksNoImage, textChecksNoImageFromCode, exports.Value(checker.Image, false))
		}
	}
	// The folder is qory's user's alone. For a run's user that is another user, its mode
	// lets that user pass through to the folders of a check, which it cannot list.
	mode := fs.FileMode(0o700)
	if uid, _, _ := strings.Cut(user, ":"); uid != strconv.Itoa(os.Getuid()) {
		mode = 0o711
	}
	noFolder := func(err error) error {
		return checksStop(fromCode, textChecksNoFolder, textChecksNoFolderFromCode, exports.Value(checker.Dir, false), strings.TrimSuffix(shownError(exports.HideError(err)), "."))
	}
	if err := os.Mkdir(checker.Dir, 0o700); err != nil {
		return nil, noFolder(err)
	}
	if err := os.Chmod(checker.Dir, mode); err != nil {
		os.RemoveAll(checker.Dir)
		return nil, noFolder(err)
	}
	return checker, nil
}

// checkImage is the reference of the image that the push checks of a run of spec start
// from: the wall's default image, --image or wall.image, which Forager reads as the name
// of an image of wall.images first and as a reference otherwise. A change to the
// repository, such as one to its .devcontainer/ that a prebuilt image is built from,
// changes the run's image and not this one, so it cannot turn the checks off. A run on a
// person's machine, with no run code and no default image, which its policy from
// --policy allows, runs them in the run's image; [pushChecker] stops a run from a run
// code with no default image before it calls this.
func checkImage(run openedRun, spec *runSpec) string {
	if spec.Image == "" {
		return run.Image().Ref
	}
	for _, i := range spec.Images {
		if i.Name == spec.Image {
			return i.Ref
		}
	}
	return spec.Image
}

// checkEngine is the engine of the push checks of a run behind d: d's command, with the
// selection of the engine that the wall recorded at the run's open, as a new
// [wall.Docker.Engine] reads it. A new selection that reaches an engine with another id
// than the recorded one is an error, which starts with its fix: the checks would run
// on another engine than the run's containers. With no recorded selection, or an engine
// that gives no id, it is d's command with this process's environment. fromCode is a run
// from a run code, see [checksStop].
func checkEngine(ctx context.Context, d *wall.Docker, fromCode bool) (pushcheck.Engine, error) {
	ctx, cancel := context.WithTimeout(ctx, engineSelectWait)
	defer cancel()
	e := pushcheck.Engine{Command: d.Command}
	id, err := d.EngineID(ctx)
	if err != nil || id == "" {
		return e, nil
	}
	selected := (&wall.Docker{Command: d.Command}).Engine(ctx)
	if selected.ID != id {
		return pushcheck.Engine{}, checksStop(fromCode, textChecksEngine, textChecksEngineFromCode, engineShown(selected.ID), engineShown(id))
	}
	return pushcheck.Engine{Command: selected.Command, Env: append([]string{}, selected.Env...)}, nil
}

// engineShown is an engine's id as a text shows it, "with no id" for none.
func engineShown(id string) string {
	if id == "" {
		return "with no id"
	}
	return exports.Value(id, true)
}

// notesFile is the file of a push check's working folder with the work item's notes.
const notesFile = "notes.json"

// textNotesNotList is the error of a work item whose notes in the run API's response are
// not a JSON array.
const textNotesNotList = "Contact support: the notes in the run API's response are not a JSON array."

// artifactsForCheck is the fetch of a push check through client: the work item's notes
// as notes.json, the artifact index, and the newest version of each artifact of the
// types that is neither cancelled nor a withdrawal, laid out as qory artifact pull writes
// them into its store. For a withdrawn name that is the version before the withdrawal, so
// a later push of the name is checked against what it had before; qory artifact pull
// leaves the name out.
func artifactsForCheck(client *runapi.Client) pushcheck.Fetch {
	return func(ctx context.Context, dir string, types []string) error {
		snapshot, err := client.WorkItem(ctx)
		if err != nil {
			return err
		}
		notes, err := checkNotes(snapshot.Notes)
		if err != nil {
			return err
		}
		index, err := client.Artifacts(ctx)
		if err != nil {
			return err
		}
		store := &artifactStore{root: filepath.Dir(dir), dir: dir, fresh: true}
		for _, artifact := range index.Artifacts {
			if !slices.Contains(types, artifact.Type) {
				continue
			}
			chosen := newestBeforeWithdrawal(artifact)
			if chosen == nil {
				continue
			}
			if err := store.fetch(ctx, client, artifact, *chosen); err != nil {
				return err
			}
		}
		if err := store.write(dir, notesFile, func(w io.Writer) error {
			_, err := w.Write(notes)
			return err
		}); err != nil {
			return err
		}
		return store.writeIndex(index.Raw)
	}
}

// checkNotes is the content of notes.json for raw, the notes of the work item's
// snapshot: raw as the run API returned it, or [] when the response has none or null.
func checkNotes(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []byte("[]"), nil
	}
	if trimmed[0] != '[' {
		return nil, errors.New(textNotesNotList)
	}
	return trimmed, nil
}

// checkBranchVersion runs the push check of the artifact type branch on content, the
// file of the push step's version of branch at ref, before the step stores it. A check
// that does not pass is the step's error: the step's fix line, then the check's line and
// its findings, each a line of qory run. So is a check that could not run.
func checkBranchVersion(ctx context.Context, checks *runPushChecks, branch *preparation.WorkBranch, ref string, content []byte) error {
	failed, err := checks.check(ctx, branchType, []pushcheck.File{{Name: branchFile, Content: content}})
	if err != nil {
		return fmt.Errorf(textNotStored, branch.VersionWays(), ref, shownError(err))
	}
	if failed == nil {
		return nil
	}
	lines := []string{fmt.Sprintf(textNotStored, branch.VersionWays(), ref, fmt.Sprintf(textNotChecked, branchType)), failed.Step}
	lines = append(lines, failed.Findings...)
	return errors.New(strings.Join(lines, "\nqory run: "))
}
