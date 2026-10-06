package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// VerifyArtifactDigest is the door other packages load through, so its own
// package must be able to say what it does: matching digest passes, a mismatch
// refuses and names both digests, and "verification on" refuses a manifest that
// declares nothing at all.
func TestVerifyArtifactDigest(t *testing.T) {
	artifact := minMemoryWasm(1)

	if err := VerifyArtifactDigest("p", digestOf(artifact), artifact, true); err != nil {
		t.Fatalf("matching digest must pass: %v", err)
	}

	// Not required and nothing declared: the historical, permissive behaviour.
	if err := VerifyArtifactDigest("p", "", artifact, false); err != nil {
		t.Fatalf("an undeclared digest must pass when it is not required: %v", err)
	}

	err := VerifyArtifactDigest("p", digestOf([]byte("other bytes")), artifact, true)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	sum := digestOf(artifact)
	for _, want := range []string{sum, digestOf([]byte("other bytes"))} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a mismatch must name both digests (%s), got: %v", want, err)
		}
	}

	if err := VerifyArtifactDigest("p", "", artifact, true); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("required-but-undeclared must refuse, got %v", err)
	}
}
