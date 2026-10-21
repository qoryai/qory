package preparation

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/qoryai/forager/link"

	"github.com/qoryai/qory/internal/agentenv"
)

// proxyVariables are the proxy variables, in upper case: each is dropped in either case.
var proxyVariables = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY"}

// caVariables are the variables that point a program at authorities.
var caVariables = []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// Dropped reports whether name is one whose image value the agent of a dispatched
// walled run of Claude Code does not get, unless it has one of [agentenv.Prefixes] and
// holds the gateway's placeholder, see [AgentEnv]: a name [agentenv.Widens] reports, a
// proxy variable in upper or lower case, or a variable that points at authorities. The
// agent keeps the values Forager, the wall and the server set. Dropping them guards
// against the values the image's environment leaves in place, not against a program of
// the image or a command of the repository, which runs as the agent's user in the
// agent's container.
func Dropped(name string) bool {
	if agentenv.Widens(name) || slices.Contains(caVariables, name) {
		return true
	}
	upper := strings.ToUpper(name)
	return slices.Contains(proxyVariables, upper) && (name == upper || name == strings.ToLower(name))
}

// prefixed reports whether name begins with one of [agentenv.Prefixes].
func prefixed(name string) bool {
	return slices.ContainsFunc(agentenv.Prefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}

// The kept names' file holds one name a line: a name Forager sets for the run, and
// [keptIfCA] before a name the wall sets only when the run has an authority.
const keptIfCA = "ca "

// KeptNames is the file of the names a dispatched walled run keeps: always, the names
// Forager sets for the run, and ca, the names the wall sets when the run has an
// authority. Each list is sorted, and a name is written once.
func KeptNames(always, ca []string) []byte {
	var b bytes.Buffer
	for _, n := range sortedSet(always) {
		b.WriteString(n + "\n")
	}
	for _, n := range sortedSet(ca) {
		b.WriteString(keptIfCA + n + "\n")
	}
	return b.Bytes()
}

// sortedSet is names sorted, each once, without an empty one.
func sortedSet(names []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "" })
	slices.Sort(out)
	return slices.Compact(out)
}

// ReadKept reads the file [KeptNames] writes: the names kept always, and the ones kept
// when the run has an authority. A line that is no variable name is an error that names
// its number alone.
func ReadKept(data []byte) (always, ca map[string]bool, err error) {
	always, ca = map[string]bool{}, map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for i := 1; sc.Scan(); i++ {
		line := sc.Text()
		set := always
		if n, ok := strings.CutPrefix(line, keptIfCA); ok {
			line, set = n, ca
		}
		if !variableName(line) {
			return nil, nil, fmt.Errorf("line %d is not a variable name", i)
		}
		set[line] = true
	}
	return always, ca, sc.Err()
}

// variableName reports whether s is a variable name: a letter or an underscore, then
// letters, digits and underscores.
func variableName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// AgentEnv is env without each [Dropped] name that keep does not hold, except a name of
// [agentenv.Prefixes] whose value is exactly [link.Placeholder]: the gateway's
// placeholders are not among the names qory knows before the run. A name of
// [agentenv.Names], a proxy variable and a variable that points at authorities are
// dropped whatever their value, since a value without a slash still names a file to
// source or a library to load.
func AgentEnv(env []string, keep map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if Dropped(name) && !keep[name] && !(prefixed(name) && value == link.Placeholder) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Mounted reports whether mountinfo, the text of /proc/self/mountinfo, has a mount at
// point: its fifth field, with the octal escapes the kernel writes for a space, a tab,
// a newline and a backslash.
func Mounted(mountinfo []byte, point string) bool {
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	sc := bufio.NewScanner(bytes.NewReader(mountinfo))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 4 && unescape.Replace(f[4]) == point {
			return true
		}
	}
	return false
}

// readMountinfo is the text of /proc/self/mountinfo, "" with no /proc, as outside
// Linux; a test replaces it.
var readMountinfo = func() ([]byte, error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// KeepFor is the names the agent keeps, from the file [KeptNames] wrote: the ones kept
// always, and the authority's when bundle, the path the wall binds the run's bundle at,
// is a mount, which it is only when the run has an authority.
func KeepFor(data []byte, bundle string) (map[string]bool, error) {
	always, ca, err := ReadKept(data)
	if err != nil {
		return nil, err
	}
	if len(ca) == 0 {
		return always, nil
	}
	info, err := readMountinfo()
	if err != nil {
		return nil, err
	}
	if Mounted(info, bundle) {
		for n := range ca {
			always[n] = true
		}
	}
	return always, nil
}
