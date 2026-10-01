package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// serverVersion builds the stdio server with the given extra ldflags, sends it
// an initialize request and returns serverInfo.version.
func serverVersion(t *testing.T, ldflags string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lastping-mcp")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "LASTPING_API_KEY=test")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n"
	if _, err := stdin.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		var resp struct {
			Result struct {
				ServerInfo struct {
					Version string `json:"version"`
				} `json:"serverInfo"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad initialize response %q: %v", line, err)
		}
		return resp.Result.ServerInfo.Version
	case <-time.After(30 * time.Second):
		t.Fatal("no initialize response")
	}
	return ""
}

func TestVersionIsSetByLdflags(t *testing.T) {
	if got := serverVersion(t, "-X main.version=9.9.9"); got != "9.9.9" {
		t.Fatalf("stamped build reports %q, want 9.9.9", got)
	}
}

func TestUnstampedBuildReportsDev(t *testing.T) {
	if got := serverVersion(t, ""); got != "dev" {
		t.Fatalf("unstamped build reports %q, want dev", got)
	}
}

// build.sh writes the tag into the manifest and must hand the same value to
// the binaries it bundles.
func TestMCPBBuildStampsManifestVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mcpb", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !regexp.MustCompile(`-ldflags "[^"]*-X main\.version=\$version"`).MatchString(src) {
		t.Error("mcpb/build.sh does not stamp -X main.version=$version into lastping-mcp")
	}
	if !strings.Contains(src, `"$stage/manifest.json" "$version"`) {
		t.Error("mcpb/build.sh no longer writes $version into the manifest")
	}
}
