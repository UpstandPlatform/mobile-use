package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"mobile-use/internal/wire"
)

func TestStageAPKChecksPinnedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.apk")
	original := []byte("test artifact bytes")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(original)
	item := androidArtifact{Path: path, SHA256: hex.EncodeToString(hash[:])}
	staged, err := stageAPK(item)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(staged)
	if err := os.WriteFile(path, []byte("modified artifact bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(staged)
	if err != nil || string(content) != string(original) {
		t.Fatalf("the staged artifact did not preserve the verified bytes: %v", err)
	}
	unexpected, err := stageAPK(item)
	if unexpected != "" {
		os.Remove(unexpected)
	}
	if err == nil || wire.Public(err).Code != "APK_CHECKSUM_MISMATCH" {
		t.Fatalf("changed artifact was accepted: %v", err)
	}
}
