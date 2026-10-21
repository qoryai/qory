// Package devcontainer reads a repository's Development Container configuration, as the
// Development Containers specification has it: the metadata reference,
// https://containers.dev/implementors/json_reference/, and the specification,
// https://containers.dev/implementors/spec/. [Find] finds the configuration, [Load]
// reads and [Parse] parses it into a [Config], and [Run] runs one of its lifecycle
// commands once [Command.Expand] has replaced its variables.
//
// The repository is untrusted. A configuration is read only from a regular file of the
// repository reached through folders of the repository, with no symbolic link on the
// way, and of at most [MaxSize] bytes. No error quotes a command's text or a value of an
// environment: an error names the member, and a named command by its name.
//
// A configuration is JSON with comments, the spec's words for it: "metadata ... can be
// stored in a JSON with Comments file called devcontainer.json". The reader takes //
// and /* */ comments and a comma after an object's last member or an array's last
// entry, and refuses what Go's JSON reader would take silently: a file that is not
// valid UTF-8, a \u escape of a lone surrogate, and an object that names a member
// twice.
package devcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/integration"
)

// MaxSize is the most bytes a configuration may hold; one is a few kilobytes.
const MaxSize = 1 << 20

// Places are where a repository's configuration is looked for, relative to its root,
// in the spec's order of precedence: "Products using it should expect to find a
// devcontainer.json file in one or more of the following locations (in order of
// precedence): .devcontainer/devcontainer.json, .devcontainer.json,
// .devcontainer/<folder>/devcontainer.json". The third place names one configuration
// among several, for a person to choose, so it is not looked in.
var Places = []string{".devcontainer/devcontainer.json", ".devcontainer.json"}

// The words of the reader. Each is one format string; the first %s is the
// configuration's path relative to the repository, or a member of it.
const (
	textLink           = "%s is a symbolic link; the development container configuration is read only from files and folders of the repository"
	textNotFolder      = "%s is not a folder; the development container configuration is read only from files and folders of the repository"
	textNotFile        = "%s is not a regular file; the development container configuration is read only from a regular file"
	textTooBig         = "%s is more than 1 MiB; a development container configuration is a few kilobytes"
	textNotRead        = "%s could not be read: %s"
	textEncoding       = "%s is not valid UTF-8"
	textUnclosed       = "%s has a /* comment that is never closed"
	textSyntax         = "%s is not JSON with comments: the error is at line %d, column %d"
	textNotObject      = "%s does not hold one JSON object"
	textSurrogateValue = `%s: the string at %s holds a \u escape of a lone surrogate`
	textSurrogateName  = `%s: the member name at %s holds a \u escape of a lone surrogate`
	textRepeated       = `%s: the object at %s names "%s" twice`
	textType           = "%s: %s is not %s"
	textHostAccess     = "%s sets %s, which give the container access to the host; no image is built from a configuration that sets them"
)

// The kinds of value [textType] names.
const (
	wantString    = "a string"
	wantObject    = "an object"
	wantStrings   = "an object of strings"
	wantStringsOr = "an object of strings or null"
	wantCommand   = "a string, an array of strings, or an object of them"
	wantNamed     = "a string or an array of strings"
)

// Config is a configuration, read: the members a run's preparation and an image's build
// use. Every other member is ignored. A string member left out or null is "".
type Config struct {
	// Path is where the configuration is, relative to the repository's root, with
	// slashes: one of [Places].
	Path string

	// OnCreate, UpdateContent, PostCreate and PostStart are onCreateCommand,
	// updateContentCommand, postCreateCommand and postStartCommand, the lifecycle
	// commands that run inside the container, in the spec's order. One left out is
	// empty.
	OnCreate, UpdateContent, PostCreate, PostStart Command
	// Initialize and PostAttach say whether initializeCommand and postAttachCommand
	// are set, present and neither null nor empty. They are never run: the first runs
	// on the host, and the second when a tool attaches.
	Initialize, PostAttach bool

	// Image is image, the name of an image in a container registry.
	Image string
	// Build is build, nil when it is left out.
	Build *Build
	// WorkspaceFolder, RemoteUser and ContainerUser are workspaceFolder, remoteUser and
	// containerUser, as written.
	WorkspaceFolder, RemoteUser, ContainerUser string
	// ContainerEnv and RemoteEnv are containerEnv and remoteEnv, as written, nil when
	// left out. A remoteEnv variable whose value is null, which the reference has unset,
	// is left out of RemoteEnv and named in RemoteUnset.
	ContainerEnv, RemoteEnv map[string]string
	// RemoteUnset are the names of the remoteEnv variables whose value is null, in the
	// order of their names, nil when there are none.
	RemoteUnset []string
	// Features are the keys of features, the Feature IDs, as written and in the order
	// written.
	Features []string

	// HostAccess are the members of [HostAccessMembers] the configuration sets, in that
	// order.
	HostAccess []string
	// Init says whether init is set, which runs tini as the container's first process.
	// It gives no access to the host, so it is reported and not refused.
	Init bool

	// clean is the text Parse read, as JSON: its comments and trailing commas written
	// as spaces.
	clean []byte
}

// Build is the build member: the image is built from a Dockerfile.
type Build struct {
	// Dockerfile and Context are build.dockerfile and build.context, as written. The
	// reference has each relative to the configuration's folder, and context "." when
	// it is left out; Context is "" then.
	Dockerfile, Context string
	// Args is build.args, nil when it is left out.
	Args map[string]string
	// Target is build.target.
	Target string
}

// HostAccessMembers are the members that give a container access to the host, each
// set when it is present and neither null, false, "", [] nor {}: privileged runs it
// --privileged, capAdd adds capabilities, securityOpt sets security options, mounts and
// workspaceMount mount what the host names, and runArgs adds any argument of docker
// run.
var HostAccessMembers = []string{"privileged", "capAdd", "securityOpt", "mounts", "runArgs", "workspaceMount"}

// Commands are the four lifecycle commands that run inside the container, in the
// spec's order: onCreateCommand, updateContentCommand, postCreateCommand and
// postStartCommand. The spec runs the next only once the one before it succeeded: "If
// one of the lifecycle scripts fails, any subsequent scripts will not be executed."
func (c *Config) Commands() []Command {
	return []Command{c.OnCreate, c.UpdateContent, c.PostCreate, c.PostStart}
}

// CheckHostAccess refuses a configuration that sets a member of [HostAccessMembers],
// naming each it sets, and returns nil for one that sets none.
func (c *Config) CheckHostAccess() error {
	if len(c.HostAccess) == 0 {
		return nil
	}
	return fmt.Errorf(textHostAccess, c.Path, list(c.HostAccess))
}

// list is names as a sentence has them: a, a and b, a, b and c.
func list(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Find returns where the repository at root keeps its configuration, the first of
// [Places] that is there, relative to root and with slashes, or "" when it keeps none,
// which is not an error. It refuses a place whose any segment is a symbolic link, a
// folder segment that is not a folder, a file that is not a regular file, and a file
// of more than [MaxSize] bytes. Without links, no place leaves root.
func Find(root string) (string, error) {
	for _, place := range Places {
		found, err := check(root, place)
		if err != nil {
			return "", err
		}
		if found {
			return place, nil
		}
	}
	return "", nil
}

// check walks place under root segment by segment, and reports whether a file is
// there, refusing what [Find] refuses.
func check(root, place string) (bool, error) {
	segments := strings.Split(place, "/")
	at := root
	for i, s := range segments {
		at = filepath.Join(at, s)
		shown := strings.Join(segments[:i+1], "/")
		fi, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, notRead(shown, err)
		}
		last := i == len(segments)-1
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return false, fmt.Errorf(textLink, shown)
		case !last && !fi.IsDir():
			return false, fmt.Errorf(textNotFolder, shown)
		case last && !fi.Mode().IsRegular():
			return false, fmt.Errorf(textNotFile, shown)
		case last && fi.Size() > MaxSize:
			return false, fmt.Errorf(textTooBig, shown)
		}
	}
	return true, nil
}

// Load finds the configuration of the repository at root as [Find] does, reads it and
// parses it as [Parse] does. It returns nil, nil when the repository keeps none. The
// file is opened through an [os.Root] of root, so a link put in its way after [Find]
// looked still never leads outside root, and it is checked again as it is read: a
// regular file of at most [MaxSize] bytes.
func Load(root string) (*Config, error) {
	place, err := Find(root)
	if err != nil || place == "" {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, notRead(place, err)
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(place))
	if err != nil {
		// A link put in the way since is refused as Find refuses it.
		if _, again := check(root, place); again != nil {
			return nil, again
		}
		return nil, notRead(place, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, notRead(place, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf(textNotFile, place)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, notRead(place, err)
	}
	return Parse(place, data)
}

// notRead is [textNotRead]: the system's reason alone, without the path it names.
func notRead(place string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf(textNotRead, place, integration.Printable(err.Error()))
}

// Parse reads data, a configuration at path, relative to the repository, as JSON with
// comments, and returns its [Config]. It refuses, in this order: more than [MaxSize]
// bytes; data that is not valid UTF-8; a /* comment never closed; data that is not one
// JSON value, at the line and column of the error; a value that is not an object; a
// \u escape of a lone surrogate or an object that names a member twice, the first in
// the data; and a member it reads that is not of its type.
func Parse(path string, data []byte) (*Config, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf(textTooBig, path)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf(textEncoding, path)
	}
	clean, ok := strip(data)
	if !ok {
		return nil, fmt.Errorf(textUnclosed, path)
	}
	var doc any
	if err := json.Unmarshal(clean, &doc); err != nil {
		// The error is at the byte the reader stopped at, the one before the offset, or
		// past the last byte when the data ends early.
		at := len(clean)
		var se *json.SyntaxError
		if errors.As(err, &se) && se.Error() != "unexpected end of JSON input" {
			at = int(max(min(se.Offset, int64(len(clean)))-1, 0))
		}
		line, column := position(clean, at)
		return nil, fmt.Errorf(textSyntax, path, line, column)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, fmt.Errorf(textNotObject, path)
	}
	if err := scan(path, clean); err != nil {
		return nil, err
	}
	top, err := members(clean)
	if err != nil {
		return nil, fmt.Errorf(textNotObject, path)
	}
	c := &Config{
		Path:          path,
		OnCreate:      Command{Member: "onCreateCommand"},
		UpdateContent: Command{Member: "updateContentCommand"},
		PostCreate:    Command{Member: "postCreateCommand"},
		PostStart:     Command{Member: "postStartCommand"},
		clean:         clean,
	}
	r := reader{path: path}
	for _, m := range top {
		switch m.name {
		case "onCreateCommand":
			c.OnCreate = r.command(m.name, m.value)
		case "updateContentCommand":
			c.UpdateContent = r.command(m.name, m.value)
		case "postCreateCommand":
			c.PostCreate = r.command(m.name, m.value)
		case "postStartCommand":
			c.PostStart = r.command(m.name, m.value)
		case "initializeCommand":
			c.Initialize = set(m.value)
		case "postAttachCommand":
			c.PostAttach = set(m.value)
		case "image":
			c.Image = r.text(m.name, m.value)
		case "build":
			c.Build = r.build(m.value)
		case "workspaceFolder":
			c.WorkspaceFolder = r.text(m.name, m.value)
		case "remoteUser":
			c.RemoteUser = r.text(m.name, m.value)
		case "containerUser":
			c.ContainerUser = r.text(m.name, m.value)
		case "containerEnv":
			c.ContainerEnv = r.texts(m.name, m.value, false)
		case "remoteEnv":
			c.RemoteEnv = r.texts(m.name, m.value, true)
			c.RemoteUnset = nullMembers(m.value)
		case "features":
			c.Features = r.keys(m.name, m.value)
		case "init":
			c.Init = set(m.value)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	for _, name := range HostAccessMembers {
		for _, m := range top {
			if m.name == name && set(m.value) {
				c.HostAccess = append(c.HostAccess, name)
			}
		}
	}
	return c, nil
}

// position is the line and the column, both from 1 and the column in bytes, of the
// byte at offset at in data.
func position(data []byte, at int) (int, int) {
	before := data[:at]
	return bytes.Count(before, []byte("\n")) + 1, at - bytes.LastIndexByte(before, '\n')
}

// set reports whether a member's value is set: neither null, false, "", [] nor {}.
func set(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// reader reads the members [Parse] takes, keeping the first error, so the members are
// read in one pass and the first wrong one in the file is the one refused.
type reader struct {
	path string
	err  error
}

// wrong keeps [textType] for the member at where, unless an error is kept already.
func (r *reader) wrong(where, want string) {
	if r.err == nil {
		r.err = fmt.Errorf(textType, r.path, integration.Printable(where), want)
	}
}

// text is a string member, "" when it is null.
func (r *reader) text(where string, raw json.RawMessage) string {
	var s *string
	if json.Unmarshal(raw, &s) != nil {
		r.wrong(where, wantString)
		return ""
	}
	if s == nil {
		return ""
	}
	return *s
}

// texts is an object of strings, nil when it is null; with nulls, a member whose value
// is null is left out.
func (r *reader) texts(where string, raw json.RawMessage, nulls bool) map[string]string {
	var m map[string]*string
	if json.Unmarshal(raw, &m) != nil {
		if nulls {
			r.wrong(where, wantStringsOr)
		} else {
			r.wrong(where, wantStrings)
		}
		return nil
	}
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch {
		case v != nil:
			out[k] = *v
		case !nulls:
			r.wrong(where, wantStrings)
			return nil
		}
	}
	return out
}

// nullMembers are the names of the members of raw, an object, whose value is null, in
// the order of their names; nil when there are none, or raw is no object of strings
// and nulls, which [reader.texts] refuses.
func nullMembers(raw json.RawMessage) []string {
	var m map[string]*string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []string
	for k, v := range m {
		if v == nil {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// keys are the names of an object's members in the order written, nil when it is null.
func (r *reader) keys(where string, raw json.RawMessage) []string {
	if string(raw) == "null" {
		return nil
	}
	ms, err := members(raw)
	if err != nil {
		r.wrong(where, wantObject)
		return nil
	}
	out := []string{}
	for _, m := range ms {
		out = append(out, m.name)
	}
	return out
}

// build is the build member, nil when it is null.
func (r *reader) build(raw json.RawMessage) *Build {
	if string(raw) == "null" {
		return nil
	}
	ms, err := members(raw)
	if err != nil {
		r.wrong("build", wantObject)
		return nil
	}
	b := &Build{}
	for _, m := range ms {
		where := "build." + m.name
		switch m.name {
		case "dockerfile":
			b.Dockerfile = r.text(where, m.value)
		case "context":
			b.Context = r.text(where, m.value)
		case "args":
			b.Args = r.texts(where, m.value, false)
		case "target":
			b.Target = r.text(where, m.value)
		}
	}
	return b
}

// command is a lifecycle command in one of the reference's three forms: a string, which
// "goes through a shell"; an array, "passed to the OS for execution without going
// through a shell"; and an object whose every member is a named command of either
// form, which the reference has "to allow for parallel execution" and the spec runs
// "in parallel during that lifecycle step". An empty string, one of spaces alone and
// an empty array run nothing, so they are left out; an object of none is empty.
func (r *reader) command(member string, raw json.RawMessage) Command {
	c := Command{Member: member}
	switch raw[0] {
	case 'n':
		return c
	case '"', '[':
		if s, ok := r.step(member, raw, wantCommand); ok {
			c.Steps = []Step{s}
		}
		return c
	case '{':
		ms, err := members(raw)
		if err != nil {
			r.wrong(member, wantCommand)
			return c
		}
		c.Named = true
		for _, m := range ms {
			s, ok := r.step(member+"."+accesskey.ShownName(m.name, false), m.value, wantNamed)
			if ok {
				s.Name = m.name
				c.Steps = append(c.Steps, s)
			}
		}
		return c
	}
	r.wrong(member, wantCommand)
	return c
}

// step is a command of the string or the array form, and false when it runs nothing.
func (r *reader) step(where string, raw json.RawMessage, want string) (Step, bool) {
	switch raw[0] {
	case '"':
		var line string
		if json.Unmarshal(raw, &line) != nil {
			r.wrong(where, want)
			return Step{}, false
		}
		return Step{Line: line}, strings.TrimSpace(line) != ""
	case '[':
		var args []string
		if json.Unmarshal(raw, &args) != nil || hasNull(raw) {
			r.wrong(where, want)
			return Step{}, false
		}
		return Step{Args: args}, len(args) > 0
	}
	r.wrong(where, want)
	return Step{}, false
}

// hasNull reports whether an array holds a null, which Go reads into a string as "".
func hasNull(raw json.RawMessage) bool {
	var vs []any
	if json.Unmarshal(raw, &vs) != nil {
		return true
	}
	for _, v := range vs {
		if v == nil {
			return true
		}
	}
	return false
}
