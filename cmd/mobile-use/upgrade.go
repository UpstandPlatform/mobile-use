package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	upgradeRepository = "UpstandPlatform/mobile-use"
	maxReleaseAsset   = 512 << 20
	maxReleaseBinary  = 200 << 20
)

var releaseTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$`)

type githubRelease struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func cmdUpgrade(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	checkOnly := f.Bool("check", false, "Check the latest release without installing it")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("usage: mobile-use upgrade [--check]")
	}

	requestContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Minute}
	token := githubToken()
	release, err := fetchLatestRelease(requestContext, client, "https://api.github.com", upgradeRepository, token)
	if err != nil {
		return err
	}
	if !validReleaseTag(release.TagName) || release.Draft || release.Prerelease {
		return fmt.Errorf("GitHub returned an invalid latest stable release for %s", upgradeRepository)
	}

	fmt.Printf("Current version: %s\nLatest version:  %s\n", version, release.TagName)
	comparison, currentIsRelease := compareReleaseVersions(version, release.TagName)
	if *checkOnly {
		if currentIsRelease && comparison >= 0 {
			fmt.Println("Already up to date")
		} else {
			fmt.Println("An upgrade is available")
		}
		return nil
	}
	if currentIsRelease && comparison >= 0 {
		fmt.Println("Already up to date")
		return nil
	}

	assetName, binaryName, err := releaseAssetNames(runtime.GOOS, runtime.GOARCH, release.TagName)
	if err != nil {
		return err
	}
	archiveAsset, ok := findReleaseAsset(release.Assets, assetName)
	if !ok {
		return fmt.Errorf("GitHub release %s does not contain %s", release.TagName, assetName)
	}
	checksumAsset, ok := findReleaseAsset(release.Assets, "SHA256SUMS")
	if !ok {
		return fmt.Errorf("GitHub release %s does not contain SHA256SUMS", release.TagName)
	}

	checksums, err := downloadReleaseAsset(requestContext, client, token, checksumAsset)
	if err != nil {
		return fmt.Errorf("download release checksums: %w", err)
	}
	wantHash, ok := checksumForAsset(checksums, assetName)
	if !ok {
		return fmt.Errorf("SHA256SUMS does not contain %s", assetName)
	}
	archive, err := downloadReleaseAsset(requestContext, client, token, archiveAsset)
	if err != nil {
		return fmt.Errorf("download %s: %w", assetName, err)
	}
	if err := verifySHA256(archive, wantHash); err != nil {
		return fmt.Errorf("verify %s: %w", assetName, err)
	}
	binary, err := extractReleaseBinary(archive, runtime.GOOS, binaryName)
	if err != nil {
		return fmt.Errorf("extract %s: %w", binaryName, err)
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve the current executable path: %w", err)
	}
	if !strings.EqualFold(filepath.Base(executable), binaryName) {
		return fmt.Errorf("the running binary is named %q; install the official %s binary before upgrading", filepath.Base(executable), binaryName)
	}
	staged, err := stageReleaseBinary(filepath.Dir(executable), binaryName, binary)
	if err != nil {
		return err
	}
	if err := verifyStagedVersion(ctx, staged, release.TagName); err != nil {
		_ = os.Remove(staged)
		return err
	}

	pending, err := installStagedBinary(staged, executable)
	if err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("install %s: %w", release.TagName, err)
	}
	if pending {
		fmt.Printf("Upgrade to %s is staged. Windows will replace the on-disk executable after this command exits; already-running processes keep their loaded version until restarted. Run `mobile-use version` to confirm.\n", release.TagName)
		return nil
	}
	fmt.Printf("Upgraded mobile-use to %s\n", release.TagName)
	return nil
}

func githubToken() string {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}
	return ""
}

func fetchLatestRelease(ctx context.Context, client *http.Client, apiBase, repository, token string) (githubRelease, error) {
	var release githubRelease
	endpoint := strings.TrimRight(apiBase, "/") + "/repos/" + strings.Trim(repository, "/") + "/releases/latest"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return release, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "mobile-use/"+version)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return release, fmt.Errorf("check GitHub releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return release, fmt.Errorf("GitHub latest release lookup for %s returned HTTP %d; check network access or retry later", repository, response.StatusCode)
		}
		return release, fmt.Errorf("GitHub release lookup failed with HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release); err != nil {
		return release, fmt.Errorf("decode GitHub release metadata: %w", err)
	}
	return release, nil
}

func releaseAssetNames(goos, goarch, tag string) (archiveName, binaryName string, err error) {
	if !validReleaseTag(tag) {
		return "", "", fmt.Errorf("invalid release tag %q", tag)
	}
	switch goos {
	case "windows":
		return "mobile-use-" + tag + "-" + goos + "-" + goarch + ".zip", "mobile-use.exe", nil
	case "linux", "darwin":
		return "mobile-use-" + tag + "-" + goos + "-" + goarch + ".tar.gz", "mobile-use", nil
	default:
		return "", "", fmt.Errorf("self-upgrade is not supported on %s/%s; download the matching archive from https://github.com/%s/releases", goos, goarch, upgradeRepository)
	}
}

func findReleaseAsset(assets []releaseAsset, name string) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name && asset.BrowserDownloadURL != "" {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func downloadReleaseAsset(ctx context.Context, client *http.Client, token string, asset releaseAsset) ([]byte, error) {
	if asset.Size < 0 || asset.Size > maxReleaseAsset {
		return nil, fmt.Errorf("asset size %d is outside the supported limit", asset.Size)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "mobile-use/"+version)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub asset download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxReleaseAsset {
		return nil, fmt.Errorf("asset exceeds the %d MiB download limit", maxReleaseAsset>>20)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseAsset+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReleaseAsset {
		return nil, fmt.Errorf("asset exceeds the %d MiB download limit", maxReleaseAsset>>20)
	}
	if asset.Size > 0 && int64(len(data)) != asset.Size {
		return nil, fmt.Errorf("downloaded %d bytes but release metadata reports %d", len(data), asset.Size)
	}
	return data, nil
}

func checksumForAsset(checksums []byte, assetName string) (string, bool) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == assetName {
			if _, err := hex.DecodeString(fields[0]); err == nil && len(fields[0]) == sha256.Size*2 {
				return strings.ToLower(fields[0]), true
			}
		}
	}
	return "", false
}

func verifySHA256(data []byte, expected string) error {
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("invalid SHA-256 checksum")
	}
	got := sha256.Sum256(data)
	if !bytes.Equal(got[:], want) {
		return fmt.Errorf("SHA-256 checksum mismatch")
	}
	return nil
}

func extractReleaseBinary(data []byte, goos, binaryName string) ([]byte, error) {
	var found []byte
	add := func(name string, size int64, reader io.Reader) error {
		if path.Base(strings.ReplaceAll(name, "\\", "/")) != binaryName {
			return nil
		}
		if size < 0 || size > maxReleaseBinary {
			return fmt.Errorf("binary size is outside the supported limit")
		}
		if found != nil {
			return fmt.Errorf("archive contains multiple %s files", binaryName)
		}
		contents, err := io.ReadAll(io.LimitReader(reader, maxReleaseBinary+1))
		if err != nil {
			return err
		}
		if len(contents) > maxReleaseBinary || (size > 0 && int64(len(contents)) != size) {
			return fmt.Errorf("invalid size for %s in archive", binaryName)
		}
		found = contents
		return nil
	}

	if goos == "windows" {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("read ZIP archive: %w", err)
		}
		for _, file := range archive.File {
			if file.FileInfo().IsDir() {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			err = add(file.Name, int64(file.UncompressedSize64), reader)
			closeErr := reader.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
	} else {
		archive, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("read gzip archive: %w", err)
		}
		defer archive.Close()
		reader := tar.NewReader(archive)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read tar archive: %w", err)
			}
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				continue
			}
			if err := add(header.Name, header.Size, reader); err != nil {
				return nil, err
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("archive does not contain %s", binaryName)
	}
	return found, nil
}

func stageReleaseBinary(dir, binaryName string, data []byte) (string, error) {
	file, err := os.CreateTemp(dir, ".mobile-use-upgrade-*"+filepath.Ext(binaryName))
	if err != nil {
		return "", fmt.Errorf("create a temporary executable in %s: %w", dir, err)
	}
	name := file.Name()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("write the downloaded executable: %w", err)
	}
	if err := os.Chmod(name, 0755); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("make the downloaded executable runnable: %w", err)
	}
	return name, nil
}

func verifyStagedVersion(ctx context.Context, staged, expected string) error {
	command := exec.CommandContext(ctx, staged, "version")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("the downloaded executable could not report its version: %w", err)
	}
	if strings.TrimSpace(string(output)) != expected {
		return fmt.Errorf("downloaded executable reported version %q, expected %q", strings.TrimSpace(string(output)), expected)
	}
	return nil
}

func validReleaseTag(tag string) bool { return releaseTagPattern.MatchString(tag) }

func compareReleaseVersions(current, latest string) (int, bool) {
	currentMatch := releaseTagPattern.FindStringSubmatch(current)
	latestMatch := releaseTagPattern.FindStringSubmatch(latest)
	if currentMatch == nil || latestMatch == nil {
		return -1, false
	}
	for i := 1; i <= 3; i++ {
		currentPart, _ := strconv.Atoi(currentMatch[i])
		latestPart, _ := strconv.Atoi(latestMatch[i])
		if currentPart < latestPart {
			return -1, true
		}
		if currentPart > latestPart {
			return 1, true
		}
	}
	return 0, true
}
