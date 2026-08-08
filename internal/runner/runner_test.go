package runner

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recorder is a stand-in ping endpoint. It records every request's path, query
// and body so a test can assert on the exact ping sequence a run produced.
type recorder struct {
	mu   sync.Mutex
	reqs []recordedReq
	srv  *httptest.Server
	fail bool // when true, every request 500s
	saw  chan struct{}
}

type recordedReq struct {
	Path string
	RID  string
	Body string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{saw: make(chan struct{}, 64)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedReq{
			Path: req.URL.Path,
			RID:  req.URL.Query().Get("rid"),
			Body: string(body),
		})
		fail := r.fail
		r.mu.Unlock()
		select {
		case r.saw <- struct{}{}:
		default:
		}
		if fail {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) all() []recordedReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedReq, len(r.reqs))
	copy(out, r.reqs)
	return out
}

// waitFor blocks until at least n requests have been recorded, or fails.
func (r *recorder) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		got := len(r.reqs)
		r.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-r.saw:
		case <-deadline:
			t.Fatalf("timed out waiting for %d pings; got %d: %+v", n, got, r.all())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

const testMonitor = "11111111-1111-1111-1111-111111111111"

func baseOpts(rec *recorder, argv ...string) Options {
	return Options{
		MonitorID: testMonitor,
		PingBase:  rec.srv.URL,
		Argv:      argv,
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		NewRID:    func() string { return "deadbeef" },
	}
}

// ── Exit code propagation ────────────────────────────────────────────────
//
// This is the property that, if broken, silently turns every red CI pipeline
// the wrapper is placed in front of green. It is asserted over a table rather
// than one happy case, and includes the two shapes that are easy to get wrong:
// a signalled child (no exit status of its own; ExitCode() reports -1) and a
// command that never started at all.

func TestRun_PropagatesChildExitCode(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want int
	}{
		{"success", []string{"sh", "-c", "exit 0"}, 0},
		{"generic failure", []string{"sh", "-c", "exit 1"}, 1},
		{"arbitrary code", []string{"sh", "-c", "exit 42"}, 42},
		{"high code", []string{"sh", "-c", "exit 200"}, 200},
		// 128+signum, the shell convention every CI system and retry wrapper
		// reads. Collapsing this to 1 would be a silent behaviour change for
		// anyone branching on it.
		{"killed by signal", []string{"sh", "-c", "kill -TERM $$; sleep 5"}, 128 + int(syscall.SIGTERM)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder(t)
			code, err := Run(baseOpts(rec, tc.argv...))
			if err != nil {
				t.Fatalf("Run returned an error for a started child: %v", err)
			}
			if code != tc.want {
				t.Fatalf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestRun_MissingCommandExitsLikeAShell pins 127 for a command that does not
// exist, so a script branching on 127 keeps working when the wrapper is put in
// front of it — and pins that the run is still CLOSED rather than left open to
// expire against the run budget hours later.
func TestRun_MissingCommandExitsLikeAShell(t *testing.T) {
	rec := newRecorder(t)
	code, err := Run(baseOpts(rec, "lastping-no-such-binary-xyzzy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 127 {
		t.Fatalf("exit code = %d, want 127", code)
	}
	rec.waitFor(t, 2)
	got := rec.all()
	if got[0].Path != "/"+testMonitor+"/start" {
		t.Errorf("first ping = %q, want the start ping", got[0].Path)
	}
	if got[1].Path != "/"+testMonitor+"/127" {
		t.Errorf("second ping = %q, want the run closed with /127", got[1].Path)
	}
}

// ── Ping protocol ─────────────────────────────────────────────────────────

// TestRun_ReportsStartThenExitCode asserts the exact ping shape: a start, then
// the numeric exit-code suffix, both carrying the same rid. The numeric suffix
// is load-bearing — ingest/classify.go maps 0 to success and non-zero to fail,
// so the wrapper never has to decide what an exit code means, and cannot
// disagree with the server about it.
func TestRun_ReportsStartThenExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"success is /0", []string{"sh", "-c", "exit 0"}, "/0"},
		{"failure is the raw code", []string{"sh", "-c", "exit 7"}, "/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder(t)
			if _, err := Run(baseOpts(rec, tc.argv...)); err != nil {
				t.Fatal(err)
			}
			rec.waitFor(t, 2)
			got := rec.all()
			if len(got) != 2 {
				t.Fatalf("want exactly 2 pings, got %d: %+v", len(got), got)
			}
			if got[0].Path != "/"+testMonitor+"/start" {
				t.Errorf("first ping path = %q", got[0].Path)
			}
			if got[1].Path != "/"+testMonitor+tc.want {
				t.Errorf("terminal ping path = %q, want %q", got[1].Path, "/"+testMonitor+tc.want)
			}
			if got[0].RID == "" || got[0].RID != got[1].RID {
				t.Errorf("both pings must carry the same non-empty rid; got %q and %q", got[0].RID, got[1].RID)
			}
		})
	}
}

// TestRun_SendsStderrTailAsBody asserts the terminal ping carries the tail of
// the child's stderr — the thing that makes a failed run diagnosable from the
// dashboard — and that it is BOUNDED, so a chatty command cannot post megabytes.
func TestRun_SendsStderrTailAsBody(t *testing.T) {
	rec := newRecorder(t)
	var stderr bytes.Buffer
	opts := baseOpts(rec, "sh", "-c", "echo 'boom: database is on fire' >&2; exit 3")
	opts.Stderr = &stderr
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 2)
	got := rec.all()
	if !strings.Contains(got[1].Body, "database is on fire") {
		t.Errorf("terminal ping body = %q, want the child's stderr", got[1].Body)
	}
	// Passthrough is not sacrificed for capture: the caller's stderr still saw it.
	if !strings.Contains(stderr.String(), "database is on fire") {
		t.Errorf("caller's stderr = %q, want the child's stderr passed through", stderr.String())
	}
}

func TestRun_StderrTailIsBounded(t *testing.T) {
	rec := newRecorder(t)
	opts := baseOpts(rec, "sh", "-c", "i=0; while [ $i -lt 4000 ]; do echo 'noisy line of stderr output' >&2; i=$((i+1)); done; exit 1")
	opts.Stderr = io.Discard
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 2)
	got := rec.all()
	if len(got[1].Body) > stderrTailLimit {
		t.Fatalf("body is %d bytes, want at most %d", len(got[1].Body), stderrTailLimit)
	}
	if len(got[1].Body) == 0 {
		t.Fatal("body is empty; the tail should have been kept")
	}
	// It must be the TAIL, not the head: the last thing it said before it died
	// is the useful part.
	if !strings.HasSuffix(strings.TrimRight(got[1].Body, "\n"), "noisy line of stderr output") {
		t.Errorf("body does not end at the last stderr line: %q", tailOf(got[1].Body))
	}
}

func tailOf(s string) string {
	if len(s) > 80 {
		return "..." + s[len(s)-80:]
	}
	return s
}

// ── stdio passthrough ─────────────────────────────────────────────────────

func TestRun_PassesStdoutAndStdinThrough(t *testing.T) {
	rec := newRecorder(t)
	var stdout bytes.Buffer
	opts := baseOpts(rec, "sh", "-c", "cat; echo done")
	opts.Stdin = strings.NewReader("hello from stdin\n")
	opts.Stdout = &stdout
	code, err := Run(opts)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !strings.Contains(stdout.String(), "hello from stdin") {
		t.Errorf("stdin was not passed through; stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "done") {
		t.Errorf("stdout was not passed through; stdout = %q", stdout.String())
	}
}

// TestRunStdio_TerminalStderrIsNotWrapped is the TTY decision, asserted rather
// than described. When stderr is a character device the child must be handed the
// file descriptor ITSELF — not an io.MultiWriter, which os/exec would implement
// with a pipe, making isatty(2) false for the child and silently changing its
// behaviour (colour off, progress bars off). The cost of that choice, no
// captured tail in the interactive case, is asserted too so it stays a
// deliberate trade rather than a surprise.
func TestRunStdio_TerminalStderrIsNotWrapped(t *testing.T) {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		// No controlling terminal (CI, `go test` under a pipe). /dev/null is a
		// character device too, which is what this test actually needs: a real
		// *os.File whose mode has os.ModeCharDevice set.
		tty, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Skipf("no character device available: %v", err)
		}
	}
	defer tty.Close()
	if !isCharDevice(tty) {
		t.Skip("opened file is not a character device on this platform")
	}

	cmd := newTestCmd()
	tail := runStdio(cmd, Options{Stdout: io.Discard, Stderr: tty})
	if cmd.Stderr != io.Writer(tty) {
		t.Fatalf("a terminal stderr must be handed to the child as the *os.File itself, got %T", cmd.Stderr)
	}
	if tail() != "" {
		t.Error("no stderr tail is captured when stderr is a terminal; that is the documented trade")
	}

	// And the converse: a non-terminal stderr IS teed, because that is the case
	// (CI, cron, a log file) where the tail is worth having.
	var buf bytes.Buffer
	cmd2 := newTestCmd()
	runStdio(cmd2, Options{Stdout: io.Discard, Stderr: &buf})
	if _, ok := cmd2.Stderr.(*bytes.Buffer); ok {
		t.Fatal("a non-terminal stderr must be teed through the ring buffer, not passed straight through")
	}
}

// TestRunStdio_StdinAndStdoutAreNeverWrapped pins the other half of the TTY
// decision: stdin and stdout are handed to the child untouched, ALWAYS, terminal
// or not. Buffering or piping either one is what breaks interactive programs
// (`lastping run -- claude`), so nothing in this package may start doing it.
func TestRunStdio_StdinAndStdoutAreNeverWrapped(t *testing.T) {
	stdin := strings.NewReader("x")
	var stdout bytes.Buffer
	cmd := newTestCmd()
	runStdio(cmd, Options{Stdin: stdin, Stdout: &stdout, Stderr: io.Discard})
	if cmd.Stdin != io.Reader(stdin) {
		t.Errorf("stdin must be passed through unwrapped, got %T", cmd.Stdin)
	}
	if cmd.Stdout != io.Writer(&stdout) {
		t.Errorf("stdout must be passed through unwrapped, got %T", cmd.Stdout)
	}
}

// ── Signals ───────────────────────────────────────────────────────────────

// TestRun_ForwardsSignalToChildAndCancelsRun is the signal hazard, proven end to
// end through the real process machinery: the child observes the signal, the
// wrapper exits 128+signum rather than dying itself, and the run is CLOSED with
// a cancel — not left open to expire against the run budget and page someone
// hours later for a job they killed themselves.
func TestRun_ForwardsSignalToChildAndCancelsRun(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			rec := newRecorder(t)
			sigs := make(chan os.Signal, 1)

			// The child traps the signal and reports it, so this proves DELIVERY,
			// not merely that the child stopped for some other reason.
			script := `trap 'echo GOT_SIGNAL >&2; exit ` + strconv.Itoa(128+int(sig)) + `' TERM INT
echo READY >&2
i=0; while [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done
exit 99`
			var stderr syncBuffer
			opts := baseOpts(rec, "sh", "-c", script)
			opts.Stderr = &stderr
			opts.Signals = sigs
			// stdin is deliberately NOT a terminal here, which is the case where
			// the wrapper must forward the signal itself: `kill -INT <wrapper>`
			// from a CI harness reaches only the wrapper, never the child.
			opts.Stdin = strings.NewReader("")

			done := make(chan int, 1)
			go func() {
				code, err := Run(opts)
				if err != nil {
					t.Error(err)
				}
				done <- code
			}()

			waitForText(t, &stderr, "READY")
			sigs <- sig

			select {
			case code := <-done:
				if code != 128+int(sig) {
					t.Errorf("exit code = %d, want %d (128+%d)", code, 128+int(sig), int(sig))
				}
			case <-time.After(15 * time.Second):
				t.Fatal("wrapper did not exit after the signal; it must not outlive its child")
			}

			if !strings.Contains(stderr.String(), "GOT_SIGNAL") {
				t.Fatalf("child never observed the signal; stderr = %q", stderr.String())
			}

			rec.waitFor(t, 2)
			got := rec.all()
			if got[len(got)-1].Path != "/"+testMonitor+"/cancel" {
				t.Errorf("last ping = %q, want the run closed with /cancel", got[len(got)-1].Path)
			}
		})
	}
}

// TestAlreadyDeliveredByTerminal pins the rule that keeps Ctrl-C from being
// delivered twice. The child is never put in its own process group, so at a
// terminal the kernel has already sent SIGINT/SIGQUIT to the whole foreground
// group; forwarding again looks, to any program treating a second Ctrl-C as
// "force quit now", exactly like the user pressing it twice. With no terminal
// there is no group delivery to rely on, so everything must be forwarded.
func TestAlreadyDeliveredByTerminal(t *testing.T) {
	cases := []struct {
		sig      os.Signal
		tty      bool
		wantSkip bool
	}{
		{syscall.SIGINT, true, true},
		{syscall.SIGQUIT, true, true},
		{syscall.SIGTERM, true, false},
		{syscall.SIGHUP, true, false},
		{syscall.SIGINT, false, false},
		{syscall.SIGQUIT, false, false},
		{syscall.SIGTERM, false, false},
	}
	for _, tc := range cases {
		got := alreadyDeliveredByTerminal(tc.sig, tc.tty)
		if got != tc.wantSkip {
			t.Errorf("alreadyDeliveredByTerminal(%v, tty=%v) = %v, want %v", tc.sig, tc.tty, got, tc.wantSkip)
		}
	}
}

// ── Reporting must never break the wrapped command ────────────────────────

// TestRun_PingFailureDoesNotAffectChild is the "never let monitoring break the
// job" hazard. With the ping endpoint returning 500 for everything, and again
// with it not existing at all, the child's exit code and output must be
// bit-identical to an unwrapped run.
func TestRun_PingFailureDoesNotAffectChild(t *testing.T) {
	t.Run("endpoint 500s", func(t *testing.T) {
		rec := newRecorder(t)
		rec.mu.Lock()
		rec.fail = true
		rec.mu.Unlock()
		var stdout, stderr bytes.Buffer
		opts := baseOpts(rec, "sh", "-c", "echo out; echo err >&2; exit 5")
		opts.Stdout = &stdout
		opts.Stderr = &stderr
		code, err := Run(opts)
		if err != nil {
			t.Fatalf("a ping failure must not surface as an error: %v", err)
		}
		if code != 5 {
			t.Fatalf("exit code = %d, want 5 — reporting must not change it", code)
		}
		if !strings.Contains(stdout.String(), "out") {
			t.Errorf("stdout = %q, want the child's output intact", stdout.String())
		}
		if !strings.Contains(stderr.String(), "err") {
			t.Errorf("stderr = %q, want the child's output intact", stderr.String())
		}
		// The user is told, on our stderr, that monitoring failed — a silent
		// monitoring failure is the thing this whole branch exists to avoid.
		if !strings.Contains(stderr.String(), "lastping: could not report") {
			t.Errorf("expected a warning about the failed ping; stderr = %q", stderr.String())
		}
	})

	t.Run("endpoint unreachable", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code, err := Run(Options{
			MonitorID: testMonitor,
			// A port nothing is listening on: connection refused, immediately.
			PingBase: "http://127.0.0.1:1",
			Argv:     []string{"sh", "-c", "echo out; exit 9"},
			Stdout:   &stdout,
			Stderr:   &stderr,
			NewRID:   func() string { return "deadbeef" },
		})
		if err != nil {
			t.Fatalf("an unreachable endpoint must not surface as an error: %v", err)
		}
		if code != 9 {
			t.Fatalf("exit code = %d, want 9", code)
		}
		if !strings.Contains(stdout.String(), "out") {
			t.Errorf("stdout = %q, want the child's output intact", stdout.String())
		}
	})
}

// ── Configuration ─────────────────────────────────────────────────────────

func TestRun_UsageErrors(t *testing.T) {
	t.Run("no command", func(t *testing.T) {
		code, err := Run(Options{MonitorID: testMonitor, PingBase: "http://127.0.0.1:1"})
		if err == nil {
			t.Fatal("want a usage error when no command was given")
		}
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
	})
	t.Run("no monitor", func(t *testing.T) {
		code, err := Run(Options{Argv: []string{"true"}})
		if err == nil {
			t.Fatal("want a usage error when no monitor id was given")
		}
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(err.Error(), "LASTPING_MONITOR") {
			t.Errorf("the error must name the env var too: %q", err)
		}
	})
	t.Run("no command must not report a run", func(t *testing.T) {
		rec := newRecorder(t)
		if _, err := Run(Options{MonitorID: testMonitor, PingBase: rec.srv.URL}); err == nil {
			t.Fatal("want an error")
		}
		if n := len(rec.all()); n != 0 {
			t.Errorf("a usage error must open no run; got %d pings", n)
		}
	})
}

// TestRun_PingBaseDefaultsToProduction pins the default without making a network
// call: an empty PingBase must resolve to DefaultPingBase, so a user who only
// sets --monitor reports to the real service.
func TestRun_PingBaseDefaultsToProduction(t *testing.T) {
	if DefaultPingBase != "https://ping.lastping.dev" {
		t.Fatalf("DefaultPingBase = %q; the wizard's copy-paste command and this constant must agree", DefaultPingBase)
	}
	var stderr bytes.Buffer
	// Resolution is observable through the warning line, which names the base
	// actually used, without needing to reach the real host: the DNS/connect
	// attempt fails fast in a sandbox and the child still runs and exits 0.
	code, err := Run(Options{
		MonitorID: testMonitor,
		Argv:      []string{"sh", "-c", "exit 0"},
		Stdout:    io.Discard,
		Stderr:    &stderr,
		Client:    &http.Client{Transport: refusingTransport{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), DefaultPingBase) {
		t.Errorf("an empty PingBase must fall back to %s; stderr = %q", DefaultPingBase, stderr.String())
	}
}

func TestRun_TrailingSlashInPingBaseDoesNotDoubleUp(t *testing.T) {
	rec := newRecorder(t)
	opts := baseOpts(rec, "sh", "-c", "exit 0")
	opts.PingBase = rec.srv.URL + "/"
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 2)
	for _, r := range rec.all() {
		if strings.Contains(r.Path, "//") {
			t.Errorf("ping path %q has a doubled slash", r.Path)
		}
	}
}

// ── Unit pieces ───────────────────────────────────────────────────────────

func TestRingBufferKeepsTheTail(t *testing.T) {
	r := &ringBuffer{max: 10}
	for _, s := range []string{"abcde", "fghij", "klmno"} {
		if _, err := r.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.String(); got != "fghijklmno" {
		t.Errorf("String() = %q, want the last 10 bytes", got)
	}
	if l := len(r.String()); l > 10 {
		t.Errorf("buffer grew to %d bytes", l)
	}
}

func TestRandomRIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		rid := randomRID()
		if len(rid) != 8 {
			t.Fatalf("rid %q is %d chars, want 8 — the shape internal/agentprompt teaches", rid, len(rid))
		}
		if _, err := url.QueryUnescape(rid); err != nil {
			t.Fatalf("rid %q is not URL-safe: %v", rid, err)
		}
		seen[rid] = true
	}
	// Not a randomness test — just proof that a fresh id is minted per call
	// rather than a constant, since core/check pairs start with success by rid.
	if len(seen) < 90 {
		t.Errorf("only %d distinct rids in 100 calls", len(seen))
	}
}

func TestExitCodeOfNil(t *testing.T) {
	if got := exitCodeOf(nil); got != 0 {
		t.Errorf("exitCodeOf(nil) = %d, want 0", got)
	}
}
