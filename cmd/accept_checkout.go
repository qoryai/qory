package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/acting"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	walled "github.com/qoryai/qory/internal/places"
)

// askTerminal reports whether v is a terminal a question can be put to; a test stands in
// for it.
var askTerminal = isTerminal

// acceptCheckout is qory setup repo --accept-checkout: in the recorded place that holds
// dir, it lists what the settings check would refuse since the place's fingerprint, the
// git keys by name, a remote's URL by its value with any user and password masked, and
// a moved .git, and the acting keys of the checkout's qory files that changed, each
// with its value now, but a variable's by its name alone, and a path that leads out
// through a link with where it leads; it asks, and on yes, under the records' lock, it
// lists them again from the records as they are then, and takes the fingerprints from
// those same reads once it has found the same list. It refuses while a walled run uses a
// recorded place that holds dir, a qory file it lists or the repository's
// configuration, before it asks and again under the lock, and without a terminal to ask
// at. It writes nothing in the checkout, and accepts neither an include into a recorded
// place, which it lists apart, nor a harness qory did not compose: a compose takes that.
func acceptCheckout(ctx context.Context, dir string, in io.Reader, out io.Writer) error {
	// acceptList reads a false from hostgit.Resolve as a folder outside a repository.
	if err := hostgit.Ready(ctx); err != nil {
		return err
	}
	recs, err := walled.Load()
	if err != nil {
		return err
	}
	if len(walled.Containing(recs, dir)) == 0 {
		return input(fmt.Errorf("no walled run has used %s; there is nothing to accept", hostgit.Shown(dir)))
	}
	shown, err := acceptList(ctx, recs, dir)
	if err != nil {
		return err
	}
	asked := &walled.Hold{}
	err = noneInUse(ctx, asked, recs, dir, shown)
	asked.Release()
	if err != nil {
		return err
	}
	if len(shown.lines) == 0 && len(shown.includes) == 0 {
		fmt.Fprintln(out, "nothing changed since a walled run began here")
		return nil
	}
	if len(shown.lines) > 0 {
		fmt.Fprintln(out, "changes since a walled run began here:")
		for _, l := range shown.lines {
			fmt.Fprintln(out, l)
		}
	}
	if len(shown.includes) > 0 {
		fmt.Fprintln(out, "not accepted, since each includes a file a walled run could have written, or names it by a relative path:")
		for _, l := range shown.includes {
			fmt.Fprintln(out, l)
		}
	}
	if len(shown.lines) == 0 {
		fmt.Fprintln(out, "accepted nothing")
		return nil
	}
	if !askTerminal(in) {
		return input(fmt.Errorf("qory setup repo --accept-checkout asks before it accepts, and needs a terminal"))
	}
	fmt.Fprint(out, "accept them? [y/N] ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		fmt.Fprintln(out, "accepted nothing")
		return nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	// The places found in use by no walled run stay held until the records are written,
	// so a run that starts in one waits for the accept.
	hold := &walled.Hold{}
	err = walled.Edit(func(recs []walled.Record) ([]walled.Record, error) {
		// What the person said yes to is what was shown: a change since is not taken, nor
		// a place a walled run began to use while qory asked. The records are the ones
		// under the lock, and what is taken comes from the reads compared: the lines, the
		// repository's fingerprint and each qory file's hashes, the whole of what an
		// accept takes.
		found := walled.Containing(recs, dir)
		if len(found) == 0 {
			return nil, input(fmt.Errorf("no walled run has used %s; there is nothing to accept", hostgit.Shown(dir)))
		}
		current, err := acceptList(ctx, recs, dir)
		if err != nil {
			return nil, err
		}
		if err := noneInUse(ctx, hold, recs, dir, current); err != nil {
			return nil, err
		}
		// The values compared are the whole ones, not the lines shown, which mask a user
		// and a password, name a git key without its value and leave out a variable's.
		if !slices.Equal(current.sums, shown.sums) || !slices.Equal(current.includes, shown.includes) ||
			current.isRepo != shown.isRepo || !reflect.DeepEqual(current.fingerprint, shown.fingerprint) || !reflect.DeepEqual(current.qory, shown.qory) {
			return nil, errors.New("the checkout changed while qory setup repo --accept-checkout asked, so it accepted nothing; run it again to see the changes")
		}
		changed := map[string]*walled.Record{}
		edit := func(path string) *walled.Record {
			if r, ok := changed[path]; ok {
				return r
			}
			for i := range recs {
				if recs[i].Path == path {
					changed[path] = &recs[i]
					return &recs[i]
				}
			}
			return nil
		}
		rec := edit(found[0].Path)
		rec.Accepted = &now
		if current.isRepo {
			fp, repo := current.fingerprint, current.repo
			i := slices.IndexFunc(rec.Git, func(e walled.Repository) bool {
				return repo.Worktree != "" && e.Worktree == repo.Worktree || e.CommonDir == repo.CommonDir
			})
			if i >= 0 {
				rec.Git[i] = fp
			} else {
				rec.Git = append(rec.Git, fp)
			}
		}
		// Each qory file is taken in the record of the nearest place that holds it, a
		// mount around the checkout say.
		for _, q := range current.qory {
			r := edit(q.place)
			if r == nil {
				continue
			}
			if r.Qory == nil {
				r.Qory = &walled.Qory{}
			}
			if r.Qory.Files == nil {
				r.Qory.Files = map[string][]string{}
			}
			r.Qory.Files[q.rel] = q.hashes
		}
		var out []walled.Record
		for _, r := range changed {
			out = append(out, *r)
		}
		return out, nil
	})
	hold.Release()
	if err != nil {
		return err
	}
	if len(shown.includes) > 0 {
		fmt.Fprintln(out, "accepted; qory takes the checkout as it is now, but for the includes it does not accept")
		return nil
	}
	fmt.Fprintln(out, "accepted; qory takes the checkout as it is now")
	return nil
}

// noneInUse refuses when a walled run uses a recorded place of recs that holds dir, a
// qory file a lists, or a configuration file of the repository a found, see
// [walled.InUse], and holds each place in h.
func noneInUse(ctx context.Context, h *walled.Hold, recs []walled.Record, dir string, a accepting) error {
	paths := []string{dir}
	for _, q := range a.qory {
		paths = append(paths, q.file)
	}
	if a.isRepo {
		paths = append(paths, filepath.Join(a.repo.CommonDir, "config"), filepath.Join(a.repo.GitDir, "config.worktree"))
	}
	var seen []string
	for _, p := range paths {
		for _, r := range walled.Containing(recs, p) {
			if slices.Contains(seen, r.Path) {
				continue
			}
			seen = append(seen, r.Path)
			busy, err := walled.InUse(ctx, h, r.Path)
			if id, entry, unreadable, ok := walled.Unreachable(err); ok {
				if unreadable {
					return fmt.Errorf("qory cannot read %s, the record of the walled run %s, so it cannot tell whether that run used %s or is still going, and does not accept the checkout yet. If docker ps --all --filter label=dev.qory.run=%s lists no container, delete %s", hostgit.Shown(entry), hostgit.Shown(id), hostgit.Shown(r.Path), hostgit.Shown(id), hostgit.Shown(entry))
				}
				if entry == "" {
					return fmt.Errorf("The check for a container of the walled run %s failed. That run used %s, so qory does not accept the checkout yet", hostgit.Shown(id), hostgit.Shown(r.Path))
				}
				return fmt.Errorf("The check for a container of the walled run %s failed. That run used %s, so qory does not accept the checkout yet. If docker ps --all --filter label=dev.qory.run=%s lists no container, or the Docker engine of that run no longer exists, delete %s", hostgit.Shown(id), hostgit.Shown(r.Path), hostgit.Shown(id), hostgit.Shown(entry))
			}
			if err != nil {
				return err
			}
			if busy {
				return input(fmt.Errorf("a walled run is using %s; accept once it has ended", hostgit.Shown(r.Path)))
			}
		}
	}
	return nil
}

// accepting is what qory setup repo --accept-checkout lists for dir: the lines of the
// changes it accepts, those of the includes it does not, and what it takes on yes, from
// the same reads: the repository's fingerprint and each qory file's hashes.
type accepting struct {
	lines, includes []string
	// sums holds, for each of lines, the hash of what it stands for: a remote's key and
	// its URL unmasked, a qory file's key with its whole value, or a git key alone, whose
	// value the fingerprint holds.
	sums        []string
	repo        hostgit.Repo
	isRepo      bool
	fingerprint walled.Repository
	qory        []qoryFile
}

// qoryFile is a qory file an accept takes: the file, the place of the nearest record
// that holds it, its name in that record, and the hashes of its acting keys.
type qoryFile struct {
	file, place, rel string
	hashes           []string
}

// acceptList lists, against recs, what the settings check and the qory files' check
// would refuse in dir since the record of its place.
func acceptList(ctx context.Context, recs []walled.Record, dir string) (accepting, error) {
	var a accepting
	a.repo, a.isRepo = hostgit.Resolve(ctx, dir)
	if a.isRepo {
		ch, err := hostgit.Changes(ctx, recs, a.repo)
		if err != nil {
			return a, err
		}
		a.fingerprint = ch.Fingerprint
		if ch.Moved {
			a.add("  .git now points to "+hostgit.Shown(exports.Value(ch.GitDir, false)), "moved", ch.GitDir)
		}
		for _, k := range ch.Keys {
			urls, ok := ch.URLs[k]
			if !ok {
				a.add("  "+hostgit.ShownKey(accesskey.ShownName(k, false)), "key", k)
				continue
			}
			// A remote's URL by its value, so the person sees where git would go, with a
			// user and a password masked.
			for _, u := range urls {
				a.add("  "+hostgit.ShownKey(accesskey.ShownName(k, false))+" "+hostgit.Shown(exports.Value(hostgit.WithoutUserinfo(u), false)), "url", k, u)
			}
		}
		for _, k := range ch.Includes {
			a.includes = append(a.includes, "  "+hostgit.ShownKey(accesskey.ShownName(k, false)))
		}
	}
	root := dir
	if a.isRepo && a.repo.Worktree != "" {
		root = a.repo.Worktree
	}
	files, err := qoryFiles(root)
	if err != nil {
		return a, err
	}
	more, sums, taken, err := qoryChanges(recs, files)
	if err != nil {
		return a, err
	}
	a.lines, a.sums = append(a.lines, more...), append(a.sums, sums...)
	a.qory = taken
	return a, nil
}

// add adds line to the lines of a, with the hash of whole, what it stands for.
func (a *accepting) add(line string, whole ...string) {
	a.lines = append(a.lines, line)
	a.sums = append(a.sums, wholeSum(whole...))
}

// wholeSum is the hash of parts, each apart from the next.
func wholeSum(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// qoryChanges lists, one line each, the acting keys each of files sets that the record
// of the nearest recorded place holding it does not hold: the file, the key and its
// value now. taken is, for each file read, what an accept of it takes, from the same
// read. sums holds, for each of lines, the hash of the file, the key, its whole value
// and where it leads.
func qoryChanges(recs []walled.Record, files []string) (lines, sums []string, taken []qoryFile, err error) {
	for _, f := range files {
		found := walled.Containing(recs, f)
		if len(found) == 0 {
			continue
		}
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		all, changed, err := acting.Read(found[0], f, data)
		if err != nil {
			continue
		}
		taken = append(taken, qoryFile{file: f, place: found[0].Path, rel: acting.Rel(found[0].Path, f), hashes: acting.Hashes(all)})
		for _, e := range changed {
			line := "  " + hostgit.Shown(acting.Rel(found[0].Path, f)) + ": " + hostgit.Shown(accesskey.ShownName(e.Key, false))
			// A variable's value can hold a secret: it is named alone. A repository's URL
			// shows with a user and a password masked.
			switch {
			case variable(e.Key):
			case gitURL(e.Key):
				line += " " + hostgit.Shown(exports.Value(urlValue(e.Value), false))
			default:
				line += " " + hostgit.Shown(exports.Value(e.Value, false))
			}
			// A path that leads out through a link: where the link leads now.
			if e.Leads != "" {
				line += ", which leads to " + hostgit.Shown(exports.Value(e.Leads, false))
			}
			lines = append(lines, line)
			sums = append(sums, wholeSum("qory", f, e.Key, e.Value, e.Leads))
		}
	}
	return lines, sums, taken, nil
}

// variable reports whether key is an environment variable's, or a set of them in any
// form: env and env.<name>, or <section>.env and <section>.env.<name> such as a
// launch's.
func variable(key string) bool {
	return key == "env" || strings.HasPrefix(key, "env.") || strings.HasSuffix(key, ".env") || strings.Contains(key, ".env.")
}

// gitURL reports whether key is a repository's URL: extends.git, modules[<i>].source.git,
// or either under harness.
func gitURL(key string) bool {
	key = strings.TrimPrefix(key, "harness.")
	return key == "extends.git" || strings.HasPrefix(key, "modules[") && strings.HasSuffix(key, "].source.git")
}

// urlValue is value, a JSON string, with the URL it holds masked by
// [hostgit.WithoutUserinfo], as JSON; a value of another kind is as it is.
func urlValue(value string) string {
	var u string
	if err := json.Unmarshal([]byte(value), &u); err != nil {
		return value
	}
	b, err := json.Marshal(hostgit.WithoutUserinfo(u))
	if err != nil {
		return value
	}
	return string(b)
}
