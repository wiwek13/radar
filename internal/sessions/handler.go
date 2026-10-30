package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Backend is what the HTTP layer needs from the Manager.
type Backend interface {
	Lookup(slug string) (target *url.URL, state State, contextName string, known bool)
	Snapshot() ([]Info, string, error)
	Start(contextName string) (Info, error)
	Stop(slug string) error
	StopAll(except string) int
}

// ControllerName marks the state API so the Radar UI can tell it is behind
// this controller and not some other server answering the same path.
const ControllerName = "radar-sessions"

// csrfHeader must accompany every write. A page on another site cannot set
// it without a CORS preflight, and the controller answers none.
const csrfHeader = "X-Radar-Sessions"

// pageStartWait bounds how long a page load waits for its session's Radar
// to answer. Radar serves its UI before the cluster connects, so this only
// covers the process starting.
var pageStartWait = 90 * time.Second

type targetKey struct{}

type handler struct {
	backend Backend
	initial string
	proxy   *httputil.ReverseProxy
}

// NewHandler serves the session controller. initial is the context "/"
// opens; it must be a kubeconfig context.
func NewHandler(b Backend, initial string) http.Handler {
	h := &handler{backend: b, initial: initial}
	h.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(pr.In.Context().Value(targetKey{}).(*url.URL))
			// Radar compares the browser's Origin with Host; keeping the
			// address the browser used keeps those checks meaningful.
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("[sessions] proxy %s: %v", r.URL.Path, err)
			writeJSONError(w, http.StatusBadGateway, "session_unreachable", "This cluster's Radar session is not answering. Open the sessions menu to restart it.")
		},
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		http.Error(w, "unexpected Host header", http.StatusForbidden)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/":
		http.Redirect(w, r, "/s/"+Slug(h.initial)+"/", http.StatusFound)
	case strings.HasPrefix(path, "/_sessions/api/"):
		h.serveAPI(w, r, strings.TrimPrefix(path, "/_sessions/api/"))
	case strings.HasPrefix(path, "/s/"):
		h.serveSession(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) serveSession(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	slug, _, hasSlash := strings.Cut(rest, "/")
	if !ValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	prefix := "/s/" + slug + "/"
	if r.URL.RawPath != "" && !strings.HasPrefix(r.URL.RawPath, prefix) {
		http.Error(w, "malformed session path", http.StatusBadRequest)
		return
	}
	if !hasSlash {
		u := *r.URL
		u.Path, u.RawPath = prefix, ""
		http.Redirect(w, r, u.RequestURI(), http.StatusMovedPermanently)
		return
	}

	target, state, contextName, known := h.backend.Lookup(slug)
	if !known {
		http.NotFound(w, r)
		return
	}
	if state != StateRunning || target == nil {
		// Only opening the page starts a session. A tab left open on a
		// stopped session keeps getting 503s from its API calls instead of
		// quietly bringing the session back.
		if !isNavigation(r) {
			writeJSONError(w, http.StatusServiceUnavailable, "session_"+string(state),
				"The Radar session for "+contextName+" is "+string(state)+".")
			return
		}
		var err error
		if target, err = h.awaitRunning(r.Context(), slug, contextName); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[sessions] open %s: %v", contextName, err)
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "Radar for "+contextName+" did not start: "+err.Error()+"\nReload to try again.", http.StatusServiceUnavailable)
			}
			return
		}
	}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, target)))
}

// awaitRunning starts the session if it is not running and waits until its
// Radar answers. A session that fails is started once per page load, never
// in a loop.
func (h *handler) awaitRunning(ctx context.Context, slug, contextName string) (*url.URL, error) {
	deadline := time.NewTimer(pageStartWait)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	started := false
	for {
		target, state, _, known := h.backend.Lookup(slug)
		switch {
		case !known:
			return nil, ErrUnknownContext
		case state == StateRunning && target != nil:
			return target, nil
		case state == StateFailed && started:
			return nil, errors.New("the session failed; its log is in the radar-sessions home folder")
		case state == StateStopped || state == StateFailed:
			if _, err := h.backend.Start(contextName); err != nil && !errors.Is(err, ErrBusy) {
				return nil, err
			}
			started = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("timed out waiting for it to answer")
		case <-tick.C:
		}
	}
}

func (h *handler) serveAPI(w http.ResponseWriter, r *http.Request, route string) {
	if route == "state" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
			return
		}
		if !readAllowed(r) {
			writeJSONError(w, http.StatusForbidden, "cross_origin_refused", "refused a read from another site")
			return
		}
		sessions, current, err := h.backend.Snapshot()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "kubeconfig_unreadable", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"controller": ControllerName,
			"current":    current,
			"initial":    h.initial,
			"sessions":   sessions,
		})
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if !writeAllowed(r) {
		writeJSONError(w, http.StatusForbidden, "cross_origin_refused", "refused a change that did not come from this page")
		return
	}

	if route == "stop-all" {
		var body struct {
			Except string `json:"except"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
				writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"stopped": h.backend.StopAll(body.Except)})
		return
	}

	slug, action, ok := strings.Cut(strings.TrimPrefix(route, "sessions/"), "/")
	if !ok || !strings.HasPrefix(route, "sessions/") || !ValidSlug(slug) {
		writeJSONError(w, http.StatusNotFound, "not_found", "no such route")
		return
	}
	_, _, contextName, known := h.backend.Lookup(slug)
	if !known {
		writeJSONError(w, http.StatusNotFound, "unknown_session", "no kubeconfig context has this session id")
		return
	}
	switch action {
	case "start":
		info, err := h.backend.Start(contextName)
		switch {
		case errors.Is(err, ErrUnknownContext):
			writeJSONError(w, http.StatusNotFound, "unknown_session", err.Error())
		case errors.Is(err, ErrBusy):
			writeJSONError(w, http.StatusConflict, "session_stopping", err.Error())
		case errors.Is(err, ErrShuttingDown):
			writeJSONError(w, http.StatusServiceUnavailable, "controller_stopping", err.Error())
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, "start_failed", err.Error())
		default:
			writeJSON(w, http.StatusAccepted, info)
		}
	case "stop":
		if err := h.backend.Stop(slug); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "stop_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "state": StateStopped})
	default:
		writeJSONError(w, http.StatusNotFound, "not_found", "no such route")
	}
}

// isNavigation reports whether the browser is loading a page, as opposed to
// the Radar UI fetching data.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// readAllowed refuses what the browser labels as coming from another site.
// Every localhost port is the same site, so same-site is refused too.
func readAllowed(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	return true
}

func writeAllowed(r *http.Request) bool {
	if r.Header.Get(csrfHeader) != "1" || !readAllowed(r) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// loopbackHost guards against DNS rebinding: a hostile name that resolves to
// 127.0.0.1 still carries its own name in Host.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "error_code": code})
}
