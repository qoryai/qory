package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/secretmask"
)

// A run code whose documents have step prebuild builds the environment of the run's
// target into images behind the wall of the run, as [prebuild.Launch] describes, once
// the code is redeemed. The documents set where the images go: the target image to
// <image_destination>:<run_id>, and the environment image the build keeps apart to
// <environment_destination>:<run_id>, unless the documents set environment_image, which
// the build starts from. The build takes the agent image and the user from the wall
// section of forager.yaml, and runs in the image of wall.images that wall.prebuild_image is set to.

// textPrebuildWall is the line of a prebuild from a run code on a machine whose
// forager.yaml does not set the wall settings the build takes.
const textPrebuildWall = "set wall.image, pinned by its digest, wall.user and wall.prebuild_image in forager.yaml: a prebuild from a run code builds with them" + codeUsed

// textPrebuildKeyed is the line of a prebuild from a run code on a machine with an
// access key, where %s is where the key is.
const textPrebuildKeyed = "run the prebuild on a machine without an access key: this machine has an access key in %s, and a prebuild fetches the target's code with git on this machine, outside the wall" + codeUsed

// prebuildKeyed is the error of a prebuild from a run code on a machine dir where
// [refuseUnwalled] stops an unwalled run, nil for none: [textPrebuildKeyed] with the
// key's source as [unwalledKey] finds it.
func prebuildKeyed(dir foragerdir.Dir, id *serverIdentity) error {
	source, path, err := unwalledKey(dir, id)
	if err != nil || source == 0 {
		return err
	}
	where := exports.Value(path, false)
	switch source {
	case fromFD:
		where = "the descriptor of --" + secretFDFlag
	case fromEnv:
		where = accesskey.EnvSecret
	}
	return fmt.Errorf(textPrebuildKeyed, where)
}

// launchPrebuild runs a prebuild, [prebuild.Launch]; a test replaces it.
var launchPrebuild = prebuild.Launch

// heldRunKey is the run key as [prebuild.Run] takes it: its bytes, which Clear
// overwrites. It prints as [hidden] whatever the verb.
type heldRunKey struct{ b []byte }

// Bytes is the key's bytes.
func (k *heldRunKey) Bytes() []byte { return k.b }

// Clear overwrites the key's bytes with zeros.
func (k *heldRunKey) Clear() { clear(k.b) }

// Format writes [hidden] for every verb.
func (k *heldRunKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[hidden]") }

// runCodePrebuild runs the prebuild that doc, the documents of the answer to a run code
// for the step prebuild, sets, as the run runID with the run key key: [prebuild.Launch]
// with qory's state directory state, the configuration directory user, as
// [config.UserDir] returns it, and machine, wall.image, wall.user and the wall of r,
// the image of wall.prebuild_image its one image, with the helper [wallHelper] finds
// from exe, wall.ca_env and the wall's limits. pool.registry of r, when r sets one, is
// where the images must go. qory's line on the run's gateway goes to stderr, and each
// line of the prebuild, which contains the output of the programs the build runs, to
// log, after "qory run: ". Each error is exit 1.
func runCodePrebuild(ctx context.Context, stderr, log io.Writer, r *config.Forager, state, user, exe string, key *runKey, doc contracts.Documents, runID string) error {
	var w *config.ForagerWall
	if r != nil {
		w = r.Wall
	}
	if w == nil || !policy.ImageReference(w.Image) || w.User == "" || w.PrebuildImage == "" {
		return errors.New(textPrebuildWall)
	}
	helper, err := wallHelper(*w, exe)
	if err != nil {
		return err
	}
	var images []session.Image
	for _, i := range w.Images {
		if i.Name == w.PrebuildImage {
			images = append(images, i.Session())
		}
	}
	registry := ""
	if r.Pool != nil {
		registry = r.Pool.Registry
	}
	fmt.Fprintln(stderr, throughGatewayLine(key, runID))
	_, err = launchPrebuild(ctx, prebuild.Settings{
		State: state, RunConfig: filepath.Dir(user), AgentImage: w.Image, User: w.User, Version: build().title(),
		Wall:  &wall.Docker{Command: w.Command, Helper: helper, RelayArgs: relayArgs, NestArgs: nestArgs, HoldArgs: holdArgs, User: w.User, CAEnv: w.CAEnv},
		Image: w.PrebuildImage, Images: images, CAEnv: w.CAEnv, Exec: append([]string{wall.HelperPath}, execArgs...),
		Limits:   wall.Limits{CPUs: w.CPUs, Memory: w.Memory, PIDs: w.PIDs, ShmSize: w.ShmSize},
		Registry: registry, Machine: r.Machine, Masker: secretmask.Default().Masker(), Log: func(line string) { fmt.Fprintln(log, "qory run:", line) },
	}, prebuild.Run{
		RunID: runID, Key: &heldRunKey{b: []byte(key.value)}, Inputs: doc.Inputs,
		ImageDestination: doc.ImageDestination, EnvironmentDestination: doc.EnvironmentDestination, EnvironmentImage: doc.EnvironmentImage,
	})
	return err
}
