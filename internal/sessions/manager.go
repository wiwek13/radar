package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/statedir"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

var (
	ErrUnknownContext = errors.New("no such context in kubeconfig")
	ErrShuttingDown   = errors.New("session controller is shutting down")
	ErrBusy           = errors.New("session is still stopping; try again in a moment")

	errPortTaken = errors.New("port already in use")
	errAborted   = errors.New("session was stopped while starting")
)

// ManagedRadarFlags are set by the controller on every session. Passing one
// through would let a session listen elsewhere or read another kubeconfig.
var ManagedRadarFlags = []string{"kubeconfig", "kubeconfig-dir", "port", "listen-address", "base-path", "no-browser"}

type Config struct {
	RadarPath    string
	GuardPath    string   // executable that implements RunGuard
	GuardArgs    []string // arguments selecting the guard, e.g. ["guard"]
	Home         string   // each session lives in Home/<slug>/
	SeedFrom     string   // where new sessions copy preferences from (~/.radar)
	Kubeconfig   Kubeconfig
	RadarArgs    []string // extra flags for every session
	ReadyTimeout time.Duration
	StopTimeout  time.Duration
}

// Info is one session as the UI sees it.
type Info struct {
	Context   string     `json:"context"`
	Slug      string     `json:"slug"`
	State     State      `json:"state"`
	PID       int        `json:"pid,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	RSSBytes  int64      `json:"rssBytes,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type process struct {
	pid    int // the guard; also the process group of the session
	port   int
	exited chan struct{}
}

type session struct {
	context   string
	slug      string
	state     State
	startedAt time.Time
	err       string
	proc      *process
	target    *url.URL
}

func (s *session) info() Info {
	in := Info{Context: s.context, Slug: s.slug, State: s.state, Error: s.err}
	if s.state != StateFailed {
		started := s.startedAt
		in.StartedAt = &started
	}
	if s.proc != nil && s.state != StateFailed {
		in.PID = s.proc.pid
	}
	return in
}

type Manager struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*session
	closing  bool
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.RadarPath == "" || cfg.GuardPath == "" || cfg.Home == "" {
		return nil, errors.New("sessions: RadarPath, GuardPath and Home are required")
	}
	if err := CheckRadarArgs(cfg.RadarArgs); err != nil {
		return nil, err
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 15 * time.Second
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, sessions: map[string]*session{}}
	m.sweepLeftovers()
	return m, nil
}

// CheckRadarArgs refuses pass-through flags the controller owns.
func CheckRadarArgs(args []string) error {
	for _, a := range args {
		name := strings.TrimLeft(a, "-")
		if name == a {
			continue
		}
		name, _, _ = strings.Cut(name, "=")
		if slices.Contains(ManagedRadarFlags, name) {
			return fmt.Errorf("--%s is set by the session controller for each session and cannot be passed through", name)
		}
	}
	return nil
}

// Start begins a session for contextName, or returns the one already
// starting or running. It does not wait for Radar to become ready.
func (m *Manager) Start(contextName string) (Info, error) {
	names, _, err := m.cfg.Kubeconfig.Contexts()
	if err != nil {
		return Info{}, err
	}
	if !slices.Contains(names, contextName) {
		return Info{}, ErrUnknownContext
	}
	slug := Slug(contextName)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Info{}, ErrShuttingDown
	}
	if s, ok := m.sessions[slug]; ok {
		switch s.state {
		case StateStarting, StateRunning:
			return s.info(), nil
		case StateStopping:
			return s.info(), ErrBusy
		}
	}
	s := &session{context: contextName, slug: slug, state: StateStarting, startedAt: time.Now()}
	m.sessions[slug] = s
	go m.launch(s)
	return s.info(), nil
}

func (m *Manager) launch(s *session) {
	err := m.spawn(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err == nil:
	case s.state == StateStopping || errors.Is(err, errAborted):
		if m.sessions[s.slug] == s && s.proc == nil {
			delete(m.sessions, s.slug)
		}
	default:
		s.state = StateFailed
		s.err = err.Error()
		s.proc = nil
		s.target = nil
	}
}

func (m *Manager) spawn(s *session) error {
	dir := filepath.Join(m.cfg.Home, s.slug)
	stateDir := filepath.Join(dir, "state")
	if err := prepareStateDir(stateDir, m.cfg.SeedFrom); err != nil {
		return fmt.Errorf("prepare state directory: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		port, err := freePort()
		if err != nil {
			return err
		}
		proc, err := m.exec(s, dir, stateDir, port)
		if err != nil {
			return err
		}
		lastErr = m.waitReady(s, proc)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, errPortTaken) {
			return lastErr
		}
	}
	return lastErr
}

func (m *Manager) exec(s *session, dir, stateDir string, port int) (*process, error) {
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := m.cfg.Kubeconfig.WritePinned(s.context, kubeconfig); err != nil {
		return nil, err
	}
	args := append([]string{}, m.cfg.GuardArgs...)
	args = append(args, "--parent", strconv.Itoa(os.Getpid()), "--", m.cfg.RadarPath,
		"--kubeconfig", kubeconfig,
		"--port", strconv.Itoa(port),
		"--listen-address", "127.0.0.1",
		"--base-path", "/s/"+s.slug,
		"--no-browser",
		"--no-version-check",
	)
	args = append(args, m.cfg.RadarArgs...)

	cmd := exec.Command(m.cfg.GuardPath, args...)
	cmd.Env = append(envWithout(os.Environ(), statedir.EnvVar, "KUBECONFIG"),
		statedir.EnvVar+"="+stateDir,
		"RADAR_DISABLE_VERSION_CHECK=1",
	)
	// Its own process group: a Ctrl+C in the controller's terminal reaches
	// only the controller, which then stops every session in order.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logFile, err := os.OpenFile(filepath.Join(dir, "radar.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile

	m.mu.Lock()
	if s.state != StateStarting {
		m.mu.Unlock()
		logFile.Close()
		os.Remove(kubeconfig)
		return nil, errAborted
	}
	err = cmd.Start()
	logFile.Close()
	if err != nil {
		m.mu.Unlock()
		os.Remove(kubeconfig)
		return nil, fmt.Errorf("start session: %w", err)
	}
	proc := &process{pid: cmd.Process.Pid, port: port, exited: make(chan struct{})}
	s.proc = proc
	m.mu.Unlock()

	writeRecord(dir, record{PID: proc.pid, Slug: s.slug, Context: s.context})
	go func() {
		_ = cmd.Wait()
		os.Remove(kubeconfig)
		os.Remove(filepath.Join(dir, recordFile))
		close(proc.exited)
		m.onExit(s, proc)
	}()
	return proc, nil
}

func (m *Manager) waitReady(s *session, proc *process) error {
	health := fmt.Sprintf("http://127.0.0.1:%d/s/%s/api/health", proc.port, s.slug)
	client := &http.Client{Timeout: time.Second}
	deadline := time.After(m.cfg.ReadyTimeout)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-proc.exited:
			if m.isStopping(s) {
				return errAborted
			}
			tail := m.logTail(s.slug)
			if strings.Contains(tail, "address already in use") {
				return errPortTaken
			}
			return fmt.Errorf("Radar exited while starting: %s", tail)
		case <-deadline:
			killGroup(proc.pid)
			<-proc.exited
			return fmt.Errorf("Radar did not become ready within %s; see %s", m.cfg.ReadyTimeout, filepath.Join(m.cfg.Home, s.slug, "radar.log"))
		case <-ticker.C:
			if m.isStopping(s) {
				return errAborted
			}
			resp, err := client.Get(health)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				continue
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if s.state != StateStarting || s.proc != proc {
				return errAborted
			}
			s.state = StateRunning
			s.target = &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(proc.port))}
			return nil
		}
	}
}

func (m *Manager) isStopping(s *session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.state == StateStopping
}

func (m *Manager) onExit(s *session, proc *process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.proc != proc {
		return
	}
	switch s.state {
	case StateStopping:
		if m.sessions[s.slug] == s {
			delete(m.sessions, s.slug)
		}
	case StateRunning:
		s.state = StateFailed
		s.err = "Radar exited unexpectedly: " + m.logTail(s.slug)
		s.proc = nil
		s.target = nil
	}
}

// Stop ends a session and waits for its processes to exit.
func (m *Manager) Stop(slug string) error {
	m.mu.Lock()
	s, ok := m.sessions[slug]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	if s.state == StateFailed {
		delete(m.sessions, slug)
		m.mu.Unlock()
		return nil
	}
	s.state = StateStopping
	s.target = nil
	proc := s.proc
	m.mu.Unlock()

	if proc != nil {
		m.terminate(proc)
	}
	m.mu.Lock()
	if m.sessions[slug] == s && (proc == nil || s.proc == proc) {
		delete(m.sessions, slug)
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) terminate(proc *process) {
	_ = syscall.Kill(proc.pid, syscall.SIGTERM)
	select {
	case <-proc.exited:
		return
	case <-time.After(m.cfg.StopTimeout):
	}
	killGroup(proc.pid)
	select {
	case <-proc.exited:
	case <-time.After(5 * time.Second):
	}
}

// StopAll stops every session except the one with slug except ("" for none)
// and reports how many it stopped.
func (m *Manager) StopAll(except string) int {
	m.mu.Lock()
	var slugs []string
	for slug := range m.sessions {
		if slug != except {
			slugs = append(slugs, slug)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, slug := range slugs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Stop(slug)
		}()
	}
	wg.Wait()
	return len(slugs)
}

// Shutdown refuses new sessions and stops all of them.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	m.StopAll("")
}

// Lookup returns where a running session listens. known reports whether
// the slug belongs to a context at all.
func (m *Manager) Lookup(slug string) (target *url.URL, state State, contextName string, known bool) {
	m.mu.Lock()
	if s, ok := m.sessions[slug]; ok {
		defer m.mu.Unlock()
		return s.target, s.state, s.context, true
	}
	m.mu.Unlock()
	names, _, err := m.cfg.Kubeconfig.Contexts()
	if err != nil {
		return nil, StateStopped, "", false
	}
	for _, name := range names {
		if Slug(name) == slug {
			return nil, StateStopped, name, true
		}
	}
	return nil, StateStopped, "", false
}

// Snapshot lists every kubeconfig context with its session state, plus any
// running session whose context has since left the kubeconfig.
func (m *Manager) Snapshot() ([]Info, string, error) {
	names, current, err := m.cfg.Kubeconfig.Contexts()
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	byName := map[string]Info{}
	for _, s := range m.sessions {
		byName[s.context] = s.info()
	}
	m.mu.Unlock()

	rss := groupRSS()
	out := make([]Info, 0, len(names)+len(byName))
	for _, name := range names {
		in, ok := byName[name]
		if !ok {
			in = Info{Context: name, Slug: Slug(name), State: StateStopped}
		}
		delete(byName, name)
		out = append(out, in)
	}
	for _, in := range byName {
		out = append(out, in)
	}
	for i := range out {
		if out[i].PID != 0 {
			out[i].RSSBytes = rss[out[i].PID]
		}
	}
	return out, current, nil
}

func (m *Manager) logTail(slug string) string {
	data, err := os.ReadFile(filepath.Join(m.cfg.Home, slug, "radar.log"))
	if err != nil {
		return "(no log)"
	}
	if len(data) > 4096 {
		data = data[len(data)-4096:]
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	if len(lines) == 0 {
		return "(empty log)"
	}
	return strings.Join(lines, " | ")
}

const recordFile = "session.json"

// record lets a later controller find and end a session it did not start,
// when the guard that should have ended it is itself gone or stuck.
type record struct {
	PID     int    `json:"pid"`
	Slug    string `json:"slug"`
	Context string `json:"context"`
}

func writeRecord(dir string, r record) {
	data, _ := json.Marshal(r)
	_ = writePrivateFile(filepath.Join(dir, recordFile), data)
}

func (m *Manager) sweepLeftovers() {
	paths, _ := filepath.Glob(filepath.Join(m.cfg.Home, "*", recordFile))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		var r record
		if err == nil && json.Unmarshal(data, &r) == nil && r.PID > 0 && isSessionGuard(r.PID, r.Slug) {
			_ = syscall.Kill(r.PID, syscall.SIGTERM)
			for i := 0; i < 50 && syscall.Kill(r.PID, 0) == nil; i++ {
				time.Sleep(100 * time.Millisecond)
			}
			killGroup(r.PID)
		}
		os.Remove(path)
		os.Remove(filepath.Join(filepath.Dir(path), "kubeconfig"))
	}
}

// isSessionGuard confirms pid still runs this slug's guard, so a recycled
// PID belonging to something else is never signalled.
func isSessionGuard(pid int, slug string) bool {
	if !ValidSlug(slug) {
		return false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	cmdline := string(out)
	return strings.Contains(cmdline, "--parent") && strings.Contains(cmdline, "/s/"+slug+" ")
}

func killGroup(pgid int) {
	if pgid > 1 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

// groupRSS sums resident memory per process group, in bytes.
func groupRSS() map[int]int64 {
	out, err := exec.Command("ps", "-A", "-o", "pgid=,rss=").Output()
	if err != nil {
		return nil
	}
	sums := map[int]int64{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		fields := strings.Fields(string(line))
		if len(fields) != 2 {
			continue
		}
		pgid, err1 := strconv.Atoi(fields[0])
		kb, err2 := strconv.ParseInt(fields[1], 10, 64)
		if err1 == nil && err2 == nil {
			sums[pgid] += kb * 1024
		}
	}
	return sums
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func envWithout(env []string, names ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, name) {
			out = append(out, kv)
		}
	}
	return out
}
