package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strconv"

	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// The texts of a walled run whose qory and wall user are not one ordinary user: the cause
// alone for a run from a run code, the fix first for any other run. [textOneUserUIDs] is
// line 2 of both, with the wall's user as "uid" and a number, or as the cleaned value of
// wall.user.
const (
	textOneUser         = "run qory as a dedicated user, and set wall.user in forager.yaml to its uid:gid"
	textOneUserFromCode = "qory and the wall user are not one ordinary user, so the run did not start" + codeUsed
	textOneUserUIDs     = "\n  qory runs as uid %d and the wall user is %s; a walled run needs one ordinary user for both."
)

// qoryUID is the effective uid qory runs as; a test replaces it.
var qoryUID = os.Geteuid

// numericUserShape is a wall.user that [oneUser] accepts, <uid> or <uid>:<gid> in decimal
// digits, with the uid.
var numericUserShape = regexp.MustCompile(`^([0-9]+)(:[0-9]+)?$`)

// oneUser stops a walled run, before any container of it starts, when qory runs as root or
// as another uid than the wall's user; the error closes the run machine_setup_invalid. An
// empty wall.user is qory's uid:gid, and a wall.user that does not match [numericUserShape],
// such as a name, is another uid. The gid is not compared. A prebuild stops with it before
// its run opens, so with no end code.
func oneUser(w wall.Wall, fromCode bool) error {
	uid := qoryUID()
	user, same := "uid "+strconv.Itoa(uid), true
	if d, ok := w.(*wall.Docker); ok && d.User != "" {
		user, same = integration.Printable(exports.Value(d.User, false)), false
		if m := numericUserShape.FindStringSubmatch(d.User); m != nil {
			n, err := strconv.Atoi(m[1])
			user, same = "uid "+integration.Printable(m[1]), err == nil && n == uid
		}
	}
	if uid != 0 && same {
		return nil
	}
	line := textOneUser
	if fromCode {
		line = textOneUserFromCode
	}
	return &runLineError{&reasonError{code: string(contracts.EndCodeMachineSetupInvalid), text: line + fmt.Sprintf(textOneUserUIDs, uid, user)}}
}

// noContainers is the wall of a run that [oneUser] stops: the run opens as behind Wall, so
// it closes with its end code, and the engine runs no command.
// shortcut: it has no Engine method, which runs docker info, so a qory that is
// killed between the open and the end of the close leaves a registry entry with no engine,
// and each later walled run of the user stops with engine_unreachable until someone deletes
// the entry, whose path the error has; replace it when Forager closes a run failed with an
// end code without preparing its wall.
type noContainers struct{ wall.Wall }

// Prepare prepares nothing.
func (noContainers) Prepare(context.Context, wall.Request) (wall.Enclosure, error) {
	return noEnclosure{}, nil
}

// noEnclosure is the enclosure of [noContainers]; nothing starts in it.
type noEnclosure struct{}

// errNoContainers is the error of each call that needs a container of [noEnclosure].
var errNoContainers = errors.New("the run has no containers")

// ProxyAddr is a port of this machine's loopback, which the open listens on.
func (noEnclosure) ProxyAddr() string { return "127.0.0.1:0" }

// Wrap fails.
func (noEnclosure) Wrap(context.Context, wall.Launch) (wall.Launch, error) {
	return wall.Launch{}, errNoContainers
}

// AgentAddrs fails; Forager opens a run with a route to qory only with an enclosure that
// has it.
func (noEnclosure) AgentAddrs(context.Context) ([]netip.Addr, error) { return nil, errNoContainers }

// Close has nothing to remove.
func (noEnclosure) Close(context.Context) error { return nil }
