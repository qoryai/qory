package prebuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The media types of a manifest the size is read from, and of an index of manifests,
// Docker's and OCI's.
const (
	mediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
)

// accepted is the Accept header of a manifest's GET: every type above.
var accepted = strings.Join([]string{mediaOCIManifest, mediaDockerManifest, mediaOCIIndex, mediaDockerList}, ", ")

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

// registryTimeout bounds each GET of a manifest.
const registryTimeout = 30 * time.Second

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

// manifest is a manifest or an index, as a registry answers its GET.
type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    *descriptor  `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []entry      `json:"manifests"`
}

// registryOf is the host of a registry name, such as localhost:5001 of
// localhost:5001/qory-env, and the rest, the repository's path before the image's
// name. The first segment is a host when it has a dot or a colon, or is localhost.
func registryOf(name string) (host, path string, err error) {
	host, path, ok := strings.Cut(name, "/")
	if !ok || path == "" || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return "", "", fmt.Errorf("%w: the registry's name starts with no host", errRegistry)
	}
	return host, path, nil
}

// loopback reports whether host, with its port or without, is this machine: a
// registry there is reached over http, as the engine reaches it; any other over https.
func loopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// imageSize is the size of the image pushed as repo@digest to the registry at host,
// read from its manifest: its config's size and its layers' sizes added up. The
// manifest is read from GET /v2/<repo>/manifests/<digest>, and its bytes must have that
// digest. When the digest names an index, the image is its one manifest of the
// platform want, an attestation's manifest left out; none, or more than one, is a
// failure. A variant want leaves empty takes any.
func imageSize(ctx context.Context, client *http.Client, host, repo, digest string, want platform) (int64, error) {
	m, err := getManifest(ctx, client, host, repo, digest)
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
		if m, err = getManifest(ctx, client, host, repo, picked[0].Digest); err != nil {
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

// getManifest is the manifest or index digest names in repo at host, its bytes checked
// against the digest, and its media type the one it names, or the answer's
// Content-Type when it names none.
func getManifest(ctx context.Context, client *http.Client, host, repo, digest string) (manifest, error) {
	if !digestShape.MatchString(digest) {
		return manifest{}, fmt.Errorf("%w: a manifest's digest is not sha256: and 64 lower-case hexadecimal digits", errRegistry)
	}
	scheme := "https"
	if loopback(host) {
		scheme = "http"
	}
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+"/v2/"+repo+"/manifests/"+digest, nil)
	if err != nil {
		return manifest{}, fmt.Errorf("%w: %v", errRegistry, err)
	}
	req.Header.Set("Accept", accepted)
	res, err := client.Do(req)
	if err != nil {
		return manifest{}, fmt.Errorf("%w: it could not be reached", errRegistry)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return manifest{}, fmt.Errorf("%w: it answered %d", errRegistry, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxManifest+1))
	if err != nil {
		return manifest{}, fmt.Errorf("%w: its answer could not be read", errRegistry)
	}
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
		m.MediaType, _, _ = strings.Cut(res.Header.Get("Content-Type"), ";")
		m.MediaType = strings.TrimSpace(m.MediaType)
	}
	return m, nil
}

// noRedirect is a client's CheckRedirect that follows none: the answer is the
// redirect's, which is not a 200.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
