// Package places keeps the record of every place a walled run could write: the checkout
// a walled run ran in, and each read-write mount it had. A run in a container can write
// any file there, a git configuration and a qory.yaml among them, and qory on this
// machine reads those files afterwards. The record says which places those are, and
// holds the fingerprints taken when a place was first recorded, so a later command can
// tell a setting the person had from one a run wrote.
//
// A record is one JSON file under [Dir], named by [Name], mode 0600 in a directory of mode
// 0700 inside qory's state directory, which Forager refuses to mount into a run. It holds
// hashes only, never a value: a git setting can hold a token, and a URL credentials.
//
// [Update] reads, changes and writes a record under one lock, through a temporary file
// renamed into place. [Load] reads every record. [Starting] holds a place for a walled
// run that starts there, and [InUse] reports whether a walled run uses one. A record is
// never removed by a run; the person removes it, or qory worktree remove does when it
// deletes the worktree's folder.
package places

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
)

// Version is the format of a record.
const Version = 1

// Kinds of place.
const (
	// KindCheckout is the checkout a walled run ran in.
	KindCheckout = "checkout"
	// KindMount is a read-write mount of a walled run.
	KindMount = "mount"
)

// Record is one place a walled run could write, and what it held when it was recorded.
type Record struct {
	// Version is the record's format, [Version].
	Version int `json:"version"`
	// Path is the place, its real path: symlinks resolved, each name in the case its
	// folder lists, see [Real].
	Path string `json:"path"`
	// Kind is [KindCheckout] or [KindMount].
	Kind string `json:"kind"`
	// Recorded is when the first walled run recorded the place.
	Recorded time.Time `json:"recorded"`
	// Run is the id of that run.
	Run string `json:"run"`
	// Accepted is when the person last accepted the place as it was, nil for never.
	Accepted *time.Time `json:"accepted,omitempty"`
	// Git holds the repositories whose git settings were fingerprinted: the place's
	// repository when the place is a repository's work tree or git directory, and any
	// repository inside it the person accepted.
	Git []Repository `json:"git,omitempty"`
	// Qory holds the fingerprints of the qory files in the place, nil until they are
	// taken.
	Qory *Qory `json:"qory,omitempty"`

	// file is the path of the file the record was read from, "" for one not read.
	file string
}

// Repository is the fingerprint of one repository's git settings.
type Repository struct {
	// Worktree is the directory holding the repository's .git, "" for a git directory
	// reached without one.
	Worktree string `json:"worktree,omitempty"`
	// GitDir is the repository's git directory as git resolved it, its real path.
	GitDir string `json:"git_dir"`
	// CommonDir is the repository's common directory, its real path: the git directory
	// of the main worktree, which holds config.
	CommonDir string `json:"common_dir"`
	// Settings are the hashes of the settings in CommonDir/config, one per key and value,
	// sorted; see [Setting].
	Settings []string `json:"settings"`
	// WorktreeSettings are the hashes of the settings in the git directory's
	// config.worktree, sorted; none when the file is not there.
	WorktreeSettings []string `json:"worktree_settings,omitempty"`
	// Objects maps objects/info/alternates and info/grafts, relative to CommonDir with
	// forward slashes, to the hash of each as it was, "-" for one that was not there;
	// nil in a record written before qory took them.
	Objects map[string]string `json:"objects,omitempty"`
	// ObjectLinks maps each link among the objects of CommonDir as it was, by Lstat:
	// objects itself, and each entry of objects, objects/pack, objects/info and every
	// other folder in objects, such as objects/ab, relative to CommonDir with forward
	// slashes, to "link " and the path it led to with its links resolved; none when there
	// was none, and in a record written before qory took them.
	ObjectLinks map[string]string `json:"object_links,omitempty"`
	// RefLinks maps each link among the refs of CommonDir as it was, by Lstat: HEAD,
	// packed-refs, refs itself and each entry under refs, and in each worktree's git
	// directory, worktrees/<name> itself, its HEAD and its refs, relative to CommonDir with
	// forward slashes, to "link " and the path it led to with its links resolved; none
	// when there was none, and in a record written before qory took them.
	RefLinks map[string]string `json:"ref_links,omitempty"`
}

// Qory is the section that holds the fingerprints of the qory files in a place.
type Qory struct {
	// Files maps each qory file in the place, relative to the place with forward
	// slashes, to the hashes of the keys it sets that qory acts on, sorted.
	Files map[string][]string `json:"files,omitempty"`
	// Harness maps each home composed in the place, by its real path, to the hashes of
	// what the last compose wrote there.
	Harness map[string]Harness `json:"harness,omitempty"`
}

// Harness is the fingerprint of one composed home: its report and its tree, as the last
// compose wrote them.
type Harness struct {
	// Report is the hash of the report beside the home.
	Report string `json:"report"`
	// Home is the hash of the home's tree: every path in it, its kind and mode, and a
	// file's content or a link's target.
	Home string `json:"home"`
}

// Setting is the hash of one git setting as git config --list prints it: the key, and
// the value when the setting has one. A setting written without a value, such as a bare
// "bare" line, hashes apart from one whose value is empty.
func Setting(key string, value *string) string {
	h := sha256.New()
	h.Write([]byte(key))
	if value == nil {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
		h.Write([]byte(*value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Holds reports whether the sorted hashes contain h.
func Holds(hashes []string, h string) bool {
	i := sort.SearchStrings(hashes, h)
	return i < len(hashes) && hashes[i] == h
}

// ErrRelativeStateHome is the refusal of an XDG_STATE_HOME that is no absolute path,
// which [StateDir] returns before anything is read from the directory it would name.
var ErrRelativeStateHome = errors.New("qory: XDG_STATE_HOME is not an absolute path; qory keeps its state only in an absolute one, so that it does not change with the working folder")

// ErrNoStateDir is a state directory that cannot be found: HOME and XDG_STATE_HOME are
// both unset.
var ErrNoStateDir = errors.New("no state directory: set HOME or XDG_STATE_HOME")

// StateDir is qory's state directory, absolute: $XDG_STATE_HOME/qory, else
// ~/.local/state/qory when XDG_STATE_HOME is unset. An XDG_STATE_HOME that is set but is
// no absolute path is [ErrRelativeStateHome], so that the record of the places never
// moves with the working folder, and no HOME with no XDG_STATE_HOME is [ErrNoStateDir].
func StateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		if !filepath.IsAbs(base) {
			return "", ErrRelativeStateHome
		}
		return filepath.Join(base, "qory"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ErrNoStateDir
	}
	return filepath.Abs(filepath.Join(home, ".local", "state", "qory"))
}

// Dir is the directory of the records, places under [StateDir].
func Dir() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "places"), nil
}

// Real is path made absolute with its symlinks resolved, or made absolute and clean
// when it cannot be resolved, such as a path that is not there; either way with each
// name that exists as the folder that contains it lists it, see [cased].
func Real(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return cased(real)
	}
	return cased(abs)
}

// cased is the absolute, clean path with each of its names that exists as the folder
// that contains it lists it, from the root down to the first name that does not exist,
// and the names from there on as given. No link in it is resolved. On a file system
// that ignores case, such as macOS's, /Users/v/Code and /Users/v/code are one folder,
// and cased returns the case that folder's parent lists for both. A name the folder
// lists as given stays; another case of it is taken only when it is the same file. On a
// file system that tells names apart by case, such as Linux's, path is returned as it
// is.
func cased(path string) string {
	if !filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return path
	}
	names := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	out := string(filepath.Separator)
	for i, name := range names {
		if name == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(out, name))
		if err != nil {
			return filepath.Join(append([]string{out}, names[i:]...)...)
		}
		out = filepath.Join(out, storedName(out, name, info))
	}
	return out
}

// storedName is the name in the folder dir that is the file info, which dir lists as
// name in some case: name when dir lists it as given, or when no other case of it is the
// same file, and otherwise the case dir lists.
func storedName(dir, name string, info fs.FileInfo) string {
	swapped := strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, name)
	if swapped == name {
		return name
	}
	// A file system that tells names apart by case has no file by the swapped name, or
	// another file: name is the one it lists.
	if other, err := os.Lstat(filepath.Join(dir, swapped)); err != nil || !os.SameFile(info, other) {
		return name
	}
	names := listing(dir)
	folded := names[strings.ToLower(name)]
	if slices.Contains(folded, name) {
		return name
	}
	for _, n := range folded {
		if other, err := os.Lstat(filepath.Join(dir, n)); err == nil && os.SameFile(info, other) {
			return n
		}
	}
	return name
}

// listings is what [listing] keeps for the life of the process: the names of each folder
// listed, with the folder's file info when it was listed.
var listings struct {
	sync.Mutex
	folders map[string]listed
}

// listed is one folder's names, by their lower case, and its file info then.
type listed struct {
	info  fs.FileInfo
	names map[string][]string
}

// listing returns the names in the folder dir, by their lower case, none when it cannot
// be listed. A folder listed before is listed again only when it is another file now, or
// its modification time changed, as it does when a name in it is added, removed or
// changed.
func listing(dir string) map[string][]string {
	info, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	listings.Lock()
	defer listings.Unlock()
	if l, ok := listings.folders[dir]; ok && os.SameFile(l.info, info) && l.info.ModTime().Equal(info.ModTime()) {
		return l.names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := map[string][]string{}
	for _, e := range entries {
		k := strings.ToLower(e.Name())
		names[k] = append(names[k], e.Name())
	}
	if listings.folders == nil || len(listings.folders) >= 4096 {
		listings.folders = map[string]listed{}
	}
	listings.folders[dir] = listed{info: info, names: names}
	return names
}

// Name is the file name of the record of the place at path: its base name and the first
// 12 hex digits of the SHA-256 of its real path, with .json.
func Name(path string) string {
	real := Real(path)
	sum := sha256.Sum256([]byte(real))
	return filepath.Base(real) + "-" + hex.EncodeToString(sum[:])[:12] + ".json"
}

// Within reports whether path is place or lies inside it. Both are compared as given,
// and again with their symlinks resolved, so a path reached through a link counts.
func Within(place, path string) bool {
	if inside(place, path) {
		return true
	}
	return inside(Real(place), Real(path))
}

// Inside reports whether path lies in place both as written and with its symlinks
// resolved: path made absolute and clean is place or lies inside place as given or as
// resolved, and path resolved lies inside place resolved. A path that is inside as
// written but leads out through a link, or that leads in through a link from outside,
// is not.
func Inside(place, path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// As written, but each name in the case the folder that contains it lists it.
	written := cased(abs)
	return (inside(cased(place), written) || inside(Real(place), written)) && inside(Real(place), Real(abs))
}

func inside(place, path string) bool {
	rel, err := filepath.Rel(place, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Containing returns the records whose place holds path, nearest first.
func Containing(records []Record, path string) []Record {
	var found []Record
	for _, r := range records {
		if Within(r.Path, path) {
			found = append(found, r)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return len(found[i].Path) > len(found[j].Path) })
	return found
}

// Load reads every record. No directory is no record. A file that is not a record of
// this format is an error, so a record that cannot be read never reads as no record.
func Load() ([]Record, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return loadFrom(dir)
}

func loadFrom(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := readRecord(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

func readRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, fmt.Errorf("the record %s: %w", path, err)
	}
	if r.Version != Version || r.Path == "" {
		return Record{}, fmt.Errorf("the record %s is not one this qory reads", path)
	}
	casedRecord(&r)
	r.file = path
	return r, nil
}

// casedRecord sets each path of r as [cased] returns it, links unresolved, so that a
// record a qory wrote with the case of a path as typed matches the path in any case: its
// place, each repository's directories, the targets of their links, and the homes of
// its harness.
func casedRecord(r *Record) {
	r.Path = cased(r.Path)
	for i := range r.Git {
		g := &r.Git[i]
		for _, p := range []*string{&g.Worktree, &g.GitDir, &g.CommonDir} {
			if *p != "" {
				*p = cased(*p)
			}
		}
		for _, links := range []map[string]string{g.ObjectLinks, g.RefLinks} {
			for rel, v := range links {
				if target, ok := strings.CutPrefix(v, "link "); ok && filepath.IsAbs(target) {
					links[rel] = "link " + cased(target)
				}
			}
		}
	}
	if r.Qory != nil && len(r.Qory.Harness) > 0 {
		harness := map[string]Harness{}
		for home, h := range r.Qory.Harness {
			harness[cased(home)] = h
		}
		r.Qory.Harness = harness
	}
}

// find reads the record of the place at path from dir: the file [Name] names, else the
// record whose place is path in another case, written by a qory that kept the case as
// typed. ok is false when there is none.
func find(dir, path string) (r Record, ok bool, err error) {
	r, err = readRecord(filepath.Join(dir, Name(path)))
	if err == nil {
		return r, true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, err
	}
	records, err := loadFrom(dir)
	if err != nil {
		return Record{}, false, err
	}
	real := Real(path)
	for _, r := range records {
		if r.Path == real {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// cache is what [Cached] keeps for the life of the process.
var cache struct {
	sync.Mutex
	dir, stamp string
	records    []Record
}

// Cached is [Load], read again only when the directory of the records changed since
// the last read in this process: a record is written by renaming a file into it. No
// state directory is the error of [StateDir], never no record: qory cannot tell then
// which places a walled run used.
func Cached() ([]Record, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	cache.Lock()
	defer cache.Unlock()
	stamp := ""
	if info, err := os.Stat(dir); err == nil {
		stamp = fmt.Sprint(info.ModTime().UnixNano(), info.Size())
	}
	if stamp != "" && dir == cache.dir && stamp == cache.stamp {
		return cache.records, nil
	}
	recs, err := loadFrom(dir)
	if err != nil {
		return nil, err
	}
	cache.dir, cache.stamp, cache.records = dir, stamp, recs
	return recs, nil
}

// Forget drops what [Cached] keeps, so the next call reads every record again.
func Forget() {
	cache.Lock()
	defer cache.Unlock()
	cache.dir, cache.stamp, cache.records = "", "", nil
}

// Get reads the record of the place at path, nil when there is none.
func Get(path string) (*Record, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	r, ok, err := find(dir, path)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}

// Update reads the record of the place at path, a zero Record with the path, kind and
// version set when there is none, lets change alter it, and writes it, all under the
// records' lock. A change that returns an error writes nothing, and so does one that
// leaves a record that was there as it was.
func Update(path, kind string, change func(r *Record) error) error {
	dir, err := makeDir()
	if err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(dir, ".lock"), true, false)
	if err != nil {
		return err
	}
	defer lock.release()
	file := filepath.Join(dir, Name(path))
	r, ok, err := find(dir, path)
	created := false
	switch {
	case err != nil:
		return err
	case !ok:
		r, created = Record{Version: Version, Path: Real(path), Kind: kind}, true
	default:
		file = r.file
	}
	before, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := change(&r); err != nil {
		return err
	}
	if after, err := json.Marshal(r); err == nil && bytes.Equal(before, after) && !created {
		return nil
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(file, append(data, '\n'))
}

// Edit reads every record, lets change alter them, and writes each record change
// returns, all under the records' lock, so what change read is what is written over. A
// change that returns an error writes nothing.
func Edit(change func(records []Record) ([]Record, error)) error {
	dir, err := makeDir()
	if err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(dir, ".lock"), true, false)
	if err != nil {
		return err
	}
	defer lock.release()
	records, err := loadFrom(dir)
	if err != nil {
		return err
	}
	changed, err := change(records)
	if err != nil {
		return err
	}
	for _, r := range changed {
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		file := r.file
		if file == "" {
			file = filepath.Join(dir, Name(r.Path))
		}
		if err := writeFile(file, append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// Remove removes the record of the place at path, under the records' lock. No record is
// no error.
func Remove(path string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(dir, ".lock"), true, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.release()
	for {
		r, ok, err := find(dir, path)
		if err != nil || !ok {
			return err
		}
		if err := os.Remove(r.file); err != nil {
			return err
		}
	}
}

// makeDir makes the records' directory and leaves it, and the state directory, mode
// 0700.
func makeDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeFile writes data to a temporary file beside path, mode 0600, and renames it into
// place, so a reader never sees half a record.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Hold is a hold on the locks of places, from a check through the action it allows: a
// walled run's that starts, see [Starting], or a check's, see [InUse]. A Hold is used by
// its pointer.
type Hold struct {
	mu    sync.Mutex
	locks []*held
}

// Release ends the hold. A nil Hold releases nothing, and a second call, from any
// goroutine, releases nothing more.
func (h *Hold) Release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.locks {
		l.release()
	}
	h.locks = nil
}

// placeLock is the lock file of the place at path, beside its record in dir.
func placeLock(dir, path string) string {
	return filepath.Join(dir, strings.TrimSuffix(Name(path), ".json")+".hold")
}

// Starting holds the places at paths for a walled run that starts there, until its
// session has registered it with Forager's registry of walled runs, where [InUse] finds
// it: each place's lock exclusive, waiting for a check that holds it, in the order of
// the lock files' names. Of two runs that start together at the same place, one waits
// for the other; taking the locks in that order keeps the two from each waiting for a
// lock the other holds at once.
func Starting(paths []string) (*Hold, error) {
	dir, err := makeDir()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range paths {
		if f := placeLock(dir, p); !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	h := &Hold{}
	for _, f := range files {
		l, err := lockFile(f, true, false)
		if err != nil {
			h.Release()
			return nil, err
		}
		h.locks = append(h.locks, l)
	}
	return h, nil
}

// InUse reports whether a walled run uses the place at path, absolute: one that starts
// there, whose hold, see [Starting], h cannot share, or one still going whose binds
// overlap the place, by Forager's registry of walled runs, see [session.RunsBinding].
// The registry's answer holds only when it returns, so the place stays held in h,
// shared, until h is released: a run that starts there waits for it, from the check
// through the action. An engine the registry cannot ask about an earlier run is its
// refusal, see [Unreachable].
func InUse(ctx context.Context, h *Hold, path string) (bool, error) {
	dir, err := Dir()
	if err != nil {
		return false, err
	}
	l, err := lockFile(placeLock(dir, path), false, true)
	if errors.Is(err, fs.ErrNotExist) {
		// No lock was ever taken there. The directory is made, so a run that starts there
		// from now on waits for h.
		if _, err = makeDir(); err == nil {
			l, err = lockFile(placeLock(dir, path), false, true)
		}
	}
	switch {
	case errors.Is(err, errHeld):
		return true, nil
	case err != nil:
		return false, err
	}
	h.mu.Lock()
	h.locks = append(h.locks, l)
	h.mu.Unlock()
	return Going(ctx, path)
}

// Going reports whether a walled run still going binds the place at path, absolute,
// writable, by Forager's registry of walled runs, see [session.RunsBinding], without the
// place's lock: for a caller that holds it, see [Starting]. An engine the registry
// cannot ask about an earlier run is its refusal, see [Unreachable].
func Going(ctx context.Context, path string) (bool, error) {
	runs, err := session.RunsBinding(ctx, nil, path)
	if err != nil {
		return false, err
	}
	return len(runs) > 0, nil
}

// Unreachable reports whether err is the engine_unreachable refusal of Forager's
// registry of walled runs: Docker could not be asked whether an earlier walled run is
// still going, or, with unreadable set, the run's entry could not be read. runID is the
// run's id, and entry the path of its entry in the registry, empty when the refusal
// lists none.
func Unreachable(err error) (runID, entry string, unreadable, ok bool) {
	var r *session.Refusal
	if !errors.As(err, &r) || r.Code != refusal.EngineUnreachable || len(r.Names) == 0 {
		return "", "", false, false
	}
	if len(r.Names) > 1 {
		entry = r.Names[1]
	}
	return r.Names[0], entry, r.EntryUnreadable, true
}

// errHeld is a lock another process holds, asked for without waiting.
var errHeld = errors.New("the lock is held")

// held is a held lock on an open file.
type held struct {
	f *os.File
}

func (h *held) release() {
	if h == nil || h.f == nil {
		return
	}
	unlock(h.f)
	h.f.Close()
	h.f = nil
}

// lockFile opens path, creating it mode 0600 never through a link, and takes its lock,
// exclusive or shared, without waiting when nowait. With nowait a missing directory is
// fs.ErrNotExist: no lock was ever taken there.
func lockFile(path string, exclusive, nowait bool) (*held, error) {
	flags := os.O_RDWR | os.O_CREATE | noFollow
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f, exclusive, nowait); err != nil {
		f.Close()
		return nil, err
	}
	return &held{f: f}, nil
}
