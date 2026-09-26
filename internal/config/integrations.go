package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/qoryai/runner/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/integration"
)

// integrationKey is the grammar of an integration's key, the integration contract's
// name: the runner's credential name without the dot, which a program's name, qory-<key>,
// would read as an extension.
var integrationKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ProgramPrefix is what an integration's key follows in the name of its program when
// the entry sets none: qory-github for github, looked up on the PATH.
const ProgramPrefix = "qory-"

// RunnerIntegration is one entry of the integrations section: a program that speaks
// the integration contract, and the settings every role of it is started with.
type RunnerIntegration struct {
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
	// set by [Runner.Expand].
	Path, Version string
}

// readIntegrations reads the integrations section, a mapping from a key to an entry,
// in the file's order. The settings are written out as JSON here, and read against the
// program's description by [Runner.Expand], which runs the program.
func readIntegrations(path string, node *yaml.Node) ([]RunnerIntegration, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: integrations is a mapping from a name to an integration", path)
	}
	var out []RunnerIntegration
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !integrationKey.MatchString(key) {
			return nil, fmt.Errorf("%s: integrations: %q is not 1 to 64 of a-z, 0-9, underscore and dash, starting with a letter or a digit", path, key)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: integrations.%s is declared twice", path, key)
		}
		seen[key] = true
		in := RunnerIntegration{Key: key, Program: ProgramPrefix + key, Settings: []byte("{}")}
		entry := node.Content[i+1]
		if entry.ShortTag() == "!!null" {
			out = append(out, in)
			continue
		}
		if entry.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: integrations.%s is a mapping: program and settings", path, key)
		}
		for k := 0; k+1 < len(entry.Content); k += 2 {
			value := entry.Content[k+1]
			switch name := entry.Content[k].Value; name {
			case "program":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || value.Value == "" {
					return nil, fmt.Errorf("%s: integrations.%s.program is a path or a name on the PATH", path, key)
				}
				if strings.ContainsRune(value.Value, '/') && !filepath.IsAbs(value.Value) {
					return nil, fmt.Errorf("%s: integrations.%s.program %q is not an absolute path; program is an absolute path or a name on the PATH", path, key, value.Value)
				}
				in.Program = value.Value
			case "settings":
				if value.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("%s: integrations.%s.settings is a mapping, the settings document", path, key)
				}
				var b bytes.Buffer
				if err := writeJSON(&b, value, "settings"); err != nil {
					return nil, fmt.Errorf("%s: integrations.%s.%w", path, key, err)
				}
				in.Settings = b.Bytes()
			default:
				return nil, fmt.Errorf("%s: integrations.%s: key %q is not one", path, key, name)
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// writeJSON writes the YAML under n as compact JSON, a mapping's keys in the file's
// order. at is where n stands, for an error, which names the place and never the value.
// An alias is refused: the settings go on a command line, whole, and a few chained
// aliases expand to more than one can hold.
func writeJSON(b *bytes.Buffer, n *yaml.Node, at string) error {
	switch n.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("%s: line %d: *%s is a YAML alias, which the settings may not contain; write the value out in full", at, n.Line, n.Value)
	case yaml.MappingNode:
		b.WriteByte('{')
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode || k.ShortTag() == "!!merge" {
				return fmt.Errorf("%s: line %d: a YAML alias or merge, which the settings may not contain; write the value out in full", at, k.Line)
			}
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return fmt.Errorf("%s: line %d: a key that is not a string", at, k.Line)
			}
			if seen[k.Value] {
				return fmt.Errorf("%s.%s appears twice", at, k.Value)
			}
			seen[k.Value] = true
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k.Value)
			b.WriteByte(':')
			if err := writeJSON(b, n.Content[i+1], at+"."+k.Value); err != nil {
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
				return fmt.Errorf("%s is not a number or a boolean JSON can contain", at)
			}
			if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
				return fmt.Errorf("%s is not a number JSON can contain", at)
			}
			out, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("%s is not a value JSON can contain", at)
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

// Expansion selects which integrations [Runner.Expand] describes, and where their
// programs may not be.
type Expansion struct {
	// Workspace are the files and directories a run may write: its checkout, and the
	// mounts a wall passes to the container read-write. A program that is one of them, or
	// under one, is refused: what a run may change may not choose what runs with the
	// machine's credentials.
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
// integration that plays no role qory knows are errors that contain the file and the
// key. The credential role defines the credential whose name is the key, with the
// adapter [integration.CredentialAdapter] returns, unless the file's credentials section
// defines that name itself: then the file's definition stands, [Runner.Shadowed] lists
// the key, and the integration's credential is not defined. A role qory does not know is
// left alone. Once Expand succeeds, each further call does nothing; after an error, the
// next call describes again.
func (r *Runner) Expand(ctx context.Context, e Expansion) error {
	if r == nil || r.expanded {
		return nil
	}
	own := map[string]bool{}
	for _, c := range r.Credentials {
		own[c.Name] = true
	}
	var defined []RunnerCredential
	for i := range r.Integrations {
		in := &r.Integrations[i]
		if e.Only != nil && !e.Only(in.Key) {
			continue
		}
		fail := func(format string, a ...any) error {
			return fmt.Errorf("%s: integrations.%s: %s", r.File, in.Key, fmt.Sprintf(format, a...))
		}
		found, err := program(in.Program, e.Workspace)
		if err != nil {
			return fail("%v", err)
		}
		d, err := integration.Describe(ctx, found)
		if err != nil {
			return fail("%v", err)
		}
		if err := d.CheckSettings(in.Settings); err != nil {
			return fail("%v", err)
		}
		if d.Credential == nil {
			return fail("%s plays no role qory knows, %s, and defines nothing", in.Program, strings.Join(d.Roles, ", "))
		}
		// The version is the program's to word, and is printed as a terminal takes it.
		in.Path, in.Version = found, integration.Printable(d.ProgramVersion)
		if own[in.Key] {
			continue
		}
		c := RunnerCredential{Name: in.Key, Adapter: integration.CredentialAdapter(found, in.Settings), Argument: d.Credential.Argument, Hosts: d.Credential.Hosts, Integration: in.Key}
		if err := (session.Credential{Name: c.Name, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts}).Check(); err != nil {
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
func (r *Runner) Shadowed() []string {
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
// in among them.
func program(name string, workspace []string) (string, error) {
	found, err := exec.LookPath(name)
	switch {
	case errors.Is(err, exec.ErrDot):
		return "", fmt.Errorf("%s is found as %s through the PATH entry %q, which is relative; qory runs a program from an absolute directory of the PATH, or from the absolute path program sets", name, dotted(found), relativeEntry(found, name))
	case err != nil && filepath.IsAbs(name):
		return "", fmt.Errorf("%s is not a program this user may run", name)
	case err != nil:
		return "", fmt.Errorf("%s is not on the PATH; install it there, or set program to its path", name)
	}
	if found, err = filepath.Abs(found); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", err
	}
	// The program by its name, and by where it resolves when that is another path.
	who := name
	if resolved != name {
		who = name + " is " + resolved
	}
	if err := outside(resolved, workspace); err != nil {
		return "", fmt.Errorf("%s, %w", who, err)
	}
	// Each link on the way is read from the directory it stands in, resolved, and a
	// relative target is taken from there.
	checked, links := []string{resolved}, []string(nil)
	for hop := found; ; {
		dir, err := filepath.EvalSymlinks(filepath.Dir(hop))
		if err != nil {
			return "", err
		}
		checked = append(checked, dir)
		hop = filepath.Join(dir, filepath.Base(hop))
		info, err := os.Lstat(hop)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}
		links = append(links, hop)
		if len(links) > maxLinks {
			from := ""
			if found != name {
				from = " from " + found
			}
			return "", fmt.Errorf("%s leads through more than %d links%s", name, maxLinks, from)
		}
		target, err := os.Readlink(hop)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		hop = target
	}
	if err := ownersOnly(checked, links); err != nil {
		return "", fmt.Errorf("%s, and %w", who, err)
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

// outside refuses a path that is a file of workspace, or in or under a directory of it.
// The path and each directory above it are compared as files, [os.SameFile], so a name
// that differs only in case on a file system that ignores it is the same one.
func outside(path string, workspace []string) error {
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
				return fmt.Errorf("%swhich a run may write; qory runs an integration from outside the checkout and the container's read-write mounts", where)
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
			return fmt.Errorf("%s may be written by the group %s, which is neither root's, wheel, admin nor its owner's own", path, named(group, o.GID))
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
