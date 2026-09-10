// Package cmd is the command tree of the qory binary: one command per noun, one
// subcommand per verb.
//
// [Root] builds the tree and [Execute] runs it. The main package owns only the process: it
// sets [Example] from the files embedded in the binary, calls Execute, and turns an error
// into an exit status.
//
// The tree:
//
//	qory version           the version, and the harness format this build reads
//	qory harness init      write the hello example into the current directory
//	qory harness compose   compose the stack's modules into the checkout
//	qory harness inspect   print the report of the composed harness
//	qory harness remove    remove the composed harness and its links, or one runtime's
//
// Each harness verb has a one-letter alias under the noun, and a hidden two-letter
// shortcut at the top level for typing at a prompt many times a day: hc, hi and hr. Both
// names reach the same constructor, so a flag added once appears under every name.
//
// A command in this package does four things and nothing else. It finds where it stands
// with locate, it calls the packages that do the work, it prints through the ui package,
// and it returns an error. It holds no knowledge of modules, entries or runtimes; that
// lives in the stack, compose and render packages. The set of runtimes a build can
// render for is decided here, by the blank imports at the top of harness.go: a runtime
// package registers itself in its own init, so importing it is what makes its name valid
// for target.runtime and for the --runtime flag.
//
// Errors reach the person in one of two ways. A plain error travels up to the main
// package, which prints it under the mark. An error a command has already printed itself,
// a collision with its suggested fix being the one case, is returned wrapped so that it
// matches [ErrReported]: main recognises it and prints nothing further. Either way main
// exits with [ExitCode]: 2 for a mistake in the input, 3 for a collision, 4 for a path
// qory would not replace, 1 for anything else.
package cmd
