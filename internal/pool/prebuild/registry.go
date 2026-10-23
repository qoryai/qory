package prebuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
)

// The media types of a manifest the size is read from, and of an index of manifests,
// Docker's and OCI's.
const (
	mediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
)

// attestationType is the annotation Docker's builds mark an attestation's manifest in
// an index with, and attestationManifest its value: such a manifest is no image.
const (
	attestationType     = "vnd.docker.reference.type"
	attestationManifest = "attestation-manifest"
)

// maxManifest is the most bytes read of a manifest: a registry refuses one larger than
// 4 MiB.
const maxManifest = 4 << 20

// maxSize is the largest size the contract takes, 2^53-1.
const maxSize = 1<<53 - 1

// manifestTimeout bounds each docker buildx imagetools inspect of a manifest: 30
// seconds. A test shortens it.
var manifestTimeout = 30 * time.Second

// The words of a manifest's read. Each is one format string.
const (
	textNoImagetools = "check that docker is on the PATH every command of qory run gets: docker buildx imagetools inspect could not be started: %w"
	textNotInspected = "check that docker buildx is installed and that docker reaches the registry %s: docker buildx imagetools inspect --raw of the pushed image failed: %s"
)

// digestShape is a digest of the SHA-256 of a content: sha256: and 64 lower-case
// hexadecimal digits.
var digestShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// errRegistry is every failure to read the pushed image's size; the reason follows it.
var errRegistry = errors.New("the image's size could not be read from the registry")

// descriptor is a content a manifest names: its type, digest and size.
type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// platform is the platform of a manifest in an index.
type platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

// entry is a manifest an index names.
type entry struct {
	descriptor
	Platform    *platform         `json:"platform"`
	Annotations map[string]string `json:"annotations"`
}

// manifest is a manifest or an index, as docker buildx imagetools inspect --raw prints
// it.
type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    *descriptor  `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []entry      `json:"manifests"`
}

// registryOf is the host of a registry name, such as localhost:5001 of
// localhost:5001/qory-env. The first segment is a host when it has a dot or a colon,
// or is localhost, and a path follows it.
func registryOf(name string) (string, error) {
	host, path, ok := strings.Cut(name, "/")
	if !ok || path == "" || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return "", fmt.Errorf("%w: the registry's name starts with no host", errRegistry)
	}
	return host, nil
}

// imageSize is the size of the image pushed as name@digest, read from its manifest:
// its config's size and its layers' sizes added up, the sizes in the registry,
// compressed. Docker reads the manifest from the registry, with the settings in
// $HOME/.docker, and its bytes must have that digest. When the manifest of the digest
// is an index, the image is its one manifest of the platform want, an attestation's manifest left
// out; none, or more than one, is a failure. A variant want leaves empty takes any.
func (s Settings) imageSize(ctx context.Context, runID, state, name, digest string, want platform) (int64, error) {
	m, err := s.readManifest(ctx, runID, state, name, digest)
	if err != nil {
		return 0, err
	}
	if m.MediaType == mediaOCIIndex || m.MediaType == mediaDockerList {
		var picked []entry
		for _, e := range m.Manifests {
			if (e.MediaType == mediaOCIManifest || e.MediaType == mediaDockerManifest) && e.Platform != nil &&
				e.Annotations[attestationType] != attestationManifest &&
				e.Platform.OS == want.OS && e.Platform.Architecture == want.Architecture &&
				(want.Variant == "" || e.Platform.Variant == want.Variant) {
				picked = append(picked, e)
			}
		}
		if len(picked) != 1 {
			return 0, fmt.Errorf("%w: the index names %d manifests of %s/%s, not one", errRegistry, len(picked), want.OS, want.Architecture)
		}
		if m, err = s.readManifest(ctx, runID, state, name, picked[0].Digest); err != nil {
			return 0, err
		}
		if m.MediaType != mediaOCIManifest && m.MediaType != mediaDockerManifest {
			return 0, fmt.Errorf("%w: the index's manifest is not an image's manifest", errRegistry)
		}
	}
	if m.Config == nil || m.Config.Size < 0 {
		return 0, fmt.Errorf("%w: the manifest names no config", errRegistry)
	}
	size := m.Config.Size
	for _, l := range m.Layers {
		if l.Size < 0 || size > maxSize-l.Size {
			return 0, fmt.Errorf("%w: a layer's size is out of range", errRegistry)
		}
		size += l.Size
	}
	if size < 1 || size > maxSize {
		return 0, fmt.Errorf("%w: the size is out of range", errRegistry)
	}
	return size, nil
}

// readManifest is the manifest or index of digest under name, as docker buildx
// imagetools inspect --raw prints it, the bytes the registry stores: they are checked
// against the digest. Docker runs as the prebuild's other docker commands do, and its
// error lines go to the log. When ctx ends during the read, the error is
// [textStoppedPush], as for a stop during the push. A manifest that has no media type,
// which the OCI image specification allows, is taken as an OCI index when it lists
// manifests, else as an OCI image's manifest.
func (s Settings) readManifest(ctx context.Context, runID, state, name, digest string) (manifest, error) {
	if !digestShape.MatchString(digest) {
		return manifest{}, fmt.Errorf("%w: a manifest's digest is not sha256: and 64 lower-case hexadecimal digits", errRegistry)
	}
	docker, err := s.docker()
	if err != nil {
		return manifest{}, fmt.Errorf(textNoImagetools, err)
	}
	readCtx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	cmd := exec.CommandContext(readCtx, docker, "buildx", "imagetools", "inspect", "--raw", name+"@"+digest)
	cmd.Dir = s.poolDir()
	cmd.Env = child.Env(s.RunConfig, state, explore.RunsDir(s.State))
	var stdout bytes.Buffer
	cmd.Stdout = &limited{w: &stdout, n: maxManifest + 1}
	errOut := s.lines(runID)
	cmd.Stderr = errOut
	err = cmd.Run()
	errOut.Flush()
	switch {
	case err != nil && ctx.Err() != nil:
		return manifest{}, fmt.Errorf(textStoppedPush, context.Cause(ctx))
	case err != nil:
		cause := err.Error()
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			cause = "it ran past its time limit of " + manifestTimeout.String()
		}
		host, _ := registryOf(name)
		return manifest{}, fmt.Errorf(textNotInspected, host, cause)
	}
	body := stdout.Bytes()
	if len(body) > maxManifest {
		return manifest{}, fmt.Errorf("%w: the manifest is larger than %d bytes", errRegistry, maxManifest)
	}
	sum := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return manifest{}, fmt.Errorf("%w: the manifest's bytes are not of its digest", errRegistry)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("%w: the manifest is not JSON", errRegistry)
	}
	if m.MediaType == "" {
		m.MediaType = mediaOCIManifest
		if m.Manifests != nil {
			m.MediaType = mediaOCIIndex
		}
	}
	return m, nil
}
