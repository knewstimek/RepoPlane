package repositorycheck

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"repoplane/internal/catalog"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository check source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestRepositoryCatalogParses(t *testing.T) {
	root := repositoryRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	parsed := 0
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".json") {
			continue
		}
		path := filepath.Join(root, "catalog", entry.Name())
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_, decodeErr := catalog.Decode(file, filepath.Ext(path))
		closeErr := file.Close()
		if decodeErr != nil {
			t.Errorf("%s: %v", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			t.Errorf("%s: close: %v", entry.Name(), closeErr)
		}
		parsed++
	}
	if parsed == 0 {
		t.Fatal("repository catalog is empty")
	}
}

func TestRepositoryJSONSchemasParse(t *testing.T) {
	root := repositoryRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, "schemas", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}
}

func TestRepositoryYAMLParses(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{".github", "checks"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var value any
			if err := yaml.Unmarshal(data, &value); err != nil {
				t.Errorf("%s: %v", filepath.Base(path), err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicTreeHasNoLocalIdentityOrSecretMaterial(t *testing.T) {
	root := repositoryRoot(t)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)[a-z]:[\\/]users[\\/][^\\/\s]+`),
		regexp.MustCompile(`(?i)/ho` + `me/[^/\s]+`),
		regexp.MustCompile(`(?i)-----BEGIN ` + `(RSA |OPENSSH |EC )?PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)(gh` + `p_|github_pat_)[a-z0-9_]+`),
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "go.sum" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for _, pattern := range patterns {
			if match := pattern.Find(data); match != nil {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("%s contains forbidden public-tree pattern %q", filepath.ToSlash(relative), sanitize(match))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sanitize(match []byte) string {
	value := string(match)
	if index := strings.IndexAny(value, `\\/`); index >= 0 {
		return value[:index] + "<redacted>"
	}
	return "<redacted>"
}
