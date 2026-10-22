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

	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/security"
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
	Path string
	// Symbol is the bare declared name.
	Symbol string
	// QualifiedName is the receiver- or package-qualified coordinate.
	QualifiedName string
	// Line and EndLine bound the declaration in Path.
	Line    int
	EndLine int
}

// FileState is the deterministic content state of one target file.
type FileState struct {
	// Path is repository-relative and slash-separated.
	Path string
	// Exists reports whether the file was present when observed.
	Exists bool
	// Hash is the SHA-256 of the file content, empty when absent.
	Hash string
}

// SymbolState is the content state of one declaration.
type SymbolState struct {
	// Location is where the declaration was found.
	Location SymbolLocation
	// Exists reports whether the declaration was found.
	Exists bool
	// Hash is the SHA-256 of the declaration's source range.
	Hash string
}

// TargetSnapshotEntry is the pre-run state of one contract target.
type TargetSnapshotEntry struct {
	// Target is the coordinate the request named.
	Target contract.Target
	// Operation is the requested direction.
	Operation contract.ChangeOp
	// Resolved reports that the target could be mapped to a concrete file or
	// declaration before the run.
	Resolved bool
	// Ambiguous reports that the coordinate matched more than one declaration
	// or package directory.
	Ambiguous bool
	// Issue explains why the target could not be resolved.
	Issue string
	// Files are the file states for a path or package target.
	Files []FileState
	// Symbols are the declaration states for a symbol target.
	Symbols []SymbolState
}

// TargetSnapshot is the bounded pre-run evidence for one contract.
//
// It is captured before execution and compared after. It is deliberately not a
// repository snapshot: only the coordinates the user named are read.
type TargetSnapshot struct {
	// Root is the absolute workspace root the paths are relative to.
	Root string
	// Entries are one per target, in contract order.
	Entries []TargetSnapshotEntry
	// FilesRead and BytesRead record the verification's read cost.
	FilesRead int
	BytesRead int64
	// Issues are capture failures that were not target-specific.
	Issues []string
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
		state, err := readTargetFile(root, entry.Target.Value, cache, reads)
		if err != nil {
			entry.Issue = err.Error()
			return
		}
		entry.Resolved = true
		entry.Files = []FileState{state}

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
		default:
			check = checkCurrentTarget(snapshot.Root, entry, resolver, cache, reads, &evidence)
		}
		evidence.Checks = append(evidence.Checks, check)
	}
	sort.Strings(evidence.Unexpected)
	return evidence
}

// checkCurrentTarget performs the post-run comparison for one entry.
func checkCurrentTarget(root string, entry TargetSnapshotEntry, resolver StructuralResolver, cache map[string]targetFile, reads *targetReads, evidence *StructuralEvidence) TargetCheck {
	check := TargetCheck{Target: entry.Target, Operation: entry.Operation}
	switch entry.Target.Kind {
	case contract.TargetPath:
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

// UnexpectedMutations returns observed mutation paths outside the requested
// target scope.
//
// It only attributes a mutation when the runtime recorded a concrete path (the
// security decision for a file tool, or a command that names a target). An
// arbitrary shell mutation with no attributable path is reported as
// unattributed for a bounded mutation objective, because the runtime cannot
// prove it stayed inside the requested scope.
func UnexpectedMutations(root string, turns []Turn, intent contract.IntentContract, snapshot *TargetSnapshot) []string {
	allowedFiles, allowedDirs := allowedTargetScope(snapshot)
	hasExplicitScope := len(intent.Targets) > 0
	mutationObjective := intent.Kind == contract.ObjectiveMutation
	if mutationObjective && !hasExplicitScope {
		// No bounded scope was requested, so "outside the requested scope" has
		// no meaning. The evaluator handles the unbounded objective separately.
		return nil
	}

	var unexpected []string
	seen := map[string]bool{}
	add := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
			unexpected = append(unexpected, value)
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
			path, ok := observedMutationPath(root, outcome, allowedFiles, allowedDirs)
			if !ok {
				if !mutationObjective || hasExplicitScope {
					add("<unattributed>")
				}
				continue
			}
			if !mutationObjective {
				add(path)
				continue
			}
			if !pathAllowed(path, allowedFiles, allowedDirs) {
				add(path)
			}
		}
	}
	sort.Strings(unexpected)
	return unexpected
}

// ObservedMutationPaths returns the repository-relative paths the runtime can
// attribute to definite mutations in the run. It is used to bound a targetless
// behavioural objective to the packages that actually changed.
func ObservedMutationPaths(root string, turns []Turn, snapshot *TargetSnapshot) []string {
	allowedFiles, allowedDirs := allowedTargetScope(snapshot)
	var out []string
	seen := map[string]bool{}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				continue
			}
			mutates := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if !mutates {
				continue
			}
			path, ok := observedMutationPath(root, outcome, allowedFiles, allowedDirs)
			if ok && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out
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

// observedMutationPath returns the repository-relative path of one mutation.
func observedMutationPath(root string, outcome ToolOutcome, allowedFiles map[string]bool, allowedDirs []string) (string, bool) {
	if outcome.Authorization != nil &&
		outcome.Authorization.Capability == security.CapabilityWrite &&
		outcome.Authorization.Scope.Class == security.ScopeInside &&
		outcome.Authorization.Scope.Path != "" {
		if rel, ok := repositoryRelative(root, outcome.Authorization.Scope.Path); ok {
			return rel, true
		}
	}
	if outcome.Authorization != nil && outcome.Authorization.Capability == security.CapabilityExec {
		if command := detailString(outcome.Result.Details, "command"); command != "" {
			// Deterministic attribution: a command that names a target path is
			// attributed to that path. The runtime does not parse the command's
			// argument roles, so a shell command that changes several paths is
			// a documented limitation of exec-scope attribution.
			paths := make([]string, 0, len(allowedFiles))
			for path := range allowedFiles {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				if commandMentions(command, path) {
					return path, true
				}
			}
			for _, dir := range allowedDirs {
				if dir != "." && commandMentions(command, dir) {
					return dir, true
				}
			}
		}
	}
	for _, key := range []string{"path", "file", "target"} {
		if raw := detailString(outcome.Result.Details, key); raw != "" {
			if rel, ok := repositoryRelative(root, raw); ok {
				return rel, true
			}
		}
	}
	return "", false
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
