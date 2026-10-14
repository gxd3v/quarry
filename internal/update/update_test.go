package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.2.0", "v1.10.0", true},
		{"1.0.0", "v2.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", false},
		{"v1.0.0-rc.1", "v1.0.0-rc.2", true},
		{"dev", "v9.9.9", false},
		{"v1.0.0", "latest", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestChecker(t *testing.T) {
	var hits, full atomic.Int32
	limited := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if limited.Load() {
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(`{"tag_name": "v1.3.0", "html_url": "https://example.com/v1.3.0", "body": "- faster", "published_at": "2026-10-01T10:00:00Z",
			"assets": [{"name": "quarry-linux-amd64.tar.gz", "browser_download_url": "https://example.com/a"}]}`))
	}))
	defer srv.Close()

	c := NewChecker(srv.URL)
	for range 3 {
		r, err := c.Latest(t.Context())
		if err != nil || r.Version != "v1.3.0" || r.Notes != "- faster" || r.Assets["quarry-linux-amd64.tar.gz"] == "" || r.Date.Year() != 2026 {
			t.Fatalf("latest %+v %v", r, err)
		}
	}
	if hits.Load() != 3 || full.Load() != 1 {
		t.Fatalf("hits %d, full answers %d; want 3 and 1 (the rest 304)", hits.Load(), full.Load())
	}

	// Rate limited: the cached release comes back with the error, and no
	// request is made until the reset.
	limited.Store(true)
	c.etag = ""
	r, err := c.Latest(t.Context())
	var rl *ErrRateLimited
	if !errors.As(err, &rl) || r.Version != "v1.3.0" {
		t.Fatalf("limited: %+v %v", r, err)
	}
	before := hits.Load()
	if _, err := c.Latest(t.Context()); !errors.As(err, &rl) || hits.Load() != before {
		t.Fatalf("checked again while blocked: %v, hits %d", err, hits.Load())
	}
}

func TestCheckerBadTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name": "nightly"}`))
	}))
	defer srv.Close()
	if _, err := NewChecker(srv.URL).Latest(t.Context()); err == nil {
		t.Fatal("non-version tag accepted")
	}
}

func TestDetect(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return filepath.FromSlash(m[k]) }
	}
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	for _, tc := range []struct {
		name     string
		exe, os  string
		env      map[string]string
		writable func(string) bool
		want     Install
	}{
		{"windows portable", `C:\tools\quarry\quarry.exe`, "windows", nil, yes, Install{Binary, `C:\tools\quarry\quarry.exe`, "quarry-windows-amd64.zip"}},
		{"windows installed", `C:\Program Files\Quarry\quarry.exe`, "windows", nil, no, Install{Installer, `C:\Program Files\Quarry\quarry.exe`, "quarry-windows-amd64-installer.exe"}},
		{"appimage", "/tmp/.mount_x/usr/bin/quarry", "linux", map[string]string{"APPIMAGE": "/home/a/Apps/Quarry.AppImage"}, yes, Install{AppImage, "/home/a/Apps/Quarry.AppImage", "Quarry-x86_64.AppImage"}},
		{"linux tarball", "/home/a/bin/quarry", "linux", nil, yes, Install{Binary, "/home/a/bin/quarry", "quarry-linux-amd64.tar.gz"}},
		{"deb", "/usr/bin/quarry", "linux", nil, no, Install{Managed, "/usr/bin/quarry", ""}},
		{"mac app", "/Applications/Quarry.app/Contents/MacOS/quarry", "darwin", nil, yes, Install{AppBundle, "/Applications/Quarry.app", "quarry-macos-universal.zip"}},
		{"mac brew", "/opt/homebrew/Caskroom/Quarry.app/Contents/MacOS/quarry", "darwin", nil, no, Install{Managed, "/opt/homebrew/Caskroom/Quarry.app/Contents/MacOS/quarry", ""}},
	} {
		got := Detect(filepath.FromSlash(tc.exe), tc.os, env(tc.env), tc.writable)
		want := tc.want
		want.Target = filepath.FromSlash(want.Target)
		if got != want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, want)
		}
	}
}

// release serves the assets with a SHA256SUMS and returns the release.
func release(t *testing.T, assets map[string][]byte, corrupt string) Release {
	t.Helper()
	var sums strings.Builder
	for name, data := range assets {
		h := sha256.Sum256(data)
		if name == corrupt {
			h[0]++
		}
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	mux := http.NewServeMux()
	r := Release{Version: "v2.0.0", Assets: map[string]string{}}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for name, data := range assets {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(data) })
		r.Assets[name] = srv.URL + "/" + name
	}
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(sums.String())) })
	r.Assets["SHA256SUMS"] = srv.URL + "/SHA256SUMS"
	return r
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quarry")
	os.WriteFile(exe, []byte("old"), 0o755)
	r := release(t, map[string][]byte{"quarry-linux-amd64.tar.gz": tarGz(t, map[string]string{"quarry": "new", "quarry.desktop": "x"})}, "")
	var last int64
	got, err := Apply(t.Context(), http.DefaultClient, r, Install{Binary, exe, "quarry-linux-amd64.tar.gz"}, func(done, _ int64) { last = done })
	if err != nil || got != exe || last == 0 {
		t.Fatalf("apply: %q %v, progress %d", got, err, last)
	}
	if read(t, exe) != "new" || read(t, exe+".old") != "old" {
		t.Fatal("binary not replaced")
	}
	Cleanup(exe)
	if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old copy left after cleanup")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestApplyWindowsZip(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "Quarry (portable).exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	r := release(t, map[string][]byte{"quarry-windows-amd64.zip": zipOf(t, map[string]string{"quarry.exe": "new"})}, "")
	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{Binary, exe, "quarry-windows-amd64.zip"}, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new" {
		t.Fatal("exe not replaced")
	}
}

func TestApplyAppImageAndBundle(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Quarry.AppImage")
	os.WriteFile(img, []byte("old"), 0o755)
	r := release(t, map[string][]byte{
		"Quarry-x86_64.AppImage":     []byte("new image"),
		"quarry-macos-universal.zip": zipOf(t, map[string]string{"Quarry.app/Contents/MacOS/quarry": "new mac", "Quarry.app/Contents/Info.plist": "plist"}),
	}, "")
	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{AppImage, img, "Quarry-x86_64.AppImage"}, nil); err != nil || read(t, img) != "new image" {
		t.Fatalf("appimage: %v", err)
	}

	bundle := filepath.Join(dir, "Quarry.app")
	os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "quarry"), []byte("old mac"), 0o755)
	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{AppBundle, bundle, "quarry-macos-universal.zip"}, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(bundle, "Contents", "MacOS", "quarry")) != "new mac" || read(t, filepath.Join(bundle, "Contents", "Info.plist")) != "plist" {
		t.Fatal("bundle not replaced")
	}
	if read(t, filepath.Join(bundle+".old", "Contents", "MacOS", "quarry")) != "old mac" {
		t.Fatal("old bundle not kept")
	}
}

func TestApplyInstallerAndFailures(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quarry.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	r := release(t, map[string][]byte{"quarry-windows-amd64-installer.exe": []byte("setup"), "quarry-windows-amd64.zip": zipOf(t, map[string]string{"quarry.exe": "new"})}, "quarry-windows-amd64.zip")

	setup, err := Apply(t.Context(), http.DefaultClient, r, Install{Installer, exe, "quarry-windows-amd64-installer.exe"}, nil)
	if err != nil || read(t, setup) != "setup" || read(t, exe) != "old" {
		t.Fatalf("installer: %q %v", setup, err)
	}
	os.Remove(setup)

	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{Binary, exe, "quarry-windows-amd64.zip"}, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum: %v", err)
	}
	if read(t, exe) != "old" {
		t.Fatal("exe replaced by a corrupt download")
	}
	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{Binary, exe, "quarry-linux-amd64.tar.gz"}, nil); err == nil {
		t.Fatal("missing asset accepted")
	}
	if _, err := Apply(t.Context(), http.DefaultClient, r, Install{Kind: Managed, Target: exe}, nil); err == nil {
		t.Fatal("managed install updated")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}
