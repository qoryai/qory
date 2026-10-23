package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/server"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner"
)

// verbPool is qory run pool's verb, as its lines name it after "qory".
const verbPool = "run pool"

// poolNodePrefix is the prefix of a node pool's node id: discovery naming one says the
// access key is a pool's.
const poolNodePrefix = "np_"

// The folder of the pool's state under qory's state directory, and its lock file.
const (
	poolStateDir = "pool"
	poolLockFile = "pool.lock"
)

// poolSampleDigest is a digest of the right shape, after a name under pool.registry: the
// reference wall.images_allow is checked against.
const poolSampleDigest = "@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// poolSettings is what qory run pool runs by, read and checked before the first claim.
type poolSettings struct {
	// Forager is the pool's forager.yaml, which defines the server, and Pool its pool
	// section.
	Forager *config.Forager
	Pool    *config.ForagerPool
	// Runs is the forager.yaml of pool.run_config, which every run the pool starts reads;
	// nil when there is none.
	Runs *config.Forager
	// State is the pool's folder, mode 0700, under qory's state directory, whose lock
	// file the pool holds while it runs.
	State string
	// InstanceID and InstanceName are what the pool's requests are signed and named with.
	InstanceID, InstanceName string
	// Configuration is the server's signed discovery, read under the pool's access key.
	Configuration *server.Configuration
	// Stderr takes the pool's log.
	Stderr io.Writer
}

// newPool builds qory run pool: this machine runs a node pool's runs, claimed
// from Qory Apiary with the pool's access key, until it is stopped. Everything but the
// access key's descriptor comes from forager.yaml.
func newPool() *cobra.Command {
	var secretFD int
	c := &cobra.Command{
		Use:   "pool",
		Short: "Claim this pool's runs from Qory Apiary and run them on this machine",
		Long: `Run this machine as a node pool's program, until it is stopped. It claims the pool's
dispatches from Qory Apiary with the pool's access key, and starts each with the command
that runs on a machine a provisioner creates: qory run --run-code-file /dev/fd/3
--gateway <gateway_url>, with the run code in a file that no name links to. The pool's
access key does not reach the run, and each run reports its outcome to Qory Apiary.
While a run runs, the pool sends dispatched every 60 s. It stops the run when Qory
Apiary ends the dispatch or the claim's max_lifetime_minutes pass. Once the run ends,
the pool uploads the run's output as the dispatch's machine log: at most the last
768 KiB, with each line that contains qak_, qrk_ or qecr_ replaced. It releases a claim
that sets a machine type, and a claim it could not start.
The pool section of forager.yaml sets it: runs, registry, run_config. SIGINT or SIGTERM
stops it: it claims no new run, stops the runs it started, and exits 0.`,
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Core dumps are turned off first in RunE: before the pool's access key is
			// read from its descriptor or its folder, and before any run key.
			if err := noCoreDumps(); err != nil {
				return err
			}
			// The access key's descriptor is read and closed next, whatever comes after.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			s, err := loadPool()
			if err != nil {
				return err
			}
			lock, err := lockPool(s)
			if err != nil {
				return err
			}
			defer lock.Release()
			id, err := identify(s.Forager, cmd.ErrOrStderr(), verbPool, fdKey)
			if err != nil {
				return err
			}
			s.InstanceID, s.InstanceName, s.Stderr = id.instanceID, id.instanceName, cmd.ErrOrStderr()
			if s.Configuration, err = poolDiscovery(cmd.Context(), s.Forager, id); err != nil {
				return err
			}
			return startPool(cmd.Context(), s, id.key.key, s.Forager.Server.Pin)
		},
	}
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	return c
}

// loadPool reads forager.yaml, which must define a server and a pool section, and checks
// the pool section's run_config: a directory a run given a run key starts with,
// as [behindGateway] has it: its qory folder holds no access key, and its forager.yaml,
// which is read, has no gateway section, and has a wall section whose wall.image is pinned
// by its digest and whose wall.images_allow covers the images the builds push under
// pool.registry. Neither forager.yaml sets gateway.server.via, as [directly] has it.
// Each refusal is input.
func loadPool() (*poolSettings, error) {
	r, err := config.LoadForager()
	if err != nil {
		return nil, input(err)
	}
	if r == nil || r.Server == nil {
		return nil, input(fmt.Errorf("%s defines no server to claim the pool's runs from", config.ForagerFileName))
	}
	if err := directly(r); err != nil {
		return nil, err
	}
	if r.Pool == nil {
		return nil, input(fmt.Errorf("%s has no pool section", r.File))
	}
	p := r.Pool
	if info, err := os.Stat(p.RunConfig); err != nil || !info.IsDir() {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, which is not a folder", r.File, p.RunConfig))
	}
	// The runs' configuration directory, as each run's config.UserDir reads it.
	dir := filepath.Join(p.RunConfig, "qory")
	held, err := heldSecret(foragerdir.Dir(dir))
	if err != nil {
		return nil, err
	}
	if held != "" {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, which holds an access key; the runs' directory holds none", r.File, p.RunConfig))
	}
	runs, err := config.LoadForagerIn(dir)
	if err != nil {
		return nil, input(err)
	}
	if err := directly(runs); err != nil {
		return nil, err
	}
	if runs != nil && runs.Gateway {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, whose forager.yaml has a gateway section; the runs' directory has none", r.File, p.RunConfig))
	}
	if runs == nil {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, which holds no qory/forager.yaml; each run starts behind the wall it sets", r.File, p.RunConfig))
	}
	if runs.Wall == nil {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, whose forager.yaml sets no wall; each run starts behind the wall it sets", r.File, p.RunConfig))
	}
	if runs.Wall.Image == "" {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, whose forager.yaml's wall sets no image; give it one pinned by its digest, name@sha256: and 64 lower-case hexadecimal digits", r.File, p.RunConfig))
	}
	if !policy.ImageReference(runs.Wall.Image) {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, whose forager.yaml's wall.image is not pinned by its digest, name@sha256: and 64 lower-case hexadecimal digits, with no tag", r.File, p.RunConfig))
	}
	if _, ok := policy.ImageAllowed(runs.Wall.ImagesAllow, p.Registry+"/x"+poolSampleDigest); !ok {
		return nil, input(fmt.Errorf("%s: pool.run_config is %s, whose forager.yaml's wall.images_allow does not cover pool.registry; a run could not start the image built for it", r.File, p.RunConfig))
	}
	return &poolSettings{Forager: r, Pool: p, Runs: runs}, nil
}

// directly refuses, as input, a forager.yaml r that sets gateway.server.via: the pool
// program reaches the server directly for every call, through no gateway. r may be nil.
func directly(r *config.Forager) error {
	if r != nil && r.Server != nil && r.Server.Via != nil {
		return input(fmt.Errorf("%s: gateway.server.via does nothing for qory run pool: the pool program reaches the server directly; remove gateway.server.via", r.File))
	}
	return nil
}

// poolServerClient is [serverClient] for the pool program, which reaches the server
// directly for every call: it goes through no gateway, whatever r sets.
func poolServerClient(r *config.Forager, key *accesskey.Key, instance, name string) *server.Client {
	c := serverClient(r, key, instance, name)
	c.Config.Via = nil
	return c
}

// lockPool makes the pool's folder under qory's state directory, mode 0700, and takes its
// lock, so one qory run pool runs with it at a time; the lock is held until Release. A
// refusal prints the folder's path as [exports.HidePath] prints it.
func lockPool(s *poolSettings) (*foragerdir.Lock, error) {
	state, err := stateDir()
	if err != nil {
		return nil, err
	}
	s.State = filepath.Join(state, poolStateDir)
	if err := foragerdir.MakePrivateDir(s.State, "the pool's folder"); err != nil {
		return nil, exports.HideError(err)
	}
	lock, err := foragerdir.LockFile(filepath.Join(s.State, poolLockFile))
	if errors.Is(err, foragerdir.ErrLocked) {
		return nil, fmt.Errorf("another qory %s is running with %s", verbPool, exports.HidePath(s.State))
	}
	return lock, exports.HideError(err)
}

// poolDiscovery reads the server's signed discovery under the pool's access key, as id
// signs, and refuses, as input, a key whose node discovery names is not a node pool's. A
// refusal of the server's is worded as [explain] words it. Nothing is written on this
// machine.
func poolDiscovery(ctx context.Context, r *config.Forager, id *serverIdentity) (*server.Configuration, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	conf, _, err := poolServerClient(r, id.key.key, id.instanceID, id.instanceName).Discover(ctx)
	if err != nil {
		return nil, explain(err, id)
	}
	if !strings.HasPrefix(conf.NodeID, poolNodePrefix) {
		return nil, input(errors.New("this machine's key is not a pool's key; enrol one with a code made for the pool"))
	}
	return conf, nil
}

// startPool is what qory run pool calls once its settings are read and checked: [runPool];
// a test replaces it.
var startPool = runPool

// runPool runs the pool with its settings s, signing with key and verifying every answer
// under pin, while the caller holds the pool's lock, with core dumps off: it runs
// [pool.Pool] until ctx ends or SIGINT or SIGTERM stops it, and returns nil once every
// dispatch ended; its error is printed as [exports.HideError] prints it. A second signal
// exits at once. Its lines go to s.Stderr; each qory run gets the runs' configuration
// directory, and its folder is under the pool's folder.
func runPool(ctx context.Context, s *poolSettings, key *accesskey.Key, pin accesskey.Pin) error {
	ctx, stop := pool.OnSignals(ctx, exitNow)
	defer stop()
	stderr := s.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	log := slog.New(pool.SafeHandler(slog.NewTextHandler(stderr, nil)))
	return exports.HideError(poolLoop(s, key, pin, log).Run(ctx))
}

// poolLoop is the loop [runPool] runs with s, key and pin, logging to log: it reaches the
// server directly for every call. Each qory run it starts is this binary.
func poolLoop(s *poolSettings, key *accesskey.Key, pin accesskey.Pin, log *slog.Logger) *pool.Pool {
	return &pool.Pool{
		Settings: pool.Settings{Runs: s.Pool.Runs, State: s.State, RunConfig: s.Pool.RunConfig},
		Client: &provisioner.Client{
			URL: strings.TrimSuffix(s.Forager.Server.URL, "/"), KeyID: s.Forager.Server.AccessKeyID, Key: key, Pin: pin,
			InstanceID: s.InstanceID, UserAgent: userAgent(), Log: log,
		},
		Log: log,
	}
}

// noCoreDumps turns core dumps off for qory run pool, first: [pool.NoCoreDumps]; a test
// replaces it.
var noCoreDumps = pool.NoCoreDumps

// exitNow ends the process at once with code, at a second signal; a test replaces it.
var exitNow = os.Exit
