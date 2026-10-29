package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func startConfigServer(t *testing.T, directory, configPath string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	command := exec.CommandContext(ctx, binaryPath, "serve", "--config", configPath, "--listen", "127.0.0.1:0")
	command.Dir = directory
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	addresses := make(chan string, 1)
	logsDone, stopped := make(chan struct{}), make(chan struct{})
	var logs bytes.Buffer
	var waitErr error
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
	go func() {
		<-logsDone
		waitErr = command.Wait()
		close(stopped)
	}()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-stopped:
		case <-time.After(7 * time.Second):
			_ = command.Process.Kill()
			<-stopped
		}
		cancel()
		if waitErr != nil {
			t.Errorf("server exit: %v\n%s", waitErr, logs.String())
		}
	})
	select {
	case address := <-addresses:
		return address
	case <-stopped:
		t.Fatalf("server did not start: %v\n%s", waitErr, logs.String())
	case <-ctx.Done():
		t.Fatal("server did not report its listening address")
	}
	return ""
}

func TestCLIMultiModelConfigPathsAreRelativeToConfigDirectory(t *testing.T) {
	directory := t.TempDir()
	configDirectory := filepath.Join(directory, "configuration")
	if err := os.MkdirAll(configDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "responses.jsonl"), []byte("{\"result\":\"First 🙂\"}\n{\"result\":\"Second 🚀\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDirectory, "models.yaml")
	yaml := `listen: "127.0.0.1:1"
models:
  replay-model:
    source: jsonl
    path: responses.jsonl
    first_delay: 0s
    interval: 0s
  random-model:
    source: random
    chunks: 2
    chunk_chars: 3
    seed: 42
    first_delay: 0s
    interval: 0s
`
	if err := os.WriteFile(configPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	// Use an unrelated working directory and override the file's listen address.
	address := startConfigServer(t, t.TempDir(), configPath)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	res, err := client.Get(address + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Data []struct{ ID string } }
	err = json.NewDecoder(res.Body).Decode(&list)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("model discovery: status=%d error=%v", res.StatusCode, err)
	}
	var names []string
	for _, entry := range list.Data {
		names = append(names, entry.ID)
	}
	if !reflect.DeepEqual(names, []string{"random-model", "replay-model"}) {
		t.Fatalf("model list = %v", names)
	}
	for _, tc := range []struct{ model, expected string }{
		{"replay-model", "First 🙂"}, {"random-model", ""}, {"replay-model", "Second 🚀"},
	} {
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Hello"}]}`, tc.model)
		res, err := client.Post(address+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		var response struct {
			Model   string
			Choices []struct{ Message struct{ Content string } }
		}
		if err != nil || res.StatusCode != 200 || json.Unmarshal(data, &response) != nil || response.Model != tc.model || len(response.Choices) != 1 {
			t.Fatalf("%s response: status=%d body=%s error=%v", tc.model, res.StatusCode, data, err)
		}
		text := response.Choices[0].Message.Content
		if tc.model == "random-model" {
			if len(text) != 6 {
				t.Fatalf("random profile length = %d, want 6", len(text))
			}
		} else if text != tc.expected {
			t.Fatalf("replay = %q, want %q", text, tc.expected)
		}
	}
}

func TestCLIValidatesEveryModelBeforeListening(t *testing.T) {
	directory := t.TempDir()
	datasetPath := filepath.Join(directory, "broken.jsonl")
	if err := os.WriteFile(datasetPath, []byte("{\"result\":\"Valid\"}\n{\"output\":\"Missing result\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "models.yaml")
	if err := os.WriteFile(configPath, []byte(`models:
  a-valid-model:
    source: random
  z-broken-model:
    source: jsonl
    path: broken.jsonl
`), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, "serve", "--config", configPath, "--listen", listener.Addr().String())
	command.Dir = t.TempDir()
	output, err := command.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "z-broken-model") || !strings.Contains(string(output), datasetPath+":2:") {
		t.Fatalf("missing model/dataset diagnostic: error=%v output=%s", err, output)
	}
	if strings.Contains(string(output), "Cannot listen") || strings.Contains(string(output), "Listening on") {
		t.Fatalf("listened before validating every model: %s", output)
	}
}
