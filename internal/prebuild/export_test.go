package prebuild

import (
	"testing"
	"time"
)

// ShortenGrace sets [grace] and [cleanWait] to d for the test.
func ShortenGrace(t *testing.T, d time.Duration) {
	oldGrace, oldClean := grace, cleanWait
	grace, cleanWait = d, 10*d
	t.Cleanup(func() { grace, cleanWait = oldGrace, oldClean })
}

// ToolsDockerfile is the Dockerfile of the tools step for the wall user user, uid:gid,
// which adds node from the context when copyNode is set.
func ToolsDockerfile(base, kind, user string, copyNode bool) string {
	u, err := parseUser(user)
	if err != nil {
		panic(err)
	}
	return toolsDockerfile(base, toolsBase(kind), u, copyNode)
}

// WriteToolsContext writes the tools step's build context for the wall user user, uid:gid,
// to the new folder dir, as the build does.
func WriteToolsContext(dir, base, kind, user, parsers, node string, copyNode bool) error {
	u, err := parseUser(user)
	if err != nil {
		return err
	}
	return writeToolsContext(dir, base, toolsBase(kind), u, parsers, node, copyNode)
}
