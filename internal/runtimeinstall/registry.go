package runtimeinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The registry keeps each runtime at shared/runtimes/<runtime>: the tag newest, which
// moves to each new version, and the tag <version>. Each is an OCI image index with one
// image manifest for linux/amd64 and one for linux/arm64. Each manifest has the
// artifactType application/vnd.qory.runtime.v1, the empty config, and one layer: npm's
// tarball of the platform's build as it was published, with npm's checksum in its
// annotation dev.qory.runtime.integrity. For Codex that is the tarball of
// @openai/codex@<version>-linux-x64 or -linux-arm64, see [npmPackages].

// The media types and annotations of the registry's runtime cache.
const (
	mediaIndex     = "application/vnd.oci.image.index.v1+json"
	mediaManifest  = "application/vnd.oci.image.manifest.v1+json"
	mediaEmpty     = "application/vnd.oci.empty.v1+json"
	mediaLayer     = "application/vnd.oci.image.layer.v1.tar+gzip"
	artifactType   = "application/vnd.qory.runtime.v1"
	annotVersion   = "org.opencontainers.image.version"
	annotIntegrity = "dev.qory.runtime.integrity"
	// annotSignatures is npm's signatures of the layer's tarball, a JSON array of
	// {"keyid": "<keyid>", "sig": "<sig>"} in npm's order, see [decodeSignatures].
	annotSignatures = "dev.qory.runtime.signatures"
	// newestTag is the tag that moves to each new version.
	newestTag = "newest"
)

// The limits of what the registry fetch reads.
const (
	// maxManifest is the most bytes of a manifest or an index, as registries limit them.
	maxManifest = 4 << 20
	// maxToken is the most bytes of a token realm's response.
	maxToken = 1 << 20
	// maxBundle is the most bytes of the run's CA bundle that [ProxyClient] reads.
	maxBundle = 16 << 20
)

// textCache is the first line of each refusal of what the registry's runtime cache
// contains: the fix. The cause follows on the second line.
const textCache = "Ask Qory Apiary's instance admin to check the registry's runtime cache\n"

// ErrNotCached is the error of a registry that returned 404 for newest: its cache never
// held the runtime, and the publisher was unreachable when the registry asked.
var ErrNotCached = errors.New("the registry has no copy of the runtime")

// RegistryFetch is a [Fetch] from the runtime cache of the instance's registry at host,
// a host and a port or a host, over http for a host on loopback and https for any
// other, through client, for the platform linux/arch.
//
// It reads newest, then the image manifest of linux/arch by its digest, then <version>
// from newest's annotation org.opencontainers.image.version, which must have newest's
// digest, then the manifest of linux/arch again: see [registry.resolve] for why. The
// release's Open reads the layer by its digest, and its reader returns an error when the
// bytes do not match the layer's size or sha256 digest; Install checks the sha512
// checksum on top. A 401 with a Bearer
// challenge gets a token for repository:shared/runtimes/<runtime>:pull from the
// challenge's realm, once. The realm must be Qory Apiary's token route,
// <instance>/registry/token, where instance is the address of the Qory Apiary instance
// that the run already uses, scheme, host and port: the registry is a different host from
// the instance, and the run's proxy sets the run's credentials on a request by its host.
// With no instance, "", no realm is taken.
func RegistryFetch(client *http.Client, host, arch, instance string) Fetch {
	return func(ctx context.Context, runtime string) (Release, error) {
		if arch != "amd64" && arch != "arm64" {
			return Release{}, fmt.Errorf("Run on an amd64 or arm64 machine\nThe registry has %s for linux/amd64 and linux/arm64, and this machine is %s", runtime, arch)
		}
		if _, ok := npmPackages[runtime]; !ok {
			return Release{}, fmt.Errorf("Install %s some other way: qory installs only %s", runtime, strings.Join(Runtimes(), " and "))
		}
		r := &registry{client: client, host: host, instance: instance, runtime: runtime, repository: "shared/runtimes/" + runtime}
		rel, err := r.resolve(ctx, arch)
		if err != nil {
			return Release{}, err
		}
		rel.Signed, _ = signedName(runtime, rel.Version, arch)
		rel.Program, rel.Folder = layout(runtime, arch)
		return rel, nil
	}
}

// registry is a runtime's repository at the instance's registry, and the Bearer token
// of its first challenge.
type registry struct {
	client                    *http.Client
	host, runtime, repository string
	// instance is the address of Qory Apiary, whose token route is the only realm.
	instance string
	token    string
	// noted and lastNote are whether, and when, the fetch last told its progress: see
	// [registry.note].
	noted    bool
	lastNote time.Time
}

// descriptor is a content an index or a manifest lists.
type descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType"`
	Annotations  map[string]string `json:"annotations"`
	Platform     *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

// manifest is an OCI image index or image manifest.
type manifest struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType"`
	Config       *descriptor       `json:"config"`
	Layers       []descriptor      `json:"layers"`
	Manifests    []descriptor      `json:"manifests"`
	Annotations  map[string]string `json:"annotations"`
}

// statusError is a registry's response to a request of what, with a status other than
// 200.
type statusError struct {
	what   string
	status int
}

func (e *statusError) Error() string { return fmt.Sprintf("%s returned %d", e.what, e.status) }

// notCachedError is [ErrNotCached] with its text.
type notCachedError struct{ text string }

func (e *notCachedError) Error() string { return e.text }

// Is reports whether target is [ErrNotCached].
func (e *notCachedError) Is(target error) bool { return target == ErrNotCached }

// resolve reads newest, then the manifest of linux/arch by its digest, then the tag of
// newest's version, whose digest must be newest's, then the manifest of linux/arch by
// its digest again, and returns the release. A pull of a tag makes the registry copy
// only the index, and its garbage collection removes a tag whose index has no stored
// platform manifest; so each tag's read is followed by the read of the platform's
// manifest.
func (r *registry) resolve(ctx context.Context, arch string) (Release, error) {
	body, digest, err := r.manifest(ctx, newestTag, newestTag)
	var status *statusError
	if errors.As(err, &status) && status.status == http.StatusNotFound {
		return Release{}, &notCachedError{text: fmt.Sprintf("Check that the instance's registry reaches the publisher of %s, then run again\n"+
			"The registry has never held %s and could not fetch it: %s.", r.runtime, r.runtime, status)}
	}
	if err != nil {
		return Release{}, err
	}
	index, err := decode(body, mediaIndex)
	if err != nil {
		return Release{}, fmt.Errorf(textCache+"%s:%s is not a runtime's image index: %w", r.repository, newestTag, err)
	}
	version := index.Annotations[annotVersion]
	if !versionShape.MatchString(version) {
		return Release{}, fmt.Errorf(textCache+"%s:%s has the version %q, and a version is 1 to 64 letters, digits, ., + or -, the first a letter or a digit",
			r.repository, newestTag, version)
	}
	entry, err := r.platform(index, version, arch)
	if err != nil {
		return Release{}, err
	}
	layer, sigs, err := r.platformLayer(ctx, entry, version, arch)
	if err != nil {
		return Release{}, err
	}
	// The read of the version's tag counts as the version's pull, which keeps the tag
	// in the registry.
	_, again, err := r.manifest(ctx, version, version)
	if err != nil {
		return Release{}, err
	}
	if again != digest {
		return Release{}, fmt.Errorf(textCache+"%s:%s has the digest %s, and :%s has %s", r.repository, version, again, newestTag, digest)
	}
	if _, _, err := r.platformLayer(ctx, entry, version, arch); err != nil {
		return Release{}, err
	}
	return Release{
		Version:    version,
		Integrity:  layer.Annotations[annotIntegrity],
		Signatures: sigs,
		Open:       func(ctx context.Context) (io.ReadCloser, error) { return r.blob(ctx, layer, version) },
	}, nil
}

// platform is the one entry of index, the index of version, for linux/arch.
func (r *registry) platform(index manifest, version, arch string) (descriptor, error) {
	var entries []descriptor
	for _, e := range index.Manifests {
		if e.Platform != nil && e.Platform.OS == "linux" && e.Platform.Architecture == arch {
			entries = append(entries, e)
		}
	}
	if len(entries) != 1 || entries[0].MediaType != mediaManifest || !digestShape.MatchString(entries[0].Digest) {
		return descriptor{}, fmt.Errorf(textCache+"%s:%s has %d image manifests for linux/%s, not one", r.repository, version, len(entries), arch)
	}
	return entries[0], nil
}

// platformLayer reads the manifest of entry, the image manifest of version for
// linux/arch, by its digest, and returns its layer and npm's signatures of it.
func (r *registry) platformLayer(ctx context.Context, entry descriptor, version, arch string) (descriptor, []Signature, error) {
	what := fmt.Sprintf("the manifest of %s %s for linux/%s", r.runtime, version, arch)
	body, digest, err := r.manifest(ctx, entry.Digest, version)
	if err != nil {
		return descriptor{}, nil, err
	}
	if digest != entry.Digest || entry.Size != int64(len(body)) {
		return descriptor{}, nil, fmt.Errorf(textCache+"%s does not match its digest %s", upper(what), entry.Digest)
	}
	m, err := decode(body, mediaManifest)
	if err != nil {
		return descriptor{}, nil, fmt.Errorf(textCache+"%s is not a runtime's: %w", upper(what), err)
	}
	layer, sigs, err := runtimeLayer(m, version)
	if err != nil {
		return descriptor{}, nil, fmt.Errorf(textCache+"%s is not a runtime's: %w", upper(what), err)
	}
	return layer, sigs, nil
}

// runtimeLayer is the one layer of m, a runtime's manifest of version, with the
// annotations dev.qory.runtime.integrity and dev.qory.runtime.signatures, which Install
// checks, and the signatures, decoded strictly, see [decodeSignatures].
func runtimeLayer(m manifest, version string) (descriptor, []Signature, error) {
	switch {
	case m.ArtifactType != artifactType:
		return descriptor{}, nil, fmt.Errorf("its artifactType is %q, not %s", m.ArtifactType, artifactType)
	case m.Config == nil || m.Config.MediaType != mediaEmpty:
		return descriptor{}, nil, errors.New("its config is not the empty config")
	case m.Annotations[annotVersion] != version:
		return descriptor{}, nil, fmt.Errorf("its version is %q", m.Annotations[annotVersion])
	case len(m.Layers) != 1:
		return descriptor{}, nil, fmt.Errorf("it has %d layers, not one", len(m.Layers))
	}
	l := m.Layers[0]
	switch {
	case l.MediaType != mediaLayer:
		return descriptor{}, nil, fmt.Errorf("its layer's media type is %q, not %s", l.MediaType, mediaLayer)
	case !digestShape.MatchString(l.Digest):
		return descriptor{}, nil, errors.New("its layer's digest is not sha256: and 64 lower-case hexadecimal digits")
	case l.Size < 1 || l.Size > maxTarball:
		return descriptor{}, nil, fmt.Errorf("its layer's size is %d bytes, and a runtime's layer has 1 to %d", l.Size, maxTarball)
	case l.Annotations[annotIntegrity] == "":
		return descriptor{}, nil, fmt.Errorf("its layer has no %s", annotIntegrity)
	case l.Annotations[annotSignatures] == "":
		return descriptor{}, nil, fmt.Errorf("its layer has no %s", annotSignatures)
	}
	sigs, err := decodeSignatures([]byte(l.Annotations[annotSignatures]), true)
	if err != nil {
		return descriptor{}, nil, fmt.Errorf("its layer's %s is not npm's signatures: %w", annotSignatures, err)
	}
	return l, sigs, nil
}

// decode reads body as JSON of the media type want.
func decode(body []byte, want string) (manifest, error) {
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return manifest{}, errors.New("it is not JSON")
	}
	if m.MediaType != want {
		return manifest{}, fmt.Errorf("its media type is %q, not %s", m.MediaType, want)
	}
	return m, nil
}

// manifest is the manifest or index of ref, a tag or a digest, of version, or newest,
// and the sha256 digest of its bytes. A Docker-Content-Digest the registry sends must
// be that digest.
func (r *registry) manifest(ctx context.Context, ref, version string) ([]byte, string, error) {
	what := r.repository + ":" + ref
	if digestShape.MatchString(ref) {
		what = r.repository + "@" + ref
	}
	resp, err := r.get(ctx, "/v2/"+r.repository+"/manifests/"+url.PathEscape(ref), mediaIndex+", "+mediaManifest, what, version)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil {
		return nil, "", r.failed(what, err)
	}
	if len(body) > maxManifest {
		return nil, "", fmt.Errorf(textCache+"%s has more than %d bytes", upper(what), maxManifest)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" && got != digest {
		return nil, "", fmt.Errorf(textCache+"%s has the digest %s, and the registry sent %q", upper(what), digest, got)
	}
	return body, digest, nil
}

// blob opens the layer of version by its digest. Its reader checks the size and the
// sha256 digest as it reads: at the end of the bytes, or past the size, Read returns a
// [checkError] in place of io.EOF.
func (r *registry) blob(ctx context.Context, layer descriptor, version string) (io.ReadCloser, error) {
	what := fmt.Sprintf("the layer of %s %s", r.runtime, version)
	resp, err := r.get(ctx, "/v2/"+r.repository+"/blobs/"+layer.Digest, "", what, version)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != layer.Size {
		resp.Body.Close()
		return nil, fmt.Errorf(textCache+"%s has %d bytes, and its manifest lists %d", upper(what), resp.ContentLength, layer.Size)
	}
	return &checkedBlob{body: resp.Body, r: io.LimitReader(resp.Body, layer.Size+1), h: sha256.New(), layer: layer, what: what}, nil
}

// checkedBlob is a layer's bytes, checked against its size and sha256 digest.
type checkedBlob struct {
	body  io.ReadCloser
	r     io.Reader
	h     hash.Hash
	n     int64
	layer descriptor
	what  string
}

// Read reads the layer's bytes, and returns a *checkError once they exceed the layer's
// size, or at the end when their size or sha256 digest is not the layer's.
func (b *checkedBlob) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.h.Write(p[:n])
	b.n += int64(n)
	if b.n > b.layer.Size {
		return n, &checkError{fmt.Errorf(textCache+"%s has more bytes than its manifest's %d", upper(b.what), b.layer.Size)}
	}
	if err == io.EOF {
		if b.n != b.layer.Size {
			return n, &checkError{fmt.Errorf(textCache+"%s has %d bytes, and its manifest lists %d", upper(b.what), b.n, b.layer.Size)}
		}
		if "sha256:"+hex.EncodeToString(b.h.Sum(nil)) != b.layer.Digest {
			return n, &checkError{fmt.Errorf(textCache+"%s does not match its digest %s", upper(b.what), b.layer.Digest)}
		}
	}
	return n, err
}

// Close closes the response's body.
func (b *checkedBlob) Close() error { return b.body.Close() }

// The waits between the tries of a request that got 503 or 429 from the registry, or
// that got no response: 2, 4 and 8 seconds, then 15 seconds; a Retry-After of whole
// seconds replaces the wait, up to 30 seconds. The registry waits only 30 seconds for
// its cache to start serving, so a first fill of a version, two tarballs of about
// 170 MB with up to 10 minutes each, gets 503 until it is done. The tries of one fetch
// go on until the registry returns the file or the fetch's context ends: the run's time limit,
// its cancel or SIGTERM. A test replaces the wait.
var (
	retryWaits = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	retryEvery = 15 * time.Second
	// noteEvery is the time between two progress lines of a fetch that waits.
	noteEvery     = 5 * time.Minute
	now           = time.Now
	maxRetryAfter = 30 * time.Second
	sleep         = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

// wait is the wait before retry n+1 of a request, n counted from 0, with the response's
// Retry-After, "" for none.
func wait(n int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxRetryAfter)
	}
	if n < len(retryWaits) {
		return retryWaits[n]
	}
	return retryEvery
}

// get sends a GET of path to the registry, with Accept set to accept when it is not
// empty, and returns its response with the status 200. A 401 with a Bearer challenge gets a token from
// the challenge's realm, once, and the GET is sent again with it. A 503, a 429 or a
// request with no response is sent again after [wait], until the registry returns the file or
// ctx ends. what is the request as an error shows it.
func (r *registry) get(ctx context.Context, path, accept, what, version string) (*http.Response, error) {
	tried := false
	for n := 0; ; {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme(r.host)+"://"+r.host+path, nil)
		if err != nil {
			return nil, fmt.Errorf("Set pool.registry to an image name under the instance's registry\n%w", err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		resp, err := send(r.client, req)
		retryAfter := ""
		switch {
		case err != nil && (ctx.Err() != nil || refusedConnect(err)):
			return nil, r.failed(what, err)
		case err != nil:
		case resp.StatusCode == http.StatusOK:
			return resp, nil
		case resp.StatusCode == http.StatusUnauthorized && !tried:
			challenges := resp.Header.Values("WWW-Authenticate")
			drain(resp)
			tried = true
			if r.token, err = r.tokenFor(ctx, challenges); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
			retryAfter = resp.Header.Get("Retry-After")
			drain(resp)
		default:
			location := resp.Header.Get("Location")
			drain(resp)
			return nil, r.refused(what, resp.StatusCode, location)
		}
		d := wait(n, retryAfter)
		n++
		answer := "no response"
		if err == nil {
			answer = strconv.Itoa(resp.StatusCode)
		}
		r.note(ctx, version, answer)
		if err := sleep(ctx, d); err != nil {
			return nil, r.failed(what, err)
		}
	}
}

// note tells the fetch's progress, once the first retry starts and every [noteEvery]
// after that: the registry's host, the version waited for, "newest" when it is not known
// yet, and the status of the last response, "none" for no response. The line has no
// path, token, header or query.
func (r *registry) note(ctx context.Context, version, answer string) {
	report, _ := ctx.Value(progressKey{}).(func(string))
	t := now()
	if report == nil || r.noted && t.Sub(r.lastNote) < noteEvery {
		return
	}
	r.noted, r.lastNote = true, t
	subject := displayName(r.runtime)
	if version != "newest" {
		subject += " " + version
	}
	report(fmt.Sprintf("%s has not returned %s (%s); check pool.registry", r.host, subject, answer))
}

// displayName is the runtime's name as a person reads it.
func displayName(runtime string) string {
	switch runtime {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	}
	return runtime
}

// progressKey is the key of the context value that [Options.Progress] sets.
type progressKey struct{}

// refused is the error of a response with the status status, other than 200, to the
// request what; location is the response's Location header, which a redirect has.
func (r *registry) refused(what string, status int, location string) error {
	err := &statusError{what: what, status: status}
	if status >= 300 && status < 400 {
		// The install follows no redirect, so a layer that the registry serves from
		// another place fails, and nothing is read from that place.
		to := "the registry's host"
		if u, perr := url.Parse(location); perr == nil && u.Host != "" {
			to = u.Host
		}
		return fmt.Errorf("Ask Qory Apiary's instance admin to make the registry serve this with no redirect\n%w, which qory does not follow\nThe Location is on %s.", err, to)
	}
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf(textCache+"%w", err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Check that Qory Apiary lets the run pull %s\n%w", r.repository, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("Run again once the registry's rate limit resets\n%w", err)
	}
	return fmt.Errorf("Check that the run's proxy reaches the registry at %s\n%w", r.host, err)
}

// failed is the error of the request what that got no response.
func (r *registry) failed(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Run again with more time\nThe run ended before the registry at %s returned %s: %w", r.host, what, err)
	}
	return fmt.Errorf("Check that the run's proxy reaches the registry at %s\n%s failed: %w", r.host, what, err)
}

// tokenFor gets a pull token for the repository from the realm of the first Bearer
// challenge. The realm must be https, or http on loopback, and exactly Qory Apiary's
// token route: the scheme, host and port of the run's instance, [registry.instance], and
// the path [tokenPath]; an instance with a path is refused. The run's proxy sets the run's
// credentials on a request by its host, so a realm on another host, the registry's
// included, could have the request carry a credential meant for another service.
func (r *registry) tokenFor(ctx context.Context, challenges []string) (string, error) {
	params, ok := bearer(challenges)
	if !ok {
		return "", fmt.Errorf("Check that the registry at %s returns a Bearer challenge\nIt returned 401 with no Bearer challenge", r.host)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || realm.User != nil ||
		realm.Scheme != "https" && (realm.Scheme != "http" || scheme(realm.Host) != "http") {
		return "", fmt.Errorf("Check the registry at %s\nThe realm of its Bearer challenge is not an https URL, or http on loopback", r.host)
	}
	if r.instance == "" {
		return "", fmt.Errorf("Set gateway.server.url in forager.yaml to the address of Qory Apiary\nThe pool's forager.yaml sets no gateway.server.url, so qory cannot check the registry's token realm %s.", realm.Redacted())
	}
	want, err := tokenRoute(r.instance)
	if err != nil {
		return "", err
	}
	if !sameRoute(realm, want) {
		return "", fmt.Errorf("Ask Qory Apiary's instance admin to put the registry's token realm at %s\nThe registry's token realm is %s, and the instance's token route is %s.", want.Redacted(), realm.Redacted(), want.Redacted())
	}
	// The request is to the instance's token route, with the service and the scope alone:
	// nothing else of the realm, such as its query or fragment, goes with it.
	q := url.Values{}
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	q.Set("scope", "repository:"+r.repository+":pull")
	route := *want
	route.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, route.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := send(r.client, req)
	if err != nil {
		return "", fmt.Errorf("Check that the run's proxy reaches the token realm at %s\n%w", realm.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		drain(resp)
		return "", fmt.Errorf("Check that Qory Apiary lets the run pull %s\nThe token realm at %s returned %d", r.repository, realm.Host, resp.StatusCode)
	}
	var reply struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxToken)).Decode(&reply) != nil || reply.Token == "" && reply.AccessToken == "" {
		return "", fmt.Errorf("Check the token realm at %s\nIts response contains no token", realm.Host)
	}
	if reply.Token == "" {
		return reply.AccessToken, nil
	}
	return reply.Token, nil
}

// tokenPath is the path of Qory Apiary's token route, after the instance's address.
const tokenPath = "/registry/token"

// tokenRoute is the token route of the instance at the address instance, a URL: its
// scheme, host and port, and the path tokenPath. The error is for an instance that is no
// http or https URL with a host, or that has a path.
func tokenRoute(instance string) (*url.URL, error) {
	u, err := url.Parse(instance)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("Set the address of Qory Apiary to an https URL\nThe run's address of Qory Apiary is not one.")
	}
	if strings.TrimSuffix(u.Path, "/") != "" {
		// [CheckInstance] refuses a path too: the route is on the instance's host.
		return nil, fmt.Errorf("Set gateway.server.url in forager.yaml to an address with no path\nThe address is %q.", instance)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: tokenPath}, nil
}

// sameRoute reports whether realm is the token route want: the same scheme, host and
// path, and the same port, where a missing port is the scheme's default.
func sameRoute(realm, want *url.URL) bool {
	return realm.Scheme == want.Scheme && authority(realm) == authority(want) && realm.Path == want.Path
}

// authority is the lower-case host of u with its port, the scheme's default when u has
// none.
func authority(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// drain reads up to 64 KiB of a response the caller does not use, and closes it.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// scheme is http for a host on loopback, and https for any other, as the container
// engine reaches a registry.
func scheme(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if loopback(host) {
		return "http"
	}
	return "https"
}

// loopback reports whether host, a name or an address with no port, is localhost or a
// loopback address.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bearer is the parameters of the first Bearer challenge, and whether there is one.
func bearer(values []string) (map[string]string, bool) {
	for _, v := range values {
		kind, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(kind, "Bearer") {
			return challengeParams(rest), true
		}
	}
	return nil, false
}

// challengeParams reads the key=value parameters of a challenge, separated by commas,
// each value quoted with backslash escapes or bare.
func challengeParams(s string) map[string]string {
	params := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t,")
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return params
		}
		key = strings.ToLower(strings.TrimSpace(key))
		rest = strings.TrimLeft(rest, " \t")
		var value bytes.Buffer
		if strings.HasPrefix(rest, `"`) {
			i := 1
			for ; i < len(rest) && rest[i] != '"'; i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				value.WriteByte(rest[i])
			}
			s = rest[min(i+1, len(rest)):]
		} else {
			end := strings.IndexByte(rest, ',')
			if end < 0 {
				end = len(rest)
			}
			value.WriteString(strings.TrimSpace(rest[:end]))
			s = rest[end:]
		}
		params[key] = value.String()
	}
}

// connectRefused is a CONNECT to host that the run's proxy returned 403 or 407 to: the
// run's policy does not allow the host, or the forwarder's password is wrong. It is
// never retried.
type connectRefused struct {
	status int
	host   string
}

func (e *connectRefused) Error() string {
	return "the run's proxy returned " + strconv.Itoa(e.status) + " to the CONNECT to " + e.host
}

// connectRefusal is the [connectRefused] in err, nil when err has none.
func connectRefusal(err error) *connectRefused {
	var c *connectRefused
	if errors.As(err, &c) {
		return c
	}
	return nil
}

// refusedConnect reports whether err is a [connectRefused].
func refusedConnect(err error) bool { return connectRefusal(err) != nil }

// ProxyClient is a client whose every request goes through the forwarder at proxyURL,
// http://qory:<password>@127.0.0.1:<port>, with the password as its
// Proxy-Authorization. With caBundle set, the client trusts the authorities of that PEM
// file alone, the machine's and the run's; without it, the machine's. It follows no
// redirect. It sets no time limit for a whole request, since a first fill of the
// registry's cache can take minutes, but a limit for a connection, a handshake, a
// response's headers and a wait for the next byte of a body, see [limitTransport]; the
// caller's context sets the rest.
func ProxyClient(proxyURL, caBundle string) (*http.Client, error) {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("Run again: the run's host proxy has no http URL")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caBundle != "" {
		f, err := os.Open(caBundle)
		if err != nil {
			return nil, fmt.Errorf("Run again: the run's CA bundle cannot be read: %w", err)
		}
		pem, err := io.ReadAll(io.LimitReader(f, maxBundle))
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("Run again: the run's CA bundle cannot be read: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("Run again: the run's CA bundle %s has no certificate", caBundle)
		}
		tlsConfig.RootCAs = roots
	}
	transport := &http.Transport{
		Proxy:             http.ProxyURL(u),
		TLSClientConfig:   tlsConfig,
		DisableKeepAlives: true,
		OnProxyConnectResponse: func(_ context.Context, _ *url.URL, connect *http.Request, resp *http.Response) error {
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired {
				return &connectRefused{status: resp.StatusCode, host: connect.Host}
			}
			return nil
		},
	}
	limitTransport(transport)
	return &http.Client{Transport: transport, CheckRedirect: noRedirect}, nil
}
