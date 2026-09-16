package flywheel

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// UpgradeOptions drives the self-update flow. Every field is overridable so
// the tests can drive the whole flow against an httptest.Server; empty
// fields fall back to the defaults listed in each doc comment.
type UpgradeOptions struct {
	// APIBase is the GitHub API base; default https://api.github.com.
	APIBase string
	// DownloadBase is the host releases download from; default https://github.com.
	DownloadBase string
	// Repo is owner/repo to upgrade from; default suzworx/flywheel.
	Repo string
	// Version to install; empty means the latest release.
	Version string
	// GOOS of the asset to install; empty means runtime.GOOS.
	GOOS string
	// GOARCH of the asset to install; empty means runtime.GOARCH.
	GOARCH string
	// Dest is the binary path to replace; empty means os.Executable().
	Dest string
}

// builtPlatforms is every platform the release workflow builds, so an
// unsupported platform is rejected before any download.
var builtPlatforms = map[string]bool{
	"linux/amd64":   true,
	"linux/arm64":   true,
	"darwin/amd64":  true,
	"darwin/arm64":  true,
	"windows/amd64": true,
}

// applyDefaults fills every empty option with its default.
func (o *UpgradeOptions) applyDefaults() {
	if o.APIBase == "" {
		o.APIBase = "https://api.github.com"
	}
	if o.DownloadBase == "" {
		o.DownloadBase = "https://github.com"
	}
	if o.Repo == "" {
		o.Repo = "suzworx/flywheel"
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
}

// releaseInfo is the /releases/latest response; only tag_name is used.
type releaseInfo struct {
	TagName string `json:"tag_name"`
}

// LatestVersion returns the tag_name of the repo's latest release.
func LatestVersion(o UpgradeOptions) (string, error) {
	o.applyDefaults()
	u := o.APIBase + "/repos/" + o.Repo + "/releases/latest"
	resp, err := http.Get(u)
	if err != nil {
		return "", fmt.Errorf("latest release %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release %s: %s", u, resp.Status)
	}
	var r releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("decode latest release %s: %w", u, err)
	}
	if r.TagName == "" {
		return "", fmt.Errorf("latest release %s: no tag_name in response", u)
	}
	return r.TagName, nil
}

// trimV removes a leading "v" from a version string.
func trimV(v string) string {
	return strings.TrimPrefix(v, "v")
}

// CheckUpgrade reports whether latest differs from current. The strings are
// compared after trimming a leading "v" from each; no semver ordering is
// attempted.
func CheckUpgrade(current string, o UpgradeOptions) (latest string, available bool, err error) {
	latest, err = LatestVersion(o)
	if err != nil {
		return "", false, err
	}
	return latest, trimV(latest) != trimV(current), nil
}

// assetName builds the release asset name for a tag and platform, following
// the release workflow's convention: flywheel-<tag>-<goos>-<goarch>.zip, with
// a .exe before the .zip on Windows.
func assetName(tag, goos, goarch string) string {
	name := "flywheel-" + tag + "-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name + ".zip"
}

// Upgrade resolves the version (o.Version, else the latest), downloads the
// platform's zip and checksums.txt, verifies the SHA-256 of the downloaded
// bytes, unzips the single entry and installs it over Dest. It refuses to
// install anything when the checksum does not match or the asset is missing
// from checksums.txt. The returned installed string is the resolved tag.
func Upgrade(current string, o UpgradeOptions) (installed string, err error) {
	o.applyDefaults()
	if !builtPlatforms[o.GOOS+"/"+o.GOARCH] {
		return "", fmt.Errorf("no release asset for %s/%s; the release builds linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64", o.GOOS, o.GOARCH)
	}
	dest := o.Dest
	if dest == "" {
		dest, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve current executable: %w", err)
		}
	}
	tag := o.Version
	if tag == "" {
		latest, lerr := LatestVersion(o)
		if lerr != nil {
			return "", lerr
		}
		tag = latest
	}
	asset := assetName(tag, o.GOOS, o.GOARCH)
	base := o.DownloadBase + "/" + o.Repo + "/releases/download/" + tag
	body, err := downloadBytes(base + "/" + asset)
	if err != nil {
		return "", err
	}
	sums, err := downloadBytes(base + "/checksums.txt")
	if err != nil {
		return "", err
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return "", err
	}
	got := sha256.Sum256(body)
	if !bytes.Equal(got[:], want) {
		return "", fmt.Errorf("checksum mismatch for %s: want %x, got %x; nothing installed", asset, want, got)
	}
	payload, err := unzipSingle(body)
	if err != nil {
		return "", err
	}
	if err := installOver(payload, dest); err != nil {
		return "", err
	}
	return tag, nil
}

// downloadBytes GETs url and returns the body.
func downloadBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return b, nil
}

// checksumFor parses a sha256sum-format body and returns the decoded hash for
// the named asset. A missing asset is an error.
func checksumFor(sums, asset string) ([]byte, error) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[1] != asset {
			continue
		}
		h, err := hex.DecodeString(f[0])
		if err != nil {
			return nil, fmt.Errorf("checksum for %s: %w", asset, err)
		}
		return h, nil
	}
	return nil, fmt.Errorf("no checksum for %s in checksums.txt; nothing installed", asset)
}

// unzipSingle returns the bytes of the single entry inside the release zip.
func unzipSingle(b []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("open release zip: %w", err)
	}
	if len(zr.File) != 1 {
		return nil, fmt.Errorf("release zip has %d entries, want 1", len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return nil, fmt.Errorf("open %s in release zip: %w", zr.File[0].Name, err)
	}
	defer rc.Close()
	p, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s from release zip: %w", zr.File[0].Name, err)
	}
	return p, nil
}

// installOver replaces dest with payload atomically in a way that works on
// Windows, where a running executable can be renamed but not overwritten:
// write a temp file beside dest, rename dest to dest+".old", rename the temp
// file to dest, then best-effort remove the .old. A stale dest+".old" is
// removed best-effort on the way in.
func installOver(payload []byte, dest string) error {
	dir := filepath.Dir(dest)
	old := dest + ".old"
	_ = os.Remove(old)
	tmp, err := os.CreateTemp(dir, ".flywheel-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", dest, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		if err := os.Rename(dest, old); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("rename %s to %s: %w", dest, old, err)
		}
	}
	if err := os.Rename(tmpName, dest); err != nil {
		if _, statErr := os.Stat(dest); statErr != nil {
			_ = os.Rename(old, dest)
		}
		os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, dest, err)
	}
	_ = os.Remove(old)
	return nil
}
