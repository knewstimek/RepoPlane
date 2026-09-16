// Command repoplane starts the RepoPlane MCP server over stdin/stdout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"repoplane/internal/application"
	"repoplane/internal/config"
	"repoplane/internal/memorybackup"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "repoplane: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "usage" {
		return runUsage(args[1:])
	}
	if len(args) >= 2 && args[0] == "memory" && (args[1] == "export" || args[1] == "restore") {
		return runMemory(args[1], args[2:])
	}
	settings, err := config.Parse(args, os.Stderr)
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

func runUsage(args []string) error {
	var since, until string
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--since" || argument == "--until":
			if index+1 >= len(args) {
				return fmt.Errorf("%s requires YYYY-MM-DD", argument)
			}
			index++
			if argument == "--since" {
				since = args[index]
			} else {
				until = args[index]
			}
		case strings.HasPrefix(argument, "--since="):
			since = strings.TrimPrefix(argument, "--since=")
		case strings.HasPrefix(argument, "--until="):
			until = strings.TrimPrefix(argument, "--until=")
		default:
			remaining = append(remaining, argument)
		}
	}
	settings, err := config.Parse(remaining, os.Stderr)
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
	report, err := app.Usage(ctx, since, until)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func runMemory(action string, args []string) error {
	transfer, remaining, err := parseMemoryArgs(args)
	if err != nil {
		return err
	}
	settings, err := config.Parse(remaining, os.Stderr)
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
	var response memorybackup.Response
	if action == "export" {
		if transfer.destination == "" {
			return errors.New("memory export requires --destination")
		}
		response, err = app.ExportMemory(ctx, memorybackup.Request{Destination: transfer.destination, ByteLimit: transfer.byteLimit})
	} else {
		if transfer.archive == "" {
			return errors.New("memory restore requires --archive")
		}
		response, err = app.RestoreMemory(ctx, transfer.archive, transfer.byteLimit)
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

type memoryArgs struct {
	destination string
	archive     string
	byteLimit   uint64
}

func parseMemoryArgs(args []string) (memoryArgs, []string, error) {
	var result memoryArgs
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		name, value, matched := strings.Cut(args[index], "=")
		if name != "--destination" && name != "--archive" && name != "--byte-limit" {
			remaining = append(remaining, args[index])
			continue
		}
		if !matched {
			index++
			if index >= len(args) {
				return memoryArgs{}, nil, fmt.Errorf("%s requires a value", name)
			}
			value = args[index]
		}
		switch name {
		case "--destination":
			result.destination = value
		case "--archive":
			result.archive = value
		case "--byte-limit":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return memoryArgs{}, nil, errors.New("--byte-limit must be an unsigned integer")
			}
			result.byteLimit = parsed
		}
	}
	return result, remaining, nil
}
