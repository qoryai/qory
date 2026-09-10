// Package ui prints everything a person reads on the terminal, in one vocabulary.
//
// A [UI] writes to one writer. Colour is used when that writer is a terminal and NO_COLOR
// is unset, and the same text is written plain everywhere else, so a run into a file or a
// pipe differs from a run on a terminal only in the escape codes. [Green] is the brand
// colour, used for the mark and for headings, never for running text.
//
// Every command opens its output with [UI.Title] and ends it with [UI.Success] or with
// [UI.Fail]. In between it prints [UI.Fields] for named values, [UI.Table] for rows,
// [UI.Heading] for a section, [UI.Text] for prose, [UI.Code] for a block to paste, and
// [UI.Blank] for an empty line. [Short] shortens a path against the checkout so a field
// reads as a path inside it.
//
//	u := ui.New(cmd.OutOrStdout())
//	u.Title("acme/app", "claude sonnet")
//	u.Success("composed %d entries from %d layers %s", 12, 3, ui.Pot)
//	u.Fields([][2]string{{"home", ui.Short(home, root)}, {"link", ".claude"}})
//
// prints the shape every command's output has, a title line, a result, then fields:
//
//	🐝 acme/app · claude sonnet
//	✓ composed 12 entries from 3 layers 🍯
//	  home  .qory/harness
//	  link  .claude
//
// [UI.Title] also sets a package-level flag, which [Marked] reports. The main package
// reads [Marked] before it prints an error that reached it from a command which failed
// before printing its own title, and prints a bare title first, so that the mark opens
// the output of every run whatever failed and however early.
//
// That flag and [Green] are the package's only mutable state. Neither is guarded, so
// printing from more than one goroutine, or setting [Green] while another goroutine
// prints, is not safe.
package ui
