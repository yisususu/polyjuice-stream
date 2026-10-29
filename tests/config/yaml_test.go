package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
)

func configFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestYAMLProfilesPreserveDefaultsAndExplicitZero(t *testing.T) {
	path := configFile(t, `listen: 127.0.0.1:9910
write_timeout: 2s
models:
  seeded:
    source: random
    seed: 0
    chunks: 3
    first_delay: 0s
    interval: 0s
  default-random:
    source: random
  recorded:
    source: jsonl
    path: ../datasets/responses.jsonl
    text_field: output
    chunk_chars: 2
    first_delay: 20ms
    interval: 40ms
`)
	cfg, err := config.Parse([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9910" || cfg.WriteTimeout != 2*time.Second || len(cfg.Models) != 3 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	seeded := cfg.Models["seeded"]
	if seeded.Seed == nil || *seeded.Seed != 0 || seeded.Chunks != 3 || seeded.ChunkChars != 4 || seeded.FirstDelay != 0 || seeded.Interval != 0 {
		t.Fatalf("defaults replaced explicit zero: %+v", seeded)
	}
	defaults := cfg.Models["default-random"]
	if defaults.Seed != nil || defaults.Chunks != 100 || defaults.FirstDelay != 100*time.Millisecond || defaults.Interval != 30*time.Millisecond {
		t.Fatalf("unexpected model defaults: %+v", defaults)
	}
	recorded := cfg.Models["recorded"]
	wantPath := filepath.Clean(filepath.Join(filepath.Dir(path), "../datasets/responses.jsonl"))
	if recorded.Dataset != wantPath || recorded.TextField != "output" || recorded.ChunkChars != 2 || recorded.FirstDelay != 20*time.Millisecond || recorded.Interval != 40*time.Millisecond {
		t.Fatalf("wrong replay profile: %+v", recorded)
	}
}

func TestYAMLGlobalOverridesAndModelFlagConflicts(t *testing.T) {
	path := configFile(t, "listen: 127.0.0.1:9910\nwrite_timeout: 2s\nmodels:\n  demo:\n    source: random\n")
	cfg, err := config.Parse([]string{"--config", path, "--listen", "127.0.0.1:0", "--write-timeout", "3s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:0" || cfg.WriteTimeout != 3*time.Second {
		t.Fatalf("global overrides not applied: %+v", cfg)
	}
	for _, args := range [][]string{
		{"--model", "mock-model"}, {"--chunks", "100"}, {"--chunk-chars", "4"},
		{"--dataset", "data.jsonl"}, {"--text-field", "result"}, {"--seed", "0"},
		{"--first-delay", "100ms"}, {"--interval", "30ms"},
	} {
		if _, err := config.Parse(append([]string{"--config", path}, args...), nil); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("model flags %q must conflict with YAML: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"--config", ""}, {"--config", path, "--listen", "bad"}, {"--config", path, "--write-timeout", "0s"},
	} {
		if _, err := config.Parse(args, nil); err == nil {
			t.Fatalf("accepted bad flags %q", args)
		}
	}
}

func TestYAMLRejectsInvalidOrAmbiguousConfiguration(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"empty file", ""},
		{"empty document", "---\n"},
		{"malformed", "models: ["},
		{"multiple documents", "models: {demo: {source: random}}\n---\nmodels: {other: {source: random}}"},
		{"trailing empty document", "models: {demo: {source: random}}\n---\n"},
		{"root sequence", "- demo"},
		{"unknown global", "models: {demo: {source: random}}\nport: 9900"},
		{"duplicate global", "listen: localhost:9900\nlisten: localhost:9901\nmodels: {demo: {source: random}}"},
		{"missing models", "listen: localhost:9900"},
		{"empty models", "models: {}"},
		{"null models", "models: null"},
		{"model list", "models: [demo]"},
		{"numeric model name", "models: {123: {source: random}}"},
		{"blank model name", "models: {'': {source: random}}"},
		{"whitespace model name", "models: {' demo': {source: random}}"},
		{"duplicate model name", "models:\n  demo: {source: random}\n  demo: {source: jsonl, path: data.jsonl}"},
		{"null model", "models: {demo: null}"},
		{"unknown model field", "models: {demo: {source: random, intervl: 30ms}}"},
		{"duplicate model field", "models: {demo: {source: random, chunks: 1, chunks: 2}}"},
		{"missing source", "models: {demo: {chunks: 3}}"},
		{"unknown source", "models: {demo: {source: other}}"},
		{"null source", "models: {demo: {source: null}}"},
		{"string count", "models: {demo: {source: random, chunks: '3'}}"},
		{"fractional count", "models: {demo: {source: random, chunks: 3.5}}"},
		{"zero count", "models: {demo: {source: random, chunks: 0}}"},
		{"null seed", "models: {demo: {source: random, seed: null}}"},
		{"seed overflow", "models: {demo: {source: random, seed: 9223372036854775808}}"},
		{"count overflow", "models: {demo: {source: random, chunks: 9223372036854775808}}"},
		{"null duration", "models: {demo: {source: random, interval: null}}"},
		{"numeric duration", "models: {demo: {source: random, first_delay: 0}}"},
		{"negative duration", "models: {demo: {source: random, interval: -1ms}}"},
		{"duration overflow", "models: {demo: {source: random, chunks: 3, interval: 2000000h}}"},
		{"wrong source path", "models: {demo: {source: random, path: data.jsonl}}"},
		{"wrong source field", "models: {demo: {source: random, text_field: result}}"},
		{"missing JSONL path", "models: {demo: {source: jsonl}}"},
		{"empty JSONL path", "models: {demo: {source: jsonl, path: ''}}"},
		{"null JSONL path", "models: {demo: {source: jsonl, path: null}}"},
		{"empty JSONL field", "models: {demo: {source: jsonl, path: a, text_field: ''}}"},
		{"wrong source seed", "models: {demo: {source: jsonl, path: a, seed: 0}}"},
		{"wrong source count", "models: {demo: {source: jsonl, path: a, chunks: 100}}"},
		{"zero chunk size", "models: {demo: {source: jsonl, path: a, chunk_chars: 0}}"},
		{"null listen", "listen: null\nmodels: {demo: {source: random}}"},
		{"invalid listen", "listen: bad\nmodels: {demo: {source: random}}"},
		{"zero timeout", "write_timeout: 0s\nmodels: {demo: {source: random}}"},
		{"null timeout", "write_timeout: null\nmodels: {demo: {source: random}}"},
		{"alias", "models:\n  first: &first {source: random}\n  second: *first"},
		{"merge key", "models:\n  first: &first {source: random}\n  second: {<<: *first}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := configFile(t, tc.yaml)
			if _, err := config.Load(path); err == nil || !strings.HasPrefix(err.Error(), path+":") {
				t.Fatalf("expected located configuration error, got %v", err)
			}
		})
	}
}

func TestYAMLDuplicateNamesAndUnknownFieldsReportLines(t *testing.T) {
	for _, tc := range []struct{ text, line string }{
		{"models:\n  demo: {source: random}\n  demo: {source: random}\n", ":3:"},
		{"models:\n  demo:\n    source: random\n    intervl: 30ms\n", ":4:"},
	} {
		path := configFile(t, tc.text)
		_, err := config.Load(path)
		if err == nil || !strings.HasPrefix(err.Error(), path+tc.line) {
			t.Fatalf("wrong location: %v", err)
		}
	}
}

func TestYAMLFileBoundsAndRegularFiles(t *testing.T) {
	prefix := "models: {demo: {source: random}}\n#"
	path := configFile(t, prefix+strings.Repeat("x", config.MaxConfigBytes-len(prefix)))
	if _, err := config.Load(path); err != nil {
		t.Fatalf("exact file limit rejected: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("x")
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append: %v, close: %v", err, closeErr)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized configuration accepted: %v", err)
	}
	if _, err := config.Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory accepted as configuration: %v", err)
	}
}

func TestProfilesCopyMutableConfiguration(t *testing.T) {
	seed := int64(42)
	model := config.DefaultModel()
	model.Seed = &seed
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"demo": model}
	copy := cfg.Profiles()
	*copy["demo"].Seed = 9
	delete(copy, "demo")
	if len(cfg.Models) != 1 || *cfg.Models["demo"].Seed != 42 {
		t.Fatal("profile copy changed original configuration")
	}
	cfg.Models = nil
	cfg.Seed = &seed
	copy = cfg.Profiles()
	*copy[cfg.Model].Seed = 10
	if seed != 42 || len(copy) != 1 {
		t.Fatal("single-model profile copy shares mutable state")
	}
	cfg.Models = map[string]config.ModelConfig{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("explicit empty registry fell back to the CLI model")
	}
}
