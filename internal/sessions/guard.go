package sessions

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const (
	guardPollInterval = 500 * time.Millisecond
	guardKillAfter    = 10 * time.Second
)

// RunGuard runs one session's Radar as its child and ends it when the
// controller goes away, however the controller died. macOS has no
// parent-death signal, so the guard polls its own parent PID instead.
//
//	guard --parent <controller pid> -- <radar> [args...]
func RunGuard(args []string) int {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	parent := fs.Int("parent", 0, "controller PID to follow")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if *parent <= 0 || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: guard --parent <pid> -- <command> [args...]")
		return 2
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	if os.Getppid() != *parent {
		fmt.Fprintln(os.Stderr, "guard: controller already exited; not starting")
		return 1
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "guard: start %s: %v\n", argv[0], err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(guardPollInterval)
	defer ticker.Stop()
	var killAt <-chan time.Time
	terminate := func() {
		if killAt != nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		killAt = time.After(guardKillAfter)
	}

	for {
		select {
		case err := <-done:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode()
			}
			if err != nil {
				return 1
			}
			return 0
		case <-sigs:
			terminate()
		case <-ticker.C:
			if os.Getppid() != *parent {
				terminate()
			}
		case <-killAt:
			_ = cmd.Process.Kill()
		}
	}
}
