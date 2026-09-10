// Package report is the JSON record of one compose: every module with its pin, every entry
// with its module.
//
// [New] builds a [Report] from a [compose.Result], [Write] stores it as JSON,
// [Read] loads a stored one and [Report.Print] renders it for a person, which is what
// qory harness inspect prints. The compose writes it to .qory/harness-report.json, beside
// the composed home:
//
//	path := filepath.Join(checkout, ".qory", "harness-report.json")
//	rep := report.New(res, name, checkout, home)
//	if err := report.Write(path, rep); err != nil {
//		return err
//	}
//
// # The schema
//
// [Version] is the format version, and a stored report carries it as its version field.
// Version 1 holds the fields of Report, each under the JSON name in its struct tag. Within
// a version a field keeps its name and its meaning, and a reader may rely on that. A later
// version may add fields, so a reader ignores a field it does not know and refuses a version
// it does not read. [Read] does neither: it decodes any JSON carrying these names, and a
// caller that cares compares the version itself.
//
// A reader may also rely on the shape. Modules are in stack order, entries are sorted by
// kind then name, and excludes are in module order. Modules, Entries and Excludes are always
// arrays, empty rather than absent. Every other field marked omitempty is absent when unset,
// so a reader treats absent as the zero value. The file, checkout and home fields are
// absolute paths on the machine that composed; a module's source is the stack's own text,
// which for a path source is relative to the stack file.
//
// The report records what was composed, not what a runtime reads. It names no rendered file
// and holds no merged settings.
package report
