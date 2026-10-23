// Package proxygittest serves git repositories over https on this machine, behind a
// stand-in for a run's host proxy, for the tests of the fetches through it. The
// repositories are at https://git.test/<path>.git, a host no resolver knows, so a git
// reaches them only through the proxy, which takes only CONNECT requests with the
// proxy's password and leads only to the server. The server's certificate is from a
// certificate authority the test makes, which the CA bundle file contains.
package proxygittest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/proxygit"
)

// Host is the host the server answers for.
const Host = "git.test"

// Password is the password of the stand-in proxy.
const Password = "proxygittest-password-0123456789"

// Server is git over https behind a stand-in host proxy.
type Server struct {
	// Root is the folder the repositories are in, each at <path>.git.
	Root string
	// Proxy is the host proxy as a fetch uses it: its URL with [Password], and the CA
	// bundle.
	Proxy proxygit.Proxy

	mu      sync.Mutex
	tunnels int
	denied  int
}

// Start serves the repositories under a new folder, [Server.Root], until the test ends.
// It skips the test when git or git http-backend is not there.
func Start(t *testing.T) *Server {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git http-backend is not installed")
	}
	s := &Server{Root: t.TempDir()}
	caPEM, cert := certificates(t)
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	if err := os.WriteFile(bundle, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:   &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + s.Root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}, Stderr: io.Discard},
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxy.Close() })
	go s.serveProxy(proxy, ln.Addr().String())
	s.Proxy = proxygit.Proxy{URL: "http://qory:" + Password + "@" + proxy.Addr().String(), CABundle: bundle}
	return s
}

// URL is the https URL of the repository at path.
func (s *Server) URL(path string) string { return "https://" + Host + "/" + path + ".git" }

// Tunnels is how many connections the proxy led to the server.
func (s *Server) Tunnels() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tunnels
}

// Denied is how many requests the proxy answered with no tunnel.
func (s *Server) Denied() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied
}

// serveProxy answers each connection of ln: a CONNECT to [Host]:443 with the password
// is led to the server at addr, and any other request gets a 407 or a 403.
func (s *Server) serveProxy(ln net.Listener, addr string) {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("qory:"+Password))
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			br := bufio.NewReader(c)
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			switch {
			case req.Header.Get("Proxy-Authorization") != want:
				s.deny()
				io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"qory\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				return
			case req.Method != http.MethodConnect || req.Host != Host+":443":
				s.deny()
				io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				return
			}
			up, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer up.Close()
			s.mu.Lock()
			s.tunnels++
			s.mu.Unlock()
			io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
			done := make(chan struct{}, 2)
			go func() { io.Copy(up, br); done <- struct{}{} }()
			go func() { io.Copy(c, up); done <- struct{}{} }()
			<-done
		}()
	}
}

func (s *Server) deny() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denied++
}

// Repository makes the bare repository <path>.git under Root from a new repository with
// one commit on branch, its default, that has files, and returns that commit.
func (s *Server) Repository(t *testing.T, path, branch string, files map[string]string) string {
	t.Helper()
	return s.RepositoryWithLinks(t, path, branch, files, nil)
}

// RepositoryWithLinks is [Server.Repository] with links as well: each key of links is
// the path of a symbolic link in the commit, and its value is the link's target.
func (s *Server) RepositoryWithLinks(t *testing.T, path, branch string, files, links map[string]string) string {
	t.Helper()
	work := t.TempDir()
	Git(t, work, "init", "--quiet", "-b", branch)
	for name, content := range files {
		file := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		link := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	Git(t, work, "add", "-A")
	Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "first")
	dest := filepath.Join(s.Root, filepath.FromSlash(path)+".git")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, s.Root, "clone", "--quiet", "--bare", work, dest)
	return Git(t, work, "rev-parse", "HEAD")
}

// Git runs git in dir with args, with no configuration of the user's or the system's,
// and returns its output, trimmed; it fails the test on an error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// certificates makes a certificate authority and a certificate for [Host] it signed, and
// returns the authority's certificate as PEM and the server's certificate with its key.
func certificates(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "proxygittest CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: Host},
		DNSNames:     []string{Host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key}
}
