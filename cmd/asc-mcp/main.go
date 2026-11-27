package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vzd3v/agent_session_collector/sessionapi"
	"github.com/vzd3v/agent_session_collector/sessionmcp"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "asc-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("asc-mcp", flag.ContinueOnError)
	endpoint := flags.String("url", os.Getenv("ASC_API_URL"), "session API base URL")
	tokenPath := flags.String("token-file", os.Getenv("ASC_API_TOKEN_FILE"), "file containing the session API bearer token")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *endpoint == "" || *tokenPath == "" || flags.NArg() != 0 {
		return errors.New("-url and -token-file are required")
	}
	f, err := os.Open(*tokenPath)
	if err != nil {
		return fmt.Errorf("open token file: %w", err)
	}
	token, err := io.ReadAll(io.LimitReader(f, 4097))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(token) > 4096 {
		return errors.New("cannot read a bounded token file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client, err := sessionapi.NewClient(*endpoint, strings.TrimSpace(string(token)), &http.Client{
		Transport: transport, Timeout: 45 * time.Second,
	})
	if err != nil {
		return err
	}
	return sessionmcp.New(client, version).Run(ctx, &mcp.StdioTransport{})
}
