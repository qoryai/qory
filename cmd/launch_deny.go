package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/qoryai/forager/contracts"
)

// deniedVariables is Forager's deny list, the contract's denied-variables.json: the
// names, and the patterns in which * matches any run of characters, of the variables a
// run leaves out of the harness's defaults.
var deniedVariables = sync.OnceValues(func() (denied struct{ Names, Patterns []string }, err error) {
	b, err := fs.ReadFile(contracts.FS, "denied-variables.json")
	if err != nil {
		return denied, err
	}
	if err := json.Unmarshal(b, &denied); err != nil {
		return denied, fmt.Errorf("denied-variables.json: %w", err)
	}
	return denied, nil
})

// allowedDefaults is the harness's defaults, NAME=value, without the ones whose name the
// deny list holds, regardless of case: what a run takes of them.
func allowedDefaults(defaults []string) ([]string, error) {
	denied, err := deniedVariables()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, kv := range defaults {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if slices.ContainsFunc(denied.Names, func(n string) bool { return strings.ToUpper(n) == upper }) ||
			slices.ContainsFunc(denied.Patterns, func(p string) bool { return glob(strings.ToUpper(p), upper) }) {
			continue
		}
		out = append(out, kv)
	}
	return out, nil
}

// glob reports whether pattern, in which * matches any run of characters, the empty run
// included, matches the whole of name.
func glob(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(name, part)
		if i < 0 {
			return false
		}
		name = name[i+len(part):]
	}
	return len(name) >= len(last) && strings.HasSuffix(name, last)
}
