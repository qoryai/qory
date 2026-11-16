package runtimeinstalltest

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Token is the token the stub registry's realm gives, and ProxyPassword the stand-in
// proxy's password.
const (
	Token         = "fake-pull-token"
	ProxyPassword = "fake-proxy-password-0123456789"
)

// Registry is a stub OCI registry on loopback with the repository
// shared/runtimes/claude and a token realm at /registry/token, which gives [Token] for
// the pull scope of that repository. It holds the runtime cache's layout: an image
// index per version, with an image manifest for each platform, whose one layer is
// npm's tarball.
type Registry struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	manifests   map[string][]byte
	blobs       map[string][]byte
	blobGets    int
	paths       []string
	unavailable int
	retryAfter  string
}

// StartRegistry starts a registry on loopback until the test ends.
func StartRegistry(t *testing.T) *Registry {
	r := &Registry{t: t, manifests: map[string][]byte{}, blobs: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

// Host is the registry's host and port.
func (r *Registry) Host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// Unavailable makes the next n requests to /v2/ that carry the token get 503, with
// Retry-After set to retryAfter when it is not empty; n below 0 makes every one.
func (r *Registry) Unavailable(n int, retryAfter string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unavailable, r.retryAfter = n, retryAfter
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path)
	if req.URL.Path == "/registry/token" {
		if req.URL.Query().Get("scope") != "repository:shared/runtimes/claude:pull" || req.URL.Query().Get("service") != "stub-registry" {
			http.Error(w, "wrong scope", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": Token})
		return
	}
	if req.Header.Get("Authorization") != "Bearer "+Token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.srv.URL+`/registry/token",service="stub-registry"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.unavailable != 0 {
		if r.unavailable > 0 {
			r.unavailable--
		}
		if r.retryAfter != "" {
			w.Header().Set("Retry-After", r.retryAfter)
		}
		http.Error(w, "the cache is filling", http.StatusServiceUnavailable)
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/shared/runtimes/claude/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	if ref, ok := strings.CutPrefix(rest, "manifests/"); ok {
		b, ok := r.manifests[ref]
		if !ok {
			http.NotFound(w, req)
			return
		}
		var m struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", m.MediaType)
		w.Header().Set("Docker-Content-Digest", Digest(b))
		_, _ = w.Write(b)
		return
	}
	if d, ok := strings.CutPrefix(rest, "blobs/"); ok {
		b, ok := r.blobs[d]
		if !ok {
			http.NotFound(w, req)
			return
		}
		r.blobGets++
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, req)
}

// put stores doc as JSON, a manifest under its digest and each tag, and returns its
// bytes and digest.
func (r *Registry) put(doc any, tags ...string) ([]byte, string) {
	b, err := json.Marshal(doc)
	if err != nil {
		r.t.Fatal(err)
	}
	d := Digest(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifests[d] = b
	for _, tag := range tags {
		r.manifests[tag] = b
	}
	return b, d
}

// Tag is the manifest that tag has, nil for none.
func (r *Registry) Tag(tag string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manifests[tag]
}

// SetTag sets tag to the manifest b.
func (r *Registry) SetTag(tag string, b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifests[tag] = b
}

// Layer is how [Registry.Publish] publishes one platform's layer: its tarball, the bytes
// the blob returns in place of it when Served is set, and the integrity annotation in
// place of the tarball's checksum when Integrity is set.
type Layer struct {
	Tarball, Served []byte
	Integrity       string
}

// Publish publishes version with a manifest for each architecture of layers, amd64 and
// arm64, tagged version and, with newest, newest.
func (r *Registry) Publish(version string, newest bool, layers map[string]Layer) {
	var entries []map[string]any
	for _, arch := range []string{"amd64", "arm64"} {
		l, ok := layers[arch]
		if !ok {
			continue
		}
		served := l.Served
		if served == nil {
			served = l.Tarball
		}
		integrity := l.Integrity
		if integrity == "" {
			integrity = Integrity(l.Tarball)
		}
		ld := Digest(l.Tarball)
		r.mu.Lock()
		r.blobs[ld] = served
		r.mu.Unlock()
		mb, md := r.put(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.manifest.v1+json",
			"artifactType":  "application/vnd.qory.runtime.v1",
			"config":        map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", "size": 2},
			"layers": []any{map[string]any{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": ld, "size": len(l.Tarball),
				"annotations": map[string]string{"dev.qory.runtime.integrity": integrity},
			}},
			"annotations": map[string]string{"org.opencontainers.image.version": version},
		})
		entries = append(entries, map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": md, "size": len(mb),
			"platform": map[string]string{"os": "linux", "architecture": arch},
		})
	}
	tags := []string{version}
	if newest {
		tags = append(tags, "newest")
	}
	r.put(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     entries,
		"annotations":   map[string]string{"org.opencontainers.image.version": version},
	}, tags...)
}

// PublishPlain publishes version as newest, for both architectures, each with
// [PlainTarball] whose program says the architecture.
func (r *Registry) PublishPlain(version string) {
	r.Publish(version, true, map[string]Layer{
		"amd64": {Tarball: PlainTarball(r.t, version+" amd64")},
		"arm64": {Tarball: PlainTarball(r.t, version+" arm64")},
	})
}

// BlobGets is how many blobs the registry returned.
func (r *Registry) BlobGets() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blobGets
}

// Paths is the path of each request the registry got, in order.
func (r *Registry) Paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// Proxy stands in for the run's host proxy: it forwards each request in absolute form
// to a loopback address that carries its password, and counts the requests it passed
// and refused. [Proxy.Deny] makes it refuse every request, a CONNECT included.
type Proxy struct {
	srv              *httptest.Server
	mu               sync.Mutex
	passed, refusals int
	deny             int
}

// StartProxy starts a proxy on loopback until the test ends.
func StartProxy(t *testing.T) *Proxy {
	p := &Proxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *Proxy) serve(w http.ResponseWriter, req *http.Request) {
	auth := &http.Request{Header: http.Header{"Authorization": req.Header.Values("Proxy-Authorization")}}
	user, pass, ok := auth.BasicAuth()
	p.mu.Lock()
	if p.deny != 0 {
		status := p.deny
		p.refusals++
		p.mu.Unlock()
		http.Error(w, "the run's policy does not allow "+req.Host, status)
		return
	}
	if !ok || user != "qory" || pass != ProxyPassword || !req.URL.IsAbs() {
		p.refusals++
		p.mu.Unlock()
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if ip := net.ParseIP(req.URL.Hostname()); ip == nil || !ip.IsLoopback() {
		p.refusals++
		p.mu.Unlock()
		http.Error(w, "the test proxy forwards only to loopback", http.StatusForbidden)
		return
	}
	p.passed++
	p.mu.Unlock()
	out, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.Header = req.Header.Clone()
	out.Header.Del("Proxy-Authorization")
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// Deny makes the proxy return status, 403 or 407, to every request from now on, a CONNECT
// included, as a proxy does for a host that the run's policy does not allow; 0 ends it.
func (p *Proxy) Deny(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deny = status
}

// URL is the proxy's URL with the user qory and [ProxyPassword].
func (p *Proxy) URL() string {
	u, _ := url.Parse(p.srv.URL)
	u.User = url.UserPassword("qory", ProxyPassword)
	return u.String()
}

// Passed is how many requests the proxy passed.
func (p *Proxy) Passed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.passed
}

// Refusals is how many requests the proxy refused.
func (p *Proxy) Refusals() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refusals
}
