// Command repoplane starts the RepoPlane MCP server over stdin/stdout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"repoplane/internal/application"
	"repoplane/internal/config"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "repoplane: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := application.Open(ctx, settings, version)
	if err != nil {
		return err
	}
	defer app.Close()
	return app.Run(ctx)
}
