package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareReleaseVersions(t *testing.T) {
	for _, test := range []struct {
		current string
		latest  string
		want    int
		valid   bool
	}{
		{current: "v0.2.4", latest: "v0.2.5", want: -1, valid: true},
		{current: "v0.10.0", latest: "v0.9.9", want: 1, valid: true},
		{current: "v0.2.5", latest: "v0.2.5", want: 0, valid: true},
		{current: "development", latest: "v0.2.5", want: -1, valid: false},
	} {
		got, valid := compareReleaseVersions(test.current, test.latest)
		if got != test.want || valid != test.valid {
			t.Errorf("compareReleaseVersions(%q, %q) = (%d, %v), want (%d, %v)", test.current, test.latest, got, valid, test.want, test.valid)
		}
	}
}

func TestReleaseAssetNames(t *testing.T) {
	for _, test := range []struct {
		goos, goarch, archive, binary string
	}{
		{"windows", "amd64", "mobile-use-v0.2.5-windows-amd64.zip", "mobile-use.exe"},
		{"windows", "arm64", "mobile-use-v0.2.5-windows-arm64.zip", "mobile-use.exe"},
		{"linux", "amd64", "mobile-use-v0.2.5-linux-amd64.tar.gz", "mobile-use"},
		{"darwin", "arm64", "mobile-use-v0.2.5-darwin-arm64.tar.gz", "mobile-use"},
	} {
		archive, binary, err := releaseAssetNames(test.goos, test.goarch, "v0.2.5")
		if err != nil || archive != test.archive || binary != test.binary {
			t.Errorf("releaseAssetNames(%s, %s) = (%q, %q, %v)", test.goos, test.goarch, archive, binary, err)
		}
	}
	if _, _, err := releaseAssetNames("freebsd", "amd64", "v0.2.5"); err == nil {
		t.Fatal("expected unsupported operating system to be rejected")
	}
}

func TestChecksumForAsset(t *testing.T) {
	data := []byte("release archive")
	digest := sha256.Sum256(data)
	checksums := []byte(fmt.Sprintf("%s  other.zip\n%s *mobile-use-v0.2.5-windows-amd64.zip\n", hex.EncodeToString(digest[:]), hex.EncodeToString(digest[:])))
	got, ok := checksumForAsset(checksums, "mobile-use-v0.2.5-windows-amd64.zip")
	if !ok || got != hex.EncodeToString(digest[:]) {
		t.Fatalf("checksumForAsset() = (%q, %v)", got, ok)
	}
	if err := verifySHA256(data, got); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA256([]byte("tampered"), got); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestExtractWindowsReleaseBinary(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("mobile-use-v0.2.5-windows-amd64/mobile-use.exe")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("windows executable fixture")
	if _, err := file.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractReleaseBinary(archive.Bytes(), "windows", "mobile-use.exe")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extractReleaseBinary() = %q, %v", got, err)
	}
}

func TestFetchLatestReleaseUsesGitHubAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/mobile-use/releases/latest" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-value" {
			t.Errorf("missing bearer token")
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.5","assets":[]}`))
	}))
	defer server.Close()

	release, err := fetchLatestRelease(context.Background(), server.Client(), server.URL, "example/mobile-use", "token-value")
	if err != nil || release.TagName != "v0.2.5" {
		t.Fatalf("fetchLatestRelease() = %+v, %v", release, err)
	}
}
