package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestContentSHANormalizesCRLF checks \r\n and \n content hash the same, and
// that for LF content contentSHA is the raw sha256.
func TestContentSHANormalizesCRLF(t *testing.T) {
	t.Parallel()
	lf := []byte("a\nb\n")
	crlf := []byte("a\r\nb\r\n")
	if contentSHA(lf) != contentSHA(crlf) {
		t.Error("contentSHA() differs between LF and CRLF content")
	}
	sum := sha256.Sum256(lf)
	if contentSHA(lf) != hex.EncodeToString(sum[:]) {
		t.Error("contentSHA() is not the raw sha256 of LF content")
	}
}

// TestContentSHAKeepsLoneCR checks a \r not followed by \n is kept, so
// contentSHA("a\rb\n") differs from the raw hash of "a\rb" only by the LF.
func TestContentSHAKeepsLoneCR(t *testing.T) {
	t.Parallel()
	lone := []byte("a\rb\n")
	crlf := []byte("a\rb\r\n")
	if contentSHA(lone) != contentSHA(crlf) {
		t.Error("contentSHA() changed a lone CR followed by a CRLF")
	}
	sum := sha256.Sum256(lone)
	if contentSHA(lone) != hex.EncodeToString(sum[:]) {
		t.Error("contentSHA() did not keep the lone CR")
	}
}
