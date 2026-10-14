package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Kind is how the running copy is installed, which decides how it updates.
type Kind string

const (
	Binary    Kind = "binary"    // a quarry executable in a writable folder
	AppImage  Kind = "appimage"  // a Linux AppImage
	AppBundle Kind = "app"       // a macOS Quarry.app in a writable folder
	Installer Kind = "installer" // Windows, installed in a protected folder
	Managed   Kind = "managed"   // owned by a package manager (deb, rpm, ...)
)

// Install says what an update replaces and with which release asset.
type Install struct {
	Kind   Kind
	Target string // file or bundle replaced; the running executable for Installer
	Asset  string
}

// Detect works out how exe is installed. writable reports whether files
// can be created in a folder.
func Detect(exe, goos string, getenv func(string) string, writable func(dir string) bool) Install {
	switch goos {
	case "windows":
		if writable(filepath.Dir(exe)) {
			return Install{Kind: Binary, Target: exe, Asset: "quarry-windows-amd64.zip"}
		}
		return Install{Kind: Installer, Target: exe, Asset: "quarry-windows-amd64-installer.exe"}
	case "darwin":
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if strings.HasSuffix(bundle, ".app") && writable(filepath.Dir(bundle)) {
			return Install{Kind: AppBundle, Target: bundle, Asset: "quarry-macos-universal.zip"}
		}
	case "linux":
		if p := getenv("APPIMAGE"); p != "" {
			if writable(filepath.Dir(p)) {
				return Install{Kind: AppImage, Target: p, Asset: "Quarry-x86_64.AppImage"}
			}
		} else if writable(filepath.Dir(exe)) {
			return Install{Kind: Binary, Target: exe, Asset: "quarry-linux-amd64.tar.gz"}
		}
	}
	return Install{Kind: Managed, Target: exe}
}

// Writable reports whether a file can be created in dir.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".quarry-write-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

const tempPrefix = ".quarry-update-"

// Apply downloads the release's asset for in, checks it against the
// release's SHA256SUMS and puts it in place of in.Target, keeping the old
// copy as Target+".old" until the next start (see Cleanup). For Installer
// it only downloads the installer and returns its path. progress, when
// set, gets the bytes downloaded and the total (-1 when unknown).
func Apply(ctx context.Context, c *http.Client, r Release, in Install, progress func(done, total int64)) (string, error) {
	if in.Kind == Managed {
		return "", errors.New("installed by a package manager")
	}
	url, ok := r.Assets[in.Asset]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Version, in.Asset)
	}
	want, err := checksum(ctx, c, r, in.Asset)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(in.Target)
	if in.Kind == Installer {
		dir = os.TempDir()
	}
	file, err := download(ctx, c, url, dir, want, progress)
	if err != nil {
		return "", err
	}
	switch in.Kind {
	case Installer:
		return file, nil
	case AppImage:
		defer os.Remove(file)
		if err := os.Chmod(file, 0o755); err != nil {
			return "", err
		}
		return in.Target, replace(in.Target, file)
	case Binary:
		defer os.Remove(file)
		// The archive's executable takes the place of this one, whatever
		// this one is called.
		next := filepath.Join(dir, tempPrefix+"new-"+filepath.Base(in.Target))
		if strings.HasSuffix(in.Asset, ".zip") {
			err = extractZipFile(file, "quarry.exe", next)
		} else {
			err = extractTarFile(file, "quarry", next)
		}
		if err != nil {
			os.Remove(next)
			return "", err
		}
		return in.Target, replace(in.Target, next)
	case AppBundle:
		defer os.Remove(file)
		tmp, err := os.MkdirTemp(filepath.Dir(in.Target), tempPrefix)
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := extractZip(file, tmp); err != nil {
			return "", err
		}
		return in.Target, replace(in.Target, filepath.Join(tmp, filepath.Base(in.Target)))
	}
	return "", fmt.Errorf("unknown install kind %q", in.Kind)
}

// Cleanup removes what an earlier update left next to target.
func Cleanup(target string) {
	os.RemoveAll(target + ".old")
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), tempPrefix+"*"))
	for _, m := range matches {
		os.RemoveAll(m)
	}
}

// replace moves next to target, keeping target as target.old; running
// executables can be renamed on every platform, just not overwritten.
func replace(target, next string) error {
	old := target + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(next, target); err != nil {
		os.Rename(old, target)
		return err
	}
	return nil
}

func checksum(ctx context.Context, c *http.Client, r Release, asset string) (string, error) {
	url, ok := r.Assets["SHA256SUMS"]
	if !ok {
		return "", fmt.Errorf("release %s has no SHA256SUMS", r.Version)
	}
	res, err := get(ctx, c, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("SHA256SUMS of %s lists no %s", r.Version, asset)
}

func get(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, res.Status)
	}
	return res, nil
}

// download saves url in dir, failing unless its SHA-256 is want.
func download(ctx context.Context, c *http.Client, url, dir, want string, progress func(done, total int64)) (string, error) {
	res, err := get(ctx, c, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	f, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err = w.Write(buf[:n]); err != nil {
				break
			}
			done += int64(n)
			if progress != nil {
				progress(done, res.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			err = fmt.Errorf("download: %w", rerr)
			break
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("the download does not match its SHA-256 checksum")
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func extractTarFile(archive, name, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return writeFile(dest, tr, 0o755)
		}
	}
}

func extractZipFile(archive, name, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if !zf.FileInfo().IsDir() && filepath.Base(zf.Name) == name {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeFile(dest, rc, 0o755)
		}
	}
	return fmt.Errorf("the archive has no %s", name)
}

// extractZip unpacks a whole archive (a macOS bundle) into dir.
func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, zf := range zr.File {
		name := filepath.FromSlash(zf.Name)
		mode := zf.FileInfo().Mode()
		switch {
		case mode.IsDir():
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			link, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			if err := root.Symlink(string(link), name); err != nil {
				return err
			}
			continue
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o200)
		if err == nil {
			_, err = io.Copy(out, rc)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(dest string, r io.Reader, perm os.FileMode) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
