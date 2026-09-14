package repositorycheck

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository check source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestRepositoryYAMLParses(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(filepath.Join(root, ".github"), func(path string, entry fs.DirEntry, err error) error {
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
