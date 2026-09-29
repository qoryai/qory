// Package image checks an agent's image against what the runner's contract says an image
// provides, contracts/runner/v1/README.md §Images of github.com/qoryai/runner: it runs as
// any user the machine gives it, with HOME a place that user may write; it keeps its
// authorities in a bundle where the wall reads them; it holds the runtime and the programs
// an agent delivers its work with; and it needs no setuid program. An image that holds
// dockerd in a system directory can carry a Docker of the agent's own.
//
// A check has two sides. From outside, [Engine.Check] reads the image as the engine holds
// it, with docker image inspect, and then runs the probe in a container started the way
// the wall starts an agent: as a user with no name in the image, with no capability, no
// way to gain one and no network, and qory's Linux build mounted where the wall mounts its
// helper, as the entry point. From inside, [Probe] is what that build runs, as the hidden
// verb qory image probe: it checks the image's files and programs and prints a [Report],
// one JSON document, which the outside reads back. The two are the same release of qory,
// so the report's format is not a contract.
//
// Every result is a [Check], one line: what passed, what failed and how to mend it, or a
// fact that neither passes nor fails. A caller prints them in order and fails when
// [Failed] reports one. A check never changes the image, and a probe leaves nothing in it:
// the file it writes in HOME is removed, and the container with it.
//
// A [Probe] reads the files under its Root and runs programs through its Exec, so a test
// runs it on a directory of its own; an [Engine] runs the docker command through its Run,
// so a test stands in for the engine.
package image
