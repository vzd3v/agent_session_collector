package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/readapi"
)

func runServe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "read API TOML configuration path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *path == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "asc-hub serve: -config is required; positional arguments are not accepted")
		return 2
	}
	cfg, err := readapi.LoadConfig(*path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "asc-hub serve: %v\n", err)
		return 1
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil)).With("component", "session-read-api")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveReadAPI(ctx, cfg, log); err != nil {
		log.Error("read API stopped", "error", err)
		return 1
	}
	return 0
}

func serveReadAPI(ctx context.Context, cfg readapi.Config, log *slog.Logger) (result error) {
	api, err := readapi.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, api.Close()) }()
	network, addr := "tcp", cfg.Listen
	if cfg.Socket != "" {
		network, addr = "unix", cfg.Socket
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, network, addr)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if cfg.Socket != "" {
		if err := os.Chmod(cfg.Socket, 0o660); err != nil {
			return err
		}
	}
	server := &http.Server{
		Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: cfg.Timeout,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Info("read API listening", "network", network, "address", listener.Addr().String())
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			result = errors.Join(err, server.Close())
		}
		err := <-done
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
		return result
	}
}
