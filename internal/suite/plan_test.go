package suite_test

import (
	"errors"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

// plan is a build plan with the given Commands table, with a table of deliverables before it
// that has a row of the kind full too: only the Commands table counts.
func plan(commands string) string {
	return "# Build plan\n\n## Summary\n\nOne change.\n\n## Deliverables\n\n" +
		"| Id | Change | Criteria | Files | Check |\n|---|---|---|---|---|\n| d-1 | full | AC-1 | `a.go` | `a_test.go` |\n\n" +
		"## Commands\n\n" + commands
}

const table = "| Kind | Command | Result file | Source |\n|---|---|---|---|\n"

func TestFullRowReadsTheCommandAndTheResultFile(t *testing.T) {
	for _, c := range []struct {
		name, commands string
		want           suite.Full
	}{
		{"code spans", table + "| targeted | `go test ./a` | `r/t.xml` | x |\n| full | `go test ./... -v` | `reports/junit.xml` | repo |\n",
			suite.Full{Command: "go test ./... -v", ResultFile: "reports/junit.xml"}},
		{"plain cells", table + "| full | make test | out.xml | x |\n", suite.Full{Command: "make test", ResultFile: "out.xml"}},
		{"no result file", table + "| full | `make test` | none | x |\n", suite.Full{Command: "make test"}},
		{"escaped pipe stays", table + "| full | `a \\| b` | `r.xml` | x |\n", suite.Full{Command: `a \| b`, ResultFile: "r.xml"}},
		{"a code block before the table", "```\n| Kind | Command | Result file | Source |\n| full | `bad` | | |\n```\n\n" + table + "| full | `good` | `r.xml` | x |\n",
			suite.Full{Command: "good", ResultFile: "r.xml"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := suite.FullRow(plan(c.commands))
			if err != nil || got != c.want {
				t.Errorf("FullRow = %+v, %v, want %+v", got, err, c.want)
			}
		})
	}
}

func TestFullRowRefusesAPlanWithNoUsableRow(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       error
	}{
		{"no section", "# Build plan\n\n## Summary\n\nx\n", suite.ErrNoCommands},
		{"other columns", plan("| Kind | Command |\n|---|---|\n| full | `x` |\n"), suite.ErrNoCommands},
		{"no full row", plan(table + "| targeted | `x` | `r.xml` | s |\n"), suite.ErrNoFull},
		{"command none", plan(table + "| full | none | none | no suite |\n"), suite.ErrNoCommand},
		{"command empty", plan(table + "| full | | `r.xml` | s |\n"), suite.ErrNoCommand},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := suite.FullRow(c.text); !errors.Is(err, c.want) {
				t.Errorf("FullRow error = %v, want %v", err, c.want)
			}
		})
	}
}
