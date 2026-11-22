package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
)

// qory destination clone clones the destinations of a run's inputs, the places that the
// run's workspace works on, into [destinationsDir], each once, when the agent needs them.
// A read run whose documents contain a harness document and whose inputs list
// destinations fetches nothing before its session, see [codeRun.clonesDestinations]. The
// clone goes through the wall's proxy, which sets the run's read credential outside the
// container, so the command reads no token.

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
// [textNotRepository] exit 2, the others 1. A failure's first line is the gap of the run
// that a member reads in Qory Apiary, so it contains an id and no path of the machine.
const (
	textNotDestination  = "Use an id from destinations in the run's inputs: %s is not one of them."
	textTooManyClones   = "Clone at most 100 destinations in one session: this call would make clone 101."
	textNoAddress       = "Clone only a destination with an address: %s has none, so it has no repository."
	textNotInSession    = "Run qory destination clone inside a run's session: %s is not set."
	textNoDestinations  = "Run qory destination clone only in a run whose inputs list destinations."
	textNotRepository   = "Clone only a repository: the address of %s is not a repository path."
	textConnection      = "Give the integration access to %s in Qory Apiary, then click Run Explore again.\n  The host returned %s to the clone."
	textCloneAgain      = "Run Explore again later: the clone of %s failed: %s."
	textMachine         = "qory destination clone: The machine cannot store %s, so nothing was cloned.\n  %s"
	textBadDestinations = "qory destination clone: The run's destinations do not match their schema, so nothing was cloned.\n  %s"
	textNest            = "qory destination clone: Destination %s is inside another, so nothing was cloned.\n  %s and %s nest: one path lies inside the other."
	textProxyBlocks     = "qory destination clone: The proxy cannot reach %s's host, so nothing was cloned."
)

// allClones is the word of [textMachine] for a record that is not a list of ids,
// which concerns no one id. Every other failure of the folder has the call's first id.
const allClones = "the clones"

// envHarnessHome is the variable that a run sets to the composed home, which contains
// the run's inputs.json.
const envHarnessHome = "QORY_HARNESS_HOME"

// errNoDestinations is the error of a run whose inputs list no destinations.
var errNoDestinations = errors.New(textNoDestinations)

// destinationID is the form of a destination's id: tg_ and 16 digits or lower-case
// letters other than i, l, o and u.
var destinationID = regexp.MustCompile(`^tg_[0-9a-hjkmnp-tv-z]{16}$`)

// destinationsSchema is the schema of the destinations of the explore inputs, explore/v1
// inputs.schema.json at properties.destinations, compiled once.
var destinationsSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return contracts.CompileExplore(destinationsSchemaName)
})

// destinationsSchemaName is the place of [destinationsSchema] in the explore contract.
const destinationsSchemaName = "inputs.schema.json#/properties/destinations"

// errNotObject is the error of inputs that are not a JSON object.
var errNotObject = errors.New("inputs.json is not a JSON object")

// destinationsMember is the member destinations of inputs, a JSON object, by its exact
// name, as it came; nil when inputs have none. It is [errNotObject] for inputs that are
// not an object.
func destinationsMember(inputs []byte) (json.RawMessage, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(inputs, &in); err != nil || in == nil {
		return nil, errNotObject
	}
	return in["destinations"], nil
}

// listedDestinations is the destinations member of inputs, a JSON object, with each
// entry as it came; none when inputs are not an object or have no such array.
func listedDestinations(inputs []byte) []json.RawMessage {
	raw, err := destinationsMember(inputs)
	var list []json.RawMessage
	if err != nil || raw == nil || json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

// readDestinations reads the destinations of the inputs at path, $QORY_HARNESS_HOME's
// inputs.json, as [contracts.Destination] entries, checked against the explore inputs'
// schema of destinations, [destinationsSchema]: 1 to 250 entries, no two equal, each with
// an id of [destinationID], a collection of 1 to 200 characters, and an address, when it
// has one, with a host name for its system and a path of 1 to 200 characters with no
// control character, { or }. The schema cannot check that no two entries have the same
// id, so readDestinations checks it. A missing file, inputs with no destinations and an
// empty or null list are [errNoDestinations], as [listedDestinations] has them.
func readDestinations(path string) ([]contracts.Destination, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoDestinations
	}
	if err != nil {
		return nil, err
	}
	raw, err := destinationsMember(data)
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if raw != nil && json.Unmarshal(raw, &entries) != nil {
		return nil, errors.New("destinations is not an array of objects")
	}
	if len(entries) == 0 {
		return nil, errNoDestinations
	}
	s, err := destinationsSchema()
	if err != nil {
		return nil, fmt.Errorf("the explore contract's schema %s does not compile", destinationsSchemaName)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("destinations is not one JSON value")
	}
	var broken *jsonschema.ValidationError
	if err := s.Validate(value); errors.As(err, &broken) {
		return nil, brokenDestinations(broken)
	} else if err != nil {
		return nil, err
	}
	var list []contracts.Destination
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("destinations is not an array of destinations")
	}
	for i, d := range list {
		if slices.ContainsFunc(list[:i], func(e contracts.Destination) bool { return e.ID == d.ID }) {
			return nil, fmt.Errorf("entry %d has the id of an entry before it", i+1)
		}
	}
	return list, nil
}

// brokenDestinations is the cause of destinations that break [destinationsSchema], from
// e, the schema's error: the entry and the place in it of the first rule broken, by its
// JSON pointer, and the list for a rule of the whole list, such as its length.
func brokenDestinations(e *jsonschema.ValidationError) error {
	for len(e.Causes) > 0 {
		e = e.Causes[0]
	}
	if len(e.InstanceLocation) == 0 {
		return fmt.Errorf("destinations does not match explore/v1 %s", destinationsSchemaName)
	}
	entry, err := strconv.Atoi(e.InstanceLocation[0])
	if err != nil {
		return fmt.Errorf("destinations does not match explore/v1 %s", destinationsSchemaName)
	}
	at := ""
	for _, part := range e.InstanceLocation[1:] {
		at += "/" + integration.Printable(part)
	}
	if at == "" {
		return fmt.Errorf("entry %d does not match the schema of a destination", entry+1)
	}
	return fmt.Errorf("entry %d does not match the schema of a destination at %s", entry+1, at)
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

// clone is one destination a call clones: its id, its system and path, its folder, and
// that folder relative to [destinationsDir].
type clone struct{ id, system, path, dir, rel string }

// cloneOf is the clone of arg among list: the destination with that id, with an address
// that [preparation.Check] passes as one repository path, in a folder under root. A
// destination whose path lies inside the path of another one on the same system, or
// contains it, stops with [textNest]: its clone would put one repository's folder inside
// another's, where a link that the outer repository commits could lead out of root.
func cloneOf(arg string, list []contracts.Destination, root string) (clone, error) {
	i := slices.IndexFunc(list, func(d contracts.Destination) bool { return d.ID == arg })
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
	rel := filepath.Join(system, filepath.FromSlash(path))
	// Check's system and path have no . or .. part and no NUL; the folder stays under root.
	if !filepath.IsLocal(rel) || system == "." || system == ".." {
		return clone{}, input(fmt.Errorf(textNotRepository, d.ID))
	}
	for _, o := range list {
		if o.Target.Address != nil && o.Target.Address.System == system && nest(path, o.Target.Address.Path) {
			return clone{}, fmt.Errorf(textNest, d.ID, d.ID, o.ID)
		}
	}
	return clone{id: d.ID, system: system, path: path, dir: filepath.Join(root, rel), rel: rel}, nil
}

// nest reports whether one of the paths a and b lies inside the other, at a / between
// two parts. The same path does not nest: both ids share one clone.
func nest(a, b string) bool {
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// clonesFolder is the line of a failure to store what, an id or [allClones], in the folder
// root, with cause. The path and the cause go on the second line, from a path error or the
// new path of a rename's error when cause is one.
func clonesFolder(what, root string, cause error) error {
	path, why := root, cause.Error()
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(cause, &pe):
		path, why = pe.Path, pe.Op+": "+pe.Err.Error()
	case errors.As(cause, &le):
		path, why = le.New, le.Op+": "+le.Err.Error()
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return fmt.Errorf(textMachine, what, integration.Printable(path)+": "+integration.Printable(why))
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

// errLinkOrFile is the error of a part of a clone's folder that is a link or a file.
var errLinkOrFile = errors.New("a link or a file, and qory clones only into folders")

// openRecord makes root, mode 0700, where nothing is, stops at a root that is a link or
// no folder, and opens its record with [lockedRecord], so one call at a time reads and
// writes it. It opens root as an [os.Root], through which each clone's folder is made.
func openRecord(root string) (*os.File, *os.Root, error) {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, &fs.PathError{Op: "lstat", Path: root, Err: errLinkOrFile}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	f, err := lockedRecord(filepath.Join(root, clonedFile))
	if err != nil {
		r.Close()
		return nil, nil, err
	}
	return f, r, nil
}

// folders checks each part of rel under r, from the first, with Lstat: each must be a
// folder, never a link, so no path through them leads elsewhere. With mk, it makes each
// missing part, mode 0700; without, a missing part is [fs.ErrNotExist].
func folders(r *os.Root, rel string, mk bool) error {
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		info, err := r.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) && mk {
			if err = r.Mkdir(p, 0o700); err == nil {
				continue
			}
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &fs.PathError{Op: "lstat", Path: p, Err: errLinkOrFile}
		}
	}
	return nil
}

// cloned reports whether d's folder under r is a clone: a folder with a .git folder,
// with no link on the way.
func cloned(r *os.Root, d clone) bool {
	return folders(r, filepath.Join(d.rel, ".git"), false) == nil
}

// errNoClone is the error of a folder at a clone's place that has files and no .git.
var errNoClone = errors.New("a folder with files and no clone")

// placeFree checks the place of d's clone under r before the clone: a link or a file at a
// folder above it, at its folder or at its folder's .git, and a folder there with files and
// no .git, stop it, so no clone is made that cannot be stored. A missing part is free.
func placeFree(r *os.Root, d clone) error {
	err := folders(r, filepath.Dir(d.rel), false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := r.Lstat(d.rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return &fs.PathError{Op: "lstat", Path: d.rel, Err: errLinkOrFile}
	}
	git := filepath.Join(d.rel, ".git")
	info, err = r.Lstat(git)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return &fs.PathError{Op: "lstat", Path: git, Err: errLinkOrFile}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	f, err := r.Open(d.rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err == nil {
		return &fs.PathError{Op: "readdir", Path: d.rel, Err: errNoClone}
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// cloneInto clones d into a new folder under r and moves it to d's folder, whose parts
// [folders] makes, once [placeFree] passed.
//
// shortcut: a raw git clone in the session skips the id check and the count but stays inside the run's read credential; accepted under 910
func cloneInto(c *cobra.Command, r *os.Root, d clone) error {
	root := r.Name()
	if err := placeFree(r, d); err != nil {
		return clonesFolder(d.id, root, err)
	}
	tmp, err := os.MkdirTemp(root, clonePrefix)
	if err != nil {
		return clonesFolder(d.id, root, err)
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repository")
	if err := preparation.Clone(c.Context(), d.system, d.path, repo); err != nil {
		return cloneFailed(d.id, err)
	}
	if err := folders(r, filepath.Dir(d.rel), true); err != nil {
		return clonesFolder(d.id, root, err)
	}
	if err := clearPlace(r, d); err != nil {
		return clonesFolder(d.id, root, err)
	}
	if err := r.Rename(filepath.Join(filepath.Base(tmp), "repository"), d.rel); err != nil {
		return clonesFolder(d.id, root, err)
	}
	return nil
}

// clearPlace makes d's place under r free for the move of its clone, after the clone: it
// removes an empty folder there, which [placeFree] passes and the rename does not replace.
// A link or a file there, which can appear while git runs, stops it with [errLinkOrFile],
// and a folder with files stops it with the Remove's error. Nothing there is free.
func clearPlace(r *os.Root, d clone) error {
	info, err := r.Lstat(d.rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return &fs.PathError{Op: "lstat", Path: d.rel, Err: errLinkOrFile}
	}
	return r.Remove(d.rel)
}

// The lines git writes, with LC_ALL=C, for a host that wants a credential and for one
// that has no such repository: git names no status for them.
var (
	gitNoCredential = regexp.MustCompile(`could not read Username|Authentication failed`)
	gitNotFound     = regexp.MustCompile(`repository '[^']*' not found`)
)

// cloneFailed is the line of the clone of id that failed with err, with a cause in words
// that contain no part of git's output, which contains the URL: [textMachine] for a PATH
// with no git, [textProxyBlocks] for a proxy that returned 403 or 407, [textConnection] for
// a host that returned 401, 403 or 404, and [textCloneAgain] with the status of the proxy
// or the host, git's exit status, or what stopped git.
func cloneFailed(id string, err error) error {
	var ce *preparation.CloneError
	if !errors.As(err, &ce) {
		return fmt.Errorf(textCloneAgain, id, "git did not start")
	}
	if ce.NoGit {
		return fmt.Errorf(textMachine, id, "git is not on PATH")
	}
	if m := connectStatus.FindStringSubmatch(ce.Output); m != nil {
		if m[1] == "403" || m[1] == "407" {
			return fmt.Errorf(textProxyBlocks, id)
		}
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

The clone goes through the wall's proxy, which reads with the run's credential.

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
			record, r, err := openRecord(root)
			if err != nil {
				return clonesFolder(wanted[0].id, root, err)
			}
			defer r.Close()
			defer record.Close()
			recorded, err := clonedIDs(record)
			if err != nil {
				return clonesFolder(allClones, root, err)
			}
			counted := slices.Clone(recorded)
			for _, w := range wanted {
				if !slices.Contains(counted, w.id) {
					counted = append(counted, w.id)
				}
			}
			if len(counted) > maxClones {
				return input(errors.New(textTooManyClones))
			}
			for _, w := range wanted {
				// A clone the record has but whose folder is gone, such as one the agent
				// removed, is cloned again, and counted once. A folder that is a clone
				// already, such as that of another id with the same address, is kept.
				if !cloned(r, w) {
					if err := cloneInto(c, r, w); err != nil {
						return err
					}
				}
				if !slices.Contains(recorded, w.id) {
					if _, err := fmt.Fprintln(record, w.id); err != nil {
						return clonesFolder(w.id, root, err)
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
