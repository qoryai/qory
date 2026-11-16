package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/runapi"
)

// The texts of qory artifact push --withdraw for a push that qory refuses, from the
// decision 654 (c). Each starts with its fix; [ExitCode] gives each [ExitArtifactRefused].
const (
	textWithdrawType      = "Push a new version of %s instead.\nQory Apiary reads questions, review-comments, verdict and proposal itself, so a run cannot withdraw them."
	textWithdrawUnknown   = "Define or switch on the artifact type %s in the workspace's Artifact types.\nThen run the round again. The workspace has no such type, or hides it."
	textWithdrawNoVersion = "Withdraw only a stored artifact.\n%s/%s has no version outside cancelled runs."
	textWithdrawFiles     = "Pass either files or --withdraw.\nA withdrawal stores no files, and the command has %d %s: %s."
	textWithdrawAlready   = "Leave %s/%s as it is.\nIts newest version, v%d, is already a withdrawal."
	textWithdrawPerson    = "Leave %s/%s to a member.\nIts newest version, v%d, is an upload by %s, and a run withdraws only a name whose newest version a run wrote."
)

// notWithdrawable are the artifact types that Qory Apiary reads itself, so a run cannot
// withdraw them.
var notWithdrawable = []string{"questions", "review-comments", "verdict", "proposal"}

// withdrawRefusedError is a withdrawal that qory refuses, or that the run API refuses with
// one of the codes of a withdrawal. Its text is the line for the person.
type withdrawRefusedError struct{ text string }

// Error returns the line for the person.
func (e withdrawRefusedError) Error() string { return e.text }

// withdrawalCodes are the codes of the run API's refusals of a withdrawal that depend on
// the work item's artifacts: another run or a member may have changed them since the
// index that qory read.
var withdrawalCodes = []string{
	runapi.CodeNoVersion, runapi.CodeAlreadyWithdrawn, runapi.CodePersonVersion, runapi.CodeWithdrawalWithFiles,
}

// withdrawArtifact is qory artifact push with --withdraw: it refuses files, checks the
// name against the run API's index, then sends the withdrawal. Outside a run it records
// the withdrawal in the store and sends nothing. Every refusal comes before the POST.
func withdrawArtifact(c *cobra.Command, push runapi.Push, files []string) error {
	if slices.Contains(notWithdrawable, push.Type) {
		return withdrawRefusedError{fmt.Sprintf(textWithdrawType, push.Type)}
	}

	if len(files) > 0 {
		return withdrawRefusedError{fmt.Sprintf(textWithdrawFiles, len(files), plural(len(files), "file"), strings.Join(files, ", "))}
	}

	client, err := runAPIClient()
	if err != nil {
		return err
	}

	if client == nil {
		return keepWithdrawal(c, push.Type, push.Name)
	}

	index, err := client.Artifacts(c.Context())
	if err != nil {
		return err
	}

	if refused := withdrawRefusal(index, push.Type, push.Name); refused != nil {
		return refused
	}

	pushed, err := postVersion(c.Context(), client, push)
	if err != nil {
		return raceRefusal(c, client, push, err)
	}

	_, err = fmt.Fprintf(c.OutOrStdout(), "Withdrew %s/%s in version %d.\n", push.Type, push.Name, pushed.Version.Number)

	return err
}

// withdrawRefusal returns the refusal of a withdrawal of artifactType/name against index:
// the name has no version that a cancelled run did not write, its newest such version is
// already a withdrawal, or a person wrote it. It returns nil when the name can be
// withdrawn. The checks come in the order of the run API's.
func withdrawRefusal(index *runapi.Index, artifactType, name string) error {
	var newest *runapi.Version

	for _, artifact := range index.Artifacts {
		if artifact.Type == artifactType && artifact.Name == name {
			newest = newestStored(artifact)
		}
	}

	switch {
	case newest == nil:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawNoVersion, artifactType, name)}
	case newest.Withdrawn:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawAlready, artifactType, name, newest.Number)}
	case newest.Writer.Kind == "person":
		who := newest.Writer.Name
		if who == "" {
			who = "a member"
		}

		return withdrawRefusedError{fmt.Sprintf(textWithdrawPerson, artifactType, name, newest.Number, who)}
	}

	return nil
}

// plural returns noun, with an s when n is not 1.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}

	return noun + "s"
}

// raceRefusal returns the error of a withdrawal that the run API refused. With the code
// unknown_type, the workspace changed its artifact types since qory read the index: the
// refusal for that. With one of the
// [withdrawalCodes], which the index that qory read did not show, it reads the index
// again and returns the refusal that it shows, as qory words it; with an index that
// shows none, the run API's message. Any other error comes back as it is.
func raceRefusal(c *cobra.Command, client *runapi.Client, push runapi.Push, err error) error {
	var apiError *runapi.Error
	if !errors.As(err, &apiError) {
		return err
	}

	if apiError.Code == runapi.CodeUnknownType {
		return withdrawRefusedError{fmt.Sprintf(textWithdrawUnknown, push.Type)}
	}

	known := false

	for _, code := range withdrawalCodes {
		known = known || apiError.Code == code
	}

	if !known {
		return err
	}

	if index, indexErr := client.Artifacts(c.Context()); indexErr == nil {
		if refused := withdrawRefusal(index, push.Type, push.Name); refused != nil {
			return refused
		}
	}

	return withdrawRefusedError{apiError.Error()}
}

// keepWithdrawal records a withdrawal of artifactType/name in the store, outside a run: a
// new version directory with no files. A name with no version, or whose newest version is
// already a withdrawal, is refused as in a run. A refusal creates nothing: the store is
// read before it is opened.
func keepWithdrawal(c *cobra.Command, artifactType, name string) error {
	root, err := storeRoot()
	if err != nil {
		return err
	}

	newest, empty, err := newestVersion(root, filepath.Join(checkout.QoryDir(root), artifactsDir, artifactType, name))
	if err != nil {
		return err
	}

	switch {
	case newest == 0:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawNoVersion, artifactType, name)}
	case empty:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawAlready, artifactType, name, newest)}
	}

	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	dir, err := store.keep(artifactType, name, newest+1, nil)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(c.OutOrStdout(), "Kept the withdrawal in %s%c. Outside a Qory Apiary run, qory sends nothing.\n",
		store.shown(dir), filepath.Separator)

	return err
}

// newestVersion returns the number of the newest version directory in dir, the folder of
// one artifact in the store under root, 0 for none or for no folder, and whether that
// version has no files: only a withdrawal is stored with none. It creates nothing, and
// stops at a link or a file where a folder belongs, as [artifactStore.realDir] does.
func newestVersion(root, dir string) (number int, empty bool, err error) {
	if exists, err := walkFolders(root, dir); err != nil || !exists {
		return 0, false, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, fmt.Errorf("Check %s. Reading it failed: %w", dir, err)
	}

	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "v"))
		if err == nil && strings.HasPrefix(entry.Name(), "v") && n > number {
			number = n
		}
	}

	if number == 0 {
		return 0, false, nil
	}

	version := filepath.Join(dir, "v"+strconv.Itoa(number))
	if _, err := walkFolders(dir, version); err != nil {
		return 0, false, err
	}

	files, err := os.ReadDir(version)
	if err != nil {
		return 0, false, fmt.Errorf("Check %s. Reading it failed: %w", version, err)
	}

	return number, len(files) == 0, nil
}

// walkFolders checks each folder from the one under from down to dir, which is in from:
// each must be a folder, not a link or a file. It reports false when one is missing, and
// creates nothing. The error is the one of [artifactStore.realDir].
func walkFolders(from, dir string) (exists bool, err error) {
	relative, err := filepath.Rel(from, dir)
	if err != nil {
		return false, err
	}

	at := from

	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		at = filepath.Join(at, part)

		info, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}

		if err != nil {
			return false, fmt.Errorf("Check %s. Reading it failed: %w", at, err)
		}

		if !info.IsDir() {
			return false, fmt.Errorf("Remove %s: it must be a directory, not a link or a file.", at)
		}
	}

	return true, nil
}
