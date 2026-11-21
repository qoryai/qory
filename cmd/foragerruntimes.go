package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sync"

	"github.com/qoryai/forager/contracts"
	integrations "github.com/qoryai/integrations/contracts"
)

// foragerRuntime is one runtime of Forager's runtime list: its name and its fixed
// variables, the values Forager sets in every launch of it, by name. Only these two
// members are read, by exact case.
type foragerRuntime struct {
	Name  string            `json:"name,case:strict"`
	Fixed map[string]string `json:"fixed,case:strict"`
}

// foragerList is Forager's runtime list, the contract's runtimes.json, in its order:
// the runtimes Forager ships a descriptor for.
var foragerList = sync.OnceValues(func() ([]foragerRuntime, error) {
	b, err := fs.ReadFile(contracts.FS, "runtimes.json")
	if err != nil {
		return nil, err
	}
	return readForagerList(b)
})

// readForagerList reads b, a runtimes.json, as [foragerList] has it. A member of a
// runtime object that [foragerRuntime] does not define is not read. A runtime object
// whose must_understand lists such a member, or is not a list of JSON Pointers, is an
// error, which contains the runtime's place in the list.
func readForagerList(b []byte) ([]foragerRuntime, error) {
	var list struct {
		Runtimes []json.RawMessage `json:"runtimes,case:strict"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("runtimes.json: %w", err)
	}
	out := make([]foragerRuntime, 0, len(list.Runtimes))
	for i, raw := range list.Runtimes {
		if err := integrations.CheckMustUnderstand(raw, foragerRuntime{}); err != nil {
			return nil, fmt.Errorf("runtimes.json: runtime %d: %w", i+1, err)
		}
		var rt foragerRuntime
		if err := json.Unmarshal(raw, &rt); err != nil {
			return nil, fmt.Errorf("runtimes.json: runtime %d: %w", i+1, err)
		}
		out = append(out, rt)
	}
	return out, nil
}

// foragerFixed is the fixed variables of the runtime name in Forager's runtime list,
// NAME=value sorted by name; none for a runtime that has none or is not in the list.
func foragerFixed(name string) ([]string, error) {
	list, err := foragerList()
	if err != nil {
		return nil, err
	}
	for _, rt := range list {
		if rt.Name == name {
			var out []string
			for _, n := range slices.Sorted(maps.Keys(rt.Fixed)) {
				out = append(out, n+"="+rt.Fixed[n])
			}
			return out, nil
		}
	}
	return nil, nil
}
