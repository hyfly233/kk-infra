package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// NotebookUsername separates the same account's workspaces across tenants.
func NotebookUsername(tenantID, userID string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + userID))
	return "nb-" + hex.EncodeToString(sum[:16])
}
