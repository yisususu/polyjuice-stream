package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yisususu/polyjuice-stream/src/source"
)

// Config describes the single random model served by the first implementation.
type Config struct {
	Listen       string
	Model        string
	Chunks       int
	ChunkChars   int
	FirstDelay   time.Duration
	Interval     time.Duration
	Seed         *int64
	WriteTimeout time.Duration
}

func Default() Config {
	return Config{
		Listen: "127.0.0.1:9900", Model: "mock-model", Chunks: 100, ChunkChars: 4,
		FirstDelay: 100 * time.Millisecond, Interval: 30 * time.Millisecond,
		WriteTimeout: 10 * time.Second,
	}
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
	if c.Model == "" || strings.TrimSpace(c.Model) != c.Model || strings.ContainsFunc(c.Model, unicode.IsControl) {
		return errors.New("model must be non-empty, with no surrounding whitespace or control characters")
	}
	if err := source.ValidateSize(c.Chunks, c.ChunkChars); err != nil {
		return err
	}
	if c.FirstDelay < 0 || c.Interval < 0 {
		return errors.New("first-delay and interval must not be negative")
	}
	if c.Chunks > 1 && c.Interval > (time.Duration(math.MaxInt64)-c.FirstDelay)/time.Duration(c.Chunks-1) {
		return errors.New("configured generation duration overflows time.Duration")
	}
	if c.WriteTimeout <= 0 {
		return errors.New("write-timeout must be greater than zero")
	}
	return nil
}

// Parse accepts the flags after the serve subcommand. Model files are a later stage.
func Parse(args []string, output io.Writer) (Config, error) {
	cfg := Default()
	if output == nil {
		output = io.Discard
	}
	fs := flag.NewFlagSet("polyjuice-stream serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address (host:port)")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model name advertised by /v1/models")
	fs.IntVar(&cfg.Chunks, "chunks", cfg.Chunks, "number of simulated content chunks per response")
	fs.IntVar(&cfg.ChunkChars, "chunk-chars", cfg.ChunkChars, "printable ASCII characters per random chunk")
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
		fmt.Fprintln(output, "\nServe a random mock model with streamed SSE or complete JSON responses.")
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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
