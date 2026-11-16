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
// tarball as it was published, with npm's checksum in its annotation
// dev.qory.runtime.integrity.

// The media types and annotations of the registry's runtime cache.
const (
	mediaIndex     = "application/vnd.oci.image.index.v1+json"
	mediaManifest  = "application/vnd.oci.image.manifest.v1+json"
	mediaEmpty     = "application/vnd.oci.empty.v1+json"
	mediaLayer     = "application/vnd.oci.image.layer.v1.tar+gzip"
	artifactType   = "application/vnd.qory.runtime.v1"
	annotVersion   = "org.opencontainers.image.version"
	annotIntegrity = "dev.qory.runtime.integrity"
	// newestTag is the tag that moves to each new version.
	newestTag = "newest"
	// tokenPath is the path of Qory Apiary's token route, where the run's proxy sets the
	// run's registry credential.
	tokenPath = "/registry/token"
)

// The limits of what the registry fetch reads.
const (
	// maxManifest is the most bytes of a manifest or an index, as registries limit them.
	maxManifest = 4 << 20
	// maxToken is the most bytes of a token realm's answer.
	maxToken = 1 << 20
	// maxBundle is the most bytes of the run's CA bundle that [ProxyClient] reads.
	maxBundle = 16 << 20
)

// textCache starts each refusal of what the registry's runtime cache contains.
const textCache = "ask the instance admin to check the registry's runtime cache: "

// ErrNotCached is the error of a registry that answered 404 to newest: its cache never
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
// challenge's realm, once.
func RegistryFetch(client *http.Client, host, arch string) Fetch {
	return func(ctx context.Context, runtime string) (Release, error) {
		if arch != "amd64" && arch != "arm64" {
			return Release{}, fmt.Errorf("run on an amd64 or arm64 machine: the registry has %s for linux/amd64 and linux/arm64, and this machine is %s", runtime, arch)
		}
		r := &registry{client: client, host: host, runtime: runtime, repository: "shared/runtimes/" + runtime, started: now()}
		return r.resolve(ctx, arch)
	}
}

// registry is a runtime's repository at the instance's registry, and the Bearer token
// of its first challenge.
type registry struct {
	client                    *http.Client
	host, runtime, repository string
	token                     string
	// started is when the fetch started: its tries stop at [retryLimit] after it.
	started time.Time
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

// statusError is a registry's answer other than 200 to a request of what.
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
		return Release{}, &notCachedError{text: fmt.Sprintf("check that the instance's registry reaches the publisher of %s, then run again\n"+
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
	layer, err := r.platformLayer(ctx, entry, version, arch)
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
	if _, err := r.platformLayer(ctx, entry, version, arch); err != nil {
		return Release{}, err
	}
	return Release{
		Version:   version,
		Integrity: layer.Annotations[annotIntegrity],
		Open:      func(ctx context.Context) (io.ReadCloser, error) { return r.blob(ctx, layer, version) },
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
// linux/arch, by its digest, and returns its layer.
func (r *registry) platformLayer(ctx context.Context, entry descriptor, version, arch string) (descriptor, error) {
	what := fmt.Sprintf("the manifest of %s %s for linux/%s", r.runtime, version, arch)
	body, digest, err := r.manifest(ctx, entry.Digest, version)
	if err != nil {
		return descriptor{}, err
	}
	if digest != entry.Digest || entry.Size != int64(len(body)) {
		return descriptor{}, fmt.Errorf(textCache+"%s does not match its digest %s", what, entry.Digest)
	}
	m, err := decode(body, mediaManifest)
	if err != nil {
		return descriptor{}, fmt.Errorf(textCache+"%s is not a runtime's: %w", what, err)
	}
	layer, err := runtimeLayer(m, version)
	if err != nil {
		return descriptor{}, fmt.Errorf(textCache+"%s is not a runtime's: %w", what, err)
	}
	return layer, nil
}

// runtimeLayer is the one layer of m, a runtime's manifest of version, with the
// annotation dev.qory.runtime.integrity, which Install checks.
func runtimeLayer(m manifest, version string) (descriptor, error) {
	switch {
	case m.ArtifactType != artifactType:
		return descriptor{}, fmt.Errorf("its artifactType is %q, not %s", m.ArtifactType, artifactType)
	case m.Config == nil || m.Config.MediaType != mediaEmpty:
		return descriptor{}, errors.New("its config is not the empty config")
	case m.Annotations[annotVersion] != version:
		return descriptor{}, fmt.Errorf("its version is %q", m.Annotations[annotVersion])
	case len(m.Layers) != 1:
		return descriptor{}, fmt.Errorf("it has %d layers, not one", len(m.Layers))
	}
	l := m.Layers[0]
	switch {
	case l.MediaType != mediaLayer:
		return descriptor{}, fmt.Errorf("its layer's media type is %q, not %s", l.MediaType, mediaLayer)
	case !digestShape.MatchString(l.Digest):
		return descriptor{}, errors.New("its layer's digest is not sha256: and 64 lower-case hexadecimal digits")
	case l.Size < 1 || l.Size > maxTarball:
		return descriptor{}, fmt.Errorf("its layer's size is %d bytes, and a runtime's layer has 1 to %d", l.Size, maxTarball)
	case l.Annotations[annotIntegrity] == "":
		return descriptor{}, fmt.Errorf("its layer has no %s", annotIntegrity)
	}
	return l, nil
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
		return nil, "", fmt.Errorf(textCache+"%s has more than %d bytes", what, maxManifest)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" && got != digest {
		return nil, "", fmt.Errorf(textCache+"%s has the digest %s, and the registry sent %q", what, digest, got)
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
		return nil, fmt.Errorf(textCache+"%s has %d bytes, and its manifest says %d", what, resp.ContentLength, layer.Size)
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
		return n, &checkError{fmt.Errorf(textCache+"%s has more bytes than its manifest's %d", b.what, b.layer.Size)}
	}
	if err == io.EOF {
		if b.n != b.layer.Size {
			return n, &checkError{fmt.Errorf(textCache+"%s has %d bytes, and its manifest says %d", b.what, b.n, b.layer.Size)}
		}
		if "sha256:"+hex.EncodeToString(b.h.Sum(nil)) != b.layer.Digest {
			return n, &checkError{fmt.Errorf(textCache+"%s does not match its digest %s", b.what, b.layer.Digest)}
		}
	}
	return n, err
}

// Close closes the answer's body.
func (b *checkedBlob) Close() error { return b.body.Close() }

// The waits between the tries of a request that the registry answered 503 or 429, or
// that got no answer: 2, 4 and 8 seconds, then 15 seconds; a Retry-After of whole
// seconds replaces the wait, up to 30 seconds. The registry waits only 30 seconds for
// its cache to start answering, so a first fill of a version, two tarballs of about
// 170 MB with up to 10 minutes each, gets 503 until it is done. The tries of one fetch
// stop after [retryLimit] in all; a test shortens it and replaces the clock.
var (
	retryWaits    = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	retryEvery    = 15 * time.Second
	maxRetryAfter = 30 * time.Second
	retryLimit    = 25 * time.Minute
	now           = time.Now
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

// wait is the wait before retry n+1 of a request, n counted from 0, with the answer's
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
// empty, and returns its answer of 200. A 401 with a Bearer challenge gets a token from
// the challenge's realm, once, and the GET is sent again with it. A 503, a 429 or a
// request with no answer is sent again after [wait], until [retryLimit] after the
// fetch started. what is the request as an error shows it, and ref the runtime's
// version, or newest, that it is for.
func (r *registry) get(ctx context.Context, path, accept, what, ref string) (*http.Response, error) {
	tried := false
	for n := 0; ; {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme(r.host)+"://"+r.host+path, nil)
		if err != nil {
			return nil, fmt.Errorf("set pool.registry to an image name under the instance's registry: %w", err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		resp, err := r.client.Do(req)
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
			drain(resp)
			return nil, r.refused(what, resp.StatusCode)
		}
		d := wait(n, retryAfter)
		n++
		if now().Add(d).After(r.started.Add(retryLimit)) {
			cause := "it did not answer"
			if err == nil {
				cause = "it answered " + strconv.Itoa(resp.StatusCode)
			}
			if err != nil {
				cause += " (" + err.Error() + ")"
			}
			return nil, fmt.Errorf("run again later: %s is still filling its cache of %s %s\n"+
				"The registry's cache had no %s after %s: %s.", r.host, r.runtime, ref, what, retryLimit, cause)
		}
		if err := sleep(ctx, d); err != nil {
			return nil, r.failed(what, err)
		}
	}
}

// refused is the error of an answer of status, other than 200, to the request what.
func (r *registry) refused(what string, status int) error {
	err := &statusError{what: what, status: status}
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf(textCache+"%w", err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("check that Qory Apiary lets the run pull %s: %w", r.repository, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("run again once the registry's rate limit resets: %w", err)
	}
	return fmt.Errorf("check that the run's proxy reaches the registry at %s: %w", r.host, err)
}

// failed is the error of the request what that got no answer.
func (r *registry) failed(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("run again with more time: the run ended before the registry at %s answered %s: %w", r.host, what, err)
	}
	return fmt.Errorf("check that the run's proxy reaches the registry at %s: %s failed: %w", r.host, what, err)
}

// tokenFor gets a pull token for the repository from the realm of the first Bearer
// challenge. The realm must be https, or http on loopback, and either on the registry's
// host and port or at Qory Apiary's token route, [tokenPath]: the run's proxy sets the
// run's credentials on a request by its host and path, so a realm elsewhere could have
// the request carry a credential meant for another service.
func (r *registry) tokenFor(ctx context.Context, challenges []string) (string, error) {
	params, ok := bearer(challenges)
	if !ok {
		return "", fmt.Errorf("check that the registry at %s asks for a Bearer token: it returned 401 with no Bearer challenge", r.host)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || realm.User != nil ||
		realm.Scheme != "https" && (realm.Scheme != "http" || scheme(realm.Host) != "http") {
		return "", fmt.Errorf("check the registry at %s: the realm of its Bearer challenge is not an https URL, or http on loopback", r.host)
	}
	if realm.Host != r.host && realm.Path != tokenPath {
		return "", fmt.Errorf("check the registry at %s: its token realm %s is neither on the registry's host nor at %s", r.host, realm.Redacted(), tokenPath)
	}
	q := realm.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	q.Set("scope", "repository:"+r.repository+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check that the run's proxy reaches the token realm at %s: %w", realm.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		drain(resp)
		return "", fmt.Errorf("check that Qory Apiary lets the run pull %s: the token realm at %s returned %d", r.repository, realm.Host, resp.StatusCode)
	}
	var answer struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxToken)).Decode(&answer) != nil || answer.Token == "" && answer.AccessToken == "" {
		return "", fmt.Errorf("check the token realm at %s: its answer contains no token", realm.Host)
	}
	if answer.Token == "" {
		return answer.AccessToken, nil
	}
	return answer.Token, nil
}

// drain reads up to 64 KiB of an answer the caller does not use, and closes it.
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

// connectRefused is a CONNECT that the run's proxy answered 403 or 407: the run's
// policy does not allow the host, or the forwarder's password is wrong. It is never
// retried.
type connectRefused struct{ status int }

func (e *connectRefused) Error() string {
	return "the run's proxy answered " + strconv.Itoa(e.status) + " to the connection"
}

// refusedConnect reports whether err is a [connectRefused].
func refusedConnect(err error) bool {
	var c *connectRefused
	return errors.As(err, &c)
}

// ProxyClient is a client whose every request goes through the forwarder at proxyURL,
// http://qory:<password>@127.0.0.1:<port>, with the password as its
// Proxy-Authorization. With caBundle set, the client trusts the authorities of that PEM
// file alone, the machine's and the run's; without it, the machine's. It follows no
// redirect and sets no time limit: the caller's context sets it, since a first fill of
// the registry's cache can take minutes.
func ProxyClient(proxyURL, caBundle string) (*http.Client, error) {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("run again: the run's host proxy has no http URL")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caBundle != "" {
		f, err := os.Open(caBundle)
		if err != nil {
			return nil, fmt.Errorf("run again: the run's CA bundle cannot be read: %w", err)
		}
		pem, err := io.ReadAll(io.LimitReader(f, maxBundle))
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("run again: the run's CA bundle cannot be read: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("run again: the run's CA bundle %s has no certificate", caBundle)
		}
		tlsConfig.RootCAs = roots
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(u),
			TLSClientConfig:   tlsConfig,
			DisableKeepAlives: true,
			OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, resp *http.Response) error {
				if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired {
					return &connectRefused{status: resp.StatusCode}
				}
				return nil
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
