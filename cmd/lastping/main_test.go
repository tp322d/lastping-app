package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests build and run the REAL binary and send it a REAL signal.
//
// internal/runner's tests cover the same properties against an injected signal
// channel, which is faster and more precise — but an injected channel cannot
// catch the two things that only exist here: signal.Notify not being wired up at
// all, and os.Exit being called with something other than the code runner.Run
// returned. Both are one-line mistakes that would make every unit test above
// still pass while every CI pipeline using the wrapper silently went green.

const testMonitor = "11111111-1111-1111-1111-111111111111"

// buildBinary compiles cmd/lastping once per test binary run.
var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func lastpingBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping binary build in -short mode")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lastping-bin")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "lastping")
		out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
		if err != nil {
			buildErr = err
			t.Logf("go build output: %s", out)
		}
	})
	if buildErr != nil {
		t.Fatalf("building cmd/lastping: %v", buildErr)
	}
	return binPath
}

type pingLog struct {
	mu    sync.Mutex
	paths []string
	saw   chan struct{}
}

func newPingServer(t *testing.T) (*pingLog, string) {
	t.Helper()
	pl := &pingLog{saw: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		pl.mu.Lock()
		pl.paths = append(pl.paths, r.URL.Path)
		pl.mu.Unlock()
		select {
		case pl.saw <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return pl, srv.URL
}

func (p *pingLog) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.paths))
	copy(out, p.paths)
	return out
}

func (p *pingLog) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		p.mu.Lock()
		got := len(p.paths)
		p.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-p.saw:
		case <-deadline:
			t.Fatalf("timed out waiting for %d pings; got %v", n, p.all())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestBinary_ExitCodePropagates is the CI-safety property, proven against the
// real process: whatever the wrapped command exits with, `lastping run` exits
// with. A wrapper that swallows a non-zero exit turns a red pipeline green.
func TestBinary_ExitCodePropagates(t *testing.T) {
	bin := lastpingBinary(t)
	_, pingURL := newPingServer(t)

	for _, want := range []int{0, 1, 3, 42, 77} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			cmd := exec.Command(bin, "run", "--monitor", testMonitor, "--ping-url", pingURL,
				"--", "sh", "-c", "exit "+strconv.Itoa(want))
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			err := cmd.Run()
			got := 0
			if err != nil {
				ee, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("unexpected error: %v", err)
				}
				got = ee.ExitCode()
			}
			if got != want {
				t.Fatalf("lastping run exited %d, want %d", got, want)
			}
		})
	}
}

// TestBinary_MonitorFromEnv proves the documented resolution order's env leg:
// LASTPING_MONITOR with no --monitor flag must report a run.
func TestBinary_MonitorFromEnv(t *testing.T) {
	bin := lastpingBinary(t)
	pl, pingURL := newPingServer(t)

	cmd := exec.Command(bin, "run", "--", "sh", "-c", "exit 0")
	cmd.Env = append(os.Environ(),
		"LASTPING_MONITOR="+testMonitor,
		"LASTPING_PING_URL="+pingURL,
	)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	pl.waitFor(t, 2)
	got := pl.all()
	if got[0] != "/"+testMonitor+"/start" || got[1] != "/"+testMonitor+"/0" {
		t.Fatalf("pings = %v, want start then /0", got)
	}
}

// TestBinary_SIGTERMForwardsAndClosesTheRun is the signal hazard proven end to
// end: a real SIGTERM to the real wrapper process must reach the child, and the
// run must be CLOSED (cancel) rather than left open to expire against the run
// budget and page someone hours later for a job they killed themselves.
func TestBinary_SIGTERMForwardsAndClosesTheRun(t *testing.T) {
	bin := lastpingBinary(t)
	pl, pingURL := newPingServer(t)

	// The child traps TERM and exits 143, so a 143 from the wrapper proves the
	// signal was delivered to the CHILD, not merely that the wrapper died.
	script := `trap 'exit 143' TERM
echo READY
i=0; while [ $i -lt 1200 ]; do sleep 0.05; i=$((i+1)); done
exit 99`

	cmd := exec.Command(bin, "run", "--monitor", testMonitor, "--ping-url", pingURL, "--", "sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the child to have installed its trap before signalling; a fixed
	// sleep here is how this test would become flaky on a loaded machine.
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "READY") {
				close(ready)
				io.Copy(io.Discard, stdout) //nolint:errcheck // draining
				return
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child never signalled READY")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		code := 0
		if werr != nil {
			ee, ok := werr.(*exec.ExitError)
			if !ok {
				t.Fatalf("unexpected error: %v", werr)
			}
			code = ee.ExitCode()
		}
		if code != 143 {
			t.Errorf("wrapper exited %d, want 143 — the child's trapped exit, propagated", code)
		}
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("wrapper did not exit after SIGTERM; it must not outlive its child")
	}

	pl.waitFor(t, 2)
	got := pl.all()
	if got[len(got)-1] != "/"+testMonitor+"/cancel" {
		t.Fatalf("pings = %v; the last must be /cancel so the run does not sit open", got)
	}
}

// TestBinary_NoMonitorIsAUsageError: without a monitor id there is nothing to
// report to, and the wrapper must say so rather than silently running the
// command unmonitored — a wrapper that appears to work while reporting nothing
// is the exact failure this whole feature exists to remove.
func TestBinary_NoMonitorIsAUsageError(t *testing.T) {
	bin := lastpingBinary(t)
	cmd := exec.Command(bin, "run", "--", "true")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("want a non-zero exit when no monitor id is configured")
	}
	if !strings.Contains(string(out), "LASTPING_MONITOR") {
		t.Errorf("the message must name both ways to supply it; got %q", out)
	}
}
