package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// UI writes styled text to one writer, and decides once, in [New], whether that writer
// gets colour. It holds no buffer: every method writes through as it is called, and none
// of them reports a write error.
type UI struct {
	w       io.Writer
	heading lipgloss.Style // a section title and the name in a title line: bold green
	key     lipgloss.Style // a field key and the rest of a title line: faint
	name    lipgloss.Style // the first column of a table and a line of code: bold
	ok      lipgloss.Style // the check mark of a success line: bold green
	bad     lipgloss.Style // the cross of a failure line: bold red
	yellow  lipgloss.Style // one stripe of a box's frame: the bee's yellow
	black   lipgloss.Style // the other stripe: the bee's black
	columns int            // the width of w when it is a terminal, else 0
}

// Green is the brand colour, used for marks and headings, never for running text. A
// program that sets it must do so before it builds its first UI, because [New] resolves
// the styles once.
var Green = lipgloss.Color("#2FD174")

// Yellow and Black are the bee's stripes, which frame a [UI.Box]. The yellow is a fixed
// shade. The black is the terminal's bright black, the eighth colour of its palette,
// which every theme keeps visible against its own background, dark grey on a dark
// ground and near black on a light one. Neither is an adaptive colour, because those
// make lipgloss ask the terminal for its background and wait for the answer, which a
// terminal that answers late or not at all turns into stray characters or a pause.
var (
	Yellow = lipgloss.Color("#FFD21E")
	Black  = lipgloss.Color("8")
)

// New returns a UI that writes to w. It asks lipgloss what w supports, so the UI colours
// its output when w is a terminal and NO_COLOR is unset, and writes the same text plain
// otherwise. The decision is made here, once per UI, and not at each call.
func New(w io.Writer) *UI {
	r := lipgloss.NewRenderer(w)
	columns := 0
	if f, ok := w.(*os.File); ok {
		if cols, _, err := term.GetSize(f.Fd()); err == nil {
			columns = cols
		}
	}
	return &UI{
		w:       w,
		columns: columns,
		heading: r.NewStyle().Bold(true).Foreground(Green),
		key:     r.NewStyle().Faint(true),
		name:    r.NewStyle().Bold(true),
		ok:      r.NewStyle().Foreground(Green).Bold(true),
		bad:     r.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		yellow:  r.NewStyle().Foreground(Yellow),
		black:   r.NewStyle().Foreground(Black),
	}
}

const (
	// Mark is Qory's mark, the bee that does the work. Every title starts with it, so it is
	// the first character of a command's output.
	Mark = "🐝"
	// Jar is the jar that waits for the bee. Nothing in this tool prints it yet.
	Jar = "🫙"
	// Pot is the work done. A command ends a success line with it, after the numbers.
	Pot = "🍯"
)

// Success prints a line that starts with a check mark, then format with args applied to
// it, as [fmt.Printf] would.
func (u *UI) Success(format string, args ...any) {
	fmt.Fprintf(u.w, "%s %s\n", u.ok.Render("✓"), fmt.Sprintf(format, args...))
}

// Fail prints err on a line that starts with a cross. A multi-line error keeps its lines
// aligned under the first, indented by the width of the cross and its space. Fail prints
// only what err.Error() returns and does not unwrap it.
func (u *UI) Fail(err error) {
	lines := strings.Split(err.Error(), "\n")
	fmt.Fprintf(u.w, "%s %s\n", u.bad.Render("✗"), lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(u.w, "  %s\n", line)
	}
}

// Heading prints text as a section title, in the brand colour and bold, at the left
// margin that [UI.Fields] and [UI.Table] indent from.
func (u *UI) Heading(text string) {
	fmt.Fprintln(u.w, u.heading.Render(text))
}

// marked records that a title carrying [Mark] has been printed. [UI.Title] sets it and
// [Marked] reads it. It is package-level and not per UI, because the process prints one
// title however many writers it opens, and it is never cleared.
var marked bool

// Marked reports whether a title with the mark has been printed in this process. A caller
// that prints an error outside a command uses it to decide whether the output still needs
// a title; see [UI.Title].
func Marked() bool { return marked }

// Title prints the first line of every command's output: the mark, then name in the brand
// colour, then rest faint after a middle dot, its parts separated by spaces. Empty parts
// of rest are skipped, and the dot is left out when nothing is left. Title also records
// that the mark has been printed, which [Marked] reports for the rest of the process.
func (u *UI) Title(name string, rest ...string) {
	marked = true
	line := Mark + " " + u.heading.Render(name)
	var parts []string
	for _, r := range rest {
		if r != "" {
			parts = append(parts, r)
		}
	}
	if len(parts) > 0 {
		line += " " + u.key.Render("· "+strings.Join(parts, " "))
	}
	fmt.Fprintln(u.w, line)
}

// Fields prints one indented row per pair, the key faint and padded to the width of the
// widest key, then the value. The width is counted in bytes, so a key outside ASCII pads
// short. The rows keep the order given, and a key may repeat.
func (u *UI) Fields(rows [][2]string) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r[0]))
	}
	for _, r := range rows {
		fmt.Fprintf(u.w, "  %s  %s\n", u.key.Render(pad(r[0], width)), r[1])
	}
}

// Table prints indented rows with every column padded to the widest cell in it, two
// spaces between columns and no trailing space. The first column is bold. Widths are the
// display width of a cell, counting a wide rune as two columns, so a cell holding an icon
// still lines up. Rows may differ in length; a short row simply ends early.
func (u *UI) Table(rows [][]string) {
	var widths []int
	for _, r := range rows {
		for i, cell := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	for _, r := range rows {
		var cells []string
		for i, cell := range r {
			padded := cell + strings.Repeat(" ", widths[i]-lipgloss.Width(cell))
			if i == 0 {
				padded = u.name.Render(padded)
			}
			cells = append(cells, padded)
		}
		fmt.Fprintf(u.w, "  %s\n", strings.TrimRight(strings.Join(cells, "  "), " "))
	}
}

// Blank prints an empty line.
func (u *UI) Blank() { fmt.Fprintln(u.w) }

// Text prints one indented line of prose per argument. It neither wraps nor re-flows a
// line, so a caller that wants a break passes another argument.
func (u *UI) Text(lines ...string) {
	for _, l := range lines {
		fmt.Fprintf(u.w, "  %s\n", l)
	}
}

// Code prints one line per argument, bold and indented one step further than [UI.Text],
// as a block the reader can paste. Each line is printed as given, so the caller carries
// any indentation inside the block in the string.
func (u *UI) Code(lines ...string) {
	for _, l := range lines {
		fmt.Fprintf(u.w, "    %s\n", u.name.Render(l))
	}
}

// Box prints lines inside a double-line frame striped like the bee, yellow and black in
// turn all the way round, each line centred on the widest, one blank line of padding
// above and below them and three columns at each side, the whole indented one step and
// set off by a blank line before and after. It is the shape of a notice that has to be
// seen among a command's output, such as that a newer release is available. A line may
// carry styling from [UI.Strong], [UI.Brand] or [UI.Alert]; the frame is measured on the
// text and not the escape codes.
//
// On a terminal too narrow for the frame the lines are printed without it, indented and
// set off by blank lines, since a frame the terminal wraps is worse than none.
func (u *UI) Box(lines ...string) {
	const pad = 3
	inner := 0
	for _, l := range lines {
		inner = max(inner, lipgloss.Width(l))
	}
	width := inner + 2*pad // cells between the corners
	rows := len(lines) + 2 // the lines, and the blank row above and below them
	if u.columns > 0 && width+4 > u.columns {
		fmt.Fprintln(u.w)
		u.Text(lines...)
		fmt.Fprintln(u.w)
		return
	}

	// The stripes run round the frame from the top left corner: along the top, down the
	// right side, back along the bottom and up the left side. A cell of a horizontal
	// edge is one step and a row of a vertical edge two, since a row is about twice as
	// tall as a cell is wide, so the stripes look alike on every edge.
	var cells []string // every cell of the frame, in that order
	var steps []int    // where along the frame each cell starts
	step := 0
	add := func(cell string, size int) {
		cells = append(cells, cell)
		steps = append(steps, step)
		step += size
	}
	add("╔", 1)
	for range width {
		add("═", 1)
	}
	add("╗", 1)
	for range rows {
		add("║", 2)
	}
	add("╝", 1)
	for range width {
		add("═", 1)
	}
	add("╚", 1)
	for range rows {
		add("║", 2)
	}
	top := u.edge(cells[:width+2], steps[:width+2])
	right := u.each(cells[width+2:width+2+rows], steps[width+2:width+2+rows])
	bottomCells := slices.Clone(cells[width+2+rows : 2*width+4+rows])
	bottomSteps := slices.Clone(steps[width+2+rows : 2*width+4+rows])
	slices.Reverse(bottomCells)
	slices.Reverse(bottomSteps)
	bottom := u.edge(bottomCells, bottomSteps)
	left := u.each(cells[2*width+4+rows:], steps[2*width+4+rows:])
	slices.Reverse(left)

	fmt.Fprintln(u.w)
	fmt.Fprintf(u.w, "  %s\n", top)
	for r := range rows {
		text := ""
		if r > 0 && r <= len(lines) {
			text = lines[r-1]
		}
		w := lipgloss.Width(text)
		before := (width - w) / 2
		fmt.Fprintf(u.w, "  %s%s%s%s%s\n", left[r], strings.Repeat(" ", before), text, strings.Repeat(" ", width-w-before), right[r])
	}
	fmt.Fprintf(u.w, "  %s\n", bottom)
	fmt.Fprintln(u.w)
}

// stripeLength is how many steps along a frame each stripe of a [UI.Box] runs.
const stripeLength = 4

// stripe is the style of the cell that starts at step along the frame: yellow for the
// first stripe, black for the second, and so on by turns.
func (u *UI) stripe(step int) lipgloss.Style {
	if (step/stripeLength)%2 == 1 {
		return u.black
	}
	return u.yellow
}

// edge paints the cells of one horizontal edge, given where along the frame each starts,
// and joins them. Cells of one stripe that follow each other are rendered as one run, so
// the edge carries as few escape codes as its stripes need.
func (u *UI) edge(cells []string, steps []int) string {
	var out strings.Builder
	for i := 0; i < len(cells); {
		j := i
		var run strings.Builder
		for j < len(cells) && steps[j]/stripeLength == steps[i]/stripeLength {
			run.WriteString(cells[j])
			j++
		}
		out.WriteString(u.stripe(steps[i]).Render(run.String()))
		i = j
	}
	return out.String()
}

// each paints the cells of one vertical edge one by one, since each is its own row.
func (u *UI) each(cells []string, steps []int) []string {
	painted := make([]string, len(cells))
	for i, cell := range cells {
		painted[i] = u.stripe(steps[i]).Render(cell)
	}
	return painted
}

// Strong returns s bold, for the one thing in a line to act on, such as a command to run.
func (u *UI) Strong(s string) string { return u.name.Render(s) }

// Brand returns s in the brand colour, bold.
func (u *UI) Brand(s string) string { return u.ok.Render(s) }

// Alert returns s in red, bold, for what is behind or wrong.
func (u *UI) Alert(s string) string { return u.bad.Render(s) }

func pad(s string, width int) string {
	return s + strings.Repeat(" ", width-len(s))
}

// Short shortens path for reading. It returns path relative to base when path is under
// base, "." when the two are the same, and path with the home directory replaced by "~"
// when it is under the home directory instead. Otherwise it returns path unchanged. An
// empty base skips the first step, which is how a caller shortens a path that belongs to
// no checkout.
func Short(path, base string) string {
	if base != "" {
		if rel, err := filepath.Rel(base, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
