// Package suite runs the full suite of a run's repository outside the agent's session.
//
// A [Registry] clones a commit of the session's checkout into a new folder, runs the
// full suite's command on the clone through a [Runner], and keeps the state, the counts
// and the last part of the output of each suite it started. The command and the file it
// writes its result to come from the launch build plan, see [FullRow]; the counts come
// from that file, see [ReadResult] and [ParseJUnit].
package suite

import (
	"errors"
	"regexp"
	"strings"
)

// Full is the full row of the Commands table of a build plan: the command that runs the
// repository's whole test suite, and the file it writes the result to, relative to the top
// of the checkout; ResultFile is "" for a command that writes none.
type Full struct {
	Command    string
	ResultFile string
}

// The errors of [FullRow].
var (
	// ErrNoCommands is the error of a build plan with no table of commands.
	ErrNoCommands = errors.New("the build plan has no table of commands")
	// ErrNoFull is the error of a build plan whose commands have no row of kind full.
	ErrNoFull = errors.New("the commands of the build plan have no row of kind full")
	// ErrNoCommand is the error of a full row whose command is none or empty.
	ErrNoCommand = errors.New("the full row of the build plan has no command")
)

// commandColumns are the columns of the Commands table of a build plan.
var commandColumns = []string{"Kind", "Command", "Result file", "Source"}

// FullRow reads the full row of the table under the heading "## Commands" in text, a
// build plan as the harness writes it: a Markdown table with the columns Kind, Command,
// Result file and Source. A cell that is one code span is the text inside it, and any
// other cell has its backticks taken out; none and an empty cell are no command and no
// file. Only the first table of the section counts, and a line inside a code block is no
// table row.
func FullRow(text string) (Full, error) {
	rows, ok := commandRows(text)
	if !ok {
		return Full{}, ErrNoCommands
	}
	for _, row := range rows {
		if plainCell(row[0]) != "full" {
			continue
		}
		command := plainCell(row[1])
		if command == "" || command == "none" {
			return Full{}, ErrNoCommand
		}
		file := plainCell(row[2])
		if file == "none" {
			file = ""
		}
		return Full{Command: command, ResultFile: file}, nil
	}
	return Full{}, ErrNoFull
}

// commandRows returns the rows of the Commands table, with four cells each.
func commandRows(text string) ([][]string, bool) {
	var rows [][]string
	inSection, inCode, header, started := false, false, false, false
	var fence string
	for line := range strings.SplitSeq(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text), "\n") {
		if mark := fenceOf(line); mark != "" {
			switch {
			case !inCode:
				inCode, fence = true, mark
			case strings.HasPrefix(mark, fence[:1]) && len(mark) >= len(fence) && strings.TrimSpace(line) == mark:
				inCode = false
			}
			if started {
				break
			}
			continue
		}
		if inCode {
			continue
		}
		if level, title := heading(line); level > 0 {
			if started {
				break
			}
			inSection = level == 2 && title == "Commands"
			continue
		}
		if !inSection {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			if started {
				break
			}
			continue
		}
		cells := splitCells(line)
		switch {
		case !started:
			started = true
			if !equalCells(cells, commandColumns) {
				return nil, false
			}
			header = true
		case header:
			header = false
		default:
			for len(cells) < len(commandColumns) {
				cells = append(cells, "")
			}
			rows = append(rows, cells)
		}
	}
	return rows, started
}

var (
	fenceRe   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	headingRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:\s+(.*))?$`)
)

// fenceOf is the run of fence characters that starts a code block at the start of line,
// "" for a line that does not.
func fenceOf(line string) string {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// heading is the level and the title of a Markdown heading line, level 0 for any other.
func heading(line string) (int, string) {
	m := headingRe.FindStringSubmatch(line)
	if m == nil {
		return 0, ""
	}
	return len(m[1]), strings.TrimSpace(strings.TrimRight(m[2], "# "))
}

// splitCells is the cells of a table line, cut at each | that has no backslash before it.
func splitCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = strings.TrimSuffix(line, "|")
	}
	var cells []string
	from := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '|' && (i == 0 || line[i-1] != '\\') {
			cells = append(cells, strings.TrimSpace(line[from:i]))
			from = i + 1
		}
	}
	return append(cells, strings.TrimSpace(line[from:]))
}

// equalCells reports whether cells are the names, in order.
func equalCells(cells, names []string) bool {
	if len(cells) != len(names) {
		return false
	}
	for i := range cells {
		if cells[i] != names[i] {
			return false
		}
	}
	return true
}

// plainCell is the text of a cell: the inside of one code span, or the cell without its
// backticks.
func plainCell(cell string) string {
	cell = strings.TrimSpace(cell)
	if len(cell) > 1 && strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") && strings.Count(cell, "`") == 2 {
		return strings.TrimSpace(cell[1 : len(cell)-1])
	}
	return strings.TrimSpace(strings.ReplaceAll(cell, "`", ""))
}
