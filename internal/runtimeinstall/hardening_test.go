package runtimeinstall_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// TestTwoInstallsOfOneVersionReadOneTarball starts two installs of claude 2.1.320 on one
// folder while the first reads its tarball: the second waits for the first and uses its
// folder, so the source opens one tarball, and one install reports a download.
func TestTwoInstallsOfOneVersionReadOneTarball(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	reading, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fetch := func(ctx context.Context, runtime string) (runtimeinstall.Release, error) {
		rel, err := in.src.Fetch(ctx, runtime)
		open := rel.Open
		rel.Open = func(ctx context.Context) (io.ReadCloser, error) {
			once.Do(func() {
				close(reading)
				<-release
			})
			return open(ctx)
		}
		return rel, err
	}
	results := make(chan runtimeinstall.Installed, 2)
	errs := make(chan error, 2)
	run := func() {
		got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: fetch, Runtime: "claude", Folder: in.folder})
		results <- got
		errs <- err
	}
	go run()
	<-reading
	go run()
	// The second install fetched the release, and waits at the install lock.
	for in.src.Fetches() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	close(release)
	downloaded := 0
	for range 2 {
		got, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		defer got.Release()
		if got.Downloaded {
			downloaded++
		}
		if !exists(got.Program()) {
			t.Errorf("%s is not there", got.Program())
		}
	}
	if in.src.Opens() != 1 || downloaded != 1 {
		t.Fatalf("%d tarballs opened, %d installs downloaded; want 1 and 1", in.src.Opens(), downloaded)
	}
	if got := files(t, in.folder); len(got) != len(installedFiles) {
		t.Errorf("files = %q, want %q", got, installedFiles)
	}
}

// TestAWaitingInstallEndsWithTheRun makes a second install wait for the first, which
// reads its tarball, and ends the second install's time: it returns the fix to run
// again with more time, and the first install finishes.
func TestAWaitingInstallEndsWithTheRun(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	reading, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := func(ctx context.Context, runtime string) (runtimeinstall.Release, error) {
		rel, err := in.src.Fetch(ctx, runtime)
		open := rel.Open
		rel.Open = func(ctx context.Context) (io.ReadCloser, error) {
			once.Do(func() {
				close(reading)
				<-release
			})
			return open(ctx)
		}
		return rel, err
	}
	done := make(chan error, 1)
	go func() {
		got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: slow, Runtime: "claude", Folder: in.folder})
		got.Release()
		done <- err
	}()
	<-reading
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := runtimeinstall.Install(ctx, runtimeinstall.Options{Fetch: in.src.Fetch, Runtime: "claude", Folder: in.folder})
	if err == nil || !strings.HasPrefix(err.Error(), "run again with more time\nThe run ended while another run installed claude 2.1.320: ") {
		t.Fatalf("err = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestADirectClientFollowsNoRedirect has a registry that redirects, within its host, the
// request that carries the pull token: the direct client returns the redirect as the
// response, so the install fails and the token goes to no other path.
func TestADirectClientFollowsNoRedirect(t *testing.T) {
	var mu sync.Mutex
	var elsewhere []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/registry/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": rt.Token})
		case r.URL.Path == "/elsewhere":
			elsewhere = append(elsewhere, r.Header.Get("Authorization"))
		case r.Header.Get("Authorization") == "":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/registry/token"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(runtimeinstall.DirectClient(), strings.TrimPrefix(srv.URL, "http://"), "amd64", srv.URL),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "returned 302") {
		t.Fatalf("err = %v, want one that says the registry returned 302", err)
	}
	if len(elsewhere) != 0 {
		t.Fatalf("the client followed the redirect with %q", elsewhere)
	}
}

// TestNpmFollowsARedirectOnlyToItsSchemeAndHost redirects npm's dist-tags within its
// host and scheme, which the fetch follows, and to https on the same host, which it
// does not.
func TestNpmFollowsARedirectOnlyToItsSchemeAndHost(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/-/package/@anthropic-ai/claude-code/dist-tags":
			http.Redirect(w, r, "/moved/tags", http.StatusFound)
		case "/moved/tags":
			_, _ = io.WriteString(w, `{"latest":"2.1.320"}`)
		case "/https/-/package/@anthropic-ai/claude-code/dist-tags":
			http.Redirect(w, r, "https://"+host+"/moved/tags", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")
	_, err := runtimeinstall.NpmFetch(http.DefaultClient, srv.URL, "amd64")(context.Background(), "claude")
	if err == nil || !strings.Contains(err.Error(), "@anthropic-ai/claude-code-linux-x64 2.1.320 returned 404") {
		t.Fatalf("a redirect within the host: err = %v, want the 404 of the platform's document, after the dist-tags", err)
	}
	_, err = runtimeinstall.NpmFetch(http.DefaultClient, srv.URL+"/https", "amd64")(context.Background(), "claude")
	if err == nil || !strings.Contains(err.Error(), "a redirect leads to another host or scheme") {
		t.Fatalf("a redirect to https: err = %v, want the redirect refused", err)
	}
}

// TestTheClientsHaveTimeouts reads the transports of both clients: each has a limit
// for the TLS handshake and for the response's headers.
func TestTheClientsHaveTimeouts(t *testing.T) {
	proxy, err := runtimeinstall.ProxyClient("http://qory:x@127.0.0.1:1", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*http.Client{"direct": runtimeinstall.DirectClient(), "proxy": proxy} {
		tr, ok := c.Transport.(*http.Transport)
		if !ok || tr.TLSHandshakeTimeout != 30*time.Second || tr.ResponseHeaderTimeout != 60*time.Second || tr.DialContext == nil {
			t.Errorf("%s: transport = %+v, want a 30 s handshake limit, a 60 s limit for headers and a dialer", name, tr)
		}
		if c.CheckRedirect == nil {
			t.Errorf("%s: the client follows redirects", name)
		}
	}
}

// stalling is a server that sends a response's headers and the start of its body, then
// sends no byte until the test ends.
type stalling struct {
	srv  *httptest.Server
	stop chan struct{}
}

func newStalling(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, stall func()) bool) *stalling {
	s := &stalling{stop: make(chan struct{})}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stall := func() {
			w.(http.Flusher).Flush()
			<-s.stop
		}
		if !handler(w, r, stall) {
			http.NotFound(w, r)
		}
	}))
	// Registered after the server's, so it runs first and frees the handlers.
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { close(s.stop) })
	return s
}

// TestAResponseThatStopsSendingEndsTheInstall has a server stop sending in the middle of
// a response's body: of the dist-tags, of the tarball from npm, and of a manifest from a
// registry. The install ends after the stall limit, 200 ms here, with an error that
// says which read stopped, though the run has no time limit.
func TestAResponseThatStopsSendingEndsTheInstall(t *testing.T) {
	defer runtimeinstall.SetStallLimit(200 * time.Millisecond)()
	tarball := rt.PlainTarball(t, "2.1.320")
	var url string
	npm := func(stallOn string) func(w http.ResponseWriter, r *http.Request, stall func()) bool {
		return func(w http.ResponseWriter, r *http.Request, stall func()) bool {
			switch r.URL.Path {
			case "/-/package/@anthropic-ai/claude-code/dist-tags":
				if stallOn == "tags" {
					_, _ = io.WriteString(w, `{"lat`)
					stall()
					return true
				}
				_, _ = io.WriteString(w, `{"latest":"2.1.320"}`)
			case "/@anthropic-ai/claude-code-linux-x64/2.1.320":
				_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.1.320", "dist": map[string]string{"integrity": rt.Integrity(tarball), "tarball": url + "/claude.tgz"}})
			case "/claude.tgz":
				_, _ = w.Write(tarball[:10])
				stall()
			default:
				return false
			}
			return true
		}
	}
	for _, c := range []struct {
		name  string
		serve func(w http.ResponseWriter, r *http.Request, stall func()) bool
		fetch func(url string) runtimeinstall.Fetch
		want  string
	}{
		{"dist-tags", npm("tags"), func(u string) runtimeinstall.Fetch { return runtimeinstall.NpmFetch(http.DefaultClient, u, "amd64") },
			"run again\nThe read of the dist-tags of @anthropic-ai/claude-code from "},
		{"tarball", npm(""), func(u string) runtimeinstall.Fetch { return runtimeinstall.NpmFetch(http.DefaultClient, u, "amd64") },
			"run again\nThe download of claude 2.1.320 stopped: the response sent no byte for 200ms"},
		{"manifest", func(w http.ResponseWriter, _ *http.Request, stall func()) bool {
			_, _ = io.WriteString(w, `{"sche`)
			stall()
			return true
		}, func(u string) runtimeinstall.Fetch {
			return runtimeinstall.RegistryFetch(http.DefaultClient, strings.TrimPrefix(u, "http://"), "amd64", "")
		}, "check that the run's proxy reaches the registry at "},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStalling(t, c.serve)
			url = s.srv.URL
			begun := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := runtimeinstall.Install(ctx, runtimeinstall.Options{Fetch: c.fetch(s.srv.URL), Runtime: "claude", Folder: t.TempDir()})
			if err == nil || !strings.HasPrefix(err.Error(), c.want) || !strings.Contains(err.Error(), "the response sent no byte for 200ms") {
				t.Fatalf("err = %v\nwant it to start with %q and say that the response sent no byte for 200ms", err, c.want)
			}
			if took := time.Since(begun); took > 3*time.Second {
				t.Fatalf("the install took %s", took)
			}
		})
	}
}
