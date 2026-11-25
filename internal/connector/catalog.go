package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mobile-use/internal/wire"
)

const maxAPK = 256 << 20

type androidArtifact struct {
	wire.CatalogItem
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type AndroidCatalog struct {
	artifacts map[string]androidArtifact
	aapt2     string
}

func LoadAndroidCatalog(path, aapt2 string) (*AndroidCatalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []androidArtifact
	d := json.NewDecoder(io.LimitReader(file, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&entries); err != nil {
		return nil, err
	}
	if d.Decode(new(json.RawMessage)) != io.EOF || len(entries) < 1 || len(entries) > 64 {
		return nil, fmt.Errorf("the Android catalog must contain between 1 and 64 entries")
	}
	if aapt2 == "" {
		aapt2 = "aapt2"
	}
	catalog := &AndroidCatalog{artifacts: make(map[string]androidArtifact, len(entries)), aapt2: aapt2}
	for _, entry := range entries {
		_, duplicate := catalog.artifacts[entry.ID]
		if duplicate || !wire.ValidID(entry.ID) || !packageMatch.MatchString(entry.AppID) || entry.Label == "" || len(entry.Label) > 100 || !strings.EqualFold(filepath.Ext(entry.Path), ".apk") {
			return nil, fmt.Errorf("invalid Android catalog entry %q", entry.ID)
		}
		if hash, err := hex.DecodeString(entry.SHA256); err != nil || len(hash) != sha256.Size {
			return nil, fmt.Errorf("catalog entry %q needs a SHA-256 checksum", entry.ID)
		}
		if !filepath.IsAbs(entry.Path) {
			entry.Path = filepath.Join(filepath.Dir(path), entry.Path)
		}
		entry.Path, err = filepath.Abs(entry.Path)
		if err != nil {
			return nil, err
		}
		entry.SHA256 = strings.ToLower(entry.SHA256)
		catalog.artifacts[entry.ID] = entry
	}
	return catalog, nil
}

func (c *AndroidCatalog) items() []wire.CatalogItem {
	if c == nil {
		return nil
	}
	items := make([]wire.CatalogItem, 0, len(c.artifacts))
	for _, entry := range c.artifacts {
		items = append(items, entry.CatalogItem)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

var apkApplication = regexp.MustCompile(`(?m)^package:\s+name='([A-Za-z][A-Za-z0-9_.]+)'`)

func (a *androidDevice) Maintain(ctx context.Context, kind, id string) error {
	if a.catalog == nil {
		return wire.ErrUnsupported
	}
	item, ok := a.catalog.artifacts[id]
	if !ok {
		return wire.ErrUnsupported
	}
	switch kind {
	case "app_reset":
		out, err := a.shell(ctx, "pm clear --user current "+item.AppID)
		if err != nil {
			return wire.ErrUnknown
		}
		if strings.TrimSpace(out) != "Success" {
			return wire.Error("RESET_FAILED", "Android did not clear the approved application's data.", 422)
		}
		return nil
	case "app_install":
		staged, err := stageAPK(item)
		if err != nil {
			return err
		}
		defer os.Remove(staged)
		output, err := runProcess(ctx, a.catalog.aapt2, "dump", "badging", staged)
		if err != nil {
			return wire.Error("APK_METADATA_UNAVAILABLE", "The connector could not inspect the pinned APK with Android Build Tools.", 422)
		}
		match := apkApplication.FindSubmatch(output)
		if len(match) != 2 || string(match[1]) != item.AppID {
			return wire.Error("APK_PACKAGE_MISMATCH", "The APK package does not match the catalog entry.", 422)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		output, err = a.run(ctx, "install", "--user", "current", "-r", staged)
		if err != nil {
			return wire.ErrUnknown
		}
		if !strings.Contains(string(output), "Success") {
			return wire.Error("INSTALL_FAILED", "Android did not confirm installation of the approved APK.", 422)
		}
		return nil
	default:
		return wire.ErrUnsupported
	}
}

func stageAPK(item androidArtifact) (string, error) {
	source, err := os.Open(item.Path)
	if err != nil {
		return "", wire.Error("APK_UNAVAILABLE", "The configured APK could not be opened on the connector.", 422)
	}
	defer source.Close()
	checked, err := os.CreateTemp("", "mobile-use-catalog-*.apk")
	if err != nil {
		return "", err
	}
	path := checked.Name()
	ok := false
	defer func() {
		if !ok {
			_ = checked.Close()
			_ = os.Remove(path)
		}
	}()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(checked, digest), io.LimitReader(source, maxAPK+1))
	if err != nil {
		return "", err
	}
	if n > maxAPK || hex.EncodeToString(digest.Sum(nil)) != item.SHA256 {
		return "", wire.Error("APK_CHECKSUM_MISMATCH", "The APK does not match the catalog checksum or size limit.", 422)
	}
	if err := checked.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}
