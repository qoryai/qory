package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/qoryai/forager/wall"
)

// StateRepo is the state that [Dir.SetupStates] sets for a repository that the setup made, a
// folder with a .git folder, until [Dir.Recorded] replaces it. [Dir.GitSession] takes no path
// with this state.
const StateRepo = "repo"

// recordBranch is the branch of the record session's git folders, which no commit reaches.
const recordBranch = "qory-record"

// RecordScriptName is the file in [Dir.SetupRecord] that has [recordScript].
const RecordScriptName = "qory-git-record.sh"

// SetupRecord is the record session's folder: its git folders, its HOME and the states it
// writes.
func (d Dir) SetupRecord() string { return filepath.Join(d.root, setupRecordName) }

// RecordMounts are the record session's binds: the copy read-only and [Dir.SetupRecord]
// read-write, and nothing else.
func (d Dir) RecordMounts() []wall.Mount {
	return []wall.Mount{{Path: d.Work(), ReadOnly: true}, {Path: d.SetupRecord()}}
}

// RecordSession is the session that records each repository that the setup made, which out has
// with [StateRepo], after the setup and before the agent's session. It runs repo_state, as the git
// session does, so the two compare the same bytes: the HEAD, refs, reflogs and .gitignore files of
// such a repository and what git status lists there, with its submodules, through a git folder
// with an empty configuration. The caller runs it as it runs [Dir.GitSession], in a container of
// the run's image, and then calls [Dir.Recorded]. It reports false, and makes nothing, when out
// has no such repository. RecordSession makes [Dir.SetupRecord], an empty folder that the session
// alone binds.
func (d Dir) RecordSession(ctx context.Context, repos []Repository, out map[string]LeftOut) (GitSession, bool, error) {
	if err := ctx.Err(); err != nil {
		return GitSession{}, false, err
	}
	lists := make([]string, len(repos))
	needed := false
	for n, r := range repos {
		rel, err := repositoryRel(r)
		if err != nil {
			return GitSession{}, false, err
		}
		if !objectID.MatchString(r.Base) {
			return GitSession{}, false, fmt.Errorf(textBase, shown(rel), shown(r.Base))
		}
		var b strings.Builder
		count := 0
		for _, s := range out[r.Path].Setup {
			if s.State != StateRepo {
				continue
			}
			if !cleanRel(s.Path) {
				return GitSession{}, false, fmt.Errorf(textLeftOut, shown(s.Path))
			}
			b.WriteString(" " + shellQuote(s.Path))
			count++
		}
		needed = needed || count > 0
		lists[n] = fmt.Sprintf("rel=%s base=%s icase=%t setup=%d\nset --%s\n", shellQuote(rel), r.Base, r.ignorecase, count, b.String())
	}
	if !needed {
		return GitSession{}, false, nil
	}
	if err := os.Mkdir(d.SetupRecord(), 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return GitSession{}, false, fmt.Errorf(textExists, shown(d.SetupRecord()))
		}
		return GitSession{}, false, fileErr(d.SetupRecord(), err)
	}
	script := filepath.Join(d.SetupRecord(), RecordScriptName)
	if err := writeNew(script, recordScript); err != nil {
		return GitSession{}, false, fileErr(script, err)
	}
	for n, list := range lists {
		p := filepath.Join(d.SetupRecord(), "list-"+strconv.Itoa(n))
		if err := writeNew(p, list); err != nil {
			return GitSession{}, false, fileErr(p, err)
		}
	}
	return GitSession{
		Command: "/bin/sh",
		Args:    []string{script, sessionPath, d.Work(), d.SetupRecord(), recordBranch, "", strconv.Itoa(len(repos)), ""},
		Env: []string{
			"PATH=" + sessionPath, "HOME=" + d.SetupRecord() + "/home",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		},
		Dir: d.Work(),
	}, true, nil
}

// Recorded replaces each [StateRepo] in out with the state that [Dir.RecordSession] wrote for it,
// when ran is set, and with other when ran is unset or the session wrote none: then the git
// session keeps the copy for that folder. ran is whether the record session exited 0 within its
// time limit.
func (d Dir) Recorded(repos []Repository, out map[string]LeftOut, ran bool) {
	for n, r := range repos {
		o, ok := out[r.Path]
		if !ok {
			continue
		}
		setup := slices.Clone(o.Setup)
		i := 0
		for k, s := range setup {
			if s.State != StateRepo {
				continue
			}
			setup[k].State = "other"
			if ran {
				if state, ok := d.recordedState(n, i); ok {
					setup[k].State = state
				}
			}
			i++
		}
		o.Setup = setup
		out[r.Path] = o
	}
}

// maxStateBytes is the most bytes of a state file that [Dir.Recorded] reads: repo:, a sha256 id
// and a line break.
const maxStateBytes = 128

// recordedState is the state that the record session wrote for its path number i of the
// repository number n, in the file n-i.state, when that file is a regular file with a state of
// the form repo:<id>.
func (d Dir) recordedState(n, i int) (string, bool) {
	p := filepath.Join(d.SetupRecord(), strconv.Itoa(n)+"-"+strconv.Itoa(i)+".state")
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStateBytes))
	if err != nil {
		return "", false
	}
	state := strings.TrimSuffix(string(b), "\n")
	if !strings.HasPrefix(state, "repo:") || !stateForm.MatchString(state) {
		return "", false
	}
	return state, true
}

// recordScript is the record session's script, [scriptFunctions] with the same arguments as
// [sessionScript]: the PATH, the copy, [Dir.SetupRecord], a branch, an empty message, the number of
// repositories and an empty prefix. For the repository number n it sources list-n: its folder,
// its base commit, whether the copy ignores case, and the paths of the repositories the setup
// made. For each such path number i it writes n-i.state with what repo_state prints, when the
// path is still a folder with a .git folder, and git status there, with that of each submodule,
// succeeds with no gitlink it cannot check: one that tree_status lists with n.left. With no
// state file, the path is other.
const recordScript = scriptFunctions + `r=0
while [ "$r" -lt "$count" ]; do
	. "$B/list-$r"
	if [ "$rel" = . ]; then T=$W; else T=$W/$rel; fi
	G=$B/git-$r C=$T/.git
	cd "$T"
	gitfolder /dev/null
	export GIT_DIR="$G" GIT_WORK_TREE="$T" GIT_INDEX_FILE="$G/index"
	execs=$nl$(find . -path ./.git -prune -o -type f -perm -100 -print 2>/dev/null || :)$nl
	i=0
	for a do
		n=$r-$i
		st=$((st + 1))
		f=$T/$a
		if [ -d "$f" ] && [ ! -h "$f" ] && [ -d "$f/.git" ] && [ ! -h "$f/.git" ] &&
			submodule_status "$a" "" deep && [ ! -e "$B/$n.left" ] && r_s=$(repo_state "$a" "$G/status"); then
			printf '%s\n' "$r_s" > "$B/$n.state"
		fi
		i=$((i + 1))
	done
	r=$((r + 1))
done
`
