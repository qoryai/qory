package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sync"

	"github.com/qoryai/forager/contracts"
)

// foragerRuntimes is the names in Forager's runtime list, the contract's runtimes.json,
// in its order: the runtimes Forager ships a descriptor for.
var foragerRuntimes = sync.OnceValues(func() ([]string, error) {
	b, err := fs.ReadFile(contracts.FS, "runtimes.json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Runtimes []struct {
			Name string `json:"name"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("runtimes.json: %w", err)
	}
	names := make([]string, len(list.Runtimes))
	for i, rt := range list.Runtimes {
		names[i] = rt.Name
	}
	return names, nil
})
