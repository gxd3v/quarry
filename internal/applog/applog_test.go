package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "quarry.log")
	w, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dd\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read(path); got != "cccc\ndd\n" {
		t.Errorf("log = %q", got)
	}
	if got := read(path + ".1"); got != "aaaa\nbbbb\n" {
		t.Errorf("rotated log = %q", got)
	}
}

func TestReopenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quarry.log")
	for range 2 {
		w, err := Open(path, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x\n"))
		w.Close()
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "x\n") != 2 {
		t.Errorf("log = %q", b)
	}
	w, _ := Open(path, 1<<20)
	w.Close()
	if _, err := w.Write([]byte("y")); err == nil {
		t.Error("write after close succeeded")
	}
}
