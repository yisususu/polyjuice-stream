package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var binaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "polyjuice-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(dir, "polyjuice-stream")
	build := exec.Command("go", "build", "-o", binaryPath, "./src/cmd/polyjuice-stream")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build CLI: %v\n%s", err, output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestCLIHelpAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		text string
	}{
		{"help", []string{"serve", "--help"}, 0, "9900"},
		{"invalid option", []string{"serve", "--chunks", "0"}, 2, "Configuration error"},
		{"unknown command", []string{"unknown"}, 2, "Unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binaryPath, tc.args...)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			if command.ProcessState.ExitCode() != tc.code || !strings.Contains(string(output), tc.text) {
				t.Fatalf("exit=%d error=%v output=%s", command.ProcessState.ExitCode(), err, output)
			}
		})
	}
}

func TestCLIPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, "serve", "--listen", listener.Addr().String())
	output, err := command.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "Choose another address with --listen") {
		t.Fatalf("port conflict: error=%v output=%s", err, output)
	}
}

func TestCLIServesAndStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, "serve", "--listen", "127.0.0.1:0", "--chunks", "3", "--first-delay", "0s", "--interval", "0s", "--seed", "42")
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	addresses := make(chan string, 1)
	logsDone := make(chan struct{})
	var logs bytes.Buffer
	go func() {
		defer close(logsDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(&logs, line)
			if strings.HasPrefix(line, "Listening on ") {
				addresses <- strings.TrimPrefix(line, "Listening on ")
			}
		}
	}()
	// Wait only after stderr is drained; Cmd.Wait otherwise closes its pipe.
	stopped := make(chan error, 1)
	go func() { <-logsDone; stopped <- command.Wait() }()
	var address string
	select {
	case address = <-addresses:
	case err := <-stopped:
		t.Fatalf("early exit: %v\n%s", err, logs.String())
	case <-ctx.Done():
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(address + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	response, err = client.Post(address+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"Hello"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(content, []byte("data: [DONE]")) {
		t.Fatalf("stream: status=%d error=%v body=%s", response.StatusCode, err, content)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("shutdown: %v\n%s", err, logs.String())
		}
	case <-ctx.Done():
		t.Fatal("server did not stop")
	}
}
