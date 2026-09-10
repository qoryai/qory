// Package config reads qory.yaml: how qory runs on this machine, for this person and this
// checkout, as opposed to what the harness is, which the profile says.
//
// Every setting has a default, so qory runs the same with no file at all. A file sets the
// keys it names and leaves the rest as they were. Files are read in this order, each
// overriding the one before it: the user's own file, $XDG_CONFIG_HOME/qory/qory.yaml or
// ~/.config/qory/qory.yaml; qory.yaml in the ancestor directories of the checkout that
// the current user owns, the farthest first; qory.yaml in the checkout root. A command
// line flag overrides every file.
//
//	apiVersion: qory.ai/v1alpha1
//	kind: QoryConfig
//	runtime: [claude, codex]   # instead of the profile's target.runtime
//	model: opus                # instead of the profile's target.model
//	force: true                # replace a tracked, unmodified file where a link goes
//	update: always             # fetch every git source again on each compose
//	git:
//	  timeout: 10m             # the longest one git command may run
//	  cache: /var/cache/qory   # where git sources are fetched to
//	env:
//	  HARNESS_PROFILE: nextjs    # exported to every runtime with a place for it
//
// [Load] discovers and reads the files, [Config] is the result, and [Config.Rows] says
// where each value came from.
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

	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/source"
)

// Kind is the kind every configuration document carries.
const Kind = "QoryConfig"

// FileName is the configuration's file name, in the user's configuration directory, in an
// ancestor of the checkout, or in the checkout root.
const FileName = "qory.yaml"

// DefaultTimeout is how long one git command may run before the compose gives up on it.
const DefaultTimeout = 10 * time.Minute

// Default is the origin of a value no file set.
const Default = "default"

// Git holds the settings of the git sources.
type Git struct {
	// Timeout is the longest one git command may run.
	Timeout time.Duration
	// Cache is the directory git sources are fetched to, "" for [source.CacheDir].
	Cache string
}

// Config is the effective configuration: the defaults, overridden by every file read.
type Config struct {
	// Runtime replaces the profile's target.runtime, nil to keep the profile's.
	Runtime profile.Runtimes
	// Model replaces the profile's target.model, "" to keep the profile's.
	Model string
	// Force replaces a tracked, unmodified file of the checkout where a link goes.
	Force bool
	// Update fetches every git source again on each compose.
	Update bool
	// Git holds the git settings.
	Git Git
	// Env are the variables exported to every runtime with a place for them, on top of
	// what the layers export.
	Env map[string]string
	// Files are the files read, in the order they were applied.
	Files []string
	// origins maps a row key to the file that set it, or [Default].
	origins map[string]string
}

// file is qory.yaml as written. Every key is optional, and a pointer that stays nil is a
// key the file did not name, which leaves the value as it was.
type file struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Runtime    *profile.Runtimes `yaml:"runtime,omitempty"`
	Model      *string           `yaml:"model,omitempty"`
	Force      *bool             `yaml:"force,omitempty"`
	Update     *string           `yaml:"update,omitempty"`
	Git        *struct {
		Timeout *string `yaml:"timeout,omitempty"`
		Cache   *string `yaml:"cache,omitempty"`
	} `yaml:"git,omitempty"`
	Env map[string]string `yaml:"env,omitempty"`
}

// envName is the shape of an environment variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Defaults is the configuration with no file read: the profile's runtime and model, no
// force, no update, [DefaultTimeout], the cache under [source.CacheDir], and no
// variables.
func Defaults() Config {
	c := Config{Git: Git{Timeout: DefaultTimeout}, Env: map[string]string{}, origins: map[string]string{}}
	for _, key := range []string{"runtime", "model", "force", "update", "git.timeout", "git.cache"} {
		c.origins[key] = Default
	}
	return c
}

// Load returns the effective configuration for the checkout at root: the defaults, then
// every file [Discover] finds, applied in order. An error names the file it comes from.
// With own false the checkout's own file is left out, which is how a compose of a profile
// that extends a closed base keeps the checkout's authors from configuring the runner.
func Load(root string, own bool) (Config, error) {
	c := Defaults()
	for _, path := range Discover(root) {
		if !own && filepath.Dir(path) == root {
			continue
		}
		if err := c.apply(path); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Discover lists the configuration files for the checkout at root, in the order they
// apply: the user's file under [UserDir], the files of the ancestor directories the
// current user owns, farthest first, and the file in root. A file that is not there is
// not listed. root is absolute, so the checkout's own file is the one whose directory is
// root.
func Discover(root string) []string {
	var files []string
	if dir := UserDir(); dir != "" {
		if path := filepath.Join(dir, FileName); exists(path) {
			files = append(files, path)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return files
	}
	var ancestors []string
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, FileName)
		if info, err := os.Stat(path); err == nil && profile.OwnedByCurrentUser(info) {
			ancestors = append(ancestors, path)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		files = append(files, ancestors[i])
	}
	if path := filepath.Join(root, FileName); exists(path) {
		files = append(files, path)
	}
	return files
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

// apply reads one file and sets the keys it names.
func (c *Config) apply(path string) error {
	f, err := read(path)
	if err != nil {
		return err
	}
	c.Files = append(c.Files, path)
	if f.Runtime != nil {
		if len(*f.Runtime) == 0 {
			return fmt.Errorf("%s: runtime is empty; it names one runtime or a list of them", path)
		}
		if err := f.Runtime.Validate(); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		c.Runtime = *f.Runtime
		c.origins["runtime"] = path
	}
	if f.Model != nil {
		c.Model = *f.Model
		c.origins["model"] = path
	}
	if f.Force != nil {
		c.Force = *f.Force
		c.origins["force"] = path
	}
	if f.Update != nil {
		switch *f.Update {
		case "always":
			c.Update = true
		case "never":
			c.Update = false
		default:
			return fmt.Errorf("%s: update %q is not always or never", path, *f.Update)
		}
		c.origins["update"] = path
	}
	if f.Git != nil {
		if f.Git.Timeout != nil {
			d, err := time.ParseDuration(*f.Git.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s: git.timeout %q is not a duration above zero, such as 10m", path, *f.Git.Timeout)
			}
			c.Git.Timeout = d
			c.origins["git.timeout"] = path
		}
		if f.Git.Cache != nil {
			dir := *f.Git.Cache
			if dir == "" {
				return fmt.Errorf("%s: git.cache is empty", path)
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
			return fmt.Errorf("%s: env: %s is not an environment variable name", path, name)
		}
		if name == "QORY_HARNESS_HOME" {
			return fmt.Errorf("%s: env.QORY_HARNESS_HOME is qory's own; the configuration exports another name", path)
		}
		c.Env[name] = value
		c.origins["env."+name] = path
	}
	return nil
}

// read decodes one file. An unknown key is an error, and so is a second document, an
// apiVersion other than [profile.APIVersion] and a kind other than [Kind].
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
	if f.APIVersion != profile.APIVersion {
		return f, fmt.Errorf("%s: apiVersion %q is not one this qory reads; versions: %s", path, f.APIVersion, profile.APIVersion)
	}
	if f.Kind != Kind {
		return f, fmt.Errorf("%s: kind %q is not %s", path, f.Kind, Kind)
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
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], FileName)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// Row is one effective value and where it came from.
type Row struct {
	// Key is the setting as the file names it: runtime, git.timeout, env.NAME.
	Key string
	// Value is the effective value as text; "(profile)" for a runtime or model the profile
	// decides.
	Value string
	// Origin is the file that set the value, or [Default].
	Origin string
}

// Rows lists every effective value with its origin, the fixed keys first and the
// variables after them in name order.
func (c Config) Rows() []Row {
	runtime, model := "(profile)", "(profile)"
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
	cache := c.Git.Cache
	if cache == "" {
		cache, _ = source.CacheDir()
	}
	rows := []Row{
		{"runtime", runtime, c.origins["runtime"]},
		{"model", model, c.origins["model"]},
		{"force", fmt.Sprint(c.Force), c.origins["force"]},
		{"update", update, c.origins["update"]},
		{"git.timeout", c.Git.Timeout.String(), c.origins["git.timeout"]},
		{"git.cache", cache, c.origins["git.cache"]},
	}
	names := make([]string, 0, len(c.Env))
	for name := range c.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rows = append(rows, Row{"env." + name, c.Env[name], c.origins["env."+name]})
	}
	return rows
}
