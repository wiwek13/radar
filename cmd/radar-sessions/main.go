// Command radar-sessions runs one Radar per kubeconfig context behind a single
// loopback address, so each browser tab can stay on its own cluster.
//
//	radar-sessions [flags] [-- radar flags...]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/sessions"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "guard" {
		os.Exit(sessions.RunGuard(os.Args[2:]))
	}
	os.Exit(run())
}

func run() int {
	self, err := os.Executable()
	if err != nil {
		log.Printf("cannot locate own executable: %v", err)
		return 1
	}
	home, _ := os.UserHomeDir()

	port := flag.Int("port", 9300, "Port for the session controller (loopback only)")
	radarPath := flag.String("radar", filepath.Join(filepath.Dir(self), "radar"), "Radar binary each session runs")
	kubeconfig := flag.String("kubeconfig", "", "Comma-separated kubeconfig files (default: $KUBECONFIG, else ~/.kube/config)")
	initial := flag.String("context", "", "Context to open first (default: the kubeconfig's current-context)")
	sessionsHome := flag.String("home", filepath.Join(home, ".radar-fork", "sessions"), "Directory holding each session's kubeconfig, state and log")
	noBrowser := flag.Bool("no-browser", false, "Don't open a browser")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [-- radar flags...]\n\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()
	radarArgs := flag.Args()
	if err := sessions.CheckRadarArgs(radarArgs); err != nil {
		log.Print(err)
		return 2
	}

	kc := sessions.Kubeconfig{}
	if *kubeconfig != "" {
		kc.Paths = strings.Split(*kubeconfig, ",")
	}
	names, current, err := kc.Contexts()
	if err != nil {
		log.Printf("cannot read kubeconfig: %v", err)
		return 1
	}
	if *initial == "" {
		*initial = current
	}
	if *initial != "" && !contains(names, *initial) {
		log.Printf("context %q is not in the kubeconfig", *initial)
		return 1
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	base := "http://localhost:" + strconv.Itoa(*port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if reuseRunning(base, *initial, *noBrowser) {
			return 0
		}
		log.Printf("cannot listen on %s: %v", addr, err)
		return 1
	}

	if _, err := os.Stat(*radarPath); err != nil {
		log.Printf("radar binary not found at %s (use --radar)", *radarPath)
		return 1
	}
	m, err := sessions.NewManager(sessions.Config{
		RadarPath:  *radarPath,
		GuardPath:  self,
		GuardArgs:  []string{"guard"},
		Home:       *sessionsHome,
		SeedFrom:   filepath.Join(home, ".radar"),
		Kubeconfig: kc,
		RadarArgs:  radarArgs,
	})
	if err != nil {
		log.Print(err)
		return 1
	}

	srv := &http.Server{
		Handler:           sessions.NewHandler(m, *initial),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}()

	open := base + "/_sessions/"
	if *initial != "" {
		if _, err := m.Start(*initial); err != nil {
			log.Printf("start %s: %v", *initial, err)
		}
		open = base + "/s/" + sessions.Slug(*initial) + "/"
	}
	log.Printf("Radar sessions on %s/_sessions/ (Ctrl+C stops every session)", base)
	if !*noBrowser {
		openBrowser(open)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	<-sigs
	log.Print("Stopping every session...")
	m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
	return 0
}

// reuseRunning hands off to a controller already on the port instead of
// starting a second one: it asks that controller to start the context and
// opens the browser on it.
func reuseRunning(base, contextName string, noBrowser bool) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/_sessions/api/state")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var state struct {
		Controller string `json:"controller"`
	}
	if json.NewDecoder(resp.Body).Decode(&state) != nil || state.Controller != sessions.ControllerName {
		return false
	}
	open := base + "/_sessions/"
	if contextName != "" {
		slug := sessions.Slug(contextName)
		req, _ := http.NewRequest(http.MethodPost, base+"/_sessions/api/sessions/"+slug+"/start", nil)
		req.Header.Set("X-Radar-Sessions", "1")
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
		open = base + "/s/" + slug + "/"
	}
	log.Printf("Radar sessions is already running on %s; opening it", base)
	if !noBrowser {
		openBrowser(open)
	}
	return true
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		log.Printf("Open %s in your browser", url)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Open %s in your browser (%v)", url, err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
