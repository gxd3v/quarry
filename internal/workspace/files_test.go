package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

func openStore(t testing.TB) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// noTemps fails if dir holds leftovers of atomic writes.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestOpenCreatesDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "scratches"), filepath.Join(dir, "history")} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			t.Fatalf("%s: not a directory (%v)", d, err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, want 0700", d, fi.Mode().Perm())
		}
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct{ name, in, want string }{
		{"plain", "select 1;\nselect 2;\n", "select 1;\nselect 2;\n"},
		{"crlf", "select 1;\r\nselect 2;\r\n", "select 1;\nselect 2;\n"},
		{"bom", "\xef\xbb\xbfselect 'ação';\r\n", "select 'ação';\n"},
		{"lone cr kept", "a\rb\r\n", "a\rb\n"},
		{"empty", "", ""},
		{"latin1 kept as is", "select '\xe7';", "select '\xe7';"},
	}
	for _, tt := range tests {
		p := filepath.Join(dir, tt.name+".sql")
		if err := os.WriteFile(p, []byte(tt.in), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(p)
		if err != nil || got != tt.want {
			t.Errorf("%s: ReadFile = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestReadFileRejects(t *testing.T) {
	dir := t.TempDir()

	bin := filepath.Join(dir, "x.bin")
	os.WriteFile(bin, []byte("MZ\x00\x01\x02"), 0o644)
	if _, err := ReadFile(bin); !errors.Is(err, ErrBinary) {
		t.Errorf("binary: err = %v, want ErrBinary", err)
	}
	utf16 := filepath.Join(dir, "u16.sql")
	os.WriteFile(utf16, []byte("\xff\xfes\x00e\x00l\x00"), 0o644)
	if _, err := ReadFile(utf16); !errors.Is(err, ErrBinary) {
		t.Errorf("utf-16: err = %v, want ErrBinary", err)
	}

	big := filepath.Join(dir, "big.sql")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(MaxFileSize + 1)
	f.Close()
	_, err = ReadFile(big)
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "big.sql") {
		t.Errorf("too large: err = %v", err)
	}

	limit := filepath.Join(dir, "limit.sql")
	os.WriteFile(limit, []byte(strings.Repeat("a", MaxFileSize)), 0o644)
	if s, err := ReadFile(limit); err != nil || len(s) != MaxFileSize {
		t.Errorf("exactly at limit: len %d, err %v", len(s), err)
	}

	if _, err := ReadFile(dir); err == nil {
		t.Error("directory: want error")
	}
	if _, err := ReadFile(filepath.Join(dir, "missing.sql")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: err = %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "dir", "q.sql")
	if err := WriteFile(p, "select 1;\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(p); err != nil || got != "select 1;\n" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("new file mode %v, want 0644", fi.Mode().Perm())
		}
		if err := os.Chmod(p, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteFile(p, "select 2;\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadFile(p); got != "select 2;\n" {
		t.Errorf("overwrite = %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
			t.Errorf("mode after overwrite %v, want 0640", fi.Mode().Perm())
		}
	}
	noTemps(t, filepath.Dir(p))
	if err := WriteFile(dir, "x"); err == nil {
		t.Error("writing over a directory: want error")
	}
}

func TestWriteFileSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sql")
	link := filepath.Join(dir, "link.sql")
	os.WriteFile(target, []byte("old"), 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := WriteFile(link, "new"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced by a regular file")
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target = %q, want new", b)
	}
}

func TestBindings(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()

	b := Binding{Conn: uuid.New(), Scope: driver.Scope{Database: "app", Schema: "public"}}
	if err := s.Bind("q.sql", b); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"q.sql",
		filepath.Join(cwd, "q.sql"),
		filepath.Join(cwd, "x", "..", "q.sql"),
		"." + string(filepath.Separator) + "q.sql",
	} {
		if got, ok := s.Binding(p); !ok || got != b {
			t.Errorf("Binding(%q) = %v, %v; want %v", p, got, ok, b)
		}
	}
	if _, ok := s.Binding("other.sql"); ok {
		t.Error("unbound path reported as bound")
	}

	// Persisted across Open.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s2.Binding(filepath.Join(cwd, "q.sql")); !ok || got != b {
		t.Errorf("after reopen: %v, %v", got, ok)
	}

	// The zero Binding unbinds.
	if err := s2.Bind("q.sql", Binding{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Binding("q.sql"); ok {
		t.Error("still bound after Bind(zero)")
	}
	s3, _ := Open(dir)
	if _, ok := s3.Binding("q.sql"); ok {
		t.Error("unbind not persisted")
	}
	noTemps(t, dir)
}

func TestBindingsCase(t *testing.T) {
	dir := t.TempDir()
	b := Binding{Conn: uuid.New()}

	// The real platform behaviour.
	s := openStore(t)
	if err := s.Bind(filepath.Join(dir, "Report.SQL"), b); err != nil {
		t.Fatal(err)
	}
	_, ok := s.Binding(filepath.Join(strings.ToUpper(dir), "report.sql"))
	if want := runtime.GOOS == "windows"; ok != want {
		t.Errorf("case-insensitive lookup on %s = %v, want %v", runtime.GOOS, ok, want)
	}

	// Both behaviours, whatever the platform.
	for _, fold := range []bool{false, true} {
		old := caseInsensitive
		caseInsensitive = fold
		s := openStore(t)
		s.Bind(filepath.Join(dir, "A.sql"), b)
		_, ok := s.Binding(filepath.Join(dir, "a.sql"))
		caseInsensitive = old
		if ok != fold {
			t.Errorf("caseInsensitive=%v: lookup with other case = %v", fold, ok)
		}
	}
}

func TestBindingsCorrupt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bindings.json")
	os.WriteFile(p, []byte(`{"/a.sql": {"Conn": "nope"`), 0o600)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Binding("/a.sql"); ok {
		t.Error("binding from a corrupt file")
	}
	matches, _ := filepath.Glob(p + ".corrupt-*")
	if len(matches) != 1 {
		t.Errorf("corrupt file not quarantined: %v", matches)
	}
	if err := s.Bind("b.sql", Binding{Conn: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Binding("b.sql"); !ok {
		t.Error("cannot bind after recovering from a corrupt file")
	}
}

func TestScratchNumbering(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, ext := range []string{".sql", "js", ""} {
		p, err := s.NewScratch(ext)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	var names []string
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	if want := []string{"scratch-1.sql", "scratch-2.js", "scratch-3"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("scratch not created: %v", err)
	}

	// Deleting the newest scratches does not free their numbers, even
	// across Open.
	os.Remove(paths[2])
	os.Remove(paths[1])
	s, _ = Open(dir)
	if p, _ := s.NewScratch(".sql"); filepath.Base(p) != "scratch-4.sql" {
		t.Errorf("after deletion got %s, want scratch-4.sql", filepath.Base(p))
	}

	// Without the counter, existing files still prevent reuse.
	os.Remove(filepath.Join(dir, "scratch.seq"))
	if p, _ := s.NewScratch(".sql"); filepath.Base(p) != "scratch-5.sql" {
		t.Errorf("without counter got %s, want scratch-5.sql", filepath.Base(p))
	}

	for _, bad := range []string{"../x", `.a\b`, "./sql"} {
		if _, err := s.NewScratch(bad); err == nil {
			t.Errorf("NewScratch(%q): want error", bad)
		}
	}
	noTemps(t, dir)
}

func TestScratches(t *testing.T) {
	s := openStore(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	var paths []string
	for i := range 3 {
		p, err := s.NewScratch(".sql")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(p, []byte(strings.Repeat("x", i)), 0o600)
		paths = append(paths, p)
	}
	// scratch-1 is the most recently modified.
	os.Chtimes(paths[0], base.Add(3*time.Minute), base.Add(3*time.Minute))
	os.Chtimes(paths[1], base.Add(1*time.Minute), base.Add(1*time.Minute))
	os.Chtimes(paths[2], base.Add(2*time.Minute), base.Add(2*time.Minute))

	got, err := s.Scratches()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fi := range got {
		names = append(names, fi.Name)
	}
	if want := []string{"scratch-1.sql", "scratch-3.sql", "scratch-2.sql"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	if got[1].Size != 2 || got[1].Path != paths[2] || !got[1].Modified.Equal(base.Add(2*time.Minute)) {
		t.Errorf("info = %+v", got[1])
	}

	if !s.IsScratch(paths[0]) {
		t.Error("IsScratch(scratch) = false")
	}
	if s.IsScratch(filepath.Join(filepath.Dir(s.scratchDir), "q.sql")) {
		t.Error("IsScratch(state dir file) = true")
	}
	if s.IsScratch(filepath.Join(s.scratchDir, "sub", "q.sql")) {
		t.Error("IsScratch(nested) = true")
	}
	if want := runtime.GOOS == "windows"; s.IsScratch(strings.ToUpper(paths[0])) != want {
		t.Errorf("IsScratch(upper case) != %v", want)
	}
}
