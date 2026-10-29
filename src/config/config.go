package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yisususu/polyjuice-stream/src/source"
)

// Config describes the listener and either a CLI model or named model profiles.
type Config struct {
	Listen       string
	Model        string
	WriteTimeout time.Duration
	ModelConfig
	Models map[string]ModelConfig
}

// ModelConfig owns the content and timing settings for one independent model.
// A non-empty Dataset selects JSONL; otherwise the source generates random text.
type ModelConfig struct {
	Chunks     int
	ChunkChars int
	FirstDelay time.Duration
	Interval   time.Duration
	Seed       *int64
	Dataset    string
	TextField  string
}

func Default() Config {
	return Config{
		Listen: "127.0.0.1:9900", Model: "mock-model", WriteTimeout: 10 * time.Second,
		ModelConfig: DefaultModel(),
	}
}

func DefaultModel() ModelConfig {
	return ModelConfig{
		Chunks: 100, ChunkChars: 4, FirstDelay: 100 * time.Millisecond,
		Interval: 30 * time.Millisecond, TextField: "result",
	}
}

// Profiles returns a copy so callers do not retain the mutable configuration map.
func (c Config) Profiles() map[string]ModelConfig {
	profiles := c.Models
	if profiles == nil {
		profiles = map[string]ModelConfig{c.Model: c.ModelConfig}
	}
	result := make(map[string]ModelConfig, len(profiles))
	for name, model := range profiles {
		if model.Seed != nil {
			seed := *model.Seed
			model.Seed = &seed
		}
		result[name] = model
	}
	return result
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen must be a host:port address: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("listen port must be an integer between 0 and 65535")
	}
	if c.WriteTimeout <= 0 {
		return errors.New("write-timeout must be greater than zero")
	}
	profiles := c.Profiles()
	if len(profiles) == 0 {
		return errors.New("models must contain at least one model")
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateModelName(name); err != nil {
			return err
		}
		if err := profiles[name].Validate(); err != nil {
			return fmt.Errorf("model %q: %w", name, err)
		}
	}
	return nil
}

func validateModelName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("model must be non-empty, with no surrounding whitespace or control characters")
	}
	return nil
}

func (c ModelConfig) Validate() error {
	if c.Dataset == "" {
		if c.TextField != "" && c.TextField != "result" {
			return errors.New("text-field requires a dataset")
		}
		if err := source.ValidateSize(c.Chunks, c.ChunkChars); err != nil {
			return err
		}
		if err := c.ValidateDuration(c.Chunks); err != nil {
			return err
		}
	} else {
		if c.TextField == "" {
			return errors.New("text-field must be a non-empty top-level JSON key")
		}
		if c.Seed != nil {
			return errors.New("seed is only supported for random output; remove it when using a dataset")
		}
		if err := source.ValidateSize(1, c.ChunkChars); err != nil {
			return err
		}
		// The longest record is checked once the dataset has been loaded.
		if err := c.ValidateDuration(1); err != nil {
			return err
		}
	}
	return nil
}

// ValidateDuration checks the nominal generation duration for a selected size.
func (c ModelConfig) ValidateDuration(chunks int) error {
	if chunks < 1 {
		return errors.New("generation must contain at least one chunk")
	}
	if c.FirstDelay < 0 || c.Interval < 0 {
		return errors.New("first-delay and interval must not be negative")
	}
	if chunks > 1 && c.Interval > (time.Duration(math.MaxInt64)-c.FirstDelay)/time.Duration(chunks-1) {
		return errors.New("configured generation duration overflows time.Duration")
	}
	return nil
}

// Parse accepts either single-model flags or a YAML file, with listener overrides.
func Parse(args []string, output io.Writer) (Config, error) {
	cfg := Default()
	var configPath string
	if output == nil {
		output = io.Discard
	}
	fs := flag.NewFlagSet("polyjuice-stream serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "config", "", "YAML file defining named model profiles; cannot be combined with model flags")
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address (host:port)")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model name advertised by /v1/models")
	fs.IntVar(&cfg.Chunks, "chunks", cfg.Chunks, "number of simulated content chunks per random response")
	fs.IntVar(&cfg.ChunkChars, "chunk-chars", cfg.ChunkChars, "Unicode code points per chunk (random text uses printable ASCII)")
	fs.StringVar(&cfg.Dataset, "dataset", "", "JSONL file to load once and replay in order; paths are relative to the working directory")
	fs.StringVar(&cfg.TextField, "text-field", cfg.TextField, "exact top-level JSON key containing response text; requires --dataset")
	fs.DurationVar(&cfg.FirstDelay, "first-delay", cfg.FirstDelay, "wait before the first content chunk")
	fs.DurationVar(&cfg.Interval, "interval", cfg.Interval, "wait after each successful content write and flush")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", cfg.WriteTimeout, "timeout for each network write, not the whole generation")
	fs.Func("seed", "optional integer seed for reproducible request-assignment sequences", func(value string) error {
		seed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid seed: %w", err)
		}
		cfg.Seed = &seed
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(output, "Usage: polyjuice-stream serve [flags]")
		fmt.Fprintln(output, "\nServe random text or replay JSONL as streamed SSE or complete JSON responses.")
		fmt.Fprintln(output, "Output units are simulated text chunks, not tokenizer-accurate tokens.")
		fmt.Fprintln(output)
		fs.SetOutput(output)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["config"] {
		if configPath == "" {
			return Config{}, errors.New("config path must not be empty")
		}
		for _, name := range []string{"model", "dataset", "text-field", "chunks", "chunk-chars", "seed", "first-delay", "interval"} {
			if set[name] {
				return Config{}, fmt.Errorf("--config cannot be combined with --%s; set model options in YAML", name)
			}
		}
		loaded, err := Load(configPath)
		if err != nil {
			return Config{}, err
		}
		if set["listen"] {
			loaded.Listen = cfg.Listen
		}
		if set["write-timeout"] {
			loaded.WriteTimeout = cfg.WriteTimeout
		}
		if err := loaded.Validate(); err != nil {
			return Config{}, err
		}
		return loaded, nil
	}
	if set["dataset"] && cfg.Dataset == "" {
		return Config{}, errors.New("dataset path must not be empty")
	}
	if cfg.Dataset != "" && (set["chunks"] || set["seed"]) {
		return Config{}, errors.New("dataset cannot be combined with --chunks or --seed; replay length comes from each record")
	}
	if cfg.Dataset == "" && set["text-field"] {
		return Config{}, errors.New("text-field requires --dataset")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
