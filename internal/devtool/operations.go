package devtool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Root            string
	RequiredEnv     []string
	RequiredFiles   []string
	CommandDeadline time.Duration
}

func Verify(ctx context.Context, options Options) (Report, error) {
	root, err := workspaceRoot(options.Root)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Operation: "verify", StartedAt: time.Now().UTC()}
	report.Subject = repositorySubject(ctx, root)
	deadline := options.CommandDeadline
	if deadline <= 0 {
		deadline = 5 * time.Minute
	}
	verifyContext, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	report.Checks = append(report.Checks,
		runCommand(verifyContext, root, "schema.current", "go", "test", "-count=1", "./internal/schemaexport"),
		runCommand(verifyContext, root, "test.all", "go", "test", "-count=1", "./..."),
		runCommand(verifyContext, root, "vet.all", "go", "vet", "./..."),
		runCommand(verifyContext, root, "diff.whitespace", "git", "diff", "--check"),
	)
	buildDir, err := os.MkdirTemp("", "repoplane-verify-build-")
	if err != nil {
		report.Checks = append(report.Checks, Check{ID: "build", Status: "unknown", Summary: "temporary build directory unavailable"})
	} else {
		defer os.RemoveAll(buildDir)
		output := filepath.Join(buildDir, "repoplane")
		report.Checks = append(report.Checks, runCommand(verifyContext, root, "build", "go", "build", "-trimpath", "-o", output, "./cmd/repoplane"))
	}
	report.Finish()
	return report, nil
}

func Preflight(ctx context.Context, options Options) (Report, error) {
	root, err := workspaceRoot(options.Root)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Operation: "preflight", StartedAt: time.Now().UTC()}
	report.Subject = repositorySubject(ctx, root)
	if len(options.RequiredEnv)+len(options.RequiredFiles) > 128 {
		return Report{}, errors.New("preflight accepts at most 128 explicit prerequisites")
	}
	for _, tool := range []struct {
		id      string
		name    string
		version []string
	}{{"executable.go", "go", []string{"version"}}, {"executable.rg", "rg", []string{"--version"}}, {"executable.git", "git", []string{"--version"}}} {
		started := time.Now()
		check := Check{ID: tool.id, Status: "failed"}
		if _, lookupErr := exec.LookPath(tool.name); lookupErr != nil {
			check.Summary = "not found on PATH"
		} else if output, commandErr := commandOutput(ctx, root, tool.name, tool.version...); commandErr != nil {
			check.Summary = "version probe failed"
		} else {
			check.Status = "passed"
			check.Summary = firstLine(string(output))
		}
		check.DurationMS = time.Since(started).Milliseconds()
		report.Checks = append(report.Checks, check)
	}
	for _, name := range options.RequiredEnv {
		check := Check{ID: "env." + name, Status: "failed", Summary: "not set"}
		if validEnvName(name) {
			if _, exists := os.LookupEnv(name); exists {
				check.Status = "passed"
				check.Summary = "set (value withheld)"
			}
		} else {
			check.Summary = "invalid environment variable name"
		}
		report.Checks = append(report.Checks, check)
	}
	for _, relative := range options.RequiredFiles {
		check := Check{ID: "file." + filepath.ToSlash(relative), Status: "failed", Summary: "missing"}
		if safe, safeErr := existingWorkspacePath(root, relative); safeErr != nil {
			check.Summary = "outside workspace or invalid"
		} else if info, statErr := os.Stat(safe); statErr == nil && !info.IsDir() {
			check.Status = "passed"
			check.Summary = "present"
		}
		report.Checks = append(report.Checks, check)
	}
	gitCheck := Check{ID: "git.worktree", Status: "unknown", Summary: "Git status unavailable"}
	if report.Subject.GitCommit != "" && report.Subject.Dirty != nil {
		gitCheck.Status = "passed"
		if *report.Subject.Dirty {
			gitCheck.Summary = "worktree has changes"
		} else {
			gitCheck.Summary = "worktree clean"
		}
	}
	report.Checks = append(report.Checks, gitCheck)
	report.Finish()
	return report, nil
}

func PublicReleaseCheck(ctx context.Context, options Options) (Report, error) {
	root, err := workspaceRoot(options.Root)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Operation: "public-release-check", StartedAt: time.Now().UTC()}
	report.Subject = repositorySubject(ctx, root)
	status := Check{ID: "git.clean", Status: "failed", Summary: "worktree status unavailable"}
	if report.Subject.Dirty != nil {
		if *report.Subject.Dirty {
			status.Summary = "tracked or non-ignored untracked changes exist"
		} else {
			status.Status = "passed"
			status.Summary = "worktree clean"
		}
	}
	report.Checks = append(report.Checks, status)
	current, currentErr := scanTrackedTree(ctx, root)
	report.Checks = append(report.Checks, current)
	history, historyErr := scanHistory(ctx, root)
	report.Checks = append(report.Checks, history)
	report.Finish()
	return report, errors.Join(currentErr, historyErr)
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvName(name string) bool { return envNamePattern.MatchString(name) }

func firstLine(value string) string {
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func workspaceRelative(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("absolute path is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(root, relative))
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return candidate, nil
}

func existingWorkspacePath(root, relative string) (string, error) {
	candidate, err := workspaceRelative(root, relative)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", errors.New("resolved path escapes workspace")
	}
	return resolvedCandidate, nil
}

var publicPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z]:[\\/]users[\\/][^\\/\s]+`),
	regexp.MustCompile(`(?i)/ho` + `me/[^/\s]+`),
	regexp.MustCompile(`(?i)-----BEGIN ` + `(RSA |OPENSSH |EC )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)(gh` + `p_|github_pat_)[a-z0-9_]{12,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
}

func scanTrackedTree(ctx context.Context, root string) (Check, error) {
	check := Check{ID: "privacy.tracked-tree", Status: "failed"}
	files, err := trackedFiles(ctx, root)
	if err != nil {
		check.Summary = "cannot enumerate tracked files"
		return check, err
	}
	scanned, findings, oversized := 0, 0, 0
	for _, relative := range files {
		path, safeErr := workspaceRelative(root, relative)
		if safeErr != nil {
			findings++
			continue
		}
		info, readErr := os.Lstat(path)
		if readErr != nil {
			findings++
			continue
		}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				findings++
				continue
			}
			data = []byte(target)
		} else {
			const maximumTrackedFile = int64(8 * 1024 * 1024)
			if info.Size() > maximumTrackedFile {
				oversized++
				continue
			}
			resolved, resolveErr := existingWorkspacePath(root, relative)
			if resolveErr != nil {
				findings++
				continue
			}
			data, readErr = os.ReadFile(resolved)
			if readErr != nil {
				findings++
				continue
			}
		}
		scanned++
		if hasPublicFinding(data) {
			findings++
		}
	}
	check.Summary = fmt.Sprintf("scanned %d tracked files; findings %d; oversized %d", scanned, findings, oversized)
	if findings == 0 && oversized == 0 {
		check.Status = "passed"
		return check, nil
	}
	return check, errors.New("tracked tree has unreviewed privacy findings or oversized files")
}

func trackedFiles(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "ls-files", "-z")
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	const maximumFiles = 250_000
	reader := bufio.NewReader(stdout)
	var files []string
	for {
		value, readErr := reader.ReadString(0)
		if len(value) > 0 {
			value = strings.TrimSuffix(value, "\x00")
			if value != "" {
				files = append(files, value)
				if len(files) > maximumFiles {
					_ = command.Process.Kill()
					_ = command.Wait()
					return nil, errors.New("tracked tree exceeds 250000 file scan limit")
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = command.Wait()
			return nil, readErr
		}
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	return files, nil
}

func scanHistory(ctx context.Context, root string) (Check, error) {
	check := Check{ID: "privacy.git-history", Status: "failed"}
	hashes, err := enumerateHistoryObjects(ctx, root)
	if err != nil {
		check.Summary = "cannot enumerate Git history"
		return check, err
	}
	blobs, err := selectBlobs(ctx, root, hashes)
	if err != nil {
		check.Summary = "cannot classify Git objects"
		return check, err
	}
	scanned, findings, oversized, err := scanBlobs(ctx, root, blobs)
	if err != nil {
		check.Summary = "cannot read Git history blobs"
		return check, err
	}
	check.Summary = fmt.Sprintf("scanned %d history blobs; findings %d; oversized %d", scanned, findings, oversized)
	if findings == 0 && oversized == 0 {
		check.Status = "passed"
		return check, nil
	}
	return check, errors.New("Git history has unreviewed privacy findings or oversized blobs")
}

func enumerateHistoryObjects(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "rev-list", "--objects", "--all")
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	const maximumObjects = 250_000
	unique := make(map[string]struct{})
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		unique[fields[0]] = struct{}{}
		if len(unique) > maximumObjects {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, errors.New("Git history exceeds 250000 object scan limit")
		}
	}
	if err := scanner.Err(); err != nil {
		_ = command.Wait()
		return nil, err
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(unique))
	for hash := range unique {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	return hashes, nil
}

func selectBlobs(ctx context.Context, root string, hashes []string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	command.Dir = root
	command.Stdin = strings.NewReader(strings.Join(hashes, "\n") + "\n")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	var blobs []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[1] == "blob" {
			blobs = append(blobs, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	return blobs, nil
}

func scanBlobs(ctx context.Context, root string, hashes []string) (scanned, findings, oversized int, resultErr error) {
	command := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	command.Dir = root
	stdin, err := command.StdinPipe()
	if err != nil {
		return 0, 0, 0, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, 0, 0, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return 0, 0, 0, err
	}
	go func() {
		defer stdin.Close()
		_, _ = io.WriteString(stdin, strings.Join(hashes, "\n")+"\n")
	}()
	reader := bufio.NewReader(stdout)
	for range hashes {
		header, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = command.Wait()
			return scanned, findings, oversized, readErr
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			_ = command.Wait()
			return scanned, findings, oversized, errors.New("unexpected git cat-file header")
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size < 0 {
			_ = command.Wait()
			return scanned, findings, oversized, errors.New("invalid git blob size")
		}
		const maximumBlob = int64(8 * 1024 * 1024)
		if size > maximumBlob {
			oversized++
			if _, readErr := io.CopyN(io.Discard, reader, size+1); readErr != nil {
				_ = command.Wait()
				return scanned, findings, oversized, readErr
			}
			continue
		}
		data := make([]byte, size)
		if _, readErr := io.ReadFull(reader, data); readErr != nil {
			_ = command.Wait()
			return scanned, findings, oversized, readErr
		}
		if separator, readErr := reader.ReadByte(); readErr != nil || separator != '\n' {
			_ = command.Wait()
			return scanned, findings, oversized, errors.New("invalid git blob separator")
		}
		scanned++
		if hasPublicFinding(data) {
			findings++
		}
	}
	return scanned, findings, oversized, command.Wait()
}

func hasPublicFinding(data []byte) bool {
	for _, pattern := range publicPatterns {
		if pattern.Match(data) {
			return true
		}
	}
	return false
}
