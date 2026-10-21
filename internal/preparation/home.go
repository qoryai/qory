package preparation

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/integration"
)

// EnvAgentHome is the variable Claude Code reads its configuration directory from: its
// settings, CLAUDE.md, plugins, memory, .claude.json and transcripts.
const EnvAgentHome = "CLAUDE_CONFIG_DIR"

// AgentHome is the agent's configuration directory on a dispatched walled run of Claude
// Code, a fixed path inside the container that qory owns. It is under /tmp, which an
// image usually has and any user may write, so it does not depend on the image's HOME
// or user; it is in the container alone, so nothing of it reaches the machine or the
// checkout, and it goes with the container.
const AgentHome = "/tmp/qory-agent-home"

// managedSettings is where Claude Code on Linux reads managed settings, above every
// other source and whatever CLAUDE_CONFIG_DIR is: managed-settings.json,
// managed-settings.d/, CLAUDE.md and managed-mcp.json, see
// https://code.claude.com/docs/en/settings. A test points it elsewhere.
var managedSettings = "/etc/claude-code"

// The words of the agent's home. Each is one format string.
const (
	textManaged        = "qory run prepare: the image has %s, where Claude Code reads managed settings above every other source, so a run given a run key does not start; use an image without it"
	textManagedUnread  = "qory run prepare: %s, where Claude Code reads managed settings, could not be checked: %s"
	textHomeExists     = "qory run prepare: the agent's home %s exists already, and qory makes it only where nothing is, so a run given a run key does not start"
	textHomeNotEmpty   = "qory run prepare: the agent's home %s held something once qory made it, so a run given a run key does not start"
	textHomeParentLink = "qory run prepare: %s, above the agent's home %s, is a symbolic link; the agent starts only with a home no link leads to"
	textHomeNotMade    = "qory run prepare: the agent's home %s could not be made: %s"
)

// FreshHome makes the agent's home, [AgentHome], new and empty when getenv sets
// [EnvAgentHome] to it, as qory run does for a dispatched walled run of Claude Code, and
// does nothing otherwise. It is the last step of the preparation, and refuses anything
// at the home when it runs, such as a home the image holds or the repository's commands
// made. It refuses first an image with Claude Code's managed settings, which no
// configuration directory replaces. These checks guard against what the image and the
// repository's files leave in place, not against a repository that acts against them:
// the image's programs and the repository's commands run as the agent's user in the
// agent's container, so a program they leave running can write in the home after
// FreshHome returns; against that, the boundary is the wall. Every error's text is one
// line, ready for stderr.
func FreshHome(getenv func(string) string) error {
	if getenv(EnvAgentHome) != AgentHome {
		return nil
	}
	return agentHome(AgentHome)
}

// agentHome refuses anything at [managedSettings], a link or a file too, and then makes
// path fresh, see [freshHome].
func agentHome(path string) error {
	if _, err := os.Lstat(managedSettings); err == nil {
		return fmt.Errorf(textManaged, integration.Printable(managedSettings))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(textManagedUnread, integration.Printable(managedSettings), integration.Printable(err.Error()))
	}
	return freshHome(path)
}

// freshHome makes path an empty directory of mode 0700. It refuses anything at path when
// it runs, a file, a folder or a link, which the image or the repository's commands put
// there, and a symbolic link in a folder above it. The directory is opened after
// Mkdir without following a link, refused unless it is empty, and given its mode through
// that open file, so a folder put at path in between is used as the home only when it
// is empty, and then with mode 0700.
func freshHome(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf(textHomeExists, integration.Printable(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return homeNotMade(path, err)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		fi, err := os.Lstat(dir)
		if err != nil {
			return homeNotMade(path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(textHomeParentLink, integration.Printable(dir), integration.Printable(path))
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if err := os.Mkdir(path, 0o700); errors.Is(err, os.ErrExist) {
		return fmt.Errorf(textHomeExists, integration.Printable(path))
	} else if err != nil {
		return homeNotMade(path, err)
	}
	madeHome(path)
	f, err := openDir(path)
	if err != nil {
		return homeNotMade(path, err)
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err == nil {
		return fmt.Errorf(textHomeNotEmpty, integration.Printable(path))
	} else if !errors.Is(err, io.EOF) {
		return homeNotMade(path, err)
	}
	if err := f.Chmod(0o700); err != nil {
		return homeNotMade(path, err)
	}
	return nil
}

// madeHome runs between the Mkdir of the agent's home and its open; a test puts
// something at the path there.
var madeHome = func(string) {}

// homeNotMade is [textHomeNotMade] for path, with the error made printable.
func homeNotMade(path string, err error) error {
	return fmt.Errorf(textHomeNotMade, integration.Printable(path), integration.Printable(err.Error()))
}
