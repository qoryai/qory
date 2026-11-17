// Package testenv gives the tests of a package the same process environment, whatever the
// developer's shell holds.
//
// A TestMain function calls [StartFromFixedEnvironment] before m.Run. Only _test.go files
// import this package; product code never does.
package testenv

import (
	"os"
	"slices"
	"strings"
)

// FixedEnvironment is every environment variable that the tests start with, as set in the
// process that starts them. [StartFromFixedEnvironment] removes each variable that is not
// listed, so a token in a developer's shell reaches no session spec, no child process and
// no test output. GOWORK, GOPROXY and GOENV are not listed on purpose: the tests build
// against go.mod alone, with no workspace file and no private proxy.
var FixedEnvironment = []string{
	// The programs the tests start (git, sh, the fake runtimes) are looked up in PATH.
	"PATH",
	// HOME and TMPDIR are the paths that git and the temporary directories start from,
	// before emptyDir and t.TempDir set other paths.
	"HOME", "TMPDIR",
	// LANG and TERM select the language of git's messages and the result of the terminal
	// checks, as in the developer's shell.
	"LANG", "TERM",
	// GOCOVERDIR is where a test binary built with -cover writes its coverage data.
	"GOCOVERDIR",
	// QORY_TEST_DOCKER=1 switches on the tests that start containers, and
	// QORY_WALL_HELPER is the path of the Linux build of qory that TestRunBehindARealWall
	// mounts. Without them those tests skip.
	"QORY_TEST_DOCKER", "QORY_WALL_HELPER",
	// The docker command reads the address of its engine and its configuration from these.
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	// The go build in realWallImage reads the location of the toolchain, its caches and its
	// settings from these; the default under HOME no longer applies once emptyDir has moved
	// HOME.
	"GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN",
}

// StartFromFixedEnvironment removes every variable of the process's environment that
// [FixedEnvironment] does not list, so a session spec that qory run builds from the
// process's environment (SessionSpec.Env) contains only those and the variables a test sets.
func StartFromFixedEnvironment() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(FixedEnvironment, name) {
			os.Unsetenv(name)
		}
	}
}
