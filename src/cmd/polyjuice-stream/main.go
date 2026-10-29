package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
	"github.com/yisususu/polyjuice-stream/src/httpapi"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, output io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(output, "Usage: polyjuice-stream serve [flags]\n\nRun 'polyjuice-stream serve --help' for options.")
		return 0
	}
	if args[0] != "serve" {
		fmt.Fprintf(output, "Unknown command %q. Run 'polyjuice-stream --help' for usage.\n", args[0])
		return 2
	}
	cfg, err := config.Parse(args[1:], output)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(output, "Configuration error: %v\nRun 'polyjuice-stream serve --help' for options.\n", err)
		return 2
	}
	handler, err := httpapi.New(cfg)
	if err != nil {
		fmt.Fprintf(output, "Cannot create service: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(output, "Cannot listen on %s: %v\nChoose another address with --listen, for example --listen 127.0.0.1:9901.\n", cfg.Listen, err)
		return 1
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, ErrorLog: log.New(output, "http: ", log.LstdFlags),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	// Content writes have their own deadlines. A global WriteTimeout would also
	// include deliberate generation waits and could terminate valid long streams.
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	fmt.Fprintf(output, "Polyjuice Stream\nListening on http://%s\nModel: %s | chunks: %d | chunk chars: %d | first delay: %s | interval: %s\n", listener.Addr(), cfg.Model, cfg.Chunks, cfg.ChunkChars, cfg.FirstDelay, cfg.Interval)
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(output, "Server stopped: %v\n", err)
			return 1
		}
	case <-ctx.Done():
		fmt.Fprintln(output, "Stopping server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(output, "Server stopped: %v\n", err)
			return 1
		}
	}
	return 0
}
