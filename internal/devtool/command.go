package devtool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const commandOutputLimit = 64 * 1024

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if len(p) >= commandOutputLimit {
		b.data = append(b.data[:0], p[len(p)-commandOutputLimit:]...)
		b.truncated = true
		return written, nil
	}
	if overflow := len(b.data) + len(p) - commandOutputLimit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return written, nil
}

func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), b.truncated
}

func runCommand(ctx context.Context, root, id, executable string, args ...string) Check {
	started := time.Now()
	check := Check{ID: id, Status: "failed"}
	buffer := &boundedBuffer{}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = root
	command.Stdout = buffer
	command.Stderr = buffer
	err := command.Run()
	check.DurationMS = time.Since(started).Milliseconds()
	output, truncated := buffer.snapshot()
	check.Truncated = truncated
	if err == nil {
		zero := 0
		check.ExitCode = &zero
		check.Status = "passed"
		check.Summary = "completed"
		return check
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		code := exitError.ExitCode()
		check.ExitCode = &code
		check.Summary = fmt.Sprintf("command exited with code %d", code)
	} else {
		check.Summary = sanitizeDiagnostic(err.Error(), root)
	}
	check.Diagnostics = sanitizeDiagnostic(string(output), root)
	return check
}

func repositorySubject(ctx context.Context, root string) Subject {
	subject := Subject{}
	if output, err := commandOutput(ctx, root, "git", "rev-parse", "HEAD"); err == nil {
		subject.GitCommit = string(bytes.TrimSpace(output))
	}
	if output, err := commandOutput(ctx, root, "git", "status", "--porcelain=v1", "--untracked-files=all"); err == nil {
		dirty := len(bytes.TrimSpace(output)) > 0
		subject.Dirty = &dirty
	}
	return subject
}

func commandOutput(ctx context.Context, root, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = root
	output := &boundedBuffer{}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	data, _ := output.snapshot()
	return data, err
}

func workspaceRoot(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(root), nil
}
