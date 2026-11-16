package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/runapi"
)

// The texts of qory artifact push --withdraw for a push that qory refuses, from the
// decision 654 (c). Each starts with its fix; [ExitCode] gives each [ExitArtifactRefused].
const (
	textWithdrawNoVersion = "Pass the name of a stored artifact to --withdraw. %s/%s has no version."
	textWithdrawFiles     = "Pass either files or --withdraw. A withdrawal stores no files, and the command got %d: %s."
	textWithdrawAlready   = "Leave %s/%s as it is. Its newest version, v%d, is already a withdrawal."
	textWithdrawPerson    = "Leave %s/%s to a member. Its newest version, v%d, is an upload by %s, and a run withdraws only a name whose newest version a run wrote."
)

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
	if len(files) > 0 {
		return withdrawRefusedError{fmt.Sprintf(textWithdrawFiles, len(files), strings.Join(files, ", "))}
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

// raceRefusal returns the error of a withdrawal that the run API refused. With one of the
// [withdrawalCodes], which the index that qory read did not show, it reads the index
// again and returns the refusal that it shows, as qory words it; with an index that
// shows none, the run API's message. Any other error comes back as it is.
func raceRefusal(c *cobra.Command, client *runapi.Client, push runapi.Push, err error) error {
	var apiError *runapi.Error
	if !errors.As(err, &apiError) {
		return err
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
// already a withdrawal, is refused as in a run.
func keepWithdrawal(c *cobra.Command, artifactType, name string) error {
	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	newest, empty, err := store.newestVersion(artifactType, name)
	if err != nil {
		return err
	}

	switch {
	case newest == 0:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawNoVersion, artifactType, name)}
	case empty:
		return withdrawRefusedError{fmt.Sprintf(textWithdrawAlready, artifactType, name, newest)}
	}

	dir, err := store.keep(artifactType, name, newest+1, nil)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(c.OutOrStdout(), "Kept the withdrawal in %s%c; not sent: outside a Qory Apiary run, qory keeps versions only in .qory/artifacts.\n",
		store.shown(dir), filepath.Separator)

	return err
}

// newestVersion returns the number of the newest version directory of type and name, 0 for
// none, and whether that directory has no files: only a withdrawal is stored with none.
func (s *artifactStore) newestVersion(artifactType, name string) (number int, empty bool, err error) {
	dir, err := s.realDir(artifactType, name)
	if err != nil {
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

	files, err := os.ReadDir(filepath.Join(dir, "v"+strconv.Itoa(number)))
	if err != nil {
		return 0, false, fmt.Errorf("Check %s. Reading it failed: %w", dir, err)
	}

	return number, len(files) == 0, nil
}
