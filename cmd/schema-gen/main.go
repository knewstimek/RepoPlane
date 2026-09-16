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
	toolboxOutput := flag.String("toolbox-out", "", "optional toolbox schema path")
	metricsOutput := flag.String("metrics-out", "", "optional serialized-byte footprint report path")
	flag.Parse()
	encoded, err := schemaexport.Generate(context.Background())
	if err != nil {
		fatal(err)
	}
	if err := writeAtomic(*output, encoded); err != nil {
		fatal(err)
	}
	var toolboxEncoded []byte
	if *toolboxOutput != "" {
		toolboxEncoded, err = schemaexport.GenerateToolboxes(context.Background())
		if err != nil {
			fatal(err)
		}
		if err := writeAtomic(*toolboxOutput, toolboxEncoded); err != nil {
			fatal(err)
		}
	}
	if *metricsOutput != "" {
		var metrics []byte
		if len(toolboxEncoded) == 0 {
			metrics, err = schemaexport.GenerateFootprint(encoded)
		} else {
			metrics, err = schemaexport.GenerateFootprint(encoded, toolboxEncoded)
		}
		if err != nil {
			fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(*metricsOutput), 0o755); err != nil {
			fatal(err)
		}
		metricsTemporary := *metricsOutput + ".tmp"
		if err := os.WriteFile(metricsTemporary, metrics, 0o644); err != nil {
			fatal(err)
		}
		if err := os.Rename(metricsTemporary, *metricsOutput); err != nil {
			_ = os.Remove(metricsTemporary)
			fatal(err)
		}
	}
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
