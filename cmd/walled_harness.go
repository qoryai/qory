package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/hostgit"
	walled "github.com/qoryai/qory/internal/places"
)

// harnessPrint is the fingerprint of the home at.home and the report beside it, as they
// are now; false when neither is there.
func harnessPrint(at places) (walled.Harness, bool, error) {
	report, err := treeHash(at.report)
	if err != nil {
		return walled.Harness{}, false, err
	}
	home, err := treeHash(at.home)
	if err != nil {
		return walled.Harness{}, false, err
	}
	if report == "" && home == "" {
		return walled.Harness{}, false, nil
	}
	return walled.Harness{Report: report, Home: home}, true, nil
}

// treeHash is the hash of what is at path, followed through no link: every path under
// it in order, its kind and permissions, and a file's content or a link's target; "" when
// nothing is there.
func treeHash(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	h := sha256.New()
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), info.Mode().String())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00", target)
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			sum := sha256.New()
			_, err = io.Copy(sum, f)
			f.Close()
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%x\x00", sum.Sum(nil))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// harnessKey is the name a record gives the home at.home: its real path.
func harnessKey(at places) string { return walled.Real(at.home) }

// takeHarness sets the fingerprint of the home at.home in the record r of a place being
// recorded, when the home lies in the place and is composed.
func takeHarness(r *walled.Record, at places) {
	if at.home == "" || !walled.Within(r.Path, at.home) && !walled.Within(r.Path, at.report) {
		return
	}
	fp, ok, err := harnessPrint(at)
	if err != nil || !ok {
		return
	}
	if r.Qory.Harness == nil {
		r.Qory.Harness = map[string]walled.Harness{}
	}
	r.Qory.Harness[harnessKey(at)] = fp
}

// checkHarness refuses the home at.home and its report when they lie in a recorded place
// and are not what the last compose wrote there, as the place's record holds it. Nothing
// composed passes, and so does a home outside every recorded place.
func checkHarness(at places) error {
	recs, err := walled.Cached()
	if err != nil {
		return err
	}
	found := walled.Containing(recs, at.home)
	if len(found) == 0 {
		found = walled.Containing(recs, at.report)
	}
	if len(found) == 0 {
		return nil
	}
	now, ok, err := harnessPrint(at)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	rec := found[0]
	if rec.Qory != nil {
		if was, held := rec.Qory.Harness[harnessKey(at)]; held && was == now {
			return nil
		}
	}
	return fmt.Errorf("the harness of %s is not the one qory composed; compose again with qory harness compose", hostgit.Shown(at.root))
}

// recordHarness keeps the fingerprint of the home at.home as it is now in the record of
// every recorded place it lies in, after a compose or a remove wrote it: dropped when
// nothing is composed there any more.
func recordHarness(at places) error {
	recs, err := walled.Cached()
	if err != nil || len(recs) == 0 {
		return err
	}
	now, ok, err := harnessPrint(at)
	if err != nil {
		return err
	}
	key := harnessKey(at)
	for _, rec := range recs {
		if !walled.Within(rec.Path, at.home) && !walled.Within(rec.Path, at.report) {
			continue
		}
		err := walled.Update(rec.Path, rec.Kind, func(r *walled.Record) error {
			if r.Qory == nil {
				r.Qory = &walled.Qory{}
			}
			if !ok {
				delete(r.Qory.Harness, key)
				return nil
			}
			if r.Qory.Harness == nil {
				r.Qory.Harness = map[string]walled.Harness{}
			}
			r.Qory.Harness[key] = now
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
