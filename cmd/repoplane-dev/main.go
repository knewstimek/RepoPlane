// Command repoplane-dev runs bounded local development checks. It is not an MCP runner.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"repoplane/internal/devtool"
)

type stringsFlag []string

func (values *stringsFlag) String() string { return fmt.Sprint([]string(*values)) }
func (values *stringsFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "repoplane-dev: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: repoplane-dev <verify|preflight|public-release-check> [options]")
	}
	operation := os.Args[1]
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("workspace", ".", "repository workspace root")
	output := flags.String("output", "", "report path; default .tmp/reports/<operation>.json")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall operation timeout")
	var requiredEnv, requiredFiles stringsFlag
	flags.Var(&requiredEnv, "require-env", "required environment variable name; repeatable")
	flags.Var(&requiredFiles, "require-file", "required workspace-relative file; repeatable")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if len(requiredFiles) == 0 && operation == "preflight" {
		requiredFiles = []string{
			"go.mod", "schemas/tools.v1.json", "schemas/toolboxes.v1.json", "schemas/environment-record.v1.schema.json",
			"schemas/run-receipt.v1.schema.json", "schemas/artifact-record.v1.schema.json",
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	options := devtool.Options{Root: *root, RequiredEnv: requiredEnv, RequiredFiles: requiredFiles, CommandDeadline: *timeout}
	var report devtool.Report
	var operationErr error
	switch operation {
	case "verify":
		report, operationErr = devtool.Verify(ctx, options)
	case "preflight":
		report, operationErr = devtool.Preflight(ctx, options)
	case "public-release-check":
		report, operationErr = devtool.PublicReleaseCheck(ctx, options)
	default:
		return fmt.Errorf("unknown operation %q", operation)
	}
	if report.SchemaVersion == "" {
		return operationErr
	}
	path := *output
	if path == "" {
		path = filepath.Join(*root, ".tmp", "reports", operation+".json")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(*root, path)
	}
	if err := devtool.WriteReport(path, report); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s: %s (report: %s)\n", operation, report.Status, path)
	if operationErr != nil {
		return operationErr
	}
	if report.Status != "passed" {
		writeFailureDetails(os.Stderr, report)
		return fmt.Errorf("%s status is %s", operation, report.Status)
	}
	return nil
}

func writeFailureDetails(writer interface{ Write([]byte) (int, error) }, report devtool.Report) {
	for _, check := range report.Checks {
		if check.Status == "passed" {
			continue
		}
		exit := "none"
		if check.ExitCode != nil {
			exit = fmt.Sprint(*check.ExitCode)
		}
		_, _ = fmt.Fprintf(writer, "failed check: %s status=%s exit=%s duration_ms=%d summary=%s\n", check.ID, check.Status, exit, check.DurationMS, check.Summary)
		if check.Diagnostics != "" {
			_, _ = fmt.Fprintf(writer, "diagnostics for %s (bounded):\n%s\n", check.ID, check.Diagnostics)
		}
	}
}
