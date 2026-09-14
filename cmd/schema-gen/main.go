// Command schema-gen regenerates the versioned MCP tool schema document.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"repoplane/internal/schemaexport"
)

func main() {
	output := flag.String("out", "schemas/tools.v1.json", "output schema path")
	flag.Parse()
	encoded, err := schemaexport.Generate(context.Background())
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fatal(err)
	}
	temporary := *output + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		fatal(err)
	}
	if err := os.Rename(temporary, *output); err != nil {
		_ = os.Remove(temporary)
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
