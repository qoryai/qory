package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sync"

	"github.com/qoryai/forager/contracts"
)

// foragerRuntime is one runtime of Forager's runtime list: its name and its fixed
// variables, the values Forager sets in every launch of it, by name.
type foragerRuntime struct {
	Name  string            `json:"name"`
	Fixed map[string]string `json:"fixed"`
}

// foragerList is Forager's runtime list, the contract's runtimes.json, in its order:
// the runtimes Forager ships a descriptor for.
var foragerList = sync.OnceValues(func() ([]foragerRuntime, error) {
	b, err := fs.ReadFile(contracts.FS, "runtimes.json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Runtimes []foragerRuntime `json:"runtimes"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("runtimes.json: %w", err)
	}
	return list.Runtimes, nil
})

// foragerRuntimes is the names in Forager's runtime list, in its order.
func foragerRuntimes() ([]string, error) {
	list, err := foragerList()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(list))
	for i, rt := range list {
		names[i] = rt.Name
	}
	return names, nil
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
