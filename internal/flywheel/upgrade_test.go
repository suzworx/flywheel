package flywheel

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// buildZip returns a zip archive with a single entry named name holding
// payload.
func buildZip(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create %s: %v", name, err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("zip write %s: %v", name, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// upgradeServer serves one release end-to-end: the latest-release JSON, the
// platform's zip asset and checksums.txt. hits counts every request served.
type upgradeServer struct {
	*httptest.Server
	opts UpgradeOptions
	hits atomic.Int32
}

// newUpgradeServer serves a release for tag on goos/goarch whose zip payload
// is payload. checksums.txt holds the real SHA-256 of the zip bytes unless
// sumsOverride is given, in which case it is used verbatim.
func newUpgradeServer(t *testing.T, tag, goos, goarch string, payload []byte, sumsOverride ...string) *upgradeServer {
	t.Helper()
	asset := assetName(tag, goos, goarch)
	zipBytes := buildZip(t, strings.TrimSuffix(asset, ".zip"), payload)
	sum := sha256.Sum256(zipBytes)
	sumsBody := fmt.Sprintf("%x  %s\n", sum, asset)
	if len(sumsOverride) > 0 {
		sumsBody = sumsOverride[0]
	}
	repo := "suzworx/flywheel"
	s := &upgradeServer{opts: UpgradeOptions{
		Repo:   repo,
		GOOS:   goos,
		GOARCH: goarch,
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	})
	mux.HandleFunc("/"+repo+"/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprint(w, sumsBody)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	})
	s.Server = httptest.NewServer(mux)
	s.opts.APIBase = s.Server.URL
	s.opts.DownloadBase = s.Server.URL
	return s
}

// count wraps the handlers' shared hit counter.
func (s *upgradeServer) count() int {
	return int(s.hits.Load())
}

func TestLatestVersion(t *testing.T) {
	srv := newUpgradeServer(t, "v1.2.3", "linux", "amd64", []byte("payload"))
	defer srv.Close()
	got, err := LatestVersion(srv.opts)
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got != "v1.2.3" {
		t.Errorf("LatestVersion = %q, want %q", got, "v1.2.3")
	}
}

func TestCheckUpgrade(t *testing.T) {
	srv := newUpgradeServer(t, "v1.2.3", "linux", "amd64", []byte("payload"))
	defer srv.Close()

	latest, available, err := CheckUpgrade("v1.0.0", srv.opts)
	if err != nil {
		t.Fatalf("CheckUpgrade differs: %v", err)
	}
	if latest != "v1.2.3" || !available {
		t.Errorf("CheckUpgrade(v1.0.0) = (%q, %v), want (v1.2.3, true)", latest, available)
	}

	latest, available, err = CheckUpgrade("v1.2.3", srv.opts)
	if err != nil {
		t.Fatalf("CheckUpgrade same: %v", err)
	}
	if available {
		t.Errorf("CheckUpgrade(v1.2.3) available = true, want false (latest %q)", latest)
	}

	latest, available, err = CheckUpgrade("1.2.3", srv.opts)
	if err != nil {
		t.Fatalf("CheckUpgrade no leading v: %v", err)
	}
	if available {
		t.Errorf("CheckUpgrade(1.2.3) available = true, want false despite missing leading v")
	}
}

func TestUpgradeInstalls(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new binary\n")
	srv := newUpgradeServer(t, "v2.0.0", "linux", "amd64", payload)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	if err := os.WriteFile(dest, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	opts := srv.opts
	opts.Dest = dest

	installed, err := Upgrade("v1.0.0", opts)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if installed != "v2.0.0" {
		t.Errorf("Upgrade returned %q, want %q", installed, "v2.0.0")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("installed bytes = %q, want %q", got, payload)
	}
	if _, err := os.Stat(dest + ".old"); !os.IsNotExist(err) {
		t.Errorf("stale %s left behind", dest+".old")
	}
}

func TestUpgradeChecksumMismatch(t *testing.T) {
	payload := []byte("binary")
	asset := assetName("v2.0.0", "linux", "amd64")
	bad := strings.Repeat("0", 64) + "  " + asset + "\n"
	srv := newUpgradeServer(t, "v2.0.0", "linux", "amd64", payload, bad)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	old := []byte("old binary")
	if err := os.WriteFile(dest, old, 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	opts := srv.opts
	opts.Dest = dest

	_, err := Upgrade("v1.0.0", opts)
	if err == nil {
		t.Fatal("Upgrade succeeded on a checksum mismatch, want error")
	}
	if !strings.Contains(err.Error(), asset) {
		t.Errorf("error %q does not name the asset", err)
	}
	got, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("read dest: %v", rerr)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("dest changed after failed upgrade: got %q, want %q", got, old)
	}
}

func TestUpgradeAssetMissingFromChecksums(t *testing.T) {
	payload := []byte("binary")
	srv := newUpgradeServer(t, "v2.0.0", "linux", "amd64", payload, "# no checksums here\n")
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	old := []byte("old binary")
	if err := os.WriteFile(dest, old, 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	opts := srv.opts
	opts.Dest = dest

	_, err := Upgrade("v1.0.0", opts)
	if err == nil {
		t.Fatal("Upgrade succeeded with the asset missing from checksums.txt, want error")
	}
	got, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("read dest: %v", rerr)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("dest changed after refused upgrade: got %q, want %q", got, old)
	}
}

func TestUpgradeUnsupportedPlatform(t *testing.T) {
	srv := newUpgradeServer(t, "v2.0.0", "linux", "amd64", []byte("binary"))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	if err := os.WriteFile(dest, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	opts := srv.opts
	opts.Dest = dest
	opts.GOOS = "plan9"
	opts.GOARCH = "mips"

	_, err := Upgrade("v1.0.0", opts)
	if err == nil {
		t.Fatal("Upgrade succeeded for plan9/mips, want error")
	}
	if !strings.Contains(err.Error(), "plan9/mips") {
		t.Errorf("error %q does not name the platform", err)
	}
	if n := srv.count(); n != 0 {
		t.Errorf("made %d requests before rejecting the platform, want 0", n)
	}
}

func TestUpgradeIgnoresStaleOld(t *testing.T) {
	payload := []byte("new binary")
	srv := newUpgradeServer(t, "v2.0.0", "linux", "amd64", payload)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	if err := os.WriteFile(dest, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	if err := os.WriteFile(dest+".old", []byte("stale"), 0o755); err != nil {
		t.Fatalf("write stale .old: %v", err)
	}
	opts := srv.opts
	opts.Dest = dest

	if _, err := Upgrade("v1.0.0", opts); err != nil {
		t.Fatalf("Upgrade with stale .old: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("installed bytes = %q, want %q", got, payload)
	}
}
