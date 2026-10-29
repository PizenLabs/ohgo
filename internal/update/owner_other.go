//go:build !linux && !darwin

package update

import "os"

// ownedByCurrentUser is not decidable the same way outside the published
// platforms. Self-update refuses those platforms before installation, so this
// fallback cannot widen authority.
func ownedByCurrentUser(os.FileInfo) bool { return true }
