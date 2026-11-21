package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
)

// qory destination clone clones the destinations of a run's inputs, the places that the
// run's workspace works on, into [destinationsDir], each once, when the agent needs them.
// A run whose inputs list destinations fetches nothing before its session, see
// [codeRun.listsDestinations]. The clone goes through the wall's proxy, which sets the
// run's read credential outside the container, so the command reads no token.

// destinationsDir is the folder the clones are in, each at <system>/<path>. A variable for
// the tests.
var destinationsDir = preparation.Destinations

// maxClones is the most destinations one session clones.
const maxClones = 100

// clonedFile is the record of the ids cloned in this session, one per line, in
// [destinationsDir]. The underscore is in no system's name, so no clone's folder has the
// name.
//
// shortcut: the count of 100 is per session, because it lives in the session's container; replace it when qory keeps run state across sessions
const clonedFile = "_cloned"

// clonePrefix starts the name of the folder a clone is made in before it moves to its
// place, in [destinationsDir].
const clonePrefix = "_clone-"

// The lines of qory destination clone, each with the fix first. [textNotDestination],
// [textTooManyClones], [textNoAddress], [textNotInSession], [textNoDestinations] and
// [textNotRepository] exit 2, the others 1.
const (
	textNotDestination  = "Use an id from destinations in the run's inputs: %s is not one of them."
	textTooManyClones   = "Clone at most 100 destinations in one run: this call would make clone %d."
	textNoAddress       = "Clone only a destination with an address: %s has none, so it has no repository."
	textNotInSession    = "Run qory destination clone inside a run's session: %s is not set."
	textNoDestinations  = "Run qory destination clone only in a run whose inputs list destinations."
	textNotRepository   = "Clone only a repository: the address of %s is not a repository path."
	textConnection      = "Check the connection of %s in Qory Apiary: the host returned %s to the clone."
	textCloneAgain      = "Run qory destination clone again: the clone of %s failed: %s."
	textBadDestinations = "Report this run to the instance admin of Qory Apiary: inputs.json has malformed destinations.\n  %s"
	textClonesFolder    = "Check %s, then run qory destination clone again.\n  %s"
)

// envHarnessHome is the variable that a run sets to the composed home, which contains
// the run's inputs.json.
const envHarnessHome = "QORY_HARNESS_HOME"

// errNoDestinations is the error of a run whose inputs list no destinations.
var errNoDestinations = errors.New(textNoDestinations)

// destinationID is the form of a destination's id: tg_ and 16 digits or lower-case
// letters other than i, l, o and u.
var destinationID = regexp.MustCompile(`^tg_[0-9a-hjkmnp-tv-z]{16}$`)

// The forms of a destination's target, as the integration contract's definitions have them.
var (
	systemName = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
	noControl  = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)
)

// destination is one entry of the inputs' destinations: id, and the target, a collection
// and an address, nil for a place with none.
type destination struct {
	ID     string `json:"id"`
	Target struct {
		Collection string `json:"collection"`
		Address    *struct {
			System string `json:"system"`
			Path   string `json:"path"`
		} `json:"address"`
	} `json:"target"`
}

// inputDestinations is the member of a run's inputs that lists its destinations.
type inputDestinations struct {
	Destinations []json.RawMessage `json:"destinations"`
}

// listedDestinations is the destinations member of inputs, a JSON object, with each
// entry as it came; none when inputs are not an object or have no such array.
func listedDestinations(inputs []byte) []json.RawMessage {
	var in inputDestinations
	if json.Unmarshal(inputs, &in) != nil {
		return nil
	}
	return in.Destinations
}

// readDestinations reads the destinations of the inputs at path, $QORY_HARNESS_HOME's
// inputs.json, and checks each as the explore inputs' schema has it: 1 to 250 entries,
// each with an id of [destinationID], no id twice, a collection of 1 to 200 characters,
// and an address, when it has one, with a system of [systemName] and a path of 1 to 200
// characters with no control character. A missing file and inputs with no destinations
// are [errNoDestinations].
//
// shortcut: reads destinations with a local reader, until qory pins integrations at T-30's merge, then use the contract's reader
func readDestinations(path string) ([]destination, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoDestinations
	}
	if err != nil {
		return nil, err
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(data, &in); err != nil || in == nil {
		return nil, errors.New("inputs.json is not a JSON object")
	}
	raw, ok := in["destinations"]
	if !ok {
		return nil, errNoDestinations
	}
	var entries []destination
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.New("destinations is not an array of objects")
	}
	if len(entries) < 1 || len(entries) > 250 {
		return nil, fmt.Errorf("destinations has %d entries, not 1 to 250", len(entries))
	}
	for i, d := range entries {
		n := i + 1
		switch {
		case !destinationID.MatchString(d.ID):
			return nil, fmt.Errorf("entry %d has no id of the form tg_ and 16 characters", n)
		case slices.ContainsFunc(entries[:i], func(e destination) bool { return e.ID == d.ID }):
			return nil, fmt.Errorf("entry %d has the id of an entry before it", n)
		case d.Target.Collection == "" || utf8.RuneCountInString(d.Target.Collection) > 200:
			return nil, fmt.Errorf("entry %d has no collection of 1 to 200 characters", n)
		case d.Target.Address == nil:
			continue
		case !systemName.MatchString(d.Target.Address.System):
			return nil, fmt.Errorf("entry %d has no system that is a host name", n)
		case !noControl.MatchString(d.Target.Address.Path) || utf8.RuneCountInString(d.Target.Address.Path) > 200:
			return nil, fmt.Errorf("entry %d has no path of 1 to 200 characters without a control character", n)
		}
	}
	return entries, nil
}

// shownID is arg, an argument of qory destination clone, as a line shows it: printable,
// with a key or a code hidden, and cut to the length of an id.
func shownID(arg string) string {
	shown := integration.Printable(exports.Value(arg, false))
	if r := []rune(shown); len(r) > 19 {
		shown = string(r[:16]) + "..."
	}
	return shown
}

// clone is one destination a call clones: its id, its system and path, and its folder.
type clone struct{ id, system, path, dir string }

// cloneOf is the clone of arg among list: the destination with that id, with an address
// that [preparation.Check] passes as one repository path, in a folder under root.
func cloneOf(arg string, list []destination, root string) (clone, error) {
	i := slices.IndexFunc(list, func(d destination) bool { return d.ID == arg })
	if i < 0 {
		return clone{}, input(fmt.Errorf(textNotDestination, shownID(arg)))
	}
	d := list[i]
	if d.Target.Address == nil {
		return clone{}, input(fmt.Errorf(textNoAddress, d.ID))
	}
	system, path := d.Target.Address.System, d.Target.Address.Path
	// Check splits a list at commas, so one path back, the same, is one repository path.
	paths, err := preparation.Check(system, path)
	if err != nil || len(paths) != 1 || paths[0] != path {
		return clone{}, input(fmt.Errorf(textNotRepository, d.ID))
	}
	dir := filepath.Join(root, system, filepath.FromSlash(path))
	// Check's system and path have no . or .. part and no NUL; the folder stays under root.
	if rel, err := filepath.Rel(root, dir); err != nil || !filepath.IsLocal(rel) || system == "." || system == ".." {
		return clone{}, input(fmt.Errorf(textNotRepository, d.ID))
	}
	return clone{id: d.ID, system: system, path: path, dir: dir}, nil
}

// clonesFolder is the line of a failure of the folder root or of its record, with cause.
func clonesFolder(root string, cause error) error {
	return fmt.Errorf(textClonesFolder, integration.Printable(root), integration.Printable(cause.Error()))
}

// clonedIDs reads the record f, one id of [destinationID] per line.
func clonedIDs(f *os.File) ([]string, error) {
	var ids []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if !destinationID.MatchString(sc.Text()) {
			return nil, fmt.Errorf("%s has a line that is not an id", clonedFile)
		}
		ids = append(ids, sc.Text())
	}
	return ids, sc.Err()
}

// openRecord makes root, mode 0700, where nothing is, refuses a root that is a link or no
// folder, and opens its record with [lockedRecord], so one call at a time reads and
// writes it.
func openRecord(root string) (*os.File, error) {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("it is a link or a file, and qory clones only into a folder")
	}
	return lockedRecord(filepath.Join(root, clonedFile))
}

// cloned reports whether dir is a clone: a folder with a .git folder.
func cloned(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// cloneInto clones d into a new folder under root and moves it to d.dir.
//
// shortcut: a raw git clone in the session skips the id check and the count but stays inside the run's read credential; accepted under 910
func cloneInto(c *cobra.Command, root string, d clone) error {
	tmp, err := os.MkdirTemp(root, clonePrefix)
	if err != nil {
		return clonesFolder(root, err)
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repository")
	if err := preparation.Clone(c.Context(), d.system, d.path, repo); err != nil {
		return cloneFailed(d.id, err)
	}
	if err := os.MkdirAll(filepath.Dir(d.dir), 0o700); err != nil {
		return clonesFolder(root, err)
	}
	if err := os.Rename(repo, d.dir); err != nil {
		return fmt.Errorf(textCloneAgain, d.id, "its folder is in use")
	}
	return nil
}

// The lines git writes, with LC_ALL=C, for a host that wants a credential and for one
// that has no such repository: git names no status for them.
var (
	gitNoCredential = regexp.MustCompile(`could not read Username|Authentication failed`)
	gitNotFound     = regexp.MustCompile(`repository '[^']*' not found`)
)

// cloneFailed is the line of the clone of id that failed with err, with a cause in words
// that contain no part of git's output, which contains the URL: [textConnection] for a
// host that returned 401, 403 or 404, and [textCloneAgain] with the status of the proxy
// or the host, git's exit status, or what stopped git.
func cloneFailed(id string, err error) error {
	var ce *preparation.CloneError
	if !errors.As(err, &ce) {
		return fmt.Errorf(textCloneAgain, id, "git did not start")
	}
	if m := connectStatus.FindStringSubmatch(ce.Output); m != nil {
		return fmt.Errorf(textCloneAgain, id, "the proxy returned "+m[1])
	}
	status := ""
	if m := httpStatus.FindStringSubmatch(ce.Output); m != nil {
		status = m[1]
	} else if gitNoCredential.MatchString(ce.Output) {
		status = "401"
	} else if gitNotFound.MatchString(ce.Output) {
		status = "404"
	}
	switch {
	case status == "401" || status == "403" || status == "404":
		return fmt.Errorf(textConnection, id, status)
	case status != "":
		return fmt.Errorf(textCloneAgain, id, "the host returned "+status)
	case ce.Stopped:
		return fmt.Errorf(textCloneAgain, id, "git was stopped")
	case ce.Exit >= 0:
		return fmt.Errorf(textCloneAgain, id, "git exited with "+strconv.Itoa(ce.Exit))
	}
	return fmt.Errorf(textCloneAgain, id, "git did not start")
}

func newDestination() *cobra.Command {
	c := &cobra.Command{
		Use:   "destination",
		Short: "Clone the destinations of this run",
		Long: `Clone the destinations of this run: the places that the run's workspace works on, which
the run's inputs list.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#clone-a-destination`,
	}
	c.AddCommand(newDestinationClone())
	return c
}

func newDestinationClone() *cobra.Command {
	return &cobra.Command{
		Use:   "clone <id>...",
		Short: "Clone destinations into /tmp/qory-destinations",
		Long: `Clone each destination, by its id in destinations in $QORY_HARNESS_HOME/inputs.json, at depth
one into /tmp/qory-destinations/<system>/<path>, and print one line per id, in the order
given: the id and the folder. A destination cloned before in this session is not cloned
again, and its line is the same. A session clones at most ` + strconv.Itoa(maxClones) + ` destinations.

The clone goes through the run's proxy, which reads with the run's credential.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#clone-a-destination`,
		Example:     `  /qory/qory destination clone tg_0a1b2c3d4e5f6g7h`,
		Args:        func(c *cobra.Command, args []string) error { return input(cobra.MinimumNArgs(1)(c, args)) },
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, args []string) error {
			for _, name := range []string{envRunAPI, envHarnessHome} {
				if os.Getenv(name) == "" {
					return input(fmt.Errorf(textNotInSession, name))
				}
			}
			list, err := readDestinations(filepath.Join(os.Getenv(envHarnessHome), inputsFile))
			switch {
			case errors.Is(err, errNoDestinations):
				return input(err)
			case err != nil:
				return fmt.Errorf(textBadDestinations, integration.Printable(exports.HideURLError(err).Error()))
			}
			root := destinationsDir
			wanted := make([]clone, len(args))
			for i, arg := range args {
				if wanted[i], err = cloneOf(arg, list, root); err != nil {
					return err
				}
			}
			record, err := openRecord(root)
			if err != nil {
				return clonesFolder(root, err)
			}
			defer record.Close()
			recorded, err := clonedIDs(record)
			if err != nil {
				return clonesFolder(root, err)
			}
			counted := slices.Clone(recorded)
			for _, w := range wanted {
				if !slices.Contains(counted, w.id) {
					counted = append(counted, w.id)
				}
			}
			if len(counted) > maxClones {
				return input(fmt.Errorf(textTooManyClones, len(counted)))
			}
			for _, w := range wanted {
				// A clone the record has but whose folder is gone, such as one the agent
				// removed, is cloned again, and counted once. A folder that is a clone
				// already, such as that of another id with the same address, is kept.
				if !cloned(w.dir) {
					if err := cloneInto(c, root, w); err != nil {
						return err
					}
				}
				if !slices.Contains(recorded, w.id) {
					if _, err := fmt.Fprintln(record, w.id); err != nil {
						return clonesFolder(root, err)
					}
					recorded = append(recorded, w.id)
				}
				if _, err := fmt.Fprintln(c.OutOrStdout(), w.id, w.dir); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// hasDestinations reports whether inputs, a run's inputs, list destinations.
func hasDestinations(inputs []byte) bool {
	return len(listedDestinations(bytes.TrimSpace(inputs))) > 0
}
