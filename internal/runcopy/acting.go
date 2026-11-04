package runcopy

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/qoryai/qory/internal/acting"
	"github.com/qoryai/qory/internal/exports"
)

// maxQoryFiles is the most qory files [Quarantine.ActingChanges] reads at one commit:
// the files at the top and the stack files they extend by a path, one after another.
const maxQoryFiles = 32

// maxQoryFile is the largest qory file, in bytes, that [Quarantine.ActingChanges] reads
// as YAML. A blob from the run can inflate far past its size in the bundle; a larger
// file counts as a change to the file.
const maxQoryFile = 1 << 20

// ActingChange is an acting key of a qory file that the run's commits change: set, set
// to another value, or no longer set.
type ActingChange struct {
	// File is the file, relative to the top of the repository with forward slashes:
	// qory.yaml.
	File string
	// Key is the key, as acting.Entry's Key is: env.TOKEN. It is empty when the file
	// changed in a way that no key covers: it became or stopped being a link or a
	// folder, its YAML no longer reads, or it grew past 1 MiB.
	Key string
}

// ActingChanges lists the acting keys, by the list in package acting, that differ
// between the qory files at commit base and at commit head, both in the quarantine:
// qory.yaml, harness.yaml and qory-stack.yaml at the top, and each stack file in the
// repository that one of them extends by a path. git cat-file reads each file, so no
// filter runs. checkout is the person's repository: a path in a file resolves as if the
// file were there. The list is sorted by file, then by key.
func (q *Quarantine) ActingChanges(ctx context.Context, checkout, base, head string) ([]ActingChange, error) {
	for _, c := range []string{base, head} {
		if !objectID.MatchString(c) {
			return nil, fmt.Errorf("give each commit in full: %s is no commit id", shown(c))
		}
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return nil, err
	}
	before, err := q.qoryFiles(ctx, checkout, base)
	if err != nil {
		return nil, err
	}
	after, err := q.qoryFiles(ctx, checkout, head)
	if err != nil {
		return nil, err
	}
	var out []ActingChange
	for _, file := range unionKeys(before, after) {
		b, a := before[file], after[file]
		if (b.shape == shapeOther || a.shape == shapeOther) && (b.shape != a.shape || b.blob != a.blob) {
			out = append(out, ActingChange{File: file})
		}
		for _, key := range unionKeys(b.keys, a.keys) {
			bv, bok := b.keys[key]
			av, aok := a.keys[key]
			if bok != aok || bv != av {
				out = append(out, ActingChange{File: file, Key: key})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// shape is what a qory file is at a commit.
type shape int

const (
	// shapeMissing is no entry at the path.
	shapeMissing shape = iota
	// shapeYAML is a regular file whose YAML reads.
	shapeYAML
	// shapeOther is a link, a folder, a submodule, or a regular file larger than
	// [maxQoryFile] or whose YAML does not read.
	shapeOther
)

// qoryFile is one qory file at a commit: its shape, its blob, and each acting key it
// sets, with its value and where a path that leads out through a link leads.
type qoryFile struct {
	shape shape
	blob  string
	keys  map[string]string
}

// qoryFiles reads the qory files at commit in the quarantine, by path relative to the
// top: the files at the top, then each stack file one of them extends by a path that
// stays in the repository.
func (q *Quarantine) qoryFiles(ctx context.Context, checkout, commit string) (map[string]qoryFile, error) {
	files := map[string]qoryFile{}
	queue := []string{exports.FileName, exports.AltFileName, exports.StackFileName}
	for len(queue) > 0 && len(files) < maxQoryFiles {
		rel := queue[0]
		queue = queue[1:]
		if _, seen := files[rel]; seen {
			continue
		}
		f, data, err := q.readFile(ctx, commit, rel)
		if err != nil {
			return nil, err
		}
		if f.shape == shapeYAML {
			at := filepath.Join(checkout, filepath.FromSlash(rel))
			entries, err := acting.Entries(at, data, checkout)
			if err != nil {
				f.shape = shapeOther
			} else {
				f.keys = make(map[string]string, len(entries))
				for _, e := range entries {
					f.keys[e.Key] = e.Value + "\n" + e.Leads
				}
				if next := acting.Base(at, data); next != "" {
					if r, err := filepath.Rel(checkout, next); err == nil && cleanRel(filepath.ToSlash(r)) {
						queue = append(queue, filepath.ToSlash(r))
					}
				}
			}
		}
		files[rel] = f
	}
	return files, nil
}

// readFile reads the entry rel at commit with git ls-tree and git cat-file, which runs
// no filter: its shape and blob, and the content of a regular file.
func (q *Quarantine) readFile(ctx context.Context, commit, rel string) (qoryFile, []byte, error) {
	out, err := gitIn(ctx, q.Path, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-tree", "-z", "--full-tree", commit, "--", rel)
	if err != nil {
		return qoryFile{}, nil, err
	}
	entry := strings.TrimSuffix(out, "\x00")
	if entry == "" {
		return qoryFile{shape: shapeMissing}, nil, nil
	}
	about, name, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(about)
	if len(fields) != 3 || path.Clean(name) != rel {
		return qoryFile{}, nil, fmt.Errorf("git ls-tree printed %s for %s at %s", shown(entry), shown(rel), commit)
	}
	mode, typ, blob := fields[0], fields[1], fields[2]
	if typ != "blob" || mode != "100644" && mode != "100755" {
		return qoryFile{shape: shapeOther, blob: mode + " " + blob}, nil, nil
	}
	size, err := git(ctx, q.Path, "cat-file", "-s", blob)
	if err != nil {
		return qoryFile{}, nil, err
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil || n > maxQoryFile {
		return qoryFile{shape: shapeOther, blob: blob}, nil, nil
	}
	data, err := git(ctx, q.Path, "cat-file", "blob", blob)
	if err != nil {
		return qoryFile{}, nil, err
	}
	return qoryFile{shape: shapeYAML, blob: blob}, []byte(data), nil
}

// unionKeys is the keys of a and b, sorted, each once.
func unionKeys[V any](a, b map[string]V) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
