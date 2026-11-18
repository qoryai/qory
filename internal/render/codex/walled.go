package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// HomeInWall is the folder in the wall where a walled run binds Codex's writable home
// and sets CODEX_HOME: one fixed path in every image, whatever HOME the image sets, so
// qory knows it before the join. Codex keys each hook's trust by this path, see
// [WalledConfig]. The folder is outside /tmp, since Codex 0.167.2 writes no helper
// program, such as apply_patch, in a home under the temporary folder
// (codex-rs/arg0/src/lib.rs).
const HomeInWall = "/var/lib/qory-codex-home"

// Bound is one file or folder of a walled run's Codex home that the run binds read-only
// at Target, its path in [HomeInWall].
type Bound struct {
	// Path is the path under the home, such as config.toml or rules.
	Path string
	// Source is the file or folder on this machine, in the folder [WriteWalledHome]
	// writes, which shares no path with any other mount of the run.
	Source string
	// Target is the path in the wall: Path under [HomeInWall].
	Target string
	// Dir marks a folder.
	Dir bool
}

// boundFiles are the files of a walled run's home that [WriteWalledHome] writes, and
// boundDirs the folders it binds. Codex reads hooks from config.toml and hooks.json,
// sets each variable of .env that is not named CODEX_*, starts the program an
// environments.toml lists, and reads the instructions, rules, skills and agents of its
// home; bound read-only, none of them is one the agent changes for a later Codex process.
// Codex reads AGENTS.override.md ahead of AGENTS.md in its home and passes over one that
// is empty or only white space (codex-rs/codex-home/src/instructions/mod.rs), so an
// empty one keeps AGENTS.md the instructions.
var (
	boundFiles = []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.override.md", "AGENTS.md"}
	boundDirs  = []string{"rules", "skills", "agents"}
)

// noHooks is the hooks.json of a walled run's home: a file with no hook.
const noHooks = "{\"hooks\": {}}\n"

// WalledRun is what [WriteWalledHome] needs to know of the run.
type WalledRun struct {
	// Projects are the paths where the run's checkout is in the wall.
	Projects []string
	// Checkouts are the folders on this machine that the wall shows at Projects. qory
	// reads the skills of each one's .codex/skills and .agents/skills.
	Checkouts []string
	// WorkDirs are the paths in the wall of the folder Codex starts in, the run's working
	// folder, at each path where the wall shows it, as given and with its links resolved,
	// since Codex looks up both. Codex trusts that folder for the
	// process when neither it nor its git root has a trust entry, a .git is at or above
	// it and the sandbox gives full access. Its lookup of the git root finds none for a
	// submodule's .git file, and for a worktree whose main .git the wall does not show
	// or shows at another path, as at /workspace (codex-rs/config/src/config_toml.rs,
	// get_active_project). Each one gets an untrusted entry. WorkDirs also has the paths
	// in the wall of each folder that a -C or --cd argument of the run names, see
	// [StartDirs], since Codex starts in such a folder.
	WorkDirs []string
	// Skills are the skills the run may load, from [AllowedSkills]. With [AllSkills],
	// the run may load every skill of the home's skills folder.
	Skills []string
	// Access is what the run may do on its target, stack.AccessRead or
	// stack.AccessWrite.
	Access string
	// ReadRules are the harness's Codex execpolicy rules for a read run,
	// compose.Result.CodexReadRules; nil when it has none.
	ReadRules []byte
	// LinkRoots are the folders, besides the composed home, that a link in the composed
	// home may lead into for the copy of its skills, rules and agents: every folder the
	// compose read a module from and qory's module cache, compose.Result.SourceDirs. A
	// link to anywhere else fails [WriteWalledHome].
	LinkRoots []string
}

// ReadRulesName is the file in the rules folder of a walled read run's home that
// contains [WalledRun.ReadRules].
const ReadRulesName = "read-access.rules"

// WriteWalledHome makes the folder files, which must not exist, on this machine apart
// from the writable home and from every other mount of the run, such as
// <user cache>/qory/runs/<run id>/codex-fixed, and writes into it every part of a walled
// run's Codex home that the run binds read-only: the config.toml from dir, the runtime's
// directory of the composed home, with [WalledConfig]'s additions for run.Projects,
// run.WorkDirs and the git roots of run.Checkouts, see [gitRootsInWall], and with every skill that is not
// in run.Skills disabled, a hooks.json with no hook, an empty .env, environments.toml and
// AGENTS.override.md, dir's AGENTS.md, or an empty one, and a copy of each folder of
// [boundDirs] that dir has, with each link replaced by what it leads to, see [copyTree],
// or an empty folder. The copy of rules and agents keeps to [walledLimit]. For a run
// whose access is read, rules/ also contains run.ReadRules
// as [ReadRulesName], in place of any file of that name in dir's rules; for any other
// access, rules/ is the copy alone. A mount with a target may share no path on this
// machine with another mount, so nothing is bound from dir itself, which the run mounts
// as the harness home. It writes no auth.json. It returns the binds, files first, in a
// fixed order.
//
// The skills disabled are every name, other than run.Skills, of a skill in the copy of
// dir's skills and in run.Checkouts, by [skillNames], and every skill of run.Checkouts
// by its path in the wall, by [checkoutSkillPaths], so a checkout's skill with the name
// of an allowed one is disabled too. When run.Skills contains
// [AllSkills], every name of a skill in the copy of dir's skills counts as in run.Skills.
// A skill that is not there when WriteWalledHome runs keeps its name enabled: one the
// agent writes during the run, and one in ~/.agents/skills or /etc/codex/skills of the
// image.
func WriteWalledHome(dir, files string, run WalledRun) ([]Bound, error) {
	// A folder that exists may have links planted in it; a new one has none.
	if err := os.Mkdir(files, 0o755); err != nil {
		return nil, err
	}
	var dirs []Bound
	for _, name := range boundDirs {
		dst := filepath.Join(files, name)
		if err := os.Mkdir(dst, 0o755); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			var limit *render.CopyLimit
			if name != "skills" {
				limit = walledLimit(name)
			}
			if err := copyTree(filepath.Join(dir, name), dst, name, append([]string{filepath.Dir(dir)}, run.LinkRoots...), limit); err != nil {
				return nil, err
			}
		}
		dirs = append(dirs, Bound{Path: name, Source: dst, Target: path.Join(HomeInWall, name), Dir: true})
	}
	if run.Access == stack.AccessRead && len(run.ReadRules) > 0 {
		p := filepath.Join(files, "rules", ReadRulesName)
		// The copy may have a file of this name from dir's rules.
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := writeNew(p, run.ReadRules, 0o644); err != nil {
			return nil, err
		}
	}
	type root struct{ path, bound string }
	roots := []root{{path: filepath.Join(files, "skills")}}
	for _, c := range run.Checkouts {
		for _, r := range skillRoots {
			roots = append(roots, root{path: filepath.Join(c, r), bound: c})
		}
	}
	allowed := slices.Clone(run.Skills)
	if slices.Contains(run.Skills, AllSkills) {
		allowed = append(allowed, skillNames(roots[0].path, "")...)
	}
	var disabled []string
	for _, r := range roots {
		for _, n := range skillNames(r.path, r.bound) {
			if !slices.Contains(allowed, n) {
				disabled = append(disabled, n)
			}
		}
	}
	slices.Sort(disabled)
	var disabledPaths []string
	for _, c := range run.Checkouts {
		disabledPaths = append(disabledPaths, checkoutSkillPaths(c, run.Projects)...)
	}
	slices.Sort(disabledPaths)
	config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		config = nil
	} else if err != nil {
		return nil, err
	}
	untrusted := slices.Concat(run.Projects, run.WorkDirs)
	for _, c := range run.Checkouts {
		untrusted = append(untrusted, gitRootsInWall(c, run.Projects)...)
	}
	slices.Sort(untrusted)
	config, err = WalledConfig(config, HomeInWall, slices.Compact(untrusted), slices.Compact(disabled), slices.Compact(disabledPaths))
	if err != nil {
		return nil, err
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	content := map[string][]byte{"config.toml": config, "hooks.json": []byte(noHooks), "AGENTS.md": agents}
	var out []Bound
	for _, name := range boundFiles {
		p := filepath.Join(files, name)
		// A bind pins the file's inode, so the file is written once, in place, and never
		// replaced by a rename.
		if err := writeNew(p, content[name], 0o644); err != nil {
			return nil, err
		}
		out = append(out, Bound{Path: name, Source: p, Target: path.Join(HomeInWall, name)})
	}
	return append(out, dirs...), nil
}

// writeNew writes data to a new file at p, with mode perm. It fails when anything is at
// p already, a link included, so it never writes through a link to another file.
func writeNew(p string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// checkoutSkillPaths are the paths in the wall of each SKILL.md that [findSkills] finds
// in the skill folders of checkout, for each of projects, the paths where the wall shows
// checkout. Codex compares a skills.config path with a skill's SKILL.md path with every
// link resolved, as Codex sees it in the wall, and falls back to the path it reached
// when that fails (codex-rs/ext/skills/src/loader/host.rs). Both paths are here: the
// file's real path and the path qory reached, each relative to checkout and put under
// each project. A project is taken as Codex resolves it, as for the trust entry of
// [WalledConfig].
func checkoutSkillPaths(checkout string, projects []string) []string {
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range skillRoots {
		for _, f := range findSkills(filepath.Join(checkout, r), checkout) {
			for _, rel := range []string{relTo(realCheckout, f.real), relTo(checkout, f.path)} {
				if rel == "" {
					continue
				}
				for _, p := range projects {
					out = append(out, path.Join(p, rel))
				}
			}
		}
	}
	return out
}

// gitRootsInWall are the paths in the wall, under each of projects, of the folders
// Codex takes as the git repository of checkout when it looks up a project's trust: the
// nearest folder at or above checkout with a .git folder that has HEAD or a .git file,
// and, for a .git file of a worktree, the main repository's folder, the folder of the
// common git folder that the worktree's gitdir file names (codex-rs/git-utils/src/
// trust.rs, resolve_root_git_project_for_trust). As in Codex, a .git link to a folder
// counts as the folder, and a .git link to anything else, or one that leads nowhere,
// gives no root. Codex looks up the trust of the working
// folder, then of that root (codex-rs/config/src/config_toml.rs, get_active_project);
// with neither, it trusts the root for the process, or the working folder when it finds
// no root (app-server thread_processor.rs). Each root is put under each project by its
// path relative to checkout, so a root above checkout is above the project too.
func gitRootsInWall(checkout string, projects []string) []string {
	var roots []string
	for dir := checkout; ; {
		dotGit := filepath.Join(dir, ".git")
		if link, err := os.Lstat(dotGit); err == nil {
			info, err := os.Stat(dotGit)
			if err != nil || !info.IsDir() && link.Mode()&fs.ModeSymlink != 0 {
				break
			}
			if !info.IsDir() {
				roots = append(roots, dir)
				if main := mainRepository(dotGit); main != "" {
					roots = append(roots, main)
				}
				break
			}
			if _, err := os.Stat(filepath.Join(dotGit, "HEAD")); err == nil {
				roots = append(roots, dir)
				break
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	var out []string
	for _, r := range roots {
		rel, err := filepath.Rel(checkout, r)
		if err != nil {
			continue
		}
		for _, p := range projects {
			out = append(out, path.Join(p, filepath.ToSlash(rel)))
		}
	}
	return out
}

// mainRepository is the main repository's folder of the worktree whose .git file is
// dotGit: the folder above the common git folder of the gitdir that dotGit names, when
// that gitdir is <common>/worktrees/<name>; "" otherwise.
func mainRepository(dotGit string) string {
	data, err := readHead(dotGit, 64<<10, true)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(dotGit), gitdir)
	}
	worktrees := filepath.Dir(gitdir)
	if filepath.Base(worktrees) != "worktrees" {
		return ""
	}
	return filepath.Dir(filepath.Dir(worktrees))
}

// relTo is file relative to dir, with forward slashes; "" when file is not below dir.
func relTo(dir, file string) string {
	rel, err := filepath.Rel(dir, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// The refusals of the copy of a folder of a walled run's home, see [walledLimit]: past
// 10,000 entries and past 1 GiB, with the folder, and for a file past 64 MiB, with the
// file's path, each as a path under the home.
const (
	textWalledTooMany      = "Ship fewer files in the modules.\n  codex/%s contains more than 10,000 files, directories and links, the most a walled Codex run takes."
	textWalledTooManyBytes = "Ship fewer files in the modules.\n  The files of codex/%s add up to more than 1 GiB, the most a walled Codex run takes."
	textWalledTooLarge     = "Ship codex/%s under 64 MiB.\n  The file is larger than 64 MiB, the most a walled Codex run takes for one file."
)

// walledLimit is the limit of [WriteWalledHome]'s copy of the folder name of the home,
// rules or agents: the limits of a copy of one entry of a module, see
// [render.LimitFor], with the refusals of a walled run. The skills folder needs none:
// [placeSkills] copied each skill within those limits. A variable so a test can lower
// the limits.
var walledLimit = func(name string) *render.CopyLimit {
	limit := render.LimitFor("")
	limit.TooMany = fmt.Errorf(textWalledTooMany, exports.HidePath(name))
	limit.TooManyBytes = fmt.Errorf(textWalledTooManyBytes, exports.HidePath(name))
	limit.TooLarge = func(path string) error {
		return fmt.Errorf(textWalledTooLarge, exports.HidePath(path))
	}
	return &limit
}

// maxCopiedFile is the largest file a copy without a [render.CopyLimit] takes, the
// skills folder of a walled home: Codex
// 0.167.2 reads no larger file (codex-rs/exec-server/src/local_file_system_read.rs,
// MAX_READ_FILE_BYTES).
const maxCopiedFile = maxSkillSize

// textOutsideLink is the refusal of a link whose target lies outside the composed home
// and every module folder, [WalledRun.LinkRoots] for [WriteWalledHome] and
// compose.Result.SourceDirs for [placeSkills], with the link's path and its target.
const textOutsideLink = "Remove the link `%s` from the harness.\n  It leads to `%s`, outside the harness's modules, and a Codex run copies only files inside them."

// copyTree copies the folder src into dst, which exists, as folders and regular files
// only, with shown, src's path as a refusal shows it; see [treeCopy].
func copyTree(src, dst, shown string, roots []string, limit *render.CopyLimit) error {
	t := newTreeCopy(roots, limit)
	return t.dir(src, dst, shown)
}

// treeCopy is one copy into a Codex home, as folders and regular files only: Codex
// 0.167.2 passes over a SKILL.md that is a link
// (codex-rs/exec-server/src/local_file_system.rs, the walk). A link is followed when its
// target, with every link resolved, lies inside one of the roots, the composed home and
// the module folders: a link to a folder is copied as a folder, and a link to a file as a
// regular file. A link with any other target fails the copy rather than leaving a file
// out, which would change a skill without a word. Every entry named .git is left out, as
// a copy of a module leaves it out. Each file keeps its permission bits without write for
// group and others, see [render.FileMode], and is written new, see [writeNew]. Any other
// kind of file, and a folder that contains itself through a link, is an error.
//
// With a limit, the copy refuses more entries below the folder it starts at, more bytes
// in all, or a larger file, than the limit allows, with the limit's refusals. Without
// one, each file is at most [maxCopiedFile] bytes.
type treeCopy struct {
	roots   []string // the roots with every link resolved
	limit   *render.CopyLimit
	entries int             // the entries copied so far
	written int64           // the bytes copied so far
	open    map[string]bool // the folders being copied, by real path
}

// newTreeCopy is a [treeCopy] that follows links into roots, with limit or none.
func newTreeCopy(roots []string, limit *render.CopyLimit) *treeCopy {
	t := &treeCopy{limit: limit, open: map[string]bool{}}
	for _, r := range roots {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			t.roots = append(t.roots, real)
		}
	}
	return t
}

// failed is err of the copy of the path shown, with the prefix of every copy error and
// without the absolute path a file system error contains.
func failed(shown string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("copy %s for the Codex home: %w", exports.HidePath(shown), exports.HideError(err))
}

// readBatch is how many entries [treeCopy.dir] reads from a folder at a time, so a limit
// stops the read of a large folder early.
const readBatch = 256

// dir copies the folder src, shown as shown, into dst, which exists.
func (t *treeCopy) dir(src, dst, shown string) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return failed(shown, err)
	}
	if t.open[real] {
		return fmt.Errorf("copy %s for the Codex home: the folder contains itself through a link", exports.HidePath(shown))
	}
	t.open[real] = true
	defer delete(t.open, real)
	f, err := os.Open(src)
	if err != nil {
		return failed(shown, err)
	}
	var names []string
	for {
		batch, err := f.ReadDir(readBatch)
		for _, e := range batch {
			if e.Name() == ".git" {
				continue
			}
			if t.limit != nil {
				if t.entries++; t.entries > t.limit.Entries {
					f.Close()
					return t.limit.TooMany
				}
			}
			names = append(names, e.Name())
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return failed(shown, err)
		}
	}
	f.Close()
	slices.Sort(names)
	for _, n := range names {
		if err := t.entry(filepath.Join(src, n), filepath.Join(dst, n), path.Join(shown, n)); err != nil {
			return err
		}
	}
	return nil
}

// entry copies the file, folder or link at p, shown as shown, to the new path to.
func (t *treeCopy) entry(p, to, shown string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return failed(shown, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return failed(shown, err)
		}
		if !slices.ContainsFunc(t.roots, func(r string) bool { return within(target, r) }) {
			return fmt.Errorf(textOutsideLink, exports.HidePath(shown), exports.HidePath(target))
		}
		if info, err = os.Stat(target); err != nil {
			return failed(shown, err)
		}
		p = target
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(to, 0o755); err != nil {
			return failed(shown, err)
		}
		return t.dir(p, to, shown)
	case info.Mode().IsRegular():
		return t.file(p, to, shown, render.FileMode(info.Mode()))
	default:
		return fmt.Errorf("copy %s for the Codex home: it is neither a folder, a regular file nor a link", exports.HidePath(shown))
	}
}

// file copies the regular file src, shown as shown, to a new file to with mode perm:
// within the limit's file size and the bytes it has left, or within [maxCopiedFile]
// bytes without a limit.
func (t *treeCopy) file(src, to, shown string, perm os.FileMode) error {
	if t.limit == nil {
		if err := copyFile(src, to, perm); err != nil {
			return failed(shown, err)
		}
		return nil
	}
	n, err := copyLimited(src, to, perm, t.limit.FileBytes, t.limit.Bytes-t.written)
	t.written += n
	switch {
	case errors.Is(err, errTooLarge):
		return t.limit.TooLarge(shown)
	case errors.Is(err, errPastBudget):
		return t.limit.TooManyBytes
	case err != nil:
		return failed(shown, err)
	}
	return nil
}

// errTooLarge and errPastBudget are what [copyLimited] returns for a file larger than
// its size limit and for a copy past the bytes it has left.
var (
	errTooLarge   = errors.New("the file is larger than the copy takes")
	errPastBudget = errors.New("the copy passes the bytes it may write")
)

// copyFile copies the regular file src to a new file dst with mode perm, see
// [writeNew]. It reads no device or pipe, and fails for a file of more than
// [maxCopiedFile] bytes.
func copyFile(src, dst string, perm os.FileMode) error {
	in, info, err := openRegular(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info.Size() > maxCopiedFile {
		return fmt.Errorf("it has %d bytes, more than Codex reads, %d", info.Size(), maxCopiedFile)
	}
	n, err := writeCopy(in, dst, perm, maxCopiedFile)
	if err == nil && n > maxCopiedFile {
		err = fmt.Errorf("it grew past %d bytes while qory copied it", maxCopiedFile)
	}
	return err
}

// copyLimited copies the regular file src to a new file dst with mode perm, see
// [writeNew], and returns the bytes it wrote. It reads no device or pipe, and fails with
// [errTooLarge] for a file of more than max bytes, and with [errPastBudget] for one of
// more than left bytes, by the file's size or by what the read returns.
func copyLimited(src, dst string, perm os.FileMode, max, left int64) (int64, error) {
	in, info, err := openRegular(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	switch {
	case info.Size() > max:
		return 0, errTooLarge
	case info.Size() > left:
		return 0, errPastBudget
	}
	n, err := writeCopy(in, dst, perm, min(max, left))
	switch {
	case err != nil:
	case n > max:
		err = errTooLarge
	case n > left:
		err = errPastBudget
	}
	return n, err
}

// openRegular opens the file at src for reading, without waiting on a pipe, and fails
// when it is no regular file.
func openRegular(src string) (*os.File, fs.FileInfo, error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := in.Stat()
	if err != nil {
		in.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		in.Close()
		return nil, nil, errors.New("it is no regular file")
	}
	return in, info, nil
}

// writeCopy writes up to limit+1 bytes of in to a new file dst with mode perm, see
// [writeNew], and returns how many it wrote; more than limit means in had more.
func writeCopy(in io.Reader, dst string, perm os.FileMode, limit int64) (int64, error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err == nil {
		// The file was made with perm less the umask, so set perm itself.
		err = out.Chmod(perm)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// WalledConfig is config, a config.toml the compose rendered, with what a walled run adds
// for a Codex whose home is home in the wall: projects."<path>".trust_level = "untrusted"
// for each of projects, the paths where the run's checkout is in the wall, so Codex reads
// no hook, rule or setting of the repository's .codex and writes no trust setting
// itself; and hooks.state."<home>/config.toml:<event>:<group>:<handler>".trusted_hash
// for each command and MCP tool hook of config's hooks, so Codex runs the harness's
// hooks without --dangerously-bypass-hook-trust, which would trust an agent's hooks too.
// home is absolute and contains no link, as Codex resolves it.
//
// It also turns off the skills Codex ships and the skills of a cloud provider, with
// skills.bundled.enabled = false and cloud.skills.enabled = false, and adds a
// skills.config entry with enabled = false for each name of disabled, then one for each
// SKILL.md path of disabledPaths, after the entries config has, since a later entry
// decides (codex-rs/config/src/skills_config.rs). Codex reads skills.config from this
// file alone, never from a repository's .codex.
//
// It sets cli_auth_credentials_store = "ephemeral", so Codex keeps a login in memory
// only and reads no auth.json from the writable home, which the agent could write
// (codex-rs/login/src/auth/storage.rs, EphemeralAuthStorage). Without a login, Codex
// also reads no cloud-config-bundle-cache.json, whose managed hooks would run without
// trust (codex-rs/cloud-config/src/service.rs, load_startup_bundle). A model provider's
// env_key reads its variable and needs no stored login.
//
// It turns off what runs or keeps the agent's files at the start of a later Codex
// process: features.shell_snapshot = false, since the snapshot sources ~/.bashrc and
// ~/.profile in a login shell (codex-rs/core/src/shell_snapshot.rs); allow_login_shell =
// false, so Codex runs commands with bash -c, not bash -lc (codex-rs/core/src/shell.rs);
// features.goals = false, which would continue a goal the agent set
// (codex-rs/ext/goal/src/runtime.rs); and features.memories = false. Codex 0.167.2 turns
// the first three on by default (codex-rs/features/src/lib.rs). A --config flag, a
// trusted project's .codex and the requirements files of /etc/codex can set them again;
// projects are untrusted here, and the agent image has no /etc/codex.
func WalledConfig(config []byte, home string, projects, disabled, disabledPaths []string) ([]byte, error) {
	m := map[string]any{}
	if _, err := toml.NewDecoder(bytes.NewReader(config)).Decode(&m); err != nil {
		return nil, err
	}
	skills, _ := m["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
	}
	skills["bundled"] = map[string]any{"enabled": false}
	entries, _ := skills["config"].([]map[string]any)
	for _, name := range disabled {
		entries = append(entries, map[string]any{"name": name, "enabled": false})
	}
	for _, p := range disabledPaths {
		entries = append(entries, map[string]any{"path": p, "enabled": false})
	}
	if len(entries) > 0 {
		skills["config"] = entries
	}
	m["skills"] = skills
	cloud, _ := m["cloud"].(map[string]any)
	if cloud == nil {
		cloud = map[string]any{}
	}
	cloud["skills"] = map[string]any{"enabled": false}
	m["cloud"] = cloud
	m["cli_auth_credentials_store"] = "ephemeral"
	m["allow_login_shell"] = false
	features, _ := m["features"].(map[string]any)
	if features == nil {
		features = map[string]any{}
	}
	for _, f := range []string{"shell_snapshot", "goals", "memories"} {
		features[f] = false
	}
	m["features"] = features
	if len(projects) > 0 {
		trust := map[string]any{}
		for _, p := range projects {
			trust[p] = map[string]any{"trust_level": "untrusted"}
		}
		m["projects"] = trust
	}
	if hooks, ok := m["hooks"].(map[string]any); ok {
		state := map[string]any{}
		key := path.Join(home, "config.toml")
		for event, groups := range hooks {
			label, ok := eventLabels[event]
			if !ok {
				continue
			}
			list, ok := groups.([]map[string]any)
			if !ok {
				continue
			}
			for g, group := range list {
				handlers, _ := group["hooks"].([]map[string]any)
				for h, handler := range handlers {
					hash, ok, err := hookHash(event, group, handler)
					if err != nil {
						return nil, err
					}
					if ok {
						state[key+":"+label+":"+strconv.Itoa(g)+":"+strconv.Itoa(h)] = map[string]any{"trusted_hash": hash}
					}
				}
			}
		}
		if len(state) > 0 {
			hooks["state"] = state
		}
	}
	return render.EncodeTOML(m)
}

// eventLabels are the hook events of config.toml's hooks table, each with the name Codex
// uses for it in a hook's key and hash (codex-rs/hooks/src/lib.rs, hook_event_key_label).
var eventLabels = map[string]string{
	"PreModelRequest":   "pre_model_request",
	"PostModelRequest":  "post_model_request",
	"PreToolUse":        "pre_tool_use",
	"PermissionRequest": "permission_request",
	"PostToolUse":       "post_tool_use",
	"PreCompact":        "pre_compact",
	"PostCompact":       "post_compact",
	"SessionStart":      "session_start",
	"SessionEnd":        "session_end",
	"UserPromptSubmit":  "user_prompt_submit",
	"SubagentStart":     "subagent_start",
	"SubagentStop":      "subagent_stop",
	"Stop":              "stop",
	"Interrupt":         "interrupt",
}

// matched are the events whose hooks take a matcher; Codex leaves the matcher of any
// other event out of the hash (codex-rs/hooks/src/events/common.rs).
var matched = map[string]bool{
	"PreToolUse": true, "PermissionRequest": true, "PostToolUse": true, "SessionStart": true,
	"SessionEnd": true, "SubagentStart": true, "SubagentStop": true, "PreCompact": true,
	"PostCompact": true,
}

// contextEvents are the events whose hooks keep additionalContextLimit.
var contextEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "SessionStart": true, "UserPromptSubmit": true,
	"SubagentStart": true,
}

// defaultContextLimit is the additionalContextLimit Codex leaves out of the hash.
const defaultContextLimit = 2500

// hookHash is the trusted_hash Codex computes for one hook of event in group
// (codex-rs/hooks/src/engine/discovery.rs, hook_hash, and
// codex-rs/config/src/fingerprint.rs, version_for_toml): "sha256:" and the SHA-256 of
// the compact JSON, keys sorted, of the event's label, the matcher where the event takes
// one, and the one handler normalized, see [commandHandler] and [mcpToolHandler]. False
// for a handler of any other type, for one Codex skips, and for a hook with U+2028 or
// U+2029 in its matcher, see [lineSeparator]; none of them gets trust.
func hookHash(event string, group, handler map[string]any) (string, bool, error) {
	var normalized map[string]any
	switch handler["type"] {
	case "command":
		normalized = commandHandler(event, handler)
	case "mcp_tool":
		normalized = mcpToolHandler(event, handler)
	}
	if normalized == nil {
		return "", false, nil
	}
	identity := map[string]any{"event_name": eventLabels[event], "hooks": []any{normalized}}
	if matcher, ok := group["matcher"].(string); ok && matched[event] {
		if lineSeparator(matcher) {
			return "", false, nil
		}
		identity["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", false, fmt.Errorf("hash a %s hook: %w", event, err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), true, nil
}

// commandHandler is a command hook's handler as Codex normalizes it before the hash:
// type, command as written, timeout with Codex's default and limits, async, and
// statusMessage and additionalContextLimit when they count. nil for a command of only
// white space, which Codex skips, and for U+2028 or U+2029 in the command or
// statusMessage.
func commandHandler(event string, handler map[string]any) map[string]any {
	command, ok := handler["command"].(string)
	if !ok || strings.TrimSpace(command) == "" || lineSeparator(command) {
		return nil
	}
	normalized := map[string]any{"type": "command", "command": command, "timeout": hookTimeout(event, handler["timeout"]), "async": handler["async"] == true}
	if s, ok := handler["statusMessage"].(string); ok {
		if lineSeparator(s) {
			return nil
		}
		normalized["statusMessage"] = s
	}
	if n, ok := handler["additionalContextLimit"].(int64); ok && contextEvents[event] && n != defaultContextLimit {
		normalized["additionalContextLimit"] = n
	}
	return normalized
}

// mcpToolHandler is an MCP tool hook's handler as Codex normalizes it before the hash:
// type, server and tool as written, input, an empty table when it has none, see
// [hashedInput], timeout with Codex's default and limits, and statusMessage when it is
// set. nil for a SessionEnd hook and for an empty server or tool, which Codex skips, for
// U+2028 or U+2029 in a string, and for an input that [hashedInput] cannot write.
//
// Codex 0.167.2's app-server, asked with hooks/list, gave the same hashes for inputs
// with strings, booleans, integers, an array and a nested table, and the status trusted.
func mcpToolHandler(event string, handler map[string]any) map[string]any {
	if event == "SessionEnd" {
		return nil
	}
	server, _ := handler["server"].(string)
	tool, _ := handler["tool"].(string)
	if strings.TrimSpace(server) == "" || strings.TrimSpace(tool) == "" || lineSeparator(server) || lineSeparator(tool) {
		return nil
	}
	input := map[string]any{}
	if v, ok := handler["input"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		h, ok := hashedInput(m)
		if !ok {
			return nil
		}
		input = h.(map[string]any)
	}
	normalized := map[string]any{"type": "mcp_tool", "server": server, "tool": tool, "input": input, "timeout": hookTimeout(event, handler["timeout"])}
	if s, ok := handler["statusMessage"].(string); ok {
		if lineSeparator(s) {
			return nil
		}
		normalized["statusMessage"] = s
	}
	return normalized
}

// numberKey is the key of the table that serde_json writes in place of a number when it
// is built with its arbitrary_precision feature, as Codex 0.167.2 is: the number's text
// is the key's value.
const numberKey = "$serde_json::private::Number"

// hashedInput is v, a value TOML decoded in an MCP tool hook's input, as Codex writes it
// in the hash: a string or a boolean as it is, an integer as a table of [numberKey] and
// its decimal text, and a table or an array with each value so written. False for a
// string with U+2028 or U+2029, which encoding/json escapes and serde_json does not, and
// for a float or a date, whose text qory cannot write as Codex does.
func hashedInput(v any) (any, bool) {
	switch v := v.(type) {
	case string:
		return v, !lineSeparator(v)
	case bool:
		return v, true
	case int64:
		return map[string]any{numberKey: strconv.FormatInt(v, 10)}, true
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			h, ok := hashedInput(e)
			if !ok || lineSeparator(k) {
				return nil, false
			}
			out[k] = h
		}
		return out, true
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			h, ok := hashedInput(e)
			if !ok {
				return nil, false
			}
			out[i] = h
		}
		return out, true
	case []map[string]any:
		out := make([]any, len(v))
		for i, e := range v {
			h, ok := hashedInput(e)
			if !ok {
				return nil, false
			}
			out[i] = h
		}
		return out, true
	}
	return nil, false
}

// lineSeparator reports whether s contains U+2028 or U+2029. serde_json writes them as
// they are, and encoding/json escapes them, so a hook with either in a string it hashes
// gets no trust rather than a hash Codex does not compute.
func lineSeparator(s string) bool {
	return strings.ContainsAny(s, "\u2028\u2029")
}

// hookTimeout is a hook's timeout in seconds as Codex normalizes it: SessionEnd and
// Interrupt default to 1 and are held between 1 and 3; every other event defaults to 600
// and is at least 1.
func hookTimeout(event string, v any) int64 {
	n, set := v.(int64)
	if event == "SessionEnd" || event == "Interrupt" {
		if !set {
			return 1
		}
		return min(max(n, 1), 3)
	}
	if !set {
		return 600
	}
	return max(n, 1)
}
