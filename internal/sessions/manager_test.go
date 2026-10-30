package sessions

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/internal/statedir"
)

const testBinaryEnv = "SESSIONS_TEST_BINARY"

// The test binary doubles as the guard and as a stand-in Radar, so lifecycle
// tests exercise real processes, signals and process groups.
func TestMain(m *testing.M) {
	if os.Getenv(testBinaryEnv) == "1" {
		if len(os.Args) > 1 && os.Args[1] == "guard" {
			os.Exit(RunGuard(os.Args[2:]))
		}
		os.Exit(fakeRadar(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type whoami struct {
	PID        int    `json:"pid"`
	Context    string `json:"context"`
	Contexts   int    `json:"contexts"`
	StateDir   string `json:"stateDir"`
	Kubeconfig string `json:"kubeconfigEnv"`
}

func fakeRadar(args []string) int {
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	port := fs.Int("port", 0, "")
	base := fs.String("base-path", "", "")
	kubeconfig := fs.String("kubeconfig", "", "")
	fs.String("listen-address", "", "")
	fs.Bool("no-browser", false, "")
	fs.Bool("no-version-check", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if os.Getenv("FAKE_RADAR_EXIT") == "1" {
		fmt.Println("fatal: cannot load kubeconfig")
		return 1
	}
	if pidFile := os.Getenv("FAKE_RADAR_PIDFILE"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if os.Getenv("FAKE_RADAR_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	cfg, err := clientcmd.LoadFromFile(*kubeconfig)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc(*base+"/api/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc(*base+"/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(whoami{
			PID: os.Getpid(), Context: cfg.CurrentContext, Contexts: len(cfg.Contexts),
			StateDir: os.Getenv(statedir.EnvVar), Kubeconfig: os.Getenv("KUBECONFIG"),
		})
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: mux}
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		<-sigs
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Println(err)
		return 1
	}
	return 0
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	_, kubeconfig := writeKubeconfig(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(testBinaryEnv, "1")
	t.Setenv("KUBECONFIG", "/should/not/reach/sessions")
	home := filepath.Join(t.TempDir(), "sessions")
	m, err := NewManager(Config{
		RadarPath:    exe,
		GuardPath:    exe,
		GuardArgs:    []string{"guard"},
		Home:         home,
		SeedFrom:     t.TempDir(),
		Kubeconfig:   Kubeconfig{Paths: []string{kubeconfig}},
		ReadyTimeout: 10 * time.Second,
		StopTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, home
}

func waitState(t *testing.T, m *Manager, slug string, want State) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, state, _, _ := m.Lookup(slug); state == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, state, _, _ := m.Lookup(slug)
	t.Fatalf("session %s state = %s, want %s", slug, state, want)
}

func ask(t *testing.T, m *Manager, slug string) whoami {
	t.Helper()
	target, _, _, _ := m.Lookup(slug)
	if target == nil {
		t.Fatalf("session %s has no target", slug)
	}
	resp, err := http.Get(target.String() + "/s/" + slug + "/api/whoami")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var w whoami
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func TestSessionsAreIsolatedProcesses(t *testing.T) {
	m, home := newTestManager(t)
	dev, prod := Slug("dev"), Slug("prod")
	for _, name := range []string{"dev", "prod"} {
		if _, err := m.Start(name); err != nil {
			t.Fatal(err)
		}
	}
	waitState(t, m, dev, StateRunning)
	waitState(t, m, prod, StateRunning)

	a, b := ask(t, m, dev), ask(t, m, prod)
	if a.Context != "dev" || b.Context != "prod" {
		t.Fatalf("sessions not pinned to their own context: dev→%q prod→%q", a.Context, b.Context)
	}
	if a.Contexts != 1 || b.Contexts != 1 {
		t.Fatalf("a session can see more than its own context: dev=%d prod=%d", a.Contexts, b.Contexts)
	}
	if a.PID == b.PID {
		t.Fatal("two sessions share one process")
	}
	if a.StateDir != filepath.Join(home, dev, "state") || b.StateDir != filepath.Join(home, prod, "state") {
		t.Fatalf("state dirs not private: dev=%q prod=%q", a.StateDir, b.StateDir)
	}
	if a.Kubeconfig != "" {
		t.Fatalf("session inherited KUBECONFIG=%q", a.Kubeconfig)
	}

	if err := m.Stop(dev); err != nil {
		t.Fatal(err)
	}
	waitDead(t, a.PID)
	if _, state, _, _ := m.Lookup(dev); state != StateStopped {
		t.Fatalf("stopped session state = %s", state)
	}
	if _, err := os.Stat(filepath.Join(home, dev, "kubeconfig")); !os.IsNotExist(err) {
		t.Fatalf("stopped session left its kubeconfig behind: %v", err)
	}
	if got := ask(t, m, prod); got.PID != b.PID {
		t.Fatal("stopping dev disturbed prod")
	}
}

func TestStartIsIdempotent(t *testing.T) {
	m, _ := newTestManager(t)
	first, err := m.Start("dev")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, first.Slug, StateRunning)
	pid := ask(t, m, first.Slug).PID
	if _, err := m.Start("dev"); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, m, first.Slug).PID; got != pid {
		t.Fatalf("second Start replaced the running session (%d → %d)", pid, got)
	}
}

func TestStartRejectsUnknownContext(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Start("staging"); err != ErrUnknownContext {
		t.Fatalf("Start(unknown) err = %v", err)
	}
}

func TestStartFailureIsReported(t *testing.T) {
	m, _ := newTestManager(t)
	t.Setenv("FAKE_RADAR_EXIT", "1")
	info, err := m.Start("dev")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, info.Slug, StateFailed)
	snap, _, _ := m.Snapshot()
	for _, s := range snap {
		if s.Slug == info.Slug && !strings.Contains(s.Error, "cannot load kubeconfig") {
			t.Fatalf("failure reason not surfaced: %q", s.Error)
		}
	}
}

func TestStopEscalatesToKill(t *testing.T) {
	m, _ := newTestManager(t)
	t.Setenv("FAKE_RADAR_IGNORE_TERM", "1")
	info, err := m.Start("dev")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, info.Slug, StateRunning)
	pid := ask(t, m, info.Slug).PID
	if err := m.Stop(info.Slug); err != nil {
		t.Fatal(err)
	}
	waitDead(t, pid)
}

// The controller can die without running any cleanup (SIGKILL, crash). The
// guard must still take the session's Radar down with it.
func TestGuardEndsRadarWhenControllerDies(t *testing.T) {
	exe, _ := os.Executable()
	_, kubeconfig := writeKubeconfig(t)
	pinned := filepath.Join(t.TempDir(), "kubeconfig")
	if err := (Kubeconfig{Paths: []string{kubeconfig}}).WritePinned("dev", pinned); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "radar.pid")
	port, _ := freePort()
	script := fmt.Sprintf(`%q guard --parent $$ -- %q --port %d --base-path /s/x --kubeconfig %q & wait`, exe, exe, port, pinned)
	controller := exec.Command("/bin/sh", "-c", script)
	controller.Env = append(os.Environ(), testBinaryEnv+"=1", "FAKE_RADAR_PIDFILE="+pidFile)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	var radarPID int
	deadline := time.Now().Add(10 * time.Second)
	for radarPID == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			radarPID, _ = strconv.Atoi(string(data))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if radarPID == 0 {
		t.Fatal("fake radar never started")
	}

	_ = controller.Process.Kill()
	_ = controller.Wait()
	waitDead(t, radarPID)
}

func TestLeftoverSessionsAreSweptOnStartup(t *testing.T) {
	m, home := newTestManager(t)
	info, err := m.Start("dev")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, info.Slug, StateRunning)
	radarPID := ask(t, m, info.Slug).PID

	// Forget the session without stopping it, as a controller that died
	// with its guard stuck would.
	m.mu.Lock()
	delete(m.sessions, info.Slug)
	m.mu.Unlock()

	if _, err := NewManager(m.cfg); err != nil {
		t.Fatal(err)
	}
	waitDead(t, radarPID)
	if _, err := os.Stat(filepath.Join(home, info.Slug, recordFile)); !os.IsNotExist(err) {
		t.Fatalf("session record not removed: %v", err)
	}
}

func TestCheckRadarArgsRefusesManagedFlags(t *testing.T) {
	for _, args := range [][]string{{"--port", "1"}, {"-kubeconfig=/x"}, {"--base-path=/"}, {"--listen-address", "0.0.0.0"}} {
		if err := CheckRadarArgs(args); err == nil {
			t.Fatalf("CheckRadarArgs(%v) accepted a managed flag", args)
		}
	}
	if err := CheckRadarArgs([]string{"--disable-exec", "--namespace", "team-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotListsStoppedContexts(t *testing.T) {
	m, _ := newTestManager(t)
	snap, current, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current != "dev" || len(snap) != 2 || snap[0].State != StateStopped || snap[1].State != StateStopped {
		t.Fatalf("Snapshot() = %+v, %q", snap, current)
	}
}
