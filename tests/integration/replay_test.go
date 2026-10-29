package integration_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/source"
)

func TestCLIRejectsBadDatasetBeforeListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, tc := range []struct {
		name string
		line string
	}{
		{"malformed JSON", "{"},
		{"missing field", `{"output":"Wrong field"}`},
		{"non-string field", `{"result":42}`},
		{"oversized result", `{"result":"` + strings.Repeat("a", source.MaxOutputBytes+1) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.jsonl")
			if err := os.WriteFile(path, []byte("{\"result\":\"Valid first record\"}\n\n"+tc.line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binaryPath, "serve", "--dataset", path, "--listen", listener.Addr().String(), "--chunk-chars", "4096")
			output, err := command.CombinedOutput()
			if ctx.Err() != nil || err == nil || !strings.Contains(string(output), path+":3:") {
				t.Fatalf("dataset did not fail with file and physical line: error=%v output=%s", err, output)
			}
			// The port is occupied: a bind error would reveal validation happened too late.
			if strings.Contains(string(output), "Cannot listen") || strings.Contains(string(output), "Listening on") {
				t.Fatalf("attempted to listen before validating the whole dataset: %s", output)
			}
		})
	}
}
