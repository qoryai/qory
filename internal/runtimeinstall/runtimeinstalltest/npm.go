package runtimeinstalltest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Npm stands in for the npm registry on loopback: the dist-tags of
// @anthropic-ai/claude-code, the documents of the versions of
// @anthropic-ai/claude-code-linux-x64 and -arm64, and their tarballs.
type Npm struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	latest      string
	versions    map[string]npmVersion
	paths       []string
	tarballGets int
}

// npmVersion is a published version of a platform's package.
type npmVersion struct {
	integrity, tarball string
	body               []byte
}

// The packages the stub publishes.
const (
	npmTags     = "@anthropic-ai/claude-code"
	npmPlatform = "@anthropic-ai/claude-code-linux-"
)

// StartNpm starts an npm stub on loopback until the test ends.
func StartNpm(t *testing.T) *Npm {
	n := &Npm{t: t, versions: map[string]npmVersion{}}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.srv.Close)
	return n
}

// URL is the stub's base URL, http on loopback.
func (n *Npm) URL() string { return n.srv.URL }

// Publish publishes version for x64 and arm64, each tarball's program the text
// "<version> <amd64 or arm64>", see [PlainTarball], and makes it latest.
func (n *Npm) Publish(version string) {
	for cpu, arch := range map[string]string{"x64": "amd64", "arm64": "arm64"} {
		b := PlainTarball(n.t, version+" "+arch)
		n.PublishWith(cpu, version, b, Integrity(b), "")
	}
	n.SetLatest(version)
}

// PublishWith publishes version of the platform's package for cpu, x64 or arm64, with
// tarball, integrity as its dist.integrity, and tarballURL as its dist.tarball; an
// empty tarballURL is the tarball's path on the stub.
func (n *Npm) PublishWith(cpu, version string, tarball []byte, integrity, tarballURL string) {
	if tarballURL == "" {
		tarballURL = n.srv.URL + "/" + npmPlatform + cpu + "/-/claude-code-linux-" + cpu + "-" + version + ".tgz"
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.versions[cpu+" "+version] = npmVersion{integrity: integrity, tarball: tarballURL, body: tarball}
}

// SetLatest sets the dist-tag latest to version.
func (n *Npm) SetLatest(version string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.latest = version
}

// Paths is the path of each request the stub got, in order.
func (n *Npm) Paths() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.paths...)
}

// TarballGets is how many tarballs the stub returned.
func (n *Npm) TarballGets() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tarballGets
}

func (n *Npm) serve(w http.ResponseWriter, req *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.paths = append(n.paths, req.URL.Path)
	if req.URL.Path == "/-/package/"+npmTags+"/dist-tags" {
		if n.latest == "" {
			http.NotFound(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"latest": n.latest})
		return
	}
	for key, v := range n.versions {
		cpu, version, _ := strings.Cut(key, " ")
		if req.URL.Path == "/"+npmPlatform+cpu+"/"+version {
			doc := map[string]any{"name": npmPlatform + cpu, "version": version, "dist": map[string]string{"integrity": v.integrity, "tarball": v.tarball}}
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		if strings.HasSuffix(v.tarball, req.URL.Path) && strings.HasSuffix(req.URL.Path, ".tgz") {
			n.tarballGets++
			_, _ = w.Write(v.body)
			return
		}
	}
	http.NotFound(w, req)
}
