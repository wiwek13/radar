package sessions

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend routes each slug to its own httptest server that answers with
// its context name, so a test can see exactly which session served a request.
type fakeBackend struct {
	mu       sync.Mutex
	targets  map[string]*url.URL
	states   map[string]State
	contexts map[string]string
	started  []string
	stopped  []string
	failing  map[string]bool
}

func newFakeBackend(t *testing.T, contexts ...string) *fakeBackend {
	t.Helper()
	b := &fakeBackend{targets: map[string]*url.URL{}, states: map[string]State{}, contexts: map[string]string{}}
	for _, name := range contexts {
		name := name
		slug := Slug(name)
		b.contexts[slug] = name
		b.states[slug] = StateStopped
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") == "websocket" {
				w.Header().Set("Upgrade", "websocket")
				w.Header().Set("Connection", "Upgrade")
				w.WriteHeader(http.StatusSwitchingProtocols)
				conn, rw, _ := w.(http.Hijacker).Hijack()
				defer conn.Close()
				_, _ = rw.WriteString(name + "\n")
				_ = rw.Flush()
				return
			}
			w.Header().Set("X-Served-By", name)
			w.Header().Set("X-Seen-Host", r.Host)
			w.Header().Set("X-Seen-Forwarded", r.Header.Get("X-Forwarded-For")+r.Header.Get("X-Forwarded-Host"))
			_, _ = io.WriteString(w, name+" "+r.URL.RequestURI())
		}))
		t.Cleanup(srv.Close)
		u, _ := url.Parse(srv.URL)
		b.targets[slug] = u
	}
	return b
}

func (b *fakeBackend) run(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[Slug(name)] = StateRunning
}

func (b *fakeBackend) Lookup(slug string) (*url.URL, State, string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	name, ok := b.contexts[slug]
	if !ok {
		return nil, StateStopped, "", false
	}
	if b.states[slug] != StateRunning {
		return nil, b.states[slug], name, true
	}
	return b.targets[slug], StateRunning, name, true
}

func (b *fakeBackend) Snapshot() ([]Info, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Info
	for slug, name := range b.contexts {
		out = append(out, Info{Context: name, Slug: slug, State: b.states[slug]})
	}
	return out, "dev", nil
}

func (b *fakeBackend) Start(name string) (Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started = append(b.started, name)
	if b.failing[name] {
		b.states[Slug(name)] = StateFailed
	} else {
		b.states[Slug(name)] = StateRunning
	}
	return Info{Context: name, Slug: Slug(name), State: StateStarting}, nil
}

func (b *fakeBackend) Stop(slug string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, slug)
	b.states[slug] = StateStopped
	return nil
}

func (b *fakeBackend) StopAll(except string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for slug := range b.contexts {
		if slug != except {
			b.states[slug] = StateStopped
			n++
		}
	}
	return n
}

func serve(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "localhost:9300"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEachSlugReachesOnlyItsOwnSession(t *testing.T) {
	b := newFakeBackend(t, "dev", "prod")
	b.run("dev")
	b.run("prod")
	h := NewHandler(b, "dev")

	for _, name := range []string{"dev", "prod"} {
		rec := serve(h, "GET", "/s/"+Slug(name)+"/api/resources/pods?namespaces=a", nil)
		if got := rec.Header().Get("X-Served-By"); got != name {
			t.Fatalf("/s/%s/ served by %q", Slug(name), got)
		}
		if body := rec.Body.String(); body != name+" /s/"+Slug(name)+"/api/resources/pods?namespaces=a" {
			t.Fatalf("path or query rewritten on the way: %q", body)
		}
		if host := rec.Header().Get("X-Seen-Host"); host != "localhost:9300" {
			t.Fatalf("session saw Host %q, want the browser's", host)
		}
		if fwd := rec.Header().Get("X-Seen-Forwarded"); fwd != "" {
			t.Fatalf("proxy added forwarding headers: %q", fwd)
		}
	}
}

func TestPathTricksNeverCrossSessions(t *testing.T) {
	b := newFakeBackend(t, "dev", "prod")
	b.run("dev")
	b.run("prod")
	h := NewHandler(b, "dev")
	dev, prod := Slug("dev"), Slug("prod")

	for _, target := range []string{
		"/s/" + dev + "/../" + prod + "/api/x",
		"/s/" + dev + "/%2e%2e/" + prod + "/api/x",
		"/s/" + dev + "/..%2f" + prod + "/api/x",
	} {
		rec := serve(h, "GET", target, nil)
		if got := rec.Header().Get("X-Served-By"); got == "prod" {
			t.Fatalf("%s reached prod", target)
		}
	}
	if rec := serve(h, "GET", "/s/%64ev-x/api", nil); rec.Header().Get("X-Served-By") != "" {
		t.Fatal("encoded slug was routed")
	}
}

func TestUnknownAndMalformedSlugs(t *testing.T) {
	h := NewHandler(newFakeBackend(t, "dev"), "dev")
	for _, target := range []string{"/s/nope-00000000/", "/s/../", "/s/DEV/", "/s//api"} {
		if rec := serve(h, "GET", target, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestSlugWithoutSlashRedirects(t *testing.T) {
	h := NewHandler(newFakeBackend(t, "dev"), "dev")
	rec := serve(h, "GET", "/s/"+Slug("dev")+"?x=1", nil)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/s/"+Slug("dev")+"/?x=1" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRootOpensInitialContext(t *testing.T) {
	h := NewHandler(newFakeBackend(t, "dev"), "dev")
	rec := serve(h, "GET", "/", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/s/"+Slug("dev")+"/" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A tab left open on a stopped session must not start it again: stop has to
// mean stop. Only loading the page starts it.
func TestOnlyAPageLoadStartsASession(t *testing.T) {
	b := newFakeBackend(t, "dev")
	h := NewHandler(b, "dev")
	slug := Slug("dev")

	api := serve(h, "GET", "/s/"+slug+"/api/connection", map[string]string{"Sec-Fetch-Dest": "empty"})
	if api.Code != http.StatusServiceUnavailable || !strings.Contains(api.Body.String(), `"error_code":"session_stopped"`) {
		t.Fatalf("API call to stopped session = %d %s", api.Code, api.Body.String())
	}
	post := serve(h, "POST", "/s/"+slug+"/api/resources/apply", map[string]string{"Accept": "text/html"})
	if post.Code != http.StatusServiceUnavailable || len(b.started) != 0 {
		t.Fatalf("POST to stopped session = %d, started %v", post.Code, b.started)
	}

	page := serve(h, "GET", "/s/"+slug+"/", map[string]string{"Sec-Fetch-Dest": "document"})
	if page.Code != http.StatusOK || page.Header().Get("X-Served-By") != "dev" {
		t.Fatalf("page load = %d served by %q", page.Code, page.Header().Get("X-Served-By"))
	}
	if len(b.started) != 1 || b.started[0] != "dev" {
		t.Fatalf("page load started %v", b.started)
	}
}

func TestFailedSessionIsStartedOncePerPageLoad(t *testing.T) {
	b := newFakeBackend(t, "dev")
	b.failing = map[string]bool{"dev": true}
	h := NewHandler(b, "dev")

	page := serve(h, "GET", "/s/"+Slug("dev")+"/", map[string]string{"Sec-Fetch-Dest": "document"})
	if page.Code != http.StatusServiceUnavailable || !strings.Contains(page.Body.String(), "did not start") {
		t.Fatalf("page load = %d %q", page.Code, page.Body.String())
	}
	if len(b.started) != 1 {
		t.Fatalf("started %d times, want 1", len(b.started))
	}
}

func TestNoControllerPage(t *testing.T) {
	h := NewHandler(newFakeBackend(t, "dev"), "dev")
	for _, path := range []string{"/_sessions", "/_sessions/"} {
		if rec := serve(h, "GET", path, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestForeignHostIsRefused(t *testing.T) {
	b := newFakeBackend(t, "dev")
	b.run("dev")
	h := NewHandler(b, "dev")
	for _, host := range []string{"evil.example:9300", "127.0.0.1.nip.io:9300", "attacker.localhost.evil:9300"} {
		req := httptest.NewRequest("GET", "/s/"+Slug("dev")+"/api/x", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Served-By") != "" {
			t.Fatalf("Host %s: %d, served by %q", host, rec.Code, rec.Header().Get("X-Served-By"))
		}
	}
	for _, host := range []string{"localhost:9300", "127.0.0.1:9300", "[::1]:9300", "LOCALHOST:9300"} {
		if !loopbackHost(host) {
			t.Fatalf("loopbackHost(%q) = false", host)
		}
	}
}

func TestWritesRequireThisPage(t *testing.T) {
	b := newFakeBackend(t, "dev", "prod")
	b.run("prod")
	h := NewHandler(b, "dev")
	stop := "/_sessions/api/sessions/" + Slug("prod") + "/stop"

	refused := []map[string]string{
		{},
		{"X-Radar-Sessions": "1", "Sec-Fetch-Site": "cross-site"},
		{"X-Radar-Sessions": "1", "Sec-Fetch-Site": "same-site", "Origin": "http://localhost:3000"},
		{"X-Radar-Sessions": "1", "Origin": "http://localhost:3000"},
		{"X-Radar-Sessions": "1", "Origin": "null"},
	}
	for _, hdr := range refused {
		if rec := serve(h, "POST", stop, hdr); rec.Code != http.StatusForbidden {
			t.Fatalf("POST with %v = %d, want 403", hdr, rec.Code)
		}
	}
	if len(b.stopped) != 0 {
		t.Fatalf("a refused request stopped a session: %v", b.stopped)
	}
	if rec := serve(h, "GET", stop, map[string]string{"X-Radar-Sessions": "1"}); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on a write route = %d", rec.Code)
	}

	ok := map[string]string{"X-Radar-Sessions": "1", "Sec-Fetch-Site": "same-origin", "Origin": "http://localhost:9300"}
	if rec := serve(h, "POST", stop, ok); rec.Code != http.StatusOK {
		t.Fatalf("same-origin stop = %d %s", rec.Code, rec.Body.String())
	}
	if len(b.stopped) != 1 || b.stopped[0] != Slug("prod") {
		t.Fatalf("stopped = %v", b.stopped)
	}
	if rec := serve(h, "POST", "/_sessions/api/sessions/"+Slug("dev")+"/start", ok); rec.Code != http.StatusAccepted || b.started[0] != "dev" {
		t.Fatalf("start = %d, started %v", rec.Code, b.started)
	}
	if rec := serve(h, "POST", "/_sessions/api/sessions/nope-00000000/start", ok); rec.Code != http.StatusNotFound {
		t.Fatalf("start unknown = %d", rec.Code)
	}
}

func TestStateReadRefusesOtherSites(t *testing.T) {
	h := NewHandler(newFakeBackend(t, "dev"), "dev")
	for _, site := range []string{"cross-site", "same-site"} {
		if rec := serve(h, "GET", "/_sessions/api/state", map[string]string{"Sec-Fetch-Site": site}); rec.Code != http.StatusForbidden {
			t.Fatalf("state read from %s = %d", site, rec.Code)
		}
	}
	rec := serve(h, "GET", "/_sessions/api/state", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"controller":"radar-sessions"`) {
		t.Fatalf("state = %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebSocketUpgradeReachesOwnSession(t *testing.T) {
	b := newFakeBackend(t, "dev", "prod")
	b.run("dev")
	b.run("prod")
	srv := httptest.NewServer(NewHandler(b, "dev"))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	for _, name := range []string{"dev", "prod"} {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(addr)
		req := "GET /s/" + Slug(name) + "/api/pods/ns/p/exec HTTP/1.1\r\nHost: localhost:" + port +
			"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
		_, _ = conn.Write([]byte(req))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		r := bufio.NewReader(conn)
		status, _ := r.ReadString('\n')
		if !strings.Contains(status, "101") {
			t.Fatalf("%s upgrade status %q", name, status)
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		got, _ := r.ReadString('\n')
		conn.Close()
		if strings.TrimSpace(got) != name {
			t.Fatalf("websocket for %s answered by %q", name, got)
		}
	}
}
