package pushcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
)

// LoadScope reads the scope checks of the home at home, by plan type, from
// [render.ScopeChecksFile]; nil when the home has no such file, as a harness whose modules
// declare no scope check. A file that cannot be read, or a check with no absolute path of
// a program, is an error, the cause alone.
func LoadScope(home string) (map[string]compose.ScopeCheck, error) {
	file := filepath.Join(home, render.ScopeChecksFile)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the scope checks in %s could not be read: %w", exports.Value(file, false), exports.HideError(err))
	}
	var checks render.ScopeChecks
	if err := json.Unmarshal(data, &checks); err != nil {
		return nil, fmt.Errorf("%s is not JSON in the form the compose writes: %w", exports.Value(file, false), err)
	}
	for typ, c := range checks.ScopeChecks {
		if len(c.Command) == 0 || !filepath.IsAbs(c.Command[0]) {
			return nil, fmt.Errorf("the scope check for %s in %s has no absolute path of a program", exports.Value(typ, true), exports.Value(file, false))
		}
	}
	return checks.ScopeChecks, nil
}
