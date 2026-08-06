package delivery

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeliveryKey derives the stable idempotency key for one run's delivery from
// admitted data only: sha256(runID + NUL + workflowDigest), hex-encoded,
// prefixed wfdel:.
func DeliveryKey(runID, workflowDigest string) string {
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte{0})
	h.Write([]byte(workflowDigest))
	return "wfdel:" + hex.EncodeToString(h.Sum(nil))
}
