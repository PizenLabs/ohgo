package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/security"
)

// Target evidence limits. A completion check reads only the coordinates the
// request named; these caps keep a pathological package or file list from
// turning a review decision into an unbounded scan. Exceeding a cap is an
// unresolved target, never a silent partial verification.
const (
	// MaxTargetFiles bounds the files one package target may capture.
	MaxTargetFiles = 256
	// MaxTargetFileBytes bounds one file the verifier will read.
	MaxTargetFileBytes = 4 << 20
	// MaxTargetBytes bounds the total bytes one capture or check may read.
	MaxTargetBytes = 16 << 20
)

// StructuralResolver locates Go declarations without exposing a second index.
//
// tools.SymbolsTool implements this interface over its existing ast.Index, so
// the completion path and the model's symbol queries share one in-process
// repository coordinate store.
type StructuralResolver interface {
	// LocateSymbol returns declarations whose name or qualified name matches.
	LocateSymbol(name string) ([]SymbolLocation, error)
	// LocatePackage returns declarations in the named Go package.
	LocatePackage(name string) ([]SymbolLocation, error)
}

// SymbolLocation is one declaration coordinate.
type SymbolLocation struct {
	// Path is repository-relative and slash-separated.
	Path string `json:"path"`
	// Symbol is the bare declared name.
	Symbol string `json:"symbol"`
	// QualifiedName is the receiver- or package-qualified coordinate.
	QualifiedName string `json:"qualifiedName,omitempty"`
	// Line and EndLine bound the declaration in Path.
	Line    int `json:"line"`
	EndLine int `json:"endLine,omitempty"`
}

// FileState is the deterministic content state of one target file.
type FileState struct {
	// Path is repository-relative and slash-separated.
	Path string `json:"path"`
	// Exists reports whether the file was present when observed.
	Exists bool `json:"exists"`
	// Hash is the SHA-256 of the file content, empty when absent.
	Hash string `json:"hash,omitempty"`
}

// SymbolState is the content state of one declaration.
type SymbolState struct {
	// Location is where the declaration was found.
	Location SymbolLocation `json:"location"`
	// Exists reports whether the declaration was found.
	Exists bool `json:"exists"`
	// Hash is the SHA-256 of the declaration's source range.
	Hash string `json:"hash,omitempty"`
}

// TargetSnapshotEntry is the pre-run state of one contract target.
type TargetSnapshotEntry struct {
	// Target is the coordinate the request named.
	Target contract.Target `json:"target"`
	// Operation is the requested direction.
	Operation contract.ChangeOp `json:"operation"`
	// Resolved reports that the target could be mapped to a concrete file or
	// declaration before the run.
	Resolved bool `json:"resolved"`
	// Ambiguous reports that the coordinate matched more than one declaration
	// or package directory.
	Ambiguous bool `json:"ambiguous,omitempty"`
	// Issue explains why the target could not be resolved.
	Issue string `json:"issue,omitempty"`
	// Files are the file states for a path or package target.
	Files []FileState `json:"files,omitempty"`
	// Symbols are the declaration states for a symbol target.
	Symbols []SymbolState `json:"symbols,omitempty"`
}

// TargetSnapshot is the bounded pre-run evidence for one contract.
//
// It is captured before execution and compared after. It is deliberately not a
// repository snapshot: only the coordinates the user named are read.
type TargetSnapshot struct {
	// Root is the absolute workspace root the paths are relative to.
	Root string `json:"root,omitempty"`
	// Entries are one per target, in contract order.
	Entries []TargetSnapshotEntry `json:"entries,omitempty"`
	// FilesRead and BytesRead record the verification's read cost.
	FilesRead int   `json:"filesRead,omitempty"`
	BytesRead int64 `json:"bytesRead,omitempty"`
	// Issues are capture failures that were not target-specific.
	Issues []string `json:"issues,omitempty"`
}

// TargetStatus is the structural verdict for one expected change.
type TargetStatus string

// Target check statuses.
const (
	// TargetSatisfied means the target changed in the requested direction.
	TargetSatisfied TargetStatus = "satisfied"
	// TargetUnchanged means the target exists but its content is unchanged.
	TargetUnchanged TargetStatus = "unchanged"
	// TargetMissing means the target was absent when it had to exist or appear.
	TargetMissing TargetStatus = "missing"
	// TargetWrongOperation means the target changed in a different direction
	// than requested (deleted instead of modified, or still present after a
	// delete).
	TargetWrongOperation TargetStatus = "wrong_operation"
	// TargetUnresolved means the coordinate could not be mapped to concrete
	// evidence, so no claim can be made.
	TargetUnresolved TargetStatus = "unresolved"
	// TargetSemantic means the target describes a result or a scope (a
	// technology, the repository, a command, a resource) rather than a
	// filesystem coordinate. Structural checking does not apply, and completion
	// must never require a changed path to contain the target's name.
	TargetSemantic TargetStatus = "semantic"
)

// TargetCheck is the structural result for one expected change.
type TargetCheck struct {
	// Target is the coordinate checked.
	Target contract.Target
	// Operation is the requested direction.
	Operation contract.ChangeOp
	// Status is the deterministic verdict.
	Status TargetStatus
	// Detail explains a non-satisfied verdict in evidence terms.
	Detail string
}

// StructuralEvidence is the deterministic target evidence for one completion
// decision.
type StructuralEvidence struct {
	// Checks are one per contract target.
	Checks []TargetCheck
	// Unexpected lists observed mutation paths outside the requested scope.
	Unexpected []string
	// Unattributed lists observed mutations whose path the runtime could not
	// establish. It stays separate from Unexpected: "outside the scope" and
	// "cannot say where" are different evidence gaps.
	Unattributed []string
	// Issues are capture or resolution failures not tied to one target.
	Issues []string
}

// CaptureTargets reads the pre-run state of every contract target.
//
// A nil resolver leaves symbol and package targets unresolved, which is a
// reviewable state rather than an error: the caller may still have named a
// path target that can be checked without repository intelligence.
func CaptureTargets(root string, intent contract.IntentContract, resolver StructuralResolver) *TargetSnapshot {
	return CaptureTargetsContext(context.Background(), root, intent, resolver)
}

// CaptureTargetsContext is CaptureTargets with a cancellation check at every
// target boundary and before each file read. The structural index's own build
// remains the existing uncancellable Phase 1 operation; this seam ensures the
// completion path does not extend a cancelled run.
func CaptureTargetsContext(ctx context.Context, root string, intent contract.IntentContract, resolver StructuralResolver) *TargetSnapshot {
	snapshot := &TargetSnapshot{Root: root}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			snapshot.Issues = append(snapshot.Issues, "target capture cancelled: "+err.Error())
			return snapshot
		}
	}
	if strings.TrimSpace(root) == "" {
		snapshot.Issues = append(snapshot.Issues, "no workspace root is available for structural verification")
		return snapshot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		snapshot.Issues = append(snapshot.Issues, fmt.Sprintf("resolve workspace root: %v", err))
		return snapshot
	}
	snapshot.Root = abs

	cache := map[string]targetFile{}
	reads := &targetReads{ctx: ctx}
	for _, target := range intent.Targets {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				snapshot.Issues = append(snapshot.Issues, "target capture cancelled: "+err.Error())
				break
			}
		}
		entry := TargetSnapshotEntry{
			Target:    target,
			Operation: intent.OperationFor(target),
		}
		captureTarget(&entry, abs, resolver, cache, reads)
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	snapshot.FilesRead = reads.files
	snapshot.BytesRead = reads.bytes
	return snapshot
}

// targetReads accumulates the bounded read cost of a capture or check.
type targetReads struct {
	ctx   context.Context
	files int
	bytes int64
}

// targetFile caches one read so a path and a symbol in the same file cost one
// read, not two.
type targetFile struct {
	state FileState
	data  []byte
}

// captureTarget fills one snapshot entry.
func captureTarget(entry *TargetSnapshotEntry, root string, resolver StructuralResolver, cache map[string]targetFile, reads *targetReads) {
	switch entry.Target.Kind {
	case contract.TargetPath:
		if entry.Target.Directory {
			files, err := readTargetDirectory(root, entry.Target.Value, cache, reads)
			if err != nil {
				entry.Issue = err.Error()
				return
			}
			entry.Resolved = true
			entry.Files = files
			return
		}
		state, err := readTargetFile(root, entry.Target.Value, cache, reads)
		if err != nil {
			// A path target may name an existing directory without the
			// extractor having marked it. Capture the directory rather than
			// failing the whole target on a filesystem fact the request
			// implies.
			if directoryExists(root, entry.Target.Value) {
				files, walkErr := readTargetDirectory(root, entry.Target.Value, cache, reads)
				if walkErr == nil {
					entry.Target.Directory = true
					entry.Resolved = true
					entry.Files = files
					return
				}
			}
			entry.Issue = err.Error()
			return
		}
		entry.Resolved = true
		entry.Files = []FileState{state}

	case contract.TargetTechnology, contract.TargetRepository,
		contract.TargetCommand, contract.TargetResource:
		// These targets describe a result or a scope. There is no filesystem
		// coordinate to capture, and requiring one would turn "Next.js" back
		// into a path.
		entry.Resolved = true

	case contract.TargetSymbol:
		if resolver == nil {
			entry.Issue = "no structural resolver is available for a symbol target"
			return
		}
		locations, err := resolver.LocateSymbol(entry.Target.Value)
		if err != nil {
			entry.Issue = fmt.Sprintf("locate symbol %q: %v", entry.Target.Value, err)
			return
		}
		if len(locations) == 0 {
			if entry.Operation == contract.ChangeCreate {
				entry.Resolved = true
				return
			}
			entry.Issue = fmt.Sprintf("symbol %q was not found before the run", entry.Target.Value)
			return
		}
		if len(locations) > 1 {
			entry.Ambiguous = true
			entry.Issue = fmt.Sprintf("symbol %q matched %d declarations", entry.Target.Value, len(locations))
		}
		for _, location := range locations {
			state, err := readSymbolState(root, location, cache, reads)
			if err != nil {
				entry.Issue = err.Error()
				return
			}
			entry.Symbols = append(entry.Symbols, state)
		}
		entry.Resolved = true

	case contract.TargetPackage:
		if resolver == nil {
			entry.Issue = "no structural resolver is available for a package target"
			return
		}
		locations, err := resolver.LocatePackage(entry.Target.Value)
		if err != nil {
			entry.Issue = fmt.Sprintf("locate package %q: %v", entry.Target.Value, err)
			return
		}
		if len(locations) == 0 {
			entry.Issue = fmt.Sprintf("package %q was not found before the run", entry.Target.Value)
			return
		}
		dirs := packageDirectories(locations)
		if len(dirs) > 1 {
			entry.Ambiguous = true
			entry.Issue = fmt.Sprintf("package %q matched %d directories", entry.Target.Value, len(dirs))
			return
		}
		paths := uniqueSymbolPaths(locations)
		if len(paths) > MaxTargetFiles {
			entry.Issue = fmt.Sprintf("package %q has %d files, above the %d-file verification bound",
				entry.Target.Value, len(paths), MaxTargetFiles)
			return
		}
		for _, path := range paths {
			state, err := readTargetFile(root, path, cache, reads)
			if err != nil {
				entry.Issue = err.Error()
				return
			}
			entry.Files = append(entry.Files, state)
		}
		entry.Resolved = true

	default:
		entry.Issue = fmt.Sprintf("unknown target kind %q", entry.Target.Kind)
	}
}

// readTargetFile reads one repository-contained file into the cache.
func readTargetFile(root, value string, cache map[string]targetFile, reads *targetReads) (FileState, error) {
	if reads.ctx != nil {
		if err := reads.ctx.Err(); err != nil {
			return FileState{}, fmt.Errorf("verification cancelled: %w", err)
		}
	}
	rel, ok := repositoryRelative(root, value)
	if !ok {
		return FileState{}, fmt.Errorf("target %q is outside the workspace", value)
	}
	if cached, ok := cache[rel]; ok {
		return cached.state, nil
	}
	scope := security.NormalizeScope(root, rel)
	if scope.Class != security.ScopeInside {
		return FileState{}, fmt.Errorf("target %q is outside the workspace", value)
	}
	info, err := os.Stat(scope.Path)
	if err != nil {
		if os.IsNotExist(err) {
			state := FileState{Path: rel}
			cache[rel] = targetFile{state: state}
			return state, nil
		}
		return FileState{}, fmt.Errorf("stat target %q: %w", value, err)
	}
	if info.IsDir() {
		return FileState{}, fmt.Errorf("target %q is a directory; name a file or a Go package", value)
	}
	if info.Size() > MaxTargetFileBytes {
		return FileState{}, fmt.Errorf("target %q is %d bytes, above the %d-byte verification bound",
			value, info.Size(), MaxTargetFileBytes)
	}
	if reads.bytes+info.Size() > MaxTargetBytes {
		return FileState{}, fmt.Errorf("target verification exceeds the %d-byte total bound", MaxTargetBytes)
	}
	data, err := os.ReadFile(scope.Path)
	if err != nil {
		return FileState{}, fmt.Errorf("read target %q: %w", value, err)
	}
	state := FileState{
		Path:   rel,
		Exists: true,
		Hash:   hashBytes(data),
	}
	cache[rel] = targetFile{state: state, data: data}
	reads.files++
	reads.bytes += int64(len(data))
	return state, nil
}

// directoryExists reports whether value resolves inside the workspace to an
// existing directory.
func directoryExists(root, value string) bool {
	rel, ok := repositoryRelative(root, value)
	if !ok {
		return false
	}
	scope := security.NormalizeScope(root, rel)
	if scope.Class != security.ScopeInside {
		return false
	}
	info, err := os.Stat(scope.Path)
	return err == nil && info.IsDir()
}

// readTargetDirectory captures the bounded file set under one directory
// target.
//
// A directory target is a scope, not one file: "add app/" means files may
// appear anywhere under app/. Only the files are captured, so a rename inside
// the directory is visible as a changed path set rather than as an opaque
// directory hash.
func readTargetDirectory(root, value string, cache map[string]targetFile, reads *targetReads) ([]FileState, error) {
	rel, ok := repositoryRelative(root, value)
	if !ok {
		return nil, fmt.Errorf("target %q is outside the workspace", value)
	}
	scope := security.NormalizeScope(root, rel)
	if scope.Class != security.ScopeInside {
		return nil, fmt.Errorf("target %q is outside the workspace", value)
	}
	info, err := os.Stat(scope.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat target %q: %w", value, err)
	}
	if !info.IsDir() {
		state, err := readTargetFile(root, value, cache, reads)
		if err != nil {
			return nil, err
		}
		return []FileState{state}, nil
	}
	var files []FileState
	var total int
	err = filepath.WalkDir(scope.Path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if reads.ctx != nil {
			if err := reads.ctx.Err(); err != nil {
				return fmt.Errorf("verification cancelled: %w", err)
			}
		}
		if entry.IsDir() {
			switch strings.ToLower(entry.Name()) {
			case ".git", ".ohgo", "node_modules", "vendor", ".next", "dist", "build", "target":
				if path != scope.Path {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		targetRel, ok := repositoryRelative(root, path)
		if !ok {
			return nil
		}
		state, err := readTargetFile(root, targetRel, cache, reads)
		if err != nil {
			return err
		}
		files = append(files, state)
		total++
		if total > MaxTargetFiles {
			return fmt.Errorf("target %q has more than %d files, above the verification bound",
				value, MaxTargetFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// readSymbolState hashes one declaration's source range.
func readSymbolState(root string, location SymbolLocation, cache map[string]targetFile, reads *targetReads) (SymbolState, error) {
	fileState, err := readTargetFile(root, location.Path, cache, reads)
	if err != nil {
		return SymbolState{}, err
	}
	state := SymbolState{Location: location, Exists: fileState.Exists}
	if !fileState.Exists {
		return state, nil
	}
	cached := cache[fileState.Path]
	hash, err := hashRange(cached.data, location.Line, location.EndLine)
	if err != nil {
		return SymbolState{}, fmt.Errorf("hash symbol %q in %s: %w", location.Symbol, location.Path, err)
	}
	state.Hash = hash
	return state, nil
}

// CheckTargets compares the pre-run snapshot with the current tree.
func CheckTargets(snapshot *TargetSnapshot, resolver StructuralResolver) StructuralEvidence {
	var evidence StructuralEvidence
	if snapshot == nil {
		evidence.Issues = append(evidence.Issues, "no pre-run target evidence was captured")
		return evidence
	}
	evidence.Issues = append(evidence.Issues, snapshot.Issues...)
	cache := map[string]targetFile{}
	reads := &targetReads{}
	for _, entry := range snapshot.Entries {
		check := TargetCheck{Target: entry.Target, Operation: entry.Operation}
		switch {
		case !entry.Resolved:
			check.Status = TargetUnresolved
			check.Detail = firstNonEmptyString(entry.Issue, "the target could not be resolved before the run")
		case entry.Ambiguous:
			check.Status = TargetUnresolved
			check.Detail = firstNonEmptyString(entry.Issue, "the target is ambiguous")
		case !entry.Target.Kind.Filesystem():
			check.Status = TargetSemantic
			check.Detail = fmt.Sprintf("%s target %q describes the objective, not a filesystem coordinate",
				entry.Target.Kind, entry.Target.Value)
		default:
			check = checkCurrentTarget(snapshot.Root, entry, resolver, cache, reads, &evidence)
		}
		evidence.Checks = append(evidence.Checks, check)
	}
	sort.Strings(evidence.Unexpected)
	return evidence
}

// TargetEvidenceWithoutSnapshot derives the checks a contract can still make
// when no pre-run capture exists.
//
// A semantic target needs no capture: it describes the objective, and the
// mutation and verification evidence decide completion. A filesystem target
// without a pre-run state is unresolved evidence, not a satisfied target, so it
// remains a review boundary.
func TargetEvidenceWithoutSnapshot(intent contract.IntentContract) StructuralEvidence {
	var evidence StructuralEvidence
	for _, target := range intent.Targets {
		check := TargetCheck{Target: target, Operation: intent.OperationFor(target)}
		if target.Kind.Filesystem() {
			check.Status = TargetUnresolved
			check.Detail = "no pre-run target evidence was captured for " + target.Value
		} else {
			check.Status = TargetSemantic
			check.Detail = fmt.Sprintf("%s target %q describes the objective, not a filesystem coordinate",
				target.Kind, target.Value)
		}
		evidence.Checks = append(evidence.Checks, check)
	}
	return evidence
}

// checkCurrentTarget performs the post-run comparison for one entry.
func checkCurrentTarget(root string, entry TargetSnapshotEntry, resolver StructuralResolver, cache map[string]targetFile, reads *targetReads, evidence *StructuralEvidence) TargetCheck {
	check := TargetCheck{Target: entry.Target, Operation: entry.Operation}
	switch entry.Target.Kind {
	case contract.TargetPath:
		if entry.Target.Directory || (len(entry.Files) > 1) {
			current, err := readTargetDirectory(root, entry.Target.Value, cache, reads)
			if err != nil {
				check.Status = TargetUnresolved
				check.Detail = err.Error()
				return check
			}
			return comparePathSet(entry.Files, current, check)
		}
		current, err := readTargetFile(root, entry.Target.Value, cache, reads)
		if err != nil {
			check.Status = TargetUnresolved
			check.Detail = err.Error()
			return check
		}
		before := FileState{Path: entry.Target.Value}
		if len(entry.Files) > 0 {
			before = entry.Files[0]
		}
		return comparePath(before, current, check)

	case contract.TargetSymbol:
		if resolver == nil {
			check.Status = TargetUnresolved
			check.Detail = "no structural resolver is available for a symbol target"
			return check
		}
		locations, err := resolver.LocateSymbol(entry.Target.Value)
		if err != nil {
			check.Status = TargetUnresolved
			check.Detail = fmt.Sprintf("locate symbol %q after the run: %v", entry.Target.Value, err)
			return check
		}
		if len(locations) > 1 {
			check.Status = TargetUnresolved
			check.Detail = fmt.Sprintf("symbol %q matched %d declarations after the run", entry.Target.Value, len(locations))
			return check
		}
		var after SymbolState
		if len(locations) == 1 {
			state, err := readSymbolState(root, locations[0], cache, reads)
			if err != nil {
				check.Status = TargetUnresolved
				check.Detail = err.Error()
				return check
			}
			after = state
		}
		var before SymbolState
		if len(entry.Symbols) > 0 {
			before = entry.Symbols[0]
		}
		return compareSymbol(before, after, check)

	case contract.TargetPackage:
		if resolver == nil {
			check.Status = TargetUnresolved
			check.Detail = "no structural resolver is available for a package target"
			return check
		}
		locations, err := resolver.LocatePackage(entry.Target.Value)
		if err != nil {
			check.Status = TargetUnresolved
			check.Detail = fmt.Sprintf("locate package %q after the run: %v", entry.Target.Value, err)
			return check
		}
		dirs := packageDirectories(locations)
		if len(dirs) > 1 {
			check.Status = TargetUnresolved
			check.Detail = fmt.Sprintf("package %q matched %d directories after the run", entry.Target.Value, len(dirs))
			return check
		}
		paths := uniqueSymbolPaths(locations)
		var after []FileState
		for _, path := range paths {
			state, err := readTargetFile(root, path, cache, reads)
			if err != nil {
				check.Status = TargetUnresolved
				check.Detail = err.Error()
				return check
			}
			after = append(after, state)
		}
		return comparePackage(entry.Files, after, check)
	}
	check.Status = TargetUnresolved
	check.Detail = fmt.Sprintf("unknown target kind %q", entry.Target.Kind)
	return check
}

// comparePath applies the requested direction to one path's before/after state.
func comparePath(before, after FileState, check TargetCheck) TargetCheck {
	changed := before.Exists != after.Exists || before.Hash != after.Hash
	switch check.Operation {
	case contract.ChangeCreate:
		if before.Exists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("target %q already existed before the run", before.Path)
		} else if !after.Exists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("target %q was not created", before.Path)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeDelete:
		if !before.Exists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("target %q did not exist before the run", before.Path)
		} else if after.Exists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("target %q still exists after a delete request", before.Path)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeModify:
		if !before.Exists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("target %q did not exist before the run", before.Path)
		} else if !after.Exists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("target %q was removed instead of modified", before.Path)
		} else if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("target %q has the same content before and after the run", before.Path)
		} else {
			check.Status = TargetSatisfied
		}
	default:
		if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("target %q did not change", before.Path)
		} else {
			check.Status = TargetSatisfied
		}
	}
	return check
}

// comparePathSet applies the requested direction to a directory target's file
// set. It is the same rule as a package target: a directory is a scope, so any
// change to the files under it satisfies a modify request, and an empty scope
// is the absence the create/delete directions require.
func comparePathSet(before, after []FileState, check TargetCheck) TargetCheck {
	changed := len(before) != len(after)
	beforeByPath := map[string]string{}
	for _, state := range before {
		beforeByPath[state.Path] = state.Hash
	}
	for _, state := range after {
		hash, ok := beforeByPath[state.Path]
		if !ok || hash != state.Hash || !state.Exists {
			changed = true
		}
	}
	switch check.Operation {
	case contract.ChangeCreate:
		if len(before) > 0 {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("directory %q already had files before the run", check.Target.Value)
		} else if len(after) == 0 {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("directory %q has no files after the run", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeDelete:
		if len(after) > 0 {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("directory %q still has files after a delete request", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	default:
		if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("directory %q did not change", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	}
	return check
}

// compareSymbol applies the requested direction to one declaration.
func compareSymbol(before, after SymbolState, check TargetCheck) TargetCheck {
	beforeExists := before.Exists || before.Hash != ""
	afterExists := after.Exists || after.Hash != ""
	changed := beforeExists != afterExists || before.Hash != after.Hash ||
		before.Location.Path != after.Location.Path ||
		before.Location.QualifiedName != after.Location.QualifiedName
	label := check.Target.Value
	switch check.Operation {
	case contract.ChangeCreate:
		if beforeExists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("symbol %q already existed before the run", label)
		} else if !afterExists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("symbol %q was not created", label)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeDelete:
		if !beforeExists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("symbol %q did not exist before the run", label)
		} else if afterExists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("symbol %q still exists after a delete request", label)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeModify:
		if !beforeExists {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("symbol %q did not exist before the run", label)
		} else if !afterExists {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("symbol %q was removed instead of modified", label)
		} else if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("symbol %q has the same definition before and after the run", label)
		} else {
			check.Status = TargetSatisfied
		}
	default:
		if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("symbol %q did not change", label)
		} else {
			check.Status = TargetSatisfied
		}
	}
	return check
}

// comparePackage applies the requested direction to a package's file set.
func comparePackage(before, after []FileState, check TargetCheck) TargetCheck {
	changed := len(before) != len(after)
	beforeByPath := map[string]string{}
	for _, state := range before {
		beforeByPath[state.Path] = state.Hash
	}
	for _, state := range after {
		if hash, ok := beforeByPath[state.Path]; !ok || hash != state.Hash {
			changed = true
		}
	}
	switch check.Operation {
	case contract.ChangeCreate:
		if len(before) > 0 {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("package %q already had files before the run", check.Target.Value)
		} else if len(after) == 0 {
			check.Status = TargetMissing
			check.Detail = fmt.Sprintf("package %q was not created", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	case contract.ChangeDelete:
		if len(after) > 0 {
			check.Status = TargetWrongOperation
			check.Detail = fmt.Sprintf("package %q still has files after a delete request", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	default:
		if !changed {
			check.Status = TargetUnchanged
			check.Detail = fmt.Sprintf("package %q did not change", check.Target.Value)
		} else {
			check.Status = TargetSatisfied
		}
	}
	return check
}

// ClassifyMutations returns the scope verdict for every observed change.
//
// Three evidence sources are kept distinct:
//
//   - a tool that recorded a concrete path (a write/edit call, or a shell
//     command whose syntax names a target) is attributed to that path;
//   - the host edit journal records the workspace diff for the run, which
//     attributes a shell mutation whose paths the command text cannot reveal;
//   - neither is available, so the mutation stays UNATTRIBUTED.
//
// A captured journal is authoritative about the workspace: an empty diff means
// the run changed no captured workspace path, not that evidence is missing.
func ClassifyMutations(root string, turns []Turn, intent contract.IntentContract, snapshot *TargetSnapshot, provenance MutationProvenance) []ClassifiedMutation {
	mutationObjective := intent.Kind == contract.ObjectiveMutation
	workspaceScope, allowedFiles, allowedDirs := mutationScopeForIntent(intent, snapshot)

	var out []ClassifiedMutation
	seen := map[string]bool{}
	add := func(mutation ClassifiedMutation) {
		if mutation.Path == "" {
			return
		}
		key := mutation.Path + "\x00" + string(mutation.Class)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, mutation)
	}
	classify := func(path, status, source string) {
		class := MutationExpected
		switch {
		case !mutationObjective:
			class = MutationUnexpected
		case !workspaceScope && !pathAllowed(path, allowedFiles, allowedDirs):
			class = MutationUnexpected
		}
		add(ClassifiedMutation{Path: path, Status: status, Class: class, Source: source})
	}

	for _, change := range provenance.Changes {
		if change.Path == "" {
			continue
		}
		rel, ok := repositoryRelative(root, change.Path)
		if !ok {
			add(ClassifiedMutation{
				Path:   change.Path,
				Status: change.Status,
				Class:  MutationOutsideWorkspace,
				Source: "journal",
			})
			continue
		}
		classify(rel, change.Status, "journal")
	}

	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				continue
			}
			mutates := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if !mutates {
				continue
			}
			paths := observedMutationPaths(root, outcome, allowedFiles, allowedDirs)
			if len(paths) == 0 {
				if !provenance.Captured {
					add(ClassifiedMutation{
						Path:   "<unattributed>",
						Class:  MutationUnattributed,
						Source: "unattributed",
					})
				}
				continue
			}
			for _, path := range paths {
				classify(path, "", "tool")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Class < out[j].Class
	})
	return out
}

// UnexpectedMutations returns the paths a completion decision must report as
// outside the requested scope or outside the workspace when no journal evidence
// is available. Unattributed mutations are returned by ClassifyMutations, not
// folded into this list.
func UnexpectedMutations(root string, turns []Turn, intent contract.IntentContract, snapshot *TargetSnapshot) []string {
	return UnexpectedMutationsWithProvenance(root, turns, intent, snapshot, MutationProvenance{})
}

// UnexpectedMutationsWithProvenance is UnexpectedMutations with the run's
// journal evidence.
func UnexpectedMutationsWithProvenance(root string, turns []Turn, intent contract.IntentContract, snapshot *TargetSnapshot, provenance MutationProvenance) []string {
	var out []string
	for _, mutation := range ClassifyMutations(root, turns, intent, snapshot, provenance) {
		switch mutation.Class {
		case MutationUnexpected, MutationOutsideWorkspace:
			out = append(out, mutation.Path)
		}
	}
	return uniqueSorted(out)
}

// ObservedMutationPaths returns the repository-relative paths the runtime can
// attribute to definite mutations in the run without journal evidence.
func ObservedMutationPaths(root string, turns []Turn, snapshot *TargetSnapshot) []string {
	return ObservedMutationPathsWithProvenance(root, turns, snapshot, MutationProvenance{})
}

// ObservedMutationPathsWithProvenance is ObservedMutationPaths with the run's
// journal evidence. It is used to bound a targetless behavioural objective to
// the packages that actually changed.
func ObservedMutationPathsWithProvenance(root string, turns []Turn, snapshot *TargetSnapshot, provenance MutationProvenance) []string {
	allowedFiles, allowedDirs := allowedTargetScope(snapshot)
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for _, change := range provenance.Changes {
		if rel, ok := repositoryRelative(root, change.Path); ok {
			add(rel)
		}
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				continue
			}
			mutates := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if !mutates {
				continue
			}
			for _, path := range observedMutationPaths(root, outcome, allowedFiles, allowedDirs) {
				add(path)
			}
		}
	}
	sort.Strings(out)
	return out
}

// mutationScopeForIntent resolves the explicit mutation scope.
//
// The contract's MutationScope is authoritative when set. Older contracts and
// direct test fixtures fall back to the captured snapshot and then to the
// request's own path targets. A mutation objective whose scope cannot be
// determined is treated as workspace-scoped: that is the only boundary the
// runtime can actually enforce, and it does not turn a semantic target into a
// path.
func mutationScopeForIntent(intent contract.IntentContract, snapshot *TargetSnapshot) (bool, map[string]bool, []string) {
	files := map[string]bool{}
	var dirs []string
	switch intent.MutationScope.Kind {
	case contract.MutationScopeWorkspace:
		return true, files, dirs
	case contract.MutationScopePaths:
		for _, value := range intent.MutationScope.Files {
			files[value] = true
		}
		dirs = append(dirs, intent.MutationScope.Directories...)
		for _, target := range intent.Targets {
			if target.Kind == contract.TargetPath && target.Directory {
				dirs = append(dirs, target.Value)
			}
		}
		return false, files, uniqueStrings(dirs)
	}
	if snapshot != nil && len(snapshot.Entries) > 0 {
		files, dirs = allowedTargetScope(snapshot)
		if len(files) > 0 || len(dirs) > 0 {
			return false, files, dirs
		}
	}
	for _, target := range intent.Targets {
		if target.Kind != contract.TargetPath {
			continue
		}
		if target.Directory {
			dirs = append(dirs, target.Value)
		} else {
			files[target.Value] = true
		}
	}
	if len(files) > 0 || len(dirs) > 0 {
		return false, files, uniqueStrings(dirs)
	}
	return intent.Kind == contract.ObjectiveMutation, files, dirs
}

// allowedTargetScope turns a snapshot into exact files and package directories.
func allowedTargetScope(snapshot *TargetSnapshot) (map[string]bool, []string) {
	files := map[string]bool{}
	var dirs []string
	if snapshot == nil {
		return files, dirs
	}
	for _, entry := range snapshot.Entries {
		switch entry.Target.Kind {
		case contract.TargetPath, contract.TargetSymbol:
			if entry.Target.Kind == contract.TargetPath && entry.Target.Directory {
				dirs = append(dirs, entry.Target.Value)
				continue
			}
			for _, state := range entry.Files {
				files[state.Path] = true
			}
			for _, state := range entry.Symbols {
				files[state.Location.Path] = true
			}
		case contract.TargetPackage:
			dirs = append(dirs, packageDirectoriesFromFiles(entry.Files)...)
		}
	}
	sort.Strings(dirs)
	return files, uniqueStrings(dirs)
}

// observedMutationPaths returns the repository-relative paths the runtime can
// attribute to one mutation outcome.
//
// The tool's own AffectedPaths is the strongest evidence because it is
// produced by the code that performed the write. The authorization scope is
// the next: a file capability admitted for a concrete path names it. A shell
// command that mentions a scoped target is the last deterministic fallback.
func observedMutationPaths(root string, outcome ToolOutcome, allowedFiles map[string]bool, allowedDirs []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		if rel, ok := repositoryRelative(root, raw); ok && !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	for _, path := range outcome.Result.AffectedPaths {
		add(path)
	}
	if outcome.Authorization != nil &&
		outcome.Authorization.Capability == security.CapabilityWrite &&
		outcome.Authorization.Scope.Class == security.ScopeInside &&
		outcome.Authorization.Scope.Path != "" {
		add(outcome.Authorization.Scope.Path)
	}
	for _, key := range []string{"path", "file", "target"} {
		if raw := detailString(outcome.Result.Details, key); raw != "" {
			add(raw)
		}
	}
	if len(out) > 0 {
		return out
	}
	if outcome.Authorization != nil && outcome.Authorization.Capability == security.CapabilityExec {
		if command := detailString(outcome.Result.Details, "command"); command != "" {
			// Deterministic attribution: a command that names a scoped target
			// is attributed to that target. The runtime does not parse the
			// command's argument roles, so a command that changes several
			// paths relies on the run's journal diff instead.
			paths := make([]string, 0, len(allowedFiles))
			for path := range allowedFiles {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				if commandMentions(command, path) {
					add(path)
				}
			}
			for _, dir := range allowedDirs {
				if dir != "." && commandMentions(command, dir) {
					add(dir)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// commandMentions reports whether a command names a path as a whole token.
// "./" prefixes are normalized away so "go fmt ./tools" mentions "tools" but
// "go fmt ./tools2" does not.
func commandMentions(command, value string) bool {
	command = strings.ReplaceAll(command, "./", "")
	for start := 0; start <= len(command)-len(value); {
		index := strings.Index(command[start:], value)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(value)
		beforeOK := index == 0 || !isPathTokenByte(command[index-1])
		afterOK := end == len(command) || !isPathTokenByte(command[end])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
	return false
}

func isPathTokenByte(value byte) bool {
	return value == '_' || value == '.' || value == '/' || value == '-' ||
		(value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

// pathAllowed reports whether a changed path is inside the requested scope.
func pathAllowed(path string, files map[string]bool, dirs []string) bool {
	if files[path] {
		return true
	}
	for _, dir := range dirs {
		if dir == "." {
			if !strings.Contains(path, "/") {
				return true
			}
			continue
		}
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// repositoryRelative converts a path to the workspace-relative spelling.
func repositoryRelative(root, value string) (string, bool) {
	if strings.TrimSpace(root) == "" {
		// A caller with no workspace root can still reason about a path that is
		// already repository-relative. An absolute or escaping path is not a
		// repository coordinate, so it stays unattributed rather than becoming
		// a guessed one.
		value = filepath.ToSlash(strings.TrimSpace(value))
		value = strings.TrimPrefix(value, "./")
		if value == "" || value == "." || filepath.IsAbs(value) ||
			value == ".." || strings.HasPrefix(value, "../") ||
			strings.Contains(value, "/../") {
			return "", false
		}
		return value, true
	}
	scope := security.NormalizeScope(root, value)
	if scope.Class != security.ScopeInside || scope.Path == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, scope.Path)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// detailString reads a string field from a tool result's Details map.
func detailString(details any, key string) string {
	values, ok := details.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

// packageDirectories returns the unique directories holding a package's
// declarations.
func packageDirectories(locations []SymbolLocation) []string {
	var dirs []string
	for _, location := range locations {
		dir := filepath.ToSlash(filepath.Dir(location.Path))
		dirs = appendUniqueString(dirs, dir)
	}
	return dirs
}

// packageDirectoriesFromFiles is the same mapping for observed file states.
func packageDirectoriesFromFiles(files []FileState) []string {
	var dirs []string
	for _, state := range files {
		dir := filepath.ToSlash(filepath.Dir(state.Path))
		dirs = appendUniqueString(dirs, dir)
	}
	return dirs
}

// uniqueSymbolPaths returns the unique declaration files in coordinate order.
func uniqueSymbolPaths(locations []SymbolLocation) []string {
	var paths []string
	for _, location := range locations {
		paths = appendUniqueString(paths, location.Path)
	}
	sort.Strings(paths)
	return paths
}

// hashBytes returns the SHA-256 hex digest of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashRange hashes an inclusive 1-based line range.
func hashRange(data []byte, start, end int) (string, error) {
	if start < 1 || end < start {
		return "", fmt.Errorf("invalid range %d-%d", start, end)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if end > len(lines) {
		return "", fmt.Errorf("range %d-%d exceeds %d lines", start, end, len(lines))
	}
	digest := sha256.New()
	for line := start; line <= end; line++ {
		if _, err := digest.Write([]byte(lines[line-1])); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueStrings(values []string) []string {
	var out []string
	for _, value := range values {
		out = appendUniqueString(out, value)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
