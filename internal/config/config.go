// Package config reads qory.yaml: the repository's own document and the machine's.
//
// One schema under one of two names, qory.yaml or harness.yaml, read at three levels.
// The user's own file, $XDG_CONFIG_HOME/qory/qory.yaml or ~/.config/qory/qory.yaml, and
// the files of the checkout's ancestor directories carry the machine's choices; the file
// in the checkout root is committed with the repository and carries what the repository
// needs. Every key may appear at any level and the nearest file wins, so qory runs the
// same with no file at all: every setting has a default. Files are read in this order,
// each overriding the one before it: the user's, the ancestors' that the current user
// owns, the farthest first, the checkout root's. A command line flag overrides every
// file. A directory holds one of the two names, never both.
//
//	apiVersion: qory.dev/v1alpha1 # optional; the newest format this qory reads when left out
//	qory: ">=0.3.0"              # the qory versions this file is written for
//	harness:
//	  runtime: [claude, codex]   # instead of the stack's target.runtime
//	  model: opus                # instead of the stack's target.model
//	  force: true                # replace a tracked, unmodified file where a link goes
//	  update: always             # fetch every git source again on each compose
//	  extends: {git: git@git.example.com:acme/harness, ref: main, path: nextjs-15}
//	  modules:                   # with extends: the stack this checkout extends and its own modules
//	    - name: app              # extends may be left out when compose -f names the base
//	worktree:
//	  dir: ..                    # where worktrees go, relative to the main checkout
//	  name: wt-{branch}          # what a worktree's directory is called
//	  base: main                 # the branch a new worktree branch starts from
//	  pr: refs/pull/{n}/head     # the ref the remote publishes pull request {n}'s head under
//	  branch: delete             # what worktree remove does with the branch: delete or keep
//	  link: [.env]               # linked from the main checkout into a new worktree
//	  copy: [config/local.json]  # copied once into a new worktree
//	    # or {from: ~/secrets/app.env, to: .env}: a path from outside the checkout
//	  run:
//	    add: [pnpm install]      # run in a new worktree, after links and copies
//	    remove: []               # run in a worktree before it is removed
//	git:
//	  timeout: 10m               # the longest one git command may run
//	  cache: /var/cache/qory     # where git sources are fetched to
//	env:
//	  HARNESS_PROFILE: nextjs    # exported to every runtime with a place for it
//	exports:                     # what this repository publishes for others, by name
//	  dir: ./harness             # where stacks/ and modules/ are; default: the root
//	  stacks: [nextjs]           # harness/stacks/nextjs/qory-stack.yaml
//	  modules: [core, nextjs]    # harness/modules/<name>/qory-module.yaml
//
// [Load] discovers and reads the files, [Config] is the result, and [Config.Rows] says
// where each value came from. [DiscoverStack] finds what a checkout composes, its
// qory-stack.yaml or the harness section of its qory.yaml, and [LoadStack] reads it.
// The exports section is the checkout root's alone: [Load] checks that every export it
// lists is there, and [github.com/qoryai/qory/internal/exports] reads it for a source
// naming one.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// FileName is the configuration's file name, in the user's configuration directory, in an
// ancestor of the checkout, or in the checkout root.
const FileName = exports.FileName

// AltFileName is the file's second name, for a repository whose committed file is to say
// nothing of the tool that reads it. It is read exactly as [FileName] is, at every level,
// and a directory holds one of the two names, never both.
const AltFileName = exports.AltFileName

// Names are the file's two names, in the order a directory is searched.
var Names = []string{FileName, AltFileName}

// IsDocument reports whether path carries one of the file's two names, and so is read
// through its harness section rather than as a stack.
func IsDocument(path string) bool {
	base := filepath.Base(path)
	return base == FileName || base == AltFileName
}

// FileIn returns the configuration file dir holds, "" for none, and an error for a
// directory holding both names, since one file at each level is the rule and neither
// name comes first.
func FileIn(dir string) (string, error) { return exports.File(dir) }

// DefaultTimeout is how long one git command may run before the compose gives up on it.
const DefaultTimeout = 10 * time.Minute

// DefaultWorktreeDir is where worktrees go when no file says: the main checkout's parent.
const DefaultWorktreeDir = ".."

// DefaultWorktreeName is what a worktree's directory is called when no file says. {branch}
// is the branch with every slash made a dash, {repo} the main checkout's directory name.
const DefaultWorktreeName = "wt-{branch}"

// DefaultWorktreeBranch is what a remove does with the branch when no file says: delete it.
const DefaultWorktreeBranch = "delete"

// Default is the origin of a value no file set.
const Default = "default"

// Git holds the settings of the git sources.
type Git struct {
	// Timeout is the longest one git command may run.
	Timeout time.Duration
	// Cache is the directory git sources are fetched to, "" for [source.CacheDir].
	Cache string
}

// Worktree holds what a worktree of the repository needs and where the machine puts it.
type Worktree struct {
	// Dir is where worktrees go, relative to the main checkout unless absolute.
	Dir string
	// Name is the template of a worktree's directory name, see [DefaultWorktreeName].
	Name string
	// Base is the branch a new worktree branch starts from, "" for the remote's HEAD.
	Base string
	// PR is the ref the remote publishes a pull request's head under, {n} for its number,
	// "" for the ones GitHub, GitLab and Bitbucket Server publish.
	PR string
	// Branch is what a remove does with the worktree's branch: "delete" or "keep".
	Branch string
	// Link are paths linked into a new worktree.
	Link []Path
	// Copy are paths copied once into a new worktree.
	Copy []Path
	// Add are the commands run in a new worktree after links and copies, in order.
	Add []string
	// Remove are the commands run in a worktree before it is removed, in order.
	Remove []string
}

// Path is one entry of worktree.link or worktree.copy: where it comes from and where it
// goes in the worktree. A path named alone is inside the checkout and goes to the same
// path; {from: <path>, to: <path>} brings a path from anywhere, From absolute with a
// leading ~ expanded, To relative to the worktree.
type Path struct {
	// From is the source: relative to the main checkout, or absolute.
	From string
	// To is the destination, relative to the worktree.
	To string
}

// String is the entry as a row prints it: the path alone when the two are the same,
// else "from -> to".
func (p Path) String() string {
	if p.From == p.To {
		return p.To
	}
	return p.From + " -> " + p.To
}

// pathEntry is a worktree.link or worktree.copy entry as written: a string, or a
// mapping with from and to.
type pathEntry struct {
	From, To string
	// mapping says the entry was written as {from, to}, so an absolute from is meant.
	mapping bool
}

// UnmarshalYAML reads a string as From, and a mapping's from and to.
func (e *pathEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&e.From)
	}
	var m struct {
		From *string `yaml:"from"`
		To   *string `yaml:"to"`
	}
	if err := node.Decode(&m); err != nil {
		return err
	}
	e.mapping = true
	if m.From != nil {
		e.From = *m.From
	}
	if m.To != nil {
		e.To = *m.To
	}
	return nil
}

// Requirement is one file's qory key: the range of qory versions it is written for.
type Requirement struct {
	// File is the qory.yaml that names the range.
	File string
	// Qory is the range.
	Qory stack.Constraint
}

// Config is the effective configuration: the defaults, overridden by every file read.
type Config struct {
	// Qory are the ranges of qory versions the files name, one per file with a qory key,
	// in the order the files were read. Every range has to hold; a file's own range is
	// read under extends as well, since it can only narrow what the base allows.
	Qory []Requirement
	// Runtime replaces the stack's target.runtime, nil to keep the stack's.
	Runtime stack.Runtimes
	// Model replaces the stack's target.model, "" to keep the stack's.
	Model string
	// Force replaces a tracked, unmodified file of the checkout where a link goes.
	Force bool
	// Update fetches every git source again on each compose.
	Update bool
	// Worktree holds the worktree settings.
	Worktree Worktree
	// Git holds the git settings.
	Git Git
	// Env are the variables exported to every runtime with a place for them, on top of
	// what the modules export.
	Env map[string]string
	// Exports is what the repository publishes, from the exports section of the checkout
	// root's file; nil when it names none. A section in any other file is read and left
	// out, since an export is a repository's.
	Exports *exports.Exports
	// Files are the files read, in the order they were applied.
	Files []string
	// origins maps a row key to the file that set it, or [Default].
	origins map[string]string
}

// file is qory.yaml as written. Every key is optional, and a pointer that stays nil is a
// key the file did not name, which leaves the value as it was.
type file struct {
	APIVersion string           `yaml:"apiVersion"`
	Qory       stack.Constraint `yaml:"qory,omitempty"`
	Harness    *harnessSection  `yaml:"harness,omitempty"`
	Worktree   *worktreeSection `yaml:"worktree,omitempty"`
	Git        *struct {
		Timeout *string `yaml:"timeout,omitempty"`
		Cache   *string `yaml:"cache,omitempty"`
	} `yaml:"git,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Exports *exports.Section  `yaml:"exports,omitempty"`
}

// harnessSection is the harness key: the machine's choices for a compose, and in a
// checkout's file its document, the checkout's own stack as a target and modules, or the
// stack it extends and the modules it appends.
type harnessSection struct {
	Runtime     *stack.Runtimes           `yaml:"runtime,omitempty"`
	Model       *string                   `yaml:"model,omitempty"`
	Force       *bool                     `yaml:"force,omitempty"`
	Update      *string                   `yaml:"update,omitempty"`
	Name        string                    `yaml:"name,omitempty"`
	Description string                    `yaml:"description,omitempty"`
	Extends     stack.Source              `yaml:"extends,omitempty"`
	Target      stack.Target              `yaml:"target,omitempty"`
	Modules     []stack.Module            `yaml:"modules,omitempty"`
	Extensions  map[string]map[string]any `yaml:"extensions,omitempty"`
}

// composes reports whether the section carries a compose document: modules, extends or
// extensions. A section of machine keys alone is not a document.
func (h *harnessSection) composes() bool {
	return h != nil && (len(h.Modules) > 0 || h.extends() || len(h.Extensions) > 0)
}

// extends reports whether the section names a stack to extend, by directory or by export.
func (h *harnessSection) extends() bool {
	return h.Extends.Path != "" || h.Extends.Git != "" || h.Extends.Ref != "" || h.Extends.Stack != "" || h.Extends.Module != ""
}

// worktreeSection is the worktree key.
type worktreeSection struct {
	Dir    *string     `yaml:"dir,omitempty"`
	Name   *string     `yaml:"name,omitempty"`
	Base   *string     `yaml:"base,omitempty"`
	PR     *string     `yaml:"pr,omitempty"`
	Branch *string     `yaml:"branch,omitempty"`
	Link   []pathEntry `yaml:"link,omitempty"`
	Copy   []pathEntry `yaml:"copy,omitempty"`
	Run    *struct {
		Add    []string `yaml:"add,omitempty"`
		Remove []string `yaml:"remove,omitempty"`
	} `yaml:"run,omitempty"`
}

// envName is the shape of an environment variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fixedKeys are the row keys every configuration has, in the order [Config.Rows] prints them.
var fixedKeys = []string{"harness.runtime", "harness.model", "harness.force", "harness.update", "worktree.dir", "worktree.name", "worktree.base", "worktree.branch", "git.timeout", "git.cache"}

// Defaults is the configuration with no file read: the stack's runtime and model, no
// force, no update, worktrees beside the main checkout as wt-<branch> off the remote's
// HEAD with nothing linked, copied or run and the branch deleted on remove,
// [DefaultTimeout], the cache under [source.CacheDir], and no variables.
func Defaults() Config {
	c := Config{Worktree: Worktree{Dir: DefaultWorktreeDir, Name: DefaultWorktreeName, Branch: DefaultWorktreeBranch}, Git: Git{Timeout: DefaultTimeout}, Env: map[string]string{}, origins: map[string]string{}}
	for _, key := range fixedKeys {
		c.origins[key] = Default
	}
	return c
}

// Load returns the effective configuration for the checkout at root: the defaults, then
// every file [Discover] finds, applied in order. An error names the file it comes from.
// With own false, the checkout's own file contributes its worktree section and its
// exports only: its harness, git and env keys are left out, which is how a compose of a
// stack that extends a closed base keeps the checkout's authors from configuring the
// runner. The root file's exports are checked: an export whose directory holds no
// document is an error naming it, so the section stays true to the tree.
func Load(root string, own bool) (Config, error) {
	c := Defaults()
	root, err := filepath.Abs(root)
	if err != nil {
		return c, err
	}
	files, err := Discover(root)
	if err != nil {
		return c, err
	}
	for _, path := range files {
		f, err := c.apply(path, own || filepath.Dir(path) != root)
		if err != nil {
			return c, err
		}
		if f.Exports != nil && filepath.Dir(path) == root {
			c.Exports = f.Exports.At(root, path)
			if err := c.Exports.Verify(); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

// Discover lists the configuration files for the checkout at root, in the order they
// apply: the user's file under [UserDir], the files of the ancestor directories the
// current user owns, farthest first, and the file in root, each under either of [Names].
// A file that is not there is not listed; a directory holding both names is an error.
// root is absolute, so the checkout's own file is the one whose directory is root.
func Discover(root string) ([]string, error) {
	var files []string
	if dir := UserDir(); dir != "" {
		path, err := FileIn(dir)
		if err != nil {
			return nil, err
		}
		if path != "" {
			files = append(files, path)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return files, nil
	}
	var ancestors []string
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		path, err := FileIn(dir)
		if err != nil {
			return nil, err
		}
		if path != "" && ownedFile(path) {
			ancestors = append(ancestors, path)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		files = append(files, ancestors[i])
	}
	path, err := FileIn(root)
	if err != nil {
		return nil, err
	}
	if path != "" {
		files = append(files, path)
	}
	return files, nil
}

// DiscoverStack finds what the checkout at root composes and returns its path: the
// qory-stack.yaml in root, or root's qory.yaml when its harness section names modules or
// a stack to extend; else the nearest ancestor directory's, when the current user owns the
// file. A directory holding both is an error naming them. The error for none names root
// and does not wrap an error a caller can match.
//
// A qory-stack.yaml in root is a stack delivered to be extended, and one with no
// extending block is refused: nothing can extend it, so it is a repository's own stack
// in the wrong file, and the error says where that goes. The rule is discovery's alone:
// the same file named with -f, in an ancestor directory, or through extends is read as
// it is.
func DiscoverStack(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if found, err := composeIn(root, false); found != "" || err != nil {
		if err == nil && filepath.Base(found) == stack.FileName {
			if err := closedAtRoot(found); err != nil {
				return "", err
			}
		}
		return found, err
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if found, err := composeIn(dir, true); found != "" || err != nil {
			return found, err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return "", fmt.Errorf("no %s, and no %s or %s whose harness section names modules or a stack to extend, in %s or in an ancestor directory you own", stack.FileName, FileName, AltFileName, root)
}

// Document returns the checkout root's own document: its [FileName] or [AltFileName]
// when the harness section composes, "" for none. It is what compose -f names a base
// for. A file that cannot be read is that error.
func Document(root string) (string, error) {
	path, err := FileIn(root)
	if err != nil || path == "" {
		return "", err
	}
	f, err := read(path)
	if err != nil {
		return "", err
	}
	if !f.Harness.composes() {
		return "", nil
	}
	return path, nil
}

// closedAtRoot reads the qory-stack.yaml at path, found in a checkout root, and returns
// the error for one that declares no extending block. A file that does not load returns
// its load error, the same one [LoadStack] would.
func closedAtRoot(path string) error {
	p, err := stack.Load(path)
	if err != nil {
		return err
	}
	if p.Extending == nil {
		return fmt.Errorf("%s: a stack at a repository root is delivered to be extended, and this one declares no extending block; a repository's own stack goes under harness in %s, which qory setup repo writes", path, FileName)
	}
	return nil
}

// composeIn returns the one document dir holds for a compose, "" for none, and an error
// for both. With owned, a file the current user does not own does not count. A qory.yaml
// that cannot be read is that error, so a mistake in it is reported where it is.
func composeIn(dir string, owned bool) (string, error) {
	var found []string
	if path := filepath.Join(dir, stack.FileName); exists(path) && (!owned || ownedFile(path)) {
		found = append(found, path)
	}
	path, err := FileIn(dir)
	if err != nil {
		return "", err
	}
	if path != "" && (!owned || ownedFile(path)) {
		f, err := read(path)
		if err != nil {
			return "", err
		}
		if f.Harness.composes() {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%s holds both %s and a %s whose harness section names modules; a directory holds one of the two", dir, stack.FileName, filepath.Base(path))
}

// LoadStack reads the document at path as [DiscoverStack] or -f named it: a
// qory-stack.yaml with [stack.Load], a qory.yaml or harness.yaml through its harness
// section with [Compose].
func LoadStack(path string) (*stack.Stack, error) {
	if IsDocument(path) {
		return Compose(path)
	}
	return stack.Load(path)
}

// Compose reads the harness section of the qory.yaml at path as the checkout's document:
// its own stack, a target and modules, or the stack it extends and the modules it appends,
// with the name, description and extensions beside them. A file whose harness section
// holds no document is an error, and so is one with neither a target nor a stack to
// extend: that document composes on the base compose -f names, see [OnBase].
func Compose(path string) (*stack.Stack, error) {
	f, err := read(path)
	if err != nil {
		return nil, err
	}
	if !f.Harness.composes() {
		return nil, fmt.Errorf("%s: the harness section names no modules, no stack to extend and no extensions", path)
	}
	if !f.Harness.extends() && len(f.Harness.Target.Runtimes) == 0 {
		return nil, fmt.Errorf("%s: harness names no target.runtime and no stack to extend; the section holds this repository's own stack, a target and its modules, or extends one, or leaves extends out for the base that qory harness compose -f <stack> names", path)
	}
	return stack.NewCompose(path, f.document())
}

// OnBase reads the harness section of the qory.yaml at path as the checkout's document
// composed on the stack file base, which compose -f named: base's directory takes the
// place of whatever extends names, as if the document had named it there, so a document
// may leave extends out and carry only what is the repository's own, its modules and its
// extensions. The source the document named under extends is returned beside the stack,
// empty for none, so the compose can say what -f replaced. A document holding this
// repository's own stack, a target, is refused: it has no base to replace.
func OnBase(path, base string) (*stack.Stack, stack.Source, error) {
	f, err := read(path)
	if err != nil {
		return nil, stack.Source{}, err
	}
	if !f.Harness.composes() {
		return nil, stack.Source{}, fmt.Errorf("%s: the harness section names no modules, no stack to extend and no extensions", path)
	}
	if len(f.Harness.Target.Runtimes) > 0 || f.Harness.Target.Model != "" {
		return nil, stack.Source{}, fmt.Errorf("%s: harness sets a target, this repository's own stack, and -f names a base for a document that extends one; a document takes its base under extends or from -f, in place of a target", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, stack.Source{}, err
	}
	baseDir, err := filepath.Abs(filepath.Dir(base))
	if err != nil {
		return nil, stack.Source{}, err
	}
	dir := baseDir
	if rel, err := filepath.Rel(filepath.Dir(abs), baseDir); err == nil {
		dir = rel
	}
	p := f.document()
	named := p.Extends
	p.Extends = stack.Source{Path: dir}
	p, err = stack.NewCompose(path, p)
	return p, named, err
}

// document is the file's harness section as the stack it holds, before validation.
func (f file) document() *stack.Stack {
	h := f.Harness
	return &stack.Stack{APIVersion: f.APIVersion, Qory: f.Qory, Name: h.Name, Description: h.Description, Extends: h.Extends, Target: h.Target, Modules: h.Modules, Extensions: h.Extensions}
}

// UserDir is the user's configuration directory: $XDG_CONFIG_HOME/qory, else
// ~/.config/qory, and "" when neither can be found.
func UserDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "qory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "qory")
}

// exists reports whether a regular file, or a link to one, is at path.
func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ownedFile reports whether the current user owns the file at path.
func ownedFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && stack.OwnedByCurrentUser(info)
}

// apply reads one file, sets the keys it names and returns the file as read. With
// machine false, only the worktree section and the qory key are taken: the harness, git
// and env keys are the machine's and left out.
func (c *Config) apply(path string, machine bool) (file, error) {
	f, err := read(path)
	if err != nil {
		return f, err
	}
	c.Files = append(c.Files, path)
	if !f.Qory.Empty() {
		c.Qory = append(c.Qory, Requirement{File: path, Qory: f.Qory})
	}
	if err := c.applyWorktree(path, f.Worktree); err != nil {
		return f, err
	}
	if !machine {
		return f, nil
	}
	if h := f.Harness; h != nil {
		if h.Runtime != nil {
			if len(*h.Runtime) == 0 {
				return f, fmt.Errorf("%s: harness.runtime is empty; it names one runtime or a list of them", path)
			}
			if err := h.Runtime.Validate(); err != nil {
				return f, fmt.Errorf("%s: harness.%w", path, err)
			}
			c.Runtime = *h.Runtime
			c.origins["harness.runtime"] = path
		}
		if h.Model != nil {
			c.Model = *h.Model
			c.origins["harness.model"] = path
		}
		if h.Force != nil {
			c.Force = *h.Force
			c.origins["harness.force"] = path
		}
		if h.Update != nil {
			switch *h.Update {
			case "always":
				c.Update = true
			case "never":
				c.Update = false
			default:
				return f, fmt.Errorf("%s: harness.update %q is not always or never", path, *h.Update)
			}
			c.origins["harness.update"] = path
		}
	}
	if f.Git != nil {
		if f.Git.Timeout != nil {
			d, err := time.ParseDuration(*f.Git.Timeout)
			if err != nil || d <= 0 {
				return f, fmt.Errorf("%s: git.timeout %q is not a duration above zero, such as 10m", path, *f.Git.Timeout)
			}
			c.Git.Timeout = d
			c.origins["git.timeout"] = path
		}
		if f.Git.Cache != nil {
			dir := *f.Git.Cache
			if dir == "" {
				return f, fmt.Errorf("%s: git.cache is empty", path)
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(filepath.Dir(path), dir)
			}
			c.Git.Cache = filepath.Clean(dir)
			c.origins["git.cache"] = path
		}
	}
	for name, value := range f.Env {
		if !envName.MatchString(name) {
			return f, fmt.Errorf("%s: env: %s is not an environment variable name", path, name)
		}
		if name == "QORY_HARNESS_HOME" {
			return f, fmt.Errorf("%s: env.QORY_HARNESS_HOME is qory's own; the configuration exports another name", path)
		}
		c.Env[name] = value
		c.origins["env."+name] = path
	}
	return f, nil
}

// applyWorktree sets the worktree keys the section names. A list replaces the one before
// it; the nearest file's list is the one in force.
func (c *Config) applyWorktree(path string, w *worktreeSection) error {
	if w == nil {
		return nil
	}
	if w.Dir != nil {
		if *w.Dir == "" {
			return fmt.Errorf("%s: worktree.dir is empty; it is a directory, relative to the main checkout unless absolute", path)
		}
		c.Worktree.Dir = *w.Dir
		c.origins["worktree.dir"] = path
	}
	if w.Name != nil {
		if !strings.Contains(*w.Name, "{branch}") {
			return fmt.Errorf("%s: worktree.name %q holds no {branch}; two worktrees would get one directory", path, *w.Name)
		}
		if strings.ContainsAny(*w.Name, `/\`) {
			return fmt.Errorf("%s: worktree.name %q holds a slash; it is one directory name under worktree.dir", path, *w.Name)
		}
		c.Worktree.Name = *w.Name
		c.origins["worktree.name"] = path
	}
	if w.Base != nil {
		c.Worktree.Base = *w.Base
		c.origins["worktree.base"] = path
	}
	if w.PR != nil {
		if !strings.HasPrefix(*w.PR, "refs/") || !strings.Contains(*w.PR, "{n}") {
			return fmt.Errorf("%s: worktree.pr %q is not a ref under refs/ with {n} for the pull request number, as refs/pull/{n}/head", path, *w.PR)
		}
		c.Worktree.PR = *w.PR
		c.origins["worktree.pr"] = path
	}
	if w.Branch != nil {
		if *w.Branch != "delete" && *w.Branch != "keep" {
			return fmt.Errorf("%s: worktree.branch %q is not delete or keep", path, *w.Branch)
		}
		c.Worktree.Branch = *w.Branch
		c.origins["worktree.branch"] = path
	}
	for _, list := range []struct {
		key     string
		entries []pathEntry
		into    *[]Path
	}{{"worktree.link", w.Link, &c.Worktree.Link}, {"worktree.copy", w.Copy, &c.Worktree.Copy}} {
		if list.entries == nil {
			continue
		}
		paths := []Path{}
		for _, e := range list.entries {
			p, err := worktreePath(path, list.key, e)
			if err != nil {
				return err
			}
			paths = append(paths, p)
		}
		*list.into = paths
		c.origins[list.key] = path
	}
	if w.Run != nil {
		if w.Run.Add != nil {
			c.Worktree.Add = append([]string(nil), w.Run.Add...)
			c.origins["worktree.run.add"] = path
		}
		if w.Run.Remove != nil {
			c.Worktree.Remove = append([]string(nil), w.Run.Remove...)
			c.origins["worktree.run.remove"] = path
		}
	}
	return nil
}

// worktreePath checks one worktree.link or worktree.copy entry of the file at path and
// returns it as a [Path]. A path named alone, or a from written relative, is inside the
// checkout and goes to the same path unless to says otherwise; a from that is absolute
// or starts with ~ comes from outside the checkout and needs a to. A to is relative and
// stays inside the worktree.
func worktreePath(path, key string, e pathEntry) (Path, error) {
	from := e.From
	if from == "" {
		return Path{}, fmt.Errorf("%s: %s names an entry with no path; one is a path inside the checkout, or {from: <path>, to: <path in the worktree>}", path, key)
	}
	if from == "~" || strings.HasPrefix(from, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Path{}, fmt.Errorf("%s: %s names %s, and the home directory is unknown: %w", path, key, from, err)
		}
		from = filepath.Join(home, from[1:])
	}
	to := e.To
	switch {
	case filepath.IsAbs(from) && !e.mapping:
		return Path{}, fmt.Errorf("%s: %s names %q, which is not a path inside the checkout; a path from outside goes as {from: %s, to: <path in the worktree>}", path, key, e.From, e.From)
	case filepath.IsAbs(from):
		from = filepath.Clean(from)
		if to == "" {
			return Path{}, fmt.Errorf("%s: %s names %s with no to; a path from outside the checkout says where it goes, as {from: %s, to: <path in the worktree>}", path, key, e.From, e.From)
		}
	default:
		if !insideRel(from) {
			return Path{}, fmt.Errorf("%s: %s names %q, which is not a path inside the checkout", path, key, from)
		}
		if to == "" {
			to = from
		}
	}
	if !insideRel(to) {
		return Path{}, fmt.Errorf("%s: %s names to %q for %s, which is not a path inside the worktree", path, key, to, e.From)
	}
	return Path{From: from, To: to}, nil
}

// insideRel reports whether p is a relative path that stays inside the directory it is
// relative to.
func insideRel(p string) bool {
	clean := filepath.Clean(p)
	return p != "" && !filepath.IsAbs(p) && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// read decodes one file. An unknown key is an error, and so is a second document and an
// apiVersion other than [stack.APIVersion]. A file naming no apiVersion is read as the
// newest format this qory reads, which is that one: the file is a repository's or a
// machine's own, not a delivered document, so it need carry no version to bump.
func read(path string) (file, error) {
	var f file
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return f, decodeError(path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return f, fmt.Errorf("%s: holds more than one document; a configuration is one", path)
	}
	if f.APIVersion == "" {
		f.APIVersion = stack.APIVersion
	}
	if err := exports.CheckAPIVersion(f.APIVersion); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	if f.Exports != nil {
		if err := f.Exports.Validate(); err != nil {
			return f, fmt.Errorf("%s: %w", path, err)
		}
	}
	return f, nil
}

// unknownKey is the decoder's report of a key the document has no field for. It names the
// Go type, which the message a person reads leaves out.
var unknownKey = regexp.MustCompile(`(line \d+: )?field (\S+) not found in type \S+`)

// decodeError prefixes a decode error with path. An unknown key is reported as one the
// configuration does not read, with the decoder's line number when it gives one; any
// other error is returned as the decoder wrote it.
func decodeError(path string, err error) error {
	if m := unknownKey.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], filepath.Base(path))
	}
	return fmt.Errorf("%s: %w", path, err)
}

// Row is one effective value and where it came from.
type Row struct {
	// Key is the setting as the file names it: harness.runtime, git.timeout, env.NAME.
	Key string
	// Value is the effective value as text; "(stack)" for a runtime or model the stack
	// decides, "(remote HEAD)" for a worktree base no file set.
	Value string
	// Origin is the file that set the value, or [Default].
	Origin string
}

// Rows lists every effective value with its origin: the qory ranges when a file names
// one, the fixed keys, the worktree lists that are set, the variables after them in
// name order, and the exports when the root's file names any.
func (c Config) Rows() []Row {
	runtime, model := "(stack)", "(stack)"
	if c.Runtime != nil {
		runtime = c.Runtime.String()
	}
	if c.Model != "" {
		model = c.Model
	}
	update := "never"
	if c.Update {
		update = "always"
	}
	base := c.Worktree.Base
	if base == "" {
		base = "(remote HEAD)"
	}
	cache := c.Git.Cache
	if cache == "" {
		cache, _ = source.CacheDir()
	}
	var rows []Row
	for _, r := range c.Qory {
		rows = append(rows, Row{"qory", r.Qory.String(), r.File})
	}
	rows = append(rows,
		Row{"harness.runtime", runtime, c.origins["harness.runtime"]},
		Row{"harness.model", model, c.origins["harness.model"]},
		Row{"harness.force", fmt.Sprint(c.Force), c.origins["harness.force"]},
		Row{"harness.update", update, c.origins["harness.update"]},
		Row{"worktree.dir", c.Worktree.Dir, c.origins["worktree.dir"]},
		Row{"worktree.name", c.Worktree.Name, c.origins["worktree.name"]},
		Row{"worktree.base", base, c.origins["worktree.base"]},
		Row{"worktree.branch", c.Worktree.Branch, c.origins["worktree.branch"]},
	)
	if c.Worktree.PR != "" {
		rows = append(rows, Row{"worktree.pr", c.Worktree.PR, c.origins["worktree.pr"]})
	}
	for _, list := range []struct {
		key   string
		items []string
	}{{"worktree.link", pathStrings(c.Worktree.Link)}, {"worktree.copy", pathStrings(c.Worktree.Copy)}, {"worktree.run.add", c.Worktree.Add}, {"worktree.run.remove", c.Worktree.Remove}} {
		if len(list.items) > 0 {
			rows = append(rows, Row{list.key, strings.Join(list.items, ", "), c.origins[list.key]})
		}
	}
	rows = append(rows,
		Row{"git.timeout", c.Git.Timeout.String(), c.origins["git.timeout"]},
		Row{"git.cache", cache, c.origins["git.cache"]},
	)
	names := make([]string, 0, len(c.Env))
	for name := range c.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rows = append(rows, Row{"env." + name, c.Env[name], c.origins["env."+name]})
	}
	if e := c.Exports; e != nil {
		rows = append(rows,
			Row{"exports.dir.stacks", e.Dir.Stacks, e.File},
			Row{"exports.dir.modules", e.Dir.Modules, e.File},
			Row{"exports.stacks", listOrNone(e.Stacks), e.File},
			Row{"exports.modules", listOrNone(e.Modules), e.File},
		)
	}
	return rows
}

// pathStrings is each path as [Path.String] prints it.
func pathStrings(paths []Path) []string {
	var out []string
	for _, p := range paths {
		out = append(out, p.String())
	}
	return out
}

// listOrNone joins names for a row, "(none)" for an empty list.
func listOrNone(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
