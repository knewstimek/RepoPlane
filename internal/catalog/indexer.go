package catalog

import (
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
	"strconv"
	"strings"
	"time"

	"repoplane/internal/content"
	"repoplane/internal/store"
	"repoplane/internal/workspace"
)

const MaxManifestFiles = 10_000
const MaxCandidateFiles = 10_000
const MaxExecutableFingerprintBytes uint64 = 64 * 1024 * 1024

type Indexer struct {
	root           *workspace.Root
	repository     store.CatalogRepository
	catalogRoots   []string
	candidateRoots []string
	now            func() time.Time
}

func NewIndexer(root *workspace.Root, repository store.CatalogRepository, catalogRoots []string, candidateRoots ...[]string) *Indexer {
	candidates := []string{"tools", "scripts"}
	if len(candidateRoots) > 0 {
		candidates = append([]string(nil), candidateRoots[0]...)
	}
	return &Indexer{
		root:           root,
		repository:     repository,
		catalogRoots:   append([]string(nil), catalogRoots...),
		candidateRoots: candidates,
		now:            time.Now,
	}
}

// Refresh builds a deterministic generation and atomically publishes it when
// the catalog source fingerprint changed.
func (i *Indexer) Refresh(ctx context.Context) (store.CatalogGenerationMeta, error) {
	generation, err := i.Build(ctx)
	if err != nil {
		return store.CatalogGenerationMeta{}, err
	}
	current, err := i.repository.CurrentCatalogGeneration(ctx, i.root.ID())
	if err == nil && current.SourceFingerprint == generation.Meta.SourceFingerprint {
		return current, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.CatalogGenerationMeta{}, err
	}
	if err := i.repository.PublishCatalogGeneration(ctx, generation); err != nil {
		// A concurrent refresher may have published the same deterministic
		// generation. Re-read before treating the conflict as failure.
		if errors.Is(err, store.ErrConflict) {
			current, readErr := i.repository.CurrentCatalogGeneration(ctx, i.root.ID())
			if readErr == nil && current.SourceFingerprint == generation.Meta.SourceFingerprint {
				return current, nil
			}
		}
		return store.CatalogGenerationMeta{}, err
	}
	return generation.Meta, nil
}

func (i *Indexer) Build(ctx context.Context) (store.CatalogGeneration, error) {
	paths, err := i.manifestPaths(ctx)
	if err != nil {
		return store.CatalogGeneration{}, err
	}
	hasher := sha256.New()
	itemsByID := make(map[string][]store.CatalogItem)
	issues := make([]store.CatalogIssue, 0)
	unsupported, err := i.unsupportedManifestPaths(ctx)
	if err != nil {
		return store.CatalogGeneration{}, err
	}
	for _, relative := range unsupported {
		issues = append(issues, textIssue(
			"unsupported_manifest", sourceRef(relative, ""),
			"catalog source has a manifest-like format that this MVP does not parse",
		))
	}

	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			return store.CatalogGeneration{}, err
		}
		absolute, err := i.root.ResolveExisting(relative)
		if err != nil {
			issues = append(issues, issue("source_unavailable", sourceRef(relative, ""), err))
			continue
		}
		file, err := os.Open(absolute)
		if err != nil {
			issues = append(issues, issue("source_unavailable", sourceRef(relative, ""), err))
			continue
		}
		data, truncated, readErr := content.ReadBounded(file, uint64(MaxManifestBytes))
		closeErr := file.Close()
		if readErr != nil {
			issues = append(issues, issue("source_unavailable", sourceRef(relative, ""), readErr))
			continue
		}
		if closeErr != nil {
			issues = append(issues, issue("source_unavailable", sourceRef(relative, ""), closeErr))
			continue
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil {
			issues = append(issues, issue("source_unavailable", sourceRef(relative, ""), statErr))
			continue
		}
		fileHash := sha256.Sum256(data)
		hashText := hex.EncodeToString(fileHash[:])
		_, _ = hasher.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(fileHash[:])
		_, _ = hasher.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		_, _ = hasher.Write([]byte{0})
		if truncated {
			issues = append(issues, issue("invalid_manifest", sourceRef(relative, hashText), ErrManifestTooLarge))
			continue
		}

		manifest, err := Decode(strings.NewReader(string(data)), filepath.Ext(relative))
		if err != nil {
			issues = append(issues, issue("invalid_manifest", sourceRef(relative, hashText), err))
			continue
		}
		document, err := json.Marshal(manifest)
		if err != nil {
			return store.CatalogGeneration{}, fmt.Errorf("marshal manifest %q: %w", relative, err)
		}
		item := store.CatalogItem{
			ID:        manifest.ID,
			Revision:  strconv.FormatUint(manifest.Revision, 10),
			SourceRef: sourceRef(relative, hashText),
			Document:  document,
			Terms:     manifestTerms(manifest),
		}
		itemsByID[item.ID] = append(itemsByID[item.ID], item)
	}

	ids := make([]string, 0, len(itemsByID))
	for id := range itemsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]store.CatalogItem, 0, len(ids))
	for _, id := range ids {
		candidates := itemsByID[id]
		if len(candidates) > 1 {
			for _, candidate := range candidates {
				issues = append(issues, issue("duplicate_id", candidate.SourceRef, fmt.Errorf("catalog id %q has %d declarations", id, len(candidates))))
			}
			continue
		}
		items = append(items, candidates[0])
	}
	auditIssues, err := i.auditCandidates(ctx, items)
	if err != nil {
		return store.CatalogGeneration{}, err
	}
	issues = append(issues, auditIssues...)
	for _, item := range items {
		_, _ = hasher.Write([]byte(item.ID))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(item.ExecutionFingerprint))
		_, _ = hasher.Write([]byte{0})
	}
	sort.Slice(issues, func(a, b int) bool {
		if issues[a].SourceRef != issues[b].SourceRef {
			return issues[a].SourceRef < issues[b].SourceRef
		}
		return issues[a].Code < issues[b].Code
	})
	for _, issue := range issues {
		_, _ = hasher.Write([]byte(issue.Code))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(issue.SourceRef))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(issue.Detail)
		_, _ = hasher.Write([]byte{0})
	}
	fingerprint := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	generationHash := sha256.Sum256([]byte(i.root.ID() + "\x00" + fingerprint))
	return store.CatalogGeneration{
		Meta: store.CatalogGenerationMeta{
			ID:                "catalog_" + hex.EncodeToString(generationHash[:16]),
			WorkspaceID:       i.root.ID(),
			SourceFingerprint: fingerprint,
			CreatedAt:         i.now().UTC(),
		},
		Items:  items,
		Issues: issues,
	}, nil
}

func (i *Indexer) auditCandidates(ctx context.Context, items []store.CatalogItem) ([]store.CatalogIssue, error) {
	registered := make(map[string]struct{})
	issues := make([]store.CatalogIssue, 0)
	current, currentErr := i.repository.CurrentCatalogGeneration(ctx, i.root.ID())
	if currentErr != nil && !errors.Is(currentErr, store.ErrNotFound) {
		return nil, currentErr
	}
	for index := range items {
		item := &items[index]
		var manifest Manifest
		if err := json.Unmarshal(item.Document, &manifest); err != nil {
			return nil, fmt.Errorf("decode indexed manifest %q: %w", item.ID, err)
		}
		if manifest.Execution == nil || !pathLikeExecutableRef(manifest.Execution.ExecutableRef) {
			continue
		}
		path := normalizeRelativePath(manifest.Execution.ExecutableRef)
		registered[path] = struct{}{}
		absolute, err := i.root.ResolveExisting(filepath.FromSlash(path))
		if err != nil {
			issues = append(issues, textIssue(
				"missing_source", item.SourceRef,
				fmt.Sprintf("executable_ref %q does not resolve inside the workspace", manifest.Execution.ExecutableRef),
			))
			continue
		}
		file, err := os.Open(absolute)
		if err != nil {
			issues = append(issues, textIssue("needs_review", item.SourceRef, "executable could not be fingerprinted"))
			continue
		}
		fingerprint, _, hashErr := content.HashBounded(ctx, file, MaxExecutableFingerprintBytes)
		closeErr := file.Close()
		if hashErr != nil || closeErr != nil {
			issues = append(issues, textIssue("needs_review", item.SourceRef, "executable could not be fingerprinted within the audit limit"))
			continue
		}
		item.ExecutionFingerprint = fingerprint
		if currentErr == nil {
			previous, err := i.repository.GetCatalogItem(ctx, i.root.ID(), current.ID, item.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if err == nil && previous.Revision == item.Revision && previous.ExecutionFingerprint != "" && previous.ExecutionFingerprint != fingerprint {
				issues = append(issues, textIssue(
					"needs_review", item.SourceRef,
					"executable changed without a catalog revision change",
				))
			}
		}
	}

	candidates, err := i.candidatePaths(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if _, ok := registered[normalizeRelativePath(candidate)]; ok {
			continue
		}
		detail, _ := json.Marshal(map[string]string{
			"message": "execution candidate has no matching catalog executable_ref",
			"basis":   "heuristic",
		})
		issues = append(issues, store.CatalogIssue{
			Code: "unregistered_candidate", SourceRef: sourceRef(candidate, ""), Detail: detail,
		})
	}
	return issues, nil
}

func (i *Indexer) candidatePaths(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	for _, configured := range i.candidateRoots {
		absolute, err := i.root.ResolveExisting(configured)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve candidate root %q: %w", configured, err)
		}
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			candidate, err := executionCandidate(path, entry)
			if err != nil {
				return err
			}
			if !candidate {
				return nil
			}
			relative, err := filepath.Rel(i.root.Resolved(), path)
			if err != nil {
				return err
			}
			seen[normalizeRelativePath(relative)] = struct{}{}
			if len(seen) > MaxCandidateFiles {
				return fmt.Errorf("execution candidate count exceeds %d", MaxCandidateFiles)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func executionCandidate(path string, entry fs.DirEntry) (bool, error) {
	switch strings.ToLower(filepath.Ext(entry.Name())) {
	case ".bat", ".cmd", ".exe", ".ps1", ".py", ".sh":
		return true, nil
	}
	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	if info.Mode().Perm()&0o111 != 0 {
		return true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	prefix := make([]byte, 2)
	n, err := file.Read(prefix)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return n == 2 && string(prefix) == "#!", nil
}

func pathLikeExecutableRef(ref string) bool {
	if filepath.IsAbs(ref) || strings.ContainsAny(ref, `/\`) || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return true
	}
	switch strings.ToLower(filepath.Ext(ref)) {
	case ".bat", ".cmd", ".exe", ".ps1", ".py", ".sh":
		return true
	default:
		return false
	}
}

func normalizeRelativePath(path string) string {
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return strings.TrimPrefix(path, "./")
}

func (i *Indexer) manifestPaths(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, configured := range i.catalogRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		absolute, err := i.root.ResolveExisting(configured)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve catalog root %q: %w", configured, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if supportedManifestPath(configured) {
				relative, err := filepath.Rel(i.root.Resolved(), absolute)
				if err != nil {
					return nil, err
				}
				seen[filepath.Clean(relative)] = struct{}{}
			}
			continue
		}
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !supportedManifestPath(entry.Name()) {
				return nil
			}
			relative, err := filepath.Rel(i.root.Resolved(), path)
			if err != nil {
				return err
			}
			seen[filepath.Clean(relative)] = struct{}{}
			if len(seen) > MaxManifestFiles {
				return fmt.Errorf("catalog manifest count exceeds %d", MaxManifestFiles)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(a, b int) bool { return filepath.ToSlash(paths[a]) < filepath.ToSlash(paths[b]) })
	return paths, nil
}

func supportedManifestPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

func manifestLikeButUnsupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".toml":
		return true
	default:
		return false
	}
}

func (i *Indexer) unsupportedManifestPaths(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	for _, configured := range i.catalogRoots {
		absolute, err := i.root.ResolveExisting(configured)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve catalog root %q: %w", configured, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !supportedManifestPath(configured) {
				relative, err := filepath.Rel(i.root.Resolved(), absolute)
				if err != nil {
					return nil, err
				}
				seen[normalizeRelativePath(relative)] = struct{}{}
			}
			continue
		}
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !manifestLikeButUnsupported(entry.Name()) {
				return nil
			}
			relative, err := filepath.Rel(i.root.Resolved(), path)
			if err != nil {
				return err
			}
			seen[normalizeRelativePath(relative)] = struct{}{}
			if len(seen) > MaxManifestFiles {
				return fmt.Errorf("unsupported manifest count exceeds %d", MaxManifestFiles)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func sourceRef(relative, hash string) string {
	ref := "source:" + filepath.ToSlash(relative)
	if hash != "" {
		ref += "@sha256:" + hash
	}
	return ref
}

func issue(code, ref string, err error) store.CatalogIssue {
	if code == "source_unavailable" {
		return textIssue(code, ref, "catalog source could not be read")
	}
	return textIssue(code, ref, err.Error())
}

func textIssue(code, ref, message string) store.CatalogIssue {
	detail, _ := json.Marshal(map[string]string{"message": message})
	return store.CatalogIssue{Code: code, SourceRef: ref, Detail: detail}
}

func manifestTerms(manifest Manifest) []store.CatalogTerm {
	terms := make([]store.CatalogTerm, 0, 2+len(manifest.Tags)+len(manifest.Aliases)+len(manifest.UseWhen))
	add := func(field, value string, weight int) {
		value = strings.TrimSpace(value)
		if value != "" {
			terms = append(terms, store.CatalogTerm{Field: field, Term: value, Weight: weight})
		}
	}
	add("id", manifest.ID, 100)
	add("summary", manifest.Summary, 30)
	for _, value := range manifest.Aliases {
		add("alias", value, 25)
	}
	for _, value := range manifest.Tags {
		add("tag", value, 20)
	}
	for _, value := range manifest.UseWhen {
		add("use_when", value, 10)
	}
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].Field != terms[j].Field {
			return terms[i].Field < terms[j].Field
		}
		if terms[i].Term != terms[j].Term {
			return terms[i].Term < terms[j].Term
		}
		return terms[i].Weight < terms[j].Weight
	})
	return terms
}
