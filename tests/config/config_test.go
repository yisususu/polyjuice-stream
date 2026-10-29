package config_test

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
)

func TestParseDefaultsAndOverrides(t *testing.T) {
	defaults, err := config.Parse(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Listen != "127.0.0.1:9900" || defaults.Model != "mock-model" || defaults.Seed != nil {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	cfg, err := config.Parse([]string{
		"--listen", "[::1]:9901", "--model", "demo", "--chunks", "1000", "--chunk-chars", "8",
		"--first-delay", "0s", "--interval", "30ms", "--seed", "0", "--write-timeout", "2s",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "[::1]:9901" || cfg.Model != "demo" || cfg.Chunks != 1000 || cfg.ChunkChars != 8 ||
		cfg.FirstDelay != 0 || cfg.Interval != 30*time.Millisecond || cfg.WriteTimeout != 2*time.Second || cfg.Seed == nil || *cfg.Seed != 0 {
		t.Fatalf("flags not applied: %+v", cfg)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing port", []string{"--listen", "127.0.0.1"}},
		{"invalid port", []string{"--listen", "localhost:65536"}},
		{"empty model", []string{"--model", ""}},
		{"model whitespace", []string{"--model", " demo"}},
		{"model control", []string{"--model", "demo\n"}},
		{"zero chunks", []string{"--chunks", "0"}},
		{"negative chunks", []string{"--chunks", "-1"}},
		{"excess chunks", []string{"--chunks", "100001"}},
		{"excess chunk size", []string{"--chunk-chars", "4097"}},
		{"excess total bytes", []string{"--chunks", "2000", "--chunk-chars", "4096"}},
		{"negative delay", []string{"--first-delay", "-1ms"}},
		{"negative interval", []string{"--interval", "-1ms"}},
		{"duration overflow", []string{"--chunks", "2", "--first-delay", "2000000h", "--interval", "2000000h"}},
		{"invalid duration", []string{"--interval", "30"}},
		{"zero timeout", []string{"--write-timeout", "0s"}},
		{"invalid seed", []string{"--seed", "example"}},
		{"missing config file", []string{"--config", "models.yaml"}},
		{"empty dataset", []string{"--dataset", ""}},
		{"field without dataset", []string{"--text-field", "result"}},
		{"empty field", []string{"--dataset", "data.jsonl", "--text-field", ""}},
		{"dataset with random length", []string{"--dataset", "data.jsonl", "--chunks", "100"}},
		{"dataset with random seed", []string{"--dataset", "data.jsonl", "--seed", "0"}},
		{"dataset invalid chunk size", []string{"--dataset", "data.jsonl", "--chunk-chars", "0"}},
		{"positional argument", []string{"example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := config.Parse(tc.args, nil); err == nil {
				t.Fatalf("accepted invalid flags %q", tc.args)
			}
		})
	}
}

func TestDatasetConfiguration(t *testing.T) {
	for _, field := range []string{"result", "output", "literal.key"} {
		args := []string{"--dataset", "relative/path.jsonl", "--chunk-chars", "2"}
		if field != "result" {
			args = append(args, "--text-field", field)
		}
		cfg, err := config.Parse(args, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Dataset != "relative/path.jsonl" || cfg.TextField != field || cfg.ChunkChars != 2 || cfg.Seed != nil {
			t.Fatalf("unexpected replay config: %+v", cfg)
		}
	}
}

func TestReplayDurationUsesLoadedRecordSize(t *testing.T) {
	cfg, err := config.Parse([]string{"--dataset", "data.jsonl", "--first-delay", "0s", "--interval", "2000000h"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateDuration(2); err != nil {
		t.Fatalf("two chunks fit within time.Duration: %v", err)
	}
	if err := cfg.ValidateDuration(3); err == nil {
		t.Fatal("expected overflow for three chunks")
	}
}

func TestHelp(t *testing.T) {
	var output bytes.Buffer
	_, err := config.Parse([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	for _, expected := range []string{"Usage: polyjuice-stream serve", "9900", "first-delay", "simulated text chunks"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("help missing %q", expected)
		}
	}
}
