// Package memorybackup exports and restores portable RepoPlane durable memory.
package memorybackup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const (
	formatVersion       = 1
	defaultByteLimit    = uint64(1024 * 1024 * 1024)
	maximumByteLimit    = uint64(4 * 1024 * 1024 * 1024)
	maximumArchiveItems = 100_000
)

var (
	ErrDestination = errors.New("memory backup destination is invalid")
	ErrNotIdle     = errors.New("memory backup requires no active Runner process")
	ErrArchive     = errors.New("memory backup archive is invalid")
)

type recordTransfer interface {
	ExportRecords(context.Context, string, string, uint64) (store.RecordArchive, error)
	RestoreRecords(context.Context, string, string, store.RecordArchive) error
}

type snapshotGuard interface {
	BeginMemorySnapshot() (func(), error)
}

type Service struct {
	root     *workspace.Root
	records  recordTransfer
	runner   snapshotGuard
	stateDir string
	now      func() time.Time
}

type Request struct {
	Destination string `json:"destination" jsonschema:"existing absolute directory outside the workspace and state directory"`
	ByteLimit   uint64 `json:"byte_limit,omitempty" jsonschema:"maximum archive bytes; default 1073741824, max 4294967296"`
}

type Response struct {
	Status       string `json:"status"`
	ArchiveName  string `json:"archive_name"`
	ArchiveBytes uint64 `json:"archive_bytes"`
	SHA256       string `json:"sha256"`
	Records      uint64 `json:"records"`
	Revisions    uint64 `json:"revisions"`
	Files        uint64 `json:"files"`
	Portable     bool   `json:"portable"`
}

type manifest struct {
	Format          string    `json:"format"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	Records         uint64    `json:"records"`
	Revisions       uint64    `json:"revisions"`
	Imports         uint64    `json:"imports"`
	Files           uint64    `json:"files"`
	SecretsIncluded bool      `json:"secrets_included"`
}

func New(root *workspace.Root, records recordTransfer, runner snapshotGuard, stateDir string) *Service {
	return &Service{root: root, records: records, runner: runner, stateDir: stateDir, now: time.Now}
}

func (s *Service) Export(ctx context.Context, request Request) (Response, error) {
	limit, err := normalizeLimit(request.ByteLimit)
	if err != nil {
		return Response{}, err
	}
	destination, err := s.validateDestination(request.Destination)
	if err != nil {
		return Response{}, err
	}
	release, err := s.runner.BeginMemorySnapshot()
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrNotIdle, err)
	}
	defer release()
	archive, err := s.records.ExportRecords(ctx, s.root.ID(), s.root.ID(), maximumArchiveItems)
	if err != nil {
		return Response{}, err
	}
	files, total, err := collectFiles(ctx, s.stateDir, limit)
	if err != nil {
		return Response{}, err
	}
	recordsJSON, err := json.Marshal(archive)
	if err != nil {
		return Response{}, fmt.Errorf("encode durable memory: %w", err)
	}
	if uint64(len(recordsJSON))+total > limit {
		return Response{}, fmt.Errorf("memory backup exceeds byte_limit: %w", ErrArchive)
	}
	created := s.now().UTC()
	name := "repoplane-memory-" + created.Format("20060102T150405Z") + ".zip"
	path := filepath.Join(destination, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Response{}, fmt.Errorf("create memory backup: %w", err)
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	writer := zip.NewWriter(file)
	meta := manifest{Format: "repoplane-memory", Version: formatVersion, CreatedAt: created, Records: uint64(len(archive.Records)), Revisions: uint64(len(archive.Revisions)), Imports: uint64(len(archive.Imports)), Files: uint64(len(files)), SecretsIncluded: false}
	manifestJSON, _ := json.Marshal(meta)
	if err := writeBytes(writer, "manifest.json", manifestJSON); err != nil {
		return Response{}, err
	}
	if err := writeBytes(writer, "records.json", recordsJSON); err != nil {
		return Response{}, err
	}
	for _, item := range files {
		if err := writeFile(ctx, writer, item.source, item.name); err != nil {
			return Response{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return Response{}, fmt.Errorf("finish memory backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		return Response{}, fmt.Errorf("sync memory backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return Response{}, fmt.Errorf("close memory backup: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Response{}, err
	}
	if uint64(info.Size()) > limit {
		return Response{}, fmt.Errorf("memory backup exceeds byte_limit: %w", ErrArchive)
	}
	digest, err := hashFile(path)
	if err != nil {
		return Response{}, err
	}
	success = true
	return Response{Status: "ok", ArchiveName: name, ArchiveBytes: uint64(info.Size()), SHA256: digest, Records: meta.Records, Revisions: meta.Revisions, Files: meta.Files, Portable: true}, nil
}

func (s *Service) Restore(ctx context.Context, archivePath string, byteLimit uint64) (Response, error) {
	limit, err := normalizeLimit(byteLimit)
	if err != nil {
		return Response{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > limit {
		return Response{}, fmt.Errorf("open memory backup: %w", ErrArchive)
	}
	digest, err := hashFile(archivePath)
	if err != nil {
		return Response{}, err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return Response{}, fmt.Errorf("open memory backup: %w", ErrArchive)
	}
	defer reader.Close()
	if len(reader.File) > maximumArchiveItems+2 {
		return Response{}, fmt.Errorf("memory backup has too many entries: %w", ErrArchive)
	}
	var meta manifest
	var archive store.RecordArchive
	stage, err := os.MkdirTemp(s.stateDir, ".memory-restore-")
	if err != nil {
		return Response{}, fmt.Errorf("create restore staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	seen := make(map[string]bool)
	var expanded uint64
	for _, entry := range reader.File {
		name := filepath.ToSlash(entry.Name)
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "../") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name || entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.FileInfo().IsDir() {
			return Response{}, fmt.Errorf("unsafe memory backup entry: %w", ErrArchive)
		}
		if seen[name] {
			return Response{}, fmt.Errorf("duplicate memory backup entry: %w", ErrArchive)
		}
		seen[name] = true
		if entry.UncompressedSize64 > limit-expanded {
			return Response{}, fmt.Errorf("expanded memory backup exceeds byte_limit: %w", ErrArchive)
		}
		expanded += entry.UncompressedSize64
		switch name {
		case "manifest.json":
			if err := decodeEntry(entry, &meta, 64*1024); err != nil {
				return Response{}, err
			}
		case "records.json":
			if err := decodeEntry(entry, &archive, limit); err != nil {
				return Response{}, err
			}
		default:
			if !strings.HasPrefix(name, "files/runs/") && !strings.HasPrefix(name, "files/artifacts/") {
				return Response{}, fmt.Errorf("unsupported memory backup entry: %w", ErrArchive)
			}
			relative := strings.TrimPrefix(name, "files/")
			if err := extractEntry(ctx, entry, filepath.Join(stage, filepath.FromSlash(relative))); err != nil {
				return Response{}, err
			}
		}
	}
	if !seen["manifest.json"] || !seen["records.json"] || meta.Format != "repoplane-memory" || meta.Version != formatVersion || meta.SecretsIncluded || meta.Records != uint64(len(archive.Records)) || meta.Revisions != uint64(len(archive.Revisions)) || meta.Imports != uint64(len(archive.Imports)) || meta.Files != uint64(len(reader.File)-2) {
		return Response{}, fmt.Errorf("memory backup manifest mismatch: %w", ErrArchive)
	}
	promoted := make([]string, 0, 2)
	restoreCommitted := false
	defer func() {
		if !restoreCommitted {
			for _, target := range promoted {
				_ = os.RemoveAll(target)
			}
		}
	}()
	for _, directory := range []string{"runs", "artifacts"} {
		source := filepath.Join(stage, directory)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return Response{}, err
		}
		target := filepath.Join(s.stateDir, directory)
		if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
			return Response{}, fmt.Errorf("restore target %s is not empty: %w", directory, store.ErrConflict)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Response{}, err
		}
		_ = os.Remove(target)
		if err := os.Rename(source, target); err != nil {
			return Response{}, fmt.Errorf("restore %s: %w", directory, err)
		}
		promoted = append(promoted, target)
	}
	if err := s.records.RestoreRecords(ctx, s.root.ID(), s.root.ID(), archive); err != nil {
		return Response{}, err
	}
	restoreCommitted = true
	return Response{Status: "ok", ArchiveName: filepath.Base(archivePath), ArchiveBytes: uint64(info.Size()), SHA256: digest, Records: meta.Records, Revisions: meta.Revisions, Files: meta.Files, Portable: true}, nil
}

type archiveFile struct {
	source, name string
	size         uint64
}

func collectFiles(ctx context.Context, stateDir string, limit uint64) ([]archiveFile, uint64, error) {
	var files []archiveFile
	var total uint64
	for _, rootName := range []string{"runs", "artifacts"} {
		root := filepath.Join(stateDir, rootName)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("memory backup only accepts regular state files: %w", ErrArchive)
			}
			relative, err := filepath.Rel(stateDir, path)
			if err != nil {
				return err
			}
			total += uint64(info.Size())
			if total > limit || len(files) >= maximumArchiveItems {
				return fmt.Errorf("memory backup exceeds a bounded limit: %w", ErrArchive)
			}
			files = append(files, archiveFile{source: path, name: "files/" + filepath.ToSlash(relative), size: uint64(info.Size())})
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, 0, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, total, nil
}

func (s *Service) validateDestination(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", ErrDestination
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve backup destination: %w", ErrDestination)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", ErrDestination
	}
	for _, excluded := range []string{s.root.Resolved(), s.stateDir} {
		inside, err := pathInside(excluded, resolved)
		if err != nil || inside {
			return "", ErrDestination
		}
	}
	return resolved, nil
}

func pathInside(root, candidate string) (bool, error) {
	rootVolume, candidateVolume := filepath.VolumeName(root), filepath.VolumeName(candidate)
	if !strings.EqualFold(rootVolume, candidateVolume) {
		return false, nil
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func normalizeLimit(value uint64) (uint64, error) {
	if value == 0 {
		return defaultByteLimit, nil
	}
	if value > maximumByteLimit {
		return 0, fmt.Errorf("byte_limit exceeds maximum: %w", ErrArchive)
	}
	return value, nil
}

func writeBytes(writer *zip.Writer, name string, content []byte) error {
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = entry.Write(content)
	return err
}

func writeFile(ctx context.Context, writer *zip.Writer, source, name string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, &contextReader{ctx: ctx, reader: input})
	return err
}

func decodeEntry(entry *zip.File, target any, limit uint64) error {
	if entry.UncompressedSize64 > limit {
		return ErrArchive
	}
	reader, err := entry.Open()
	if err != nil {
		return ErrArchive
	}
	defer reader.Close()
	decoder := json.NewDecoder(io.LimitReader(reader, int64(limit)+1))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode memory backup entry: %w", ErrArchive)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("memory backup entry has trailing data: %w", ErrArchive)
	}
	return nil
}

func extractEntry(ctx context.Context, entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return ErrArchive
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, &contextReader{ctx: ctx, reader: input}); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
