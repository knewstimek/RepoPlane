package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"repoplane/internal/resultview"
)

const renderInputLimit = 16 * 1024 * 1024

func runRenderResult(args []string, input io.Reader, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("render-result", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(diagnostics, "usage: repoplane render-result < result.json")
		_, _ = fmt.Fprintln(diagnostics, "Remove duplicate JSON text echoes from one MCP CallToolResult; preserve unique content, errors, warnings, and metadata. Input limit: 16 MiB. Does not start the MCP server.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("render-result takes one CallToolResult from stdin, not positional arguments")
	}
	data, err := io.ReadAll(io.LimitReader(input, renderInputLimit+1))
	if err != nil {
		return fmt.Errorf("read render-result input: %w", err)
	}
	if len(data) > renderInputLimit {
		return errors.New("render-result input exceeds 16 MiB; no output written")
	}
	rendered, err := resultview.Render(data)
	if err != nil {
		return err
	}
	rendered = append(rendered, '\n')
	_, err = output.Write(rendered)
	return err
}

// Keep render-result independent of workspace configuration and database state.
func renderResultCommand(args []string) error {
	return runRenderResult(args, os.Stdin, os.Stdout, os.Stderr)
}
