package runcopy

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

// SessionBase is the time limit of the git session on a small copy, which [SessionLimitFor]
// raises for a large one; the caller that starts the session stops it then. A program the
// agent planted in the copy can run again in that session, under the run's egress policy, so
// the session has a time limit.
const SessionBase time.Duration = 5 * time.Minute

// SessionMax is the longest time limit of the git session, on any copy.
const SessionMax time.Duration = 60 * time.Minute

// The git session gets a minute more per sessionEntriesPerMinute entries in the copy, and per
// sessionBytesPerMinute bytes in its regular files.
const (
	sessionEntriesPerMinute = 20_000
	sessionBytesPerMinute   = 2 << 30
)

// SessionLimitFor is the time limit of the git session on a copy with entries files, folders
// and links and bytes in its regular files: [SessionBase], plus a minute per whole 20,000
// entries and a minute per whole 2 GiB, at most [SessionMax]. The two additions add up, since
// git reads each entry and hashes each byte, so a copy with many files and many bytes takes
// the time of both.
func SessionLimitFor(entries, bytes int64) time.Duration {
	extra := max(entries, 0)/sessionEntriesPerMinute + max(bytes, 0)/sessionBytesPerMinute
	if extra >= int64((SessionMax-SessionBase)/time.Minute) {
		return SessionMax
	}
	return SessionBase + time.Duration(extra)*time.Minute
}

// WorkSize counts the entries below the copy, [Dir.Work], and the bytes of its regular files,
// with lstat: it follows no link. A folder it cannot read counts as one entry, with nothing
// below it. When ctx ends, it returns the counts so far and ctx's error.
func (d Dir) WorkSize(ctx context.Context) (entries, bytes int64, err error) {
	root := d.Work()
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if path == root {
			return err
		}
		if err != nil {
			// The folder at path is counted already, and what is below it is unread.
			return nil
		}
		entries++
		if e.Type().IsRegular() {
			if info, infoErr := e.Info(); infoErr == nil {
				bytes += info.Size()
			}
		}
		return nil
	})
	return entries, bytes, err
}
