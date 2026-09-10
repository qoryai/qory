// Package checkout locates the git checkout a command works on and names it.
//
// [Root] finds the checkout, [QoryDir] the [Dir] qory keeps inside it, [ExcludeFile] the
// clone-local file that hides that directory from git, and [RepoKey] the owner/name of
// its origin remote:
//
//	root, err := checkout.Root(cwd)
//	dir := checkout.QoryDir(root)   // <root>/.qory
//	name := checkout.RepoKey(root)  // acme/app
//
// Every function runs git and none of them treats a missing git or a directory outside a
// working tree as an error. They fall back instead: [Root] to the directory itself,
// [RepoKey] to its base name, [ExcludeFile] to "". A caller reads a checkout it cannot
// name, and writes no exclude, rather than refusing to run.
package checkout
