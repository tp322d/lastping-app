package runner

import (
	"bytes"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestCmd is a throwaway *exec.Cmd for the runStdio tests, which inspect the
// wiring rather than running anything.
func newTestCmd() *exec.Cmd {
	return exec.Command("true")
}

// syncBuffer is a bytes.Buffer safe to write from os/exec's stderr-copy
// goroutine while the test reads it. Without the lock these tests are data
// races, which -race turns into failures.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForText blocks until want appears in b. Used to synchronise on the child
// actually being up and having installed its signal trap before the test sends
// a signal — sleeping a fixed duration instead is how a signal test becomes
// flaky on a loaded machine.
func waitForText(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; got %q", want, b.String())
}

// refusingTransport fails every request without touching the network, so a test
// can assert on which URL would have been called without depending on DNS,
// sandbox egress, or the real production host being up.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("refused by test transport")
}
