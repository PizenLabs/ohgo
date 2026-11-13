package commit

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/vcs"
)

// secretNames are filenames that must never be included by an automatic
// commit. The list is deliberately name-based: reading arbitrary file content to
// guess whether it is a secret would be both slower and less predictable.
var secretNames = map[string]bool{
	".env": true, ".netrc": true, ".npmrc": true, ".pypirc": true,
	".git-credentials": true, ".htpasswd": true, ".pgpass": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"credentials": true, "credentials.json": true, "credentials.yaml": true,
	"secrets.json": true, "secrets.yaml": true, "secrets.yml": true,
}

// secretSuffixes are extensions that conventionally hold private key material.
var secretSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"}

// localArtifacts are machine-local or generated paths. They are excluded only
// when untracked: a tracked file's change is explicit repository work, while a
// new ignored-looking artifact is local state that happened not to be ignored.
var localArtifacts = map[string]bool{
	".ds_store": true, "thumbs.db": true, "desktop.ini": true,
	"coverage.out": true, ".coverage": true,
}

// localArtifactSuffixes are generated extensions excluded when untracked.
var localArtifactSuffixes = []string{
	".log", ".tmp", ".temp", ".swp", ".swo", ".prof", ".trace",
	".pyc", ".pyo", ".orig", ".rej", "~",
}

// localArtifactDirs are generated directories excluded when their path is
// untracked. They are checked as path segments, not prefixes, so a source
// package called "build" is not silently protected.
var localArtifactDirs = map[string]bool{
	"node_modules": true, "dist": true, "build": true, "__pycache__": true,
	".cache": true, ".idea": true, ".vscode": true,
}

// classifyOne assigns the category, kind, and role for one status record.
func classifyOne(root string, change *FileChange) {
	change.Role = roleOf(change.Path)

	// Conflict records are never committable: the index has more than one
	// stage for the path, and the human must resolve it first.
	if isUnmerged(change.Index, change.Worktree) {
		change.Category = CategoryBlocked
		change.Kind = KindConflicted
		change.Reason = "unresolved merge conflict"
		return
	}

	abs := filepath.Join(root, filepath.FromSlash(change.Path))
	info, statErr := os.Lstat(abs)
	isDir := statErr == nil && info.IsDir()
	isSymlink := statErr == nil && info.Mode()&os.ModeSymlink != 0

	// A directory reported by status is a submodule or a nested repository.
	// Neither is a set of ordinary paths the parent commit may absorb.
	if isDir {
		if nestedRepositoryRoot(root, change.Path) != "" {
			change.Category = CategoryBlocked
			change.Kind = KindNestedRepository
			change.Reason = "nested Git repository"
			return
		}
		change.Category = CategoryBlocked
		change.Kind = KindSubmodule
		change.Reason = "submodule change requires an explicit decision"
		return
	}
	if change.Untracked && nestedRepositoryRoot(root, change.Path) != "" {
		change.Category = CategoryBlocked
		change.Kind = KindNestedRepository
		change.Reason = "path belongs to a nested Git repository"
		return
	}

	secret := secretReason(change.Path)
	originalSecret := change.OrigPath != "" && secretReason(change.OrigPath) != ""
	if secret != "" || originalSecret {
		change.Sensitive = true
		// Removing a tracked secret is safe and often the correct repair. A
		// deletion commits no protected content, so it bypasses name-based
		// exclusion but keeps the sensitive flag: the removed bytes must still
		// stay out of the model evidence.
		if change.Index == 'D' || change.Worktree == 'D' {
			change.Category = CategoryCommittable
			change.Kind = KindDeleted
			return
		}
		if secret != "" {
			change.Category = CategoryProtected
			change.Kind = KindSecret
			change.Reason = "credential or secret file"
			return
		}
		// A rename away from a secret path may have sanitized the content or
		// merely moved the secret. The evidence cannot prove which, so it
		// requires a human decision and its diff stays redacted.
		change.Category = CategoryAmbiguous
		change.Kind = KindRenamed
		change.Reason = "rename from a protected path requires a decision"
		return
	}
	// Removing a tracked generated or OS file is likewise safe.
	if change.Index == 'D' || change.Worktree == 'D' {
		change.Category = CategoryCommittable
		change.Kind = KindDeleted
		return
	}
	if isOperatingSystemState(change.Path) {
		change.Category = CategoryProtected
		change.Kind = KindGenerated
		change.Reason = "local operating-system state"
		return
	}
	if change.Untracked && isLocalArtifact(change.Path) {
		change.Category = CategoryProtected
		change.Kind = KindGenerated
		change.Reason = "generated or local machine state"
		return
	}
	if change.Untracked && isSymlink {
		change.Category = CategoryAmbiguous
		change.Kind = KindSymlink
		change.Reason = "untracked symlink; its target is not part of the repository"
		return
	}
	if change.Untracked && statErr == nil && !info.Mode().IsRegular() {
		change.Category = CategoryAmbiguous
		change.Kind = KindNonRegular
		change.Reason = "untracked non-regular file"
		return
	}
	if change.Untracked && looksBinaryFile(abs) {
		change.Binary = true
		change.Category = CategoryAmbiguous
		change.Kind = KindBinary
		change.Reason = "untracked binary; the change set cannot establish its purpose"
		return
	}
	if change.Untracked && containsPrivateKey(abs) {
		change.Sensitive = true
		change.Category = CategoryProtected
		change.Kind = KindSecret
		change.Reason = "file contains private key material"
		return
	}

	// A regular path is a candidate. The kind records the observable operation.
	switch {
	case change.Index == 'D' || change.Worktree == 'D':
		change.Kind = KindDeleted
	case change.OrigPath != "" || change.Index == 'R' || change.Index == 'C':
		change.Kind = KindRenamed
	case change.Untracked:
		change.Kind = KindUntracked
	case change.Index == 'A':
		change.Kind = KindAdded
	case change.Staged() && change.Unstaged():
		change.Kind = KindPartiallyStaged
	default:
		change.Kind = KindModified
	}
	change.Category = CategoryCommittable
}

// secretReason names why a path is credential material, or "".
func secretReason(path string) string {
	clean := strings.ToLower(strings.TrimPrefix(path, "./"))
	base := pathBase(clean)
	if secretNames[base] {
		return "credential or secret file"
	}
	if base == ".env" || (strings.HasPrefix(base, ".env.") &&
		base != ".env.example" && base != ".env.sample" && base != ".env.template") {
		return "environment file may contain secrets"
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(base, suffix) {
			return "credential or private key file"
		}
	}
	if strings.HasSuffix(clean, "/.aws/credentials") ||
		clean == ".docker/config.json" || strings.HasSuffix(clean, "/.docker/config.json") {
		return "credential or secret file"
	}
	return ""
}

// isOperatingSystemState reports files that are never repository content, even
// when an earlier commit accidentally tracked them.
func isOperatingSystemState(path string) bool {
	switch strings.ToLower(pathBase(path)) {
	case ".ds_store", "thumbs.db", "desktop.ini":
		return true
	}
	return false
}

// isLocalArtifact reports generated or editor state for an untracked path.
func isLocalArtifact(path string) bool {
	clean := strings.ToLower(strings.TrimPrefix(path, "./"))
	base := pathBase(clean)
	if localArtifacts[base] {
		return true
	}
	if strings.HasPrefix(base, "coverage.") {
		switch pathExt(base) {
		case ".out", ".html", ".xml", ".json", ".txt":
			return true
		}
	}
	for _, suffix := range localArtifactSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	segments := strings.Split(clean, "/")
	for i, segment := range segments[:len(segments)-1] {
		if !localArtifactDirs[segment] {
			continue
		}
		// "build" is also a common source package name. It is generated state
		// only at the repository root; dist and the cache-like directories are
		// generated anywhere.
		if i == 0 || segment == "dist" || segment == "node_modules" || segment == "__pycache__" {
			return true
		}
	}
	return false
}

// nestedRepositoryRoot returns the root-relative directory holding a nested
// .git entry, or "".
//
// Walking every prefix rather than only exact directories is what makes a
// status record for a path inside a nested repository safe even when Git chose
// to report the file instead of the boundary directory.
func nestedRepositoryRoot(root, path string) string {
	clean := strings.Trim(strings.TrimPrefix(path, "./"), "/")
	segments := strings.Split(clean, "/")
	// Include the path itself: Git reports a nested repository as its boundary
	// directory, not as a file inside the boundary.
	for i := 1; i <= len(segments); i++ {
		prefix := strings.Join(segments[:i], "/")
		entry := filepath.Join(root, filepath.FromSlash(prefix), ".git")
		if _, err := os.Lstat(entry); err == nil {
			return prefix
		}
	}
	return ""
}

// isUnmerged reports a porcelain conflict pair.
func isUnmerged(index, worktree byte) bool {
	switch string([]byte{index, worktree}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return index == 'U' || worktree == 'U'
}

// looksBinaryFile bounds a NUL-byte scan. Git's own binary heuristic is richer,
// but an untracked file needs a decision before git diff can describe it.
func looksBinaryFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	buf := make([]byte, 8*1024)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// containsPrivateKey checks the bounded head of an untracked regular file for a
// private-key marker. It never reads the whole file.
func containsPrivateKey(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	buf := make([]byte, 4*1024)
	n, _ := io.ReadFull(file, buf)
	head := strings.ToUpper(string(buf[:n]))
	return strings.Contains(head, "BEGIN RSA PRIVATE KEY") ||
		strings.Contains(head, "BEGIN OPENSSH PRIVATE KEY") ||
		strings.Contains(head, "BEGIN EC PRIVATE KEY") ||
		strings.Contains(head, "BEGIN PRIVATE KEY") ||
		strings.Contains(head, "BEGIN DSA PRIVATE KEY")
}

// applyBinaryFacts records diff-derived binary/churn facts on a change.
func applyBinaryFacts(change *FileChange, entry vcs.NumstatEntry, found bool) {
	if !found {
		return
	}
	change.Added = entry.Added
	change.Removed = entry.Removed
	if entry.Added == nil || entry.Removed == nil {
		change.Binary = true
		if change.Kind == "" || change.Kind == KindModified {
			change.Kind = KindBinary
		}
	}
}
