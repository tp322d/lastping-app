package mcpb_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sampleZip is a small zip with no comment, like the one mcpb pack writes.
func sampleZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"name":"sample"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signatureBlock is shaped like the block mcpb sign appends.
func signatureBlock(der []byte) []byte {
	b := []byte("MCPB_SIG_V1")
	b = binary.LittleEndian.AppendUint32(b, uint32(len(der)))
	b = append(b, der...)
	return append(b, "MCPB_SIG_END"...)
}

func runZipTool(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("node", append([]string{"zip.mjs"}, args...)...).CombinedOutput()
	return string(out), err
}

// The build declares the signature block as the zip comment before signing,
// and refuses any bundle whose trailing bytes the comment length does not
// cover: strict zip readers reject such a file.
func TestZipTool_SignatureBlockIsTheZipComment(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	packed := filepath.Join(dir, "packed.zip")
	if err := os.WriteFile(packed, sampleZip(t), 0o600); err != nil {
		t.Fatal(err)
	}
	block := signatureBlock(bytes.Repeat([]byte{0x30}, 1300))

	// What mcpb sign produces on its own: the block after a zero comment length.
	undeclared := filepath.Join(dir, "undeclared.mcpb")
	if err := os.WriteFile(undeclared, append(sampleZip(t), block...), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runZipTool(t, "check", undeclared)
	if err == nil || !strings.Contains(out, "the zip comment length is 0 but 1327 bytes follow") {
		t.Fatalf("check accepted a bundle with undeclared trailing bytes: err=%v out=%s", err, out)
	}

	// Declared first, then the block appended: a valid zip.
	declared := filepath.Join(dir, "declared.zip")
	if out, err := runZipTool(t, "declare-comment", packed, declared, strconv.Itoa(len(block))); err != nil {
		t.Fatalf("declare-comment: %v\n%s", err, out)
	}
	zipBytes, err := os.ReadFile(declared)
	if err != nil {
		t.Fatal(err)
	}
	signed := filepath.Join(dir, "signed.mcpb")
	if err := os.WriteFile(signed, append(zipBytes, block...), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runZipTool(t, "check", signed); err != nil {
		t.Fatalf("check rejected a declared signature block: %v\n%s", err, out)
	}
	r, err := zip.OpenReader(signed)
	if err != nil {
		t.Fatalf("archive/zip cannot open the signed bundle: %v", err)
	}
	defer r.Close()
	if len(r.File) != 1 || r.Comment != string(block) {
		t.Fatalf("the zip comment is not the signature block: %d files, %d byte comment", len(r.File), len(r.Comment))
	}

	// A declared length that is off by one is refused.
	short := filepath.Join(dir, "short.mcpb")
	if err := os.WriteFile(short, append(zipBytes, block[:len(block)-1]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runZipTool(t, "check", short); err == nil {
		t.Fatalf("check accepted a comment length that overstates the trailing bytes\n%s", out)
	}

	// A zip that already carries a comment is refused rather than overwritten.
	again := filepath.Join(dir, "again.zip")
	if out, err := runZipTool(t, "declare-comment", declared, again, "10"); err == nil {
		t.Fatalf("declare-comment rewrote a zip that already has a comment\n%s", out)
	}
	if out, err := runZipTool(t, "declare-comment", packed, again, "65536"); err == nil {
		t.Fatalf("declare-comment accepted a comment length over 65535\n%s", out)
	}
}
