package pool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
)

// metadataAddresses are addresses of a cloud metadata service, IPv4 and IPv6, whose
// port 80 the check of [Probe] tries.
var metadataAddresses = []string{"169.254.169.254", "fd00:ec2::254"}

// probeReached is the status the check's script exits with when a connection to an
// address of [metadataAddresses] succeeded; it prints each such address on its line.
const probeReached = 10

// probeScript is the check that runs in the agent's image under node, which the image
// has for Claude Code: a TCP connection to port 80 of each address of
// [metadataAddresses], at once, each given up after 2 seconds. It exits 0 when none
// connected, and [probeReached] when one did.
const probeScript = `const net = require("net");
const hosts = ["169.254.169.254", "fd00:ec2::254"];
const reached = [];
let left = hosts.length;
for (const host of hosts) {
  let over = false;
  const s = net.connect({ host, port: 80 });
  const end = (ok) => {
    if (over) return;
    over = true;
    clearTimeout(t);
    s.destroy();
    if (ok) reached.push(host);
    if (--left > 0) return;
    for (const h of reached) console.log(h);
    process.exit(reached.length > 0 ? 10 : 0);
  };
  const t = setTimeout(() => end(false), 2000);
  s.on("connect", () => end(true));
  s.on("error", () => end(false));
}
`

// probeHomeFolder is the folder of the pool's folder that is the check's HOME while it
// runs.
const probeHomeFolder = "probe-home"

// probeWait bounds the check's container, from docker run to its end.
const probeWait = 2 * time.Minute

// Probe is the check the pool runs before its first claim: one container, removed when
// it ends, on the engine's default bridge network, tries port 80 of each address of
// [metadataAddresses]. Its result covers that network alone, and the IPv6 address only
// when the daemon enables IPv6 there. The container runs the agent's image under the
// wall's user, with every capability dropped and no new privileges, and is never pulled.
type Probe struct {
	// Docker is the engine's program, the runs' wall.command; empty is docker. A name
	// with no path separator is found on the PATH every command gets, as
	// [child.Program] has it.
	Docker string
	// Image is the agent's image, the runs' wall.image, pinned by its digest.
	Image string
	// User is the uid:gid the container runs as, the runs' wall.user; empty is the
	// uid:gid of this process, as the wall has it.
	User string
}

// args are the arguments of docker run for the check.
func (pr Probe) args() []string {
	user := pr.User
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	return []string{
		"run", "--rm", "--pull", "never", "--network", "bridge",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", user,
		"--entrypoint", "node", pr.Image, "-e", probeScript,
	}
}

// probe runs the check of s.Probe, in the pool's folder, with the environment every
// command gets and an empty HOME. It returns nil when no connection to an address
// succeeded within its 2 seconds, and an error that starts with the fix otherwise: the host's rules when an address connected, the engine and the
// image when the check could not run. stopped is set when ctx ended during the check.
func probe(ctx context.Context, s Settings) (stopped bool, err error) {
	docker := s.Probe.Docker
	if docker == "" {
		docker = "docker"
	}
	wait, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	var lines []string
	out := child.NewLineWriter("", func(line string) {
		if len(lines) < 16 {
			lines = append(lines, line)
		} else {
			lines[len(lines)-1] = line
		}
	})
	// The check's docker gets an empty HOME, as each qory run gets one, so it reads the
	// configuration a run's docker reads: none of the pool's HOME.
	home := filepath.Join(s.State, probeHomeFolder)
	if err := os.RemoveAll(home); err != nil {
		return false, err
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		return false, err
	}
	defer os.RemoveAll(home)
	res, runErr := child.Run(wait, child.Command{
		Path: docker, Args: s.Probe.args(), Dir: s.State, ConfigHome: s.RunConfig, Home: home,
		Runs: filepath.Join(s.State, runsFolder), Output: out, Grace: 5 * time.Second,
	})
	switch {
	case ctx.Err() != nil:
		return true, nil
	case wait.Err() != nil:
		return false, fmt.Errorf("check that the container engine runs: the check of the cloud metadata service did not end within %s", probeWait)
	case runErr != nil:
		return false, fmt.Errorf("install %[1]s, or set wall.command in the runs' forager.yaml: the start of %[1]s for the check of the cloud metadata service failed: %[2]w", exports.Value(docker, false), exports.HideError(runErr))
	case res.ExitCode == 0:
		return false, nil
	case res.ExitCode == probeReached:
		var reached []string
		for _, l := range lines {
			a := strings.TrimSpace(l)
			if slices.Contains(metadataAddresses, a) && !slices.Contains(reached, a+":80") {
				reached = append(reached, a+":80")
			}
		}
		if len(reached) == 0 {
			reached = []string{"an address of it"}
		}
		return false, errors.New("drop 169.254.169.254 and fd00:ec2::254 in the raw table before Docker starts: a container on the default bridge network connected to " + strings.Join(reached, " and ") + ", the cloud metadata service, so the pool claims no run")
	}
	end := fmt.Sprintf("exited %d", res.ExitCode)
	if res.Signal != 0 {
		end = "ended on the signal " + res.Signal.String()
	}
	last := ""
	if len(lines) > 0 {
		last = ": " + lines[len(lines)-1]
	}
	return false, fmt.Errorf("pull %s onto this machine, and check that the container engine runs: the container that checks the cloud metadata service %s%s", exports.Value(s.Probe.Image, false), end, last)
}
