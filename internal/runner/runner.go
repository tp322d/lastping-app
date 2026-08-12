// Package runner implements `lastping run` — a wrapper that turns any command
// into a reported LastPing run.
//
// # WHY THIS EXISTS
//
// Asking an agent to report on every task, forever, is a continuous
// obligation, and it decays (observed 2026-08-08: an agent given that
// instruction, correctly, still reported nothing for an hour of work while
// its monitor read "up"). A one-time hook install fixes that for tools that
// fire hooks. This wrapper fixes it for everything else, and fixes it
// harder: nothing in the reporting path depends on the wrapped program
// cooperating, knowing about LastPing, or even being an agent. If the
// process ran, the run is reported; if it exited, the run is closed.
//
// On an on_demand monitor core/check.recomputeDeadlines arms no deadline
// between runs, so a lapsed agent is indistinguishable from an idle one — a
// false all-clear. A wrapper is the only one of the three mechanisms with no
// window in which that can happen.
//
// THE THREE THINGS THAT MUST NEVER BREAK
//
//  1. The child's exit code reaches our caller unchanged. This wrapper is meant
//     to be put in front of CI steps; swallowing a non-zero exit turns a red
//     pipeline green, which is worse than no monitoring at all.
//  2. The child's stdin/stdout/stderr are the caller's, and a terminal stays a
//     terminal (see runStdio below).
//  3. Reporting is best-effort and strictly one-way. A network failure posting a
//     ping changes nothing about the child: not its exit code, not its lifetime,
//     not its output. Every ping error is a warning line on our own stderr.
//
// NO CREDENTIALS. The ping URL is unauthenticated by design — the monitor id IS
// the capability. There is deliberately no API key handling here; adding some
// would make a wrapper that anyone can drop into a pipeline into one that needs
// a secret provisioned first.
package runner

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultPingBase is the production ping host. Overridable via --ping-url /
// LASTPING_PING_URL, which is what makes this package testable against an
// httptest server.
const DefaultPingBase = "https://ping.lastping.dev"

// stderrTailLimit is how much of the child's stderr is kept for the terminal
// ping's body. The ingest handler stores at most 10000 bytes (ingest/handler.go
// bodyLimit), so this stays comfortably inside that: the goal is "the last thing
// it said before it died", not a log shipper.
const stderrTailLimit = 4096

// pingTimeout bounds a single ping attempt. Short on purpose: the child has
// already exited by the time the terminal ping is sent, and a wrapper that hangs
// for 30s after its command finished is a wrapper people take back out of their
// pipeline.
const pingTimeout = 5 * time.Second

// Exit codes for failures that are ours, not the child's. These are the shell's
// own conventions so that `lastping run -- nosuchthing` behaves like
// `nosuchthing` does: a script branching on 127 keeps working when the wrapper
// is added in front.
const (
	exitCannotExecute = 126 // found, but not executable
	exitNotFound      = 127 // command not found
	// exitUsage is for the wrapper's own argument errors. 2 rather than 1 so it
	// is distinguishable from a child that legitimately exited 1.
	exitUsage = 2
)

// Options is everything Run needs. Every field with an obvious process-global
// default is injectable, because the properties worth testing here (exit-code
// propagation, signal handling, reporting never affecting the child) are
// untestable against hardcoded os.Stdout / signal.Notify / crypto-random ids.
type Options struct {
	// MonitorID is the check uuid the run is reported against. Required.
	MonitorID string
	// PingBase is the ping host, without a trailing slash. Empty means
	// DefaultPingBase.
	PingBase string
	// Argv is the command and its arguments. Argv[0] is looked up on PATH.
	Argv []string

	// Stdin/Stdout/Stderr are handed to the child. When one of these is an
	// *os.File the child inherits the file descriptor itself — see runStdio.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Client posts the pings. Nil means a client with pingTimeout.
	Client *http.Client
	// Signals delivers signals to forward. main wires signal.Notify to it; a
	// test writes to it directly. Nil means no signal handling (the child then
	// still receives terminal-generated signals via the process group, because
	// Run never puts it in a new one).
	Signals <-chan os.Signal
	// NewRID mints the run id. Nil means 8 random hex characters, the same
	// shape LastPing's own onboarding instructions recommend for a run id.
	NewRID func() string
	// Env, when non-nil, replaces the child's environment. Nil inherits ours.
	Env []string
}

// Run executes Argv, reports the run, and returns the exit code the caller
// should exit with. It returns an error ONLY for a usage problem it detected
// before running anything; once the child has been started, every problem is
// expressed as an exit code, never as an error, because there is nothing left
// for a caller to do about it but exit.
func Run(opts Options) (int, error) {
	if len(opts.Argv) == 0 {
		return exitUsage, errors.New("no command given: put the command after --, e.g. lastping run -- ./nightly.sh")
	}
	if opts.MonitorID == "" {
		return exitUsage, errors.New("no monitor id: pass --monitor <uuid> or set LASTPING_MONITOR")
	}
	base := strings.TrimRight(opts.PingBase, "/")
	if base == "" {
		base = DefaultPingBase
	}
	if _, err := url.Parse(base + "/" + opts.MonitorID); err != nil {
		return exitUsage, fmt.Errorf("bad ping url %q: %w", base, err)
	}

	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	newRID := opts.NewRID
	if newRID == nil {
		newRID = randomRID
	}
	rep := &reporter{
		base:      base,
		monitorID: opts.MonitorID,
		rid:       newRID(),
		client:    opts.Client,
		warn:      stderr,
	}

	// The start ping goes first, before the child exists. If it fails the child
	// still runs — a monitoring outage must not stop the user's work — the run
	// simply never opens, and the terminal ping below closes nothing.
	rep.post("/start", "")

	cmd := exec.Command(opts.Argv[0], opts.Argv[1:]...)
	cmd.Env = opts.Env
	tail := runStdio(cmd, opts)

	if err := cmd.Start(); err != nil {
		code := startFailureExitCode(err)
		fmt.Fprintf(stderr, "lastping: %v\n", err)
		rep.post("/"+strconv.Itoa(code), err.Error())
		return code, nil
	}

	interrupted := forwardSignals(cmd, opts)
	waitErr := cmd.Wait()
	code := exitCodeOf(waitErr)

	// A run we interrupted is reported as cancelled, not failed. core/check's
	// applyCancel ends the outstanding run and disarms the overrun guard without
	// opening an incident, which is exactly right: an operator pressing Ctrl-C
	// is not the monitored thing breaking. Reporting the 130/143 exit as a
	// failure instead would page someone for their own keystroke — and reporting
	// nothing at all would leave the run open until the run budget expired and
	// paged them anyway, several hours later, for a run they ended themselves.
	if interrupted() {
		rep.post("/cancel", tail())
	} else {
		rep.post("/"+strconv.Itoa(code), tail())
	}
	return code, nil
}

// runStdio wires the child's three streams and returns a function yielding the
// captured stderr tail (empty when nothing was captured).
//
// TTY PASSTHROUGH — the decision and its justification.
//
// stdin and stdout are handed to the child UNTOUCHED, always. When the caller's
// are *os.File (they are, in the real binary), os/exec passes the file
// descriptors straight through, so the child gets the actual terminal: raw mode,
// window size, SIGWINCH, colour detection and full-screen TUIs all work, because
// from the child's point of view there is nothing between it and the tty. This
// is why `lastping run -- claude` is usable at all. Buffering or piping either
// stream to "watch" it is what breaks interactive programs, so this package
// simply never does.
//
// stderr is the one stream we want a copy of, and capturing REQUIRES a pipe —
// which would make isatty(2) false for the child and can silently change its
// behaviour (colour off, progress bars off, and for a few programs, line
// buffering on). So the choice is conditional and made once, here:
//
//   - stderr is a character device (a terminal): pass the fd through, capture
//     NOTHING. The terminal keeps working; the terminal ping carries an empty
//     body. This is the interactive case, where a human is already reading the
//     output and a stderr excerpt in the dashboard adds nothing.
//   - otherwise (a pipe, a file, a CI log — every non-interactive case, which is
//     where this wrapper actually earns its keep): tee through a bounded ring
//     buffer, and the last few KB of stderr become the failure body on the
//     dashboard.
//
// FULL PTY ALLOCATION IS DELIBERATELY OUT OF SCOPE. Capturing output from an
// interactive program without lying to it about being a terminal needs a pty
// pair, which drags in window-size propagation, raw-mode handling on our own
// tty, escape-sequence-laden capture, and a cgo-or-x/sys dependency — a large
// amount of machinery whose entire payoff is a stderr excerpt in the one case
// where a human is watching the output live. If it is ever wanted, this function
// is the only place that changes.
func runStdio(cmd *exec.Cmd, opts Options) func() string {
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout

	if opts.Stderr == nil {
		return func() string { return "" }
	}
	if isCharDevice(opts.Stderr) {
		cmd.Stderr = opts.Stderr
		return func() string { return "" }
	}
	ring := &ringBuffer{max: stderrTailLimit}
	cmd.Stderr = io.MultiWriter(opts.Stderr, ring)
	return ring.String
}

// isCharDevice reports whether w is a terminal-ish file. Deliberately no
// golang.org/x/term: it is only an indirect dependency of this module today, and
// os.File.Stat is enough for the one question being asked. A non-*os.File (an
// httptest buffer, an io.MultiWriter) is by definition not a terminal.
func isCharDevice(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// forwardSignals starts the signal pump and returns a predicate reporting
// whether a termination signal was seen. It stops when Wait returns, which is
// guaranteed because the returned predicate is only read after cmd.Wait().
//
// The child is deliberately NOT put in its own process group. Job control then
// keeps working exactly as it does without the wrapper: Ctrl-C at a terminal is
// delivered by the kernel to every process in the foreground group, child
// included, and Ctrl-Z suspends the pipeline as a whole.
//
// That is also why forwarding is conditional. When stdin is a terminal, the
// terminal-generated signals (SIGINT, SIGQUIT) have ALREADY reached the child,
// and forwarding them again would deliver a second one — indistinguishable, to
// programs that treat a second Ctrl-C as "force quit now", from the user pressing
// it twice. When stdin is not a terminal there is no process-group delivery to
// rely on (`kill -INT <wrapper-pid>` from a CI harness reaches only us), so they
// must be forwarded. SIGTERM and SIGHUP are always forwarded: neither is
// generated by the line discipline for the foreground group in the Ctrl-C sense,
// and a duplicate of either is harmless to a process that is being asked to die.
//
// We catch these signals rather than dying on them, so that the run gets closed
// (see the cancel path in Run). A wrapper that exits immediately on SIGTERM
// leaves the run open until the run budget expires — which pages the user hours
// later for a job they killed themselves.
func forwardSignals(cmd *exec.Cmd, opts Options) func() bool {
	var seen atomic.Bool
	if opts.Signals == nil {
		return seen.Load
	}
	stdinIsTTY := isCharDevice(readerAsWriter(opts.Stdin))
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig, ok := <-opts.Signals:
				if !ok {
					return
				}
				seen.Store(true)
				if !alreadyDeliveredByTerminal(sig, stdinIsTTY) && cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			}
		}
	}()
	// Closing on the first read of the predicate is enough: Run reads it exactly
	// once, after Wait.
	var once sync.Once
	return func() bool {
		once.Do(func() { close(done) })
		return seen.Load()
	}
}

// readerAsWriter lets isCharDevice answer the same question about stdin. An
// *os.File satisfies both interfaces; anything else is not a terminal anyway.
func readerAsWriter(r io.Reader) io.Writer {
	if f, ok := r.(*os.File); ok {
		return f
	}
	return nil
}

func alreadyDeliveredByTerminal(sig os.Signal, stdinIsTTY bool) bool {
	if !stdinIsTTY {
		return false
	}
	return sig == syscall.SIGINT || sig == syscall.SIGQUIT
}

// exitCodeOf turns cmd.Wait's error into the code this process should exit with.
//
// A child killed by a signal has no exit status of its own; ExitCode() reports
// -1. Shells report 128+signum for exactly this case, and every CI system and
// retry wrapper in existence reads that convention, so it is reproduced here
// rather than collapsing to a generic 1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}

func startFailureExitCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return exitNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return exitCannotExecute
	}
	return exitNotFound
}

// randomRID returns 8 hex characters — "8 hex characters is plenty" is the
// shape LastPing's own onboarding instructions recommend for a run id. On the
// vanishingly unlikely read failure it falls back to the clock rather than
// returning an empty rid: an unpaired ping is worse than a slightly less random
// one, because a start is paired with its success by rid.
func randomRID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffffff, 16)
	}
	return hex.EncodeToString(b[:])
}

// reporter posts the run's pings. Every method is best-effort and returns
// nothing: there is no failure here that the caller is allowed to react to.
type reporter struct {
	base      string
	monitorID string
	rid       string
	client    *http.Client
	warn      io.Writer
}

// post sends one ping. suffix is "" (success), "/start", "/cancel", or
// "/<exitcode>" — the numeric form is deliberate: ingest/classify.go maps a
// numeric suffix of 0 to success and anything non-zero to fail, so the wrapper
// does not have to decide what an exit code means and cannot disagree with the
// server about it.
func (r *reporter) post(suffix, body string) {
	client := r.client
	if client == nil {
		client = &http.Client{Timeout: pingTimeout}
	}
	target := r.base + "/" + r.monitorID + suffix + "?rid=" + url.QueryEscape(r.rid)

	// Two attempts. The terminal ping is the one that closes the run, and a
	// dropped one leaves the run open until the budget expires; a single retry
	// covers the common transient case without turning a monitoring blip into a
	// noticeable pause after the user's command has already finished.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
		if err != nil {
			lastErr = err
			break
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("ping returned %s", resp.Status)
	}
	if lastErr != nil && r.warn != nil {
		// A warning, never an error: the wrapped command's outcome is not ours to
		// change because our monitoring call failed.
		fmt.Fprintf(r.warn, "lastping: could not report to %s: %v\n", r.base, lastErr)
	}
}

// ringBuffer keeps the last max bytes written to it and nothing else. It is
// written from the goroutine os/exec uses to copy the child's stderr and read
// from Run's goroutine after Wait, so it is mutex-guarded.
type ringBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	if r.buf.Len() > r.max {
		b := r.buf.Bytes()
		keep := append([]byte(nil), b[len(b)-r.max:]...)
		r.buf.Reset()
		r.buf.Write(keep)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}
