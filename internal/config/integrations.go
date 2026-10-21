package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// integrationKey is the grammar of an integration's key, the integration contract's
// name: Forager's credential name without the dot, which a program's name, qory-<key>,
// would read as an extension.
var integrationKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ProgramPrefix is what an integration's key follows in the name of its program when
// the entry sets none: qory-github for github, looked up on the PATH.
const ProgramPrefix = "qory-"

// ForagerIntegration is one entry of the integrations section: a program that speaks
// the integration contract, and the settings every role of it is started with.
type ForagerIntegration struct {
	// Key is the name the machine declares the integration under, and the name of what
	// it defines: the credential of its credential role.
	Key string
	// Program is the program as the entry sets it, [ProgramPrefix] and the key when it
	// sets none: an absolute path, or a name looked up on the PATH.
	Program string
	// Settings is the settings document as compact JSON, its keys in the file's order;
	// {} when the entry has none.
	Settings []byte
	// Path and Version are the program found and the version its description contains,
	// set by [Forager.Expand].
	Path, Version string
}

// readIntegrations reads the integrations section, a mapping from a key to an entry,
// in the file's order. The settings are written out as JSON here, and read against the
// program's description by [Forager.Expand], which runs the program.
func readIntegrations(path string, node *yaml.Node) ([]ForagerIntegration, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: gateway.integrations is a mapping from a name to an integration", path)
	}
	var out []ForagerIntegration
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		at := entryAt("gateway.integrations", node.Content[i])
		if !integrationKey.MatchString(key) {
			return nil, fmt.Errorf("%s: gateway.integrations: %s is not 1 to 64 of a-z, 0-9, underscore and dash, starting with a letter or a digit", path, quotedName(node.Content[i]))
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: %s is declared twice", path, at)
		}
		seen[key] = true
		in := ForagerIntegration{Key: key, Program: ProgramPrefix + key, Settings: []byte("{}")}
		entry := node.Content[i+1]
		if entry.ShortTag() == "!!null" {
			out = append(out, in)
			continue
		}
		if entry.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: %s is a mapping: program and settings", path, at)
		}
		for k := 0; k+1 < len(entry.Content); k += 2 {
			value := entry.Content[k+1]
			switch name := entry.Content[k].Value; name {
			case "program":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || value.Value == "" {
					return nil, fmt.Errorf("%s: %s.program is a path or a name on the PATH", path, at)
				}
				if strings.ContainsRune(value.Value, '/') && !filepath.IsAbs(value.Value) {
					return nil, fmt.Errorf("%s: %s.program is not an absolute path; set program to an absolute path or a name on the PATH", path, at)
				}
				in.Program = value.Value
			case "settings":
				if value.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("%s: %s.settings is a mapping, the settings document", path, at)
				}
				var b bytes.Buffer
				if err := writeJSON(&b, value, "settings"); err != nil {
					return nil, fmt.Errorf("%s: %s.%w", path, at, err)
				}
				in.Settings = b.Bytes()
			default:
				return nil, keyNotOne(path, at, entry.Content[k], "")
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// writeJSON writes the YAML under n as compact JSON, a mapping's keys in the file's
// order. at is where n stands, for an error, which names the place and never the value.
// An alias is refused: the settings go on a command line, whole, and a few chained
// aliases expand to more than one can hold. A key or an anchor whose name contains an
// access key secret or a run key is placed by its line, without the name.
func writeJSON(b *bytes.Buffer, n *yaml.Node, at string) error {
	switch n.Kind {
	case yaml.AliasNode:
		if accesskey.ContainsSecret(n.Value) {
			return fmt.Errorf("%s: line %d: the value is a YAML alias, which the settings may not contain; write the value out in full", at, n.Line)
		}
		return fmt.Errorf("%s: line %d: *%s is a YAML alias, which the settings may not contain; write the value out in full", at, n.Line, n.Value)
	case yaml.MappingNode:
		b.WriteByte('{')
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode || isMergeKey(k) {
				return fmt.Errorf("%s: line %d: a YAML alias or merge, which the settings may not contain; write the value out in full", at, k.Line)
			}
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return fmt.Errorf("%s: line %d: a key that is not a string", at, k.Line)
			}
			if first, ok := seen[k.Value]; ok {
				if accesskey.ContainsSecret(k.Value) {
					return fmt.Errorf("%s: line %d: a key is written twice; it was first written at line %d", at, k.Line, first)
				}
				return fmt.Errorf("%s.%s appears twice", at, k.Value)
			}
			seen[k.Value] = k.Line
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k.Value)
			b.WriteByte(':')
			if err := writeJSON(b, n.Content[i+1], entryAt(at, k)); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, c, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			b.WriteString("null")
		case "!!bool", "!!int", "!!float":
			var v any
			if err := n.Decode(&v); err != nil {
				return fmt.Errorf("%s is not a number or a boolean that JSON can represent", at)
			}
			if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
				return fmt.Errorf("%s is not a number that JSON can represent", at)
			}
			out, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("%s is not a value that JSON can represent", at)
			}
			b.Write(out)
		default:
			// A string, and a timestamp or anything else tagged, as it was written.
			writeString(b, n.Value)
		}
	default:
		return fmt.Errorf("%s: line %d: not a value the settings can contain", at, n.Line)
	}
	return nil
}

// writeString writes s as a JSON string as encoding/json's Marshal writes it, with <,
// >, &, U+2028 and U+2029 escaped: the compact JSON of step 4 of the integration
// contract's "Declaring an integration".
func writeString(b *bytes.Buffer, s string) {
	out, _ := json.Marshal(s)
	b.Write(out)
}

// Expansion selects which integrations [Forager.Expand] describes, and where their
// programs may not be.
type Expansion struct {
	// Workspace are the files and directories a run may write: its checkout, the mounts a
	// wall passes to the container read-write, and every place a walled run has used. A
	// program that is one of them, or under one, is refused: what a run may change may
	// not choose what runs with the machine's credentials.
	Workspace []string
	// Only, when set, selects which integrations are described, by key: the ones a run's
	// policy selects. Nil describes every one.
	Only func(key string) bool
}

// Expand describes the integrations the file declares, every one or the ones
// [Expansion.Only] selects, and adds the credentials they define to r.Credentials, after
// the file's own, in the section's order. For each it finds the program, runs <program>
// describe as [integration.Describe] does, and checks the settings against the
// description. The program is found by its absolute path, or on the PATH, and is
// refused in a directory of [Expansion.Workspace] and where [trusted] refuses it. A
// program that is not found or does not answer, settings the description refuses, and an
// integration that plays no role qory expands are errors that contain the file and the
// key. A key that contains an access key secret or a run key is printed as
// [accesskey.ShownName] prints it, and so is each word of the error that contains the
// key; each other word of the error that contains one, such as the program's path, is
// printed as [exports.Value] prints it. The credential role defines the credential whose name is the key, with the
// adapter [integration.CredentialAdapter] returns, unless the file's credentials section
// defines that name itself: then the file's definition stands, [Forager.Shadowed] lists
// the key, and the integration's credential is not defined. Roles qory does not expand are
// left as they are. Once Expand succeeds, each further call does nothing; after an error, the
// next call describes again.
func (r *Forager) Expand(ctx context.Context, e Expansion) error {
	if r == nil || r.expanded {
		return nil
	}
	own := map[string]bool{}
	for _, c := range r.Credentials {
		own[c.Name] = true
	}
	var defined []ForagerCredential
	for i := range r.Integrations {
		in := &r.Integrations[i]
		if e.Only != nil && !e.Only(in.Key) {
			continue
		}
		// A key that contains an access key secret or a run key is in the program's name
		// too, qory-<key>, and in its path, and Forager's refusal of the credential
		// prints it unquoted: each copy prints as accesskey.ShownName prints it. A value
		// with one, such as a program path the file sets, prints as exports.Value prints
		// it.
		fail := func(format string, a ...any) error {
			return fmt.Errorf("%s: gateway.integrations.%s: %s", r.File, accesskey.ShownName(in.Key, false), exports.HideValues(exports.HideName(fmt.Sprintf(format, a...), in.Key)))
		}
		found, err := program(in.Program, e.Workspace)
		if err != nil {
			return fail("%v", err)
		}
		if name := relativeFile(in.Settings); name != "" {
			return fail("settings.%s is a relative path; qory takes the file by its absolute path, since a relative one moves with the working folder", accesskey.ShownName(name, false))
		}
		d, err := integration.Describe(ctx, found)
		if err != nil {
			return fail("%v", err)
		}
		if err := d.CheckSettings(in.Settings); err != nil {
			return fail("%v", err)
		}
		if d.Credential == nil {
			return fail("%s plays %s, none of which qory expands, and defines nothing", in.Program, rolesText(d.Roles))
		}
		// The version is the program's to word, and is printed as a terminal takes it.
		in.Path, in.Version = found, integration.Printable(d.ProgramVersion)
		if own[in.Key] {
			continue
		}
		c := ForagerCredential{Name: in.Key, Adapter: integration.CredentialAdapter(found, in.Settings), Argument: d.Credential.Argument, Hosts: d.Credential.Hosts, Integration: in.Key}
		if err := (gateway.Credential{Name: c.Name, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts}).Check(); err != nil {
			return fail("%v", err)
		}
		defined = append(defined, c)
	}
	r.Credentials = append(r.Credentials, defined...)
	r.expanded = true
	return nil
}

// Shadowed are the keys of the integrations whose name the file's credentials section
// defines itself, in the section's order: the section's definition is the one a run
// receives.
func (r *Forager) Shadowed() []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, in := range r.Integrations {
		for _, c := range r.Credentials {
			if c.Name == in.Key && c.Integration == "" {
				out = append(out, in.Key)
				break
			}
		}
	}
	return out
}

// program finds an integration's program: name itself when it is an absolute path,
// else the first of that name in a directory of the PATH. The path returned has its
// symbolic links resolved, so the file checked is the file started. It refuses a
// program in or under a directory of workspace, judged by where its links lead, and a
// program [trusted] refuses: the resolved file and every directory above it up to /,
// and every directory above each link on the way there, the PATH directory it was found
// in among them. Its refusals print the program, its paths and the PATH entry as they
// are: [Forager.Expand] prints them as the integration's key requires.
func program(name string, workspace []string) (string, error) {
	return find(name, workspace, "an integration", finding{
		relative: "set its absolute path with the `program` key",
		missing:  "set program to its path",
		resolved: true,
		raw:      true,
	})
}

// finding is how [find] words what it refuses for one kind of program: relative and
// missing end the lines of a program found through a relative PATH entry and of one the
// PATH does not hold, and resolved is whether a line names the program where it
// resolves, after its name, when that is another path. raw is whether a line prints the
// program, its paths, the PATH entry and the directories as they are, for a caller that
// hides them in its refusal, as [Forager.Expand] does; otherwise each prints as
// [exports.Value] prints it.
type finding struct {
	relative, missing string
	resolved, raw     bool
}

// shown is v as a line of f prints it: quoted when quote is true, and, unless f is raw,
// as [exports.Value] prints it.
func (f finding) shown(v string, quote bool) string {
	switch {
	case !f.raw:
		return exports.Value(v, quote)
	case quote:
		return strconv.Quote(v)
	}
	return v
}

// hidden is err, a refusal that prints a path, as a line of f prints it: as it is when
// f is raw, and otherwise with each word that contains an access key secret or a run key
// as [exports.Value] prints it. The result unwraps to err.
func (f finding) hidden(err error) error {
	if f.raw {
		return err
	}
	return exports.HideValueError(err, "")
}

// Program finds the program of an adapter or a tool, what names it, as [program] finds
// an integration's, and returns the path it checked, which is the path to run: the
// gateway runs it by that path, so the program it starts is the one checked, wherever
// the PATH or a link on the way to it leads later. A program the PATH does not find is
// refused. A refusal prints the program, each path and the PATH entry as
// [exports.Value] prints them.
func Program(name string, workspace []string, what string) (string, error) {
	return find(name, workspace, what, finding{
		relative: "give its absolute path",
		missing:  "give its absolute path",
	})
}

// find is [program] for a program what names, worded as f says.
func find(name string, workspace []string, what string, f finding) (string, error) {
	found, err := exec.LookPath(name)
	switch {
	case errors.Is(err, exec.ErrDot):
		return "", fmt.Errorf("%s is found as %s through the PATH entry %s, which is relative; qory runs a program from an absolute directory of the PATH; install it in one, or %s", f.shown(name, false), f.shown(dotted(found), false), f.shown(relativeEntry(found, name), true), f.relative)
	case err != nil && filepath.IsAbs(name):
		return "", fmt.Errorf("%s is not a program this user may run", f.shown(name, false))
	case err != nil:
		return "", fmt.Errorf("%s is not on the PATH; install it there, or %s", f.shown(name, false), f.missing)
	}
	if found, err = filepath.Abs(found); err != nil {
		return "", exports.HideError(err)
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", exports.HideError(err)
	}
	// The program by its name, and by where it resolves when that is another path.
	who := f.shown(name, false)
	if resolved != name && f.resolved {
		who += " is " + f.shown(resolved, false)
	}
	if err := outside(resolved, workspace, what); err != nil {
		return "", fmt.Errorf("%s, %w", who, f.hidden(err))
	}
	// Each link on the way is read from the directory it stands in, resolved, and a
	// relative target is taken from there.
	checked, links := []string{resolved}, []string(nil)
	for hop := found; ; {
		dir, err := filepath.EvalSymlinks(filepath.Dir(hop))
		if err != nil {
			return "", exports.HideError(err)
		}
		checked = append(checked, dir)
		hop = filepath.Join(dir, filepath.Base(hop))
		info, err := os.Lstat(hop)
		if err != nil {
			return "", exports.HideError(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}
		links = append(links, hop)
		if len(links) > maxLinks {
			from := ""
			if found != name {
				from = " from " + f.shown(found, false)
			}
			return "", fmt.Errorf("%s leads through more than %d links%s", f.shown(name, false), maxLinks, from)
		}
		target, err := os.Readlink(hop)
		if err != nil {
			return "", exports.HideError(err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		hop = target
	}
	if err := ownersOnly(checked, links); err != nil {
		return "", fmt.Errorf("%s, and %w", who, f.hidden(err))
	}
	return resolved, nil
}

// maxLinks is the most links a program is followed through, the most Linux follows. A
// test lowers it.
var maxLinks = 40

// dotted is a path LookPath found through a relative PATH entry, as a shell writes it.
func dotted(found string) string {
	if strings.ContainsRune(found, filepath.Separator) {
		return found
	}
	return "." + string(filepath.Separator) + found
}

// relativeEntry is the relative entry of the PATH that LookPath found name through, as
// found: "." for the empty entry, which means the working directory.
func relativeEntry(found, name string) string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		if !filepath.IsAbs(dir) && filepath.Join(dir, name) == found {
			return dir
		}
	}
	return filepath.Dir(found)
}

// relativeFile is the first setting, by name, whose name ends in _file and whose value
// is a relative path; "" for none.
func relativeFile(settings json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(settings, &m) != nil {
		return ""
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		v, ok := m[name].(string)
		if before, file := strings.CutSuffix(name, "_file"); file && before != "" && ok && v != "" && !filepath.IsAbs(v) {
			return name
		}
	}
	return ""
}

// outside refuses a path that is a file of workspace, or in or under a directory of it.
// The path and each directory above it are compared as files, [os.SameFile], so a name
// that differs only in case on a file system that ignores it is the same one.
func outside(path string, workspace []string, what string) error {
	for _, dir := range workspace {
		ws, err := os.Stat(dir)
		if dir == "" || err != nil {
			continue
		}
		for p := path; ; p = filepath.Dir(p) {
			if info, err := os.Stat(p); err == nil && os.SameFile(ws, info) {
				where := "inside " + dir + ", "
				if p == path {
					where = ""
				}
				return fmt.Errorf("%swhich a run may write; qory runs %s from outside the checkout, the container's read-write mounts and the places a walled run has used", where, what)
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return nil
}

// fileOwner is what [trusted] reads of a file: its owner, its group and its mode.
type fileOwner struct {
	UID, GID uint32
	Mode     os.FileMode
}

// names turns an owner and a group into the names [trusted] compares, "" for one the
// system does not name, and gives a user's primary group.
type names struct {
	User    func(uid uint32) string
	Group   func(gid uint32) string
	Primary func(uid uint32) (gid uint32, ok bool)
}

// stickyByRoot is a directory root owns with the sticky bit set, /tmp or /nix/store
// say: whoever may write it, a file in it only its owner may rename or remove.
func stickyByRoot(o fileOwner) bool {
	return o.Mode.IsDir() && o.UID == 0 && o.Mode&os.ModeSticky != 0
}

// trusted is the rule a program and every directory above it keep, so that only root
// and the user running qory may change what an integration runs. The owner is root or
// that user, euid. A directory root owns with the sticky bit set keeps the rule. Other
// users may write nothing else. Its group may write it when the group is root's, gid 0,
// wheel or admin, or the owner's own group: the owner's primary group, named as the
// owner is.
func trusted(path string, o fileOwner, euid uint32, n names) error {
	if err := ownedBy(path, "", o, euid, n); err != nil {
		return err
	}
	if stickyByRoot(o) {
		return nil
	}
	perm := o.Mode.Perm()
	if perm&0o002 != 0 {
		return fmt.Errorf("%s may be written by every user; qory runs a program only root and its owner may change", path)
	}
	if perm&0o020 != 0 {
		group := n.Group(o.GID)
		primary, ok := n.Primary(o.UID)
		own := group != "" && group == n.User(o.UID) && ok && primary == o.GID
		if o.GID != 0 && group != "wheel" && group != "admin" && !own {
			return fmt.Errorf("%s may be written by the group %s, which is neither root's, wheel, admin nor its owner's primary group named as the owner", path, named(group, o.GID))
		}
	}
	return nil
}

// ownedBy refuses a path, a link when what says so, that neither root nor the user
// running qory owns.
func ownedBy(path, what string, o fileOwner, euid uint32, n names) error {
	if o.UID != 0 && o.UID != euid {
		return fmt.Errorf("%s is %sowned by %s, neither root nor the user running qory", path, what, named(n.User(o.UID), o.UID))
	}
	return nil
}

// named is a user's or a group's name, with its id beside it, or the id alone.
func named(name string, id uint32) string {
	if name == "" {
		return strconv.FormatUint(uint64(id), 10)
	}
	return fmt.Sprintf("%s (%d)", name, id)
}

// chainTrusted holds path and every directory above it up to / to [trusted], each read
// by stat.
func chainTrusted(path string, stat func(string) (fileOwner, error), euid uint32, n names) error {
	for p := path; ; p = filepath.Dir(p) {
		o, err := stat(p)
		if err != nil {
			return err
		}
		if err := trusted(p, o, euid, n); err != nil {
			return err
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// rolesText is the roles a description lists, as a phrase: "the role a", or "the roles a,
// b and c".
func rolesText(roles []string) string {
	switch len(roles) {
	case 0:
		return "no role"
	case 1:
		return "the role " + roles[0]
	}
	return "the roles " + strings.Join(roles[:len(roles)-1], ", ") + " and " + roles[len(roles)-1]
}
