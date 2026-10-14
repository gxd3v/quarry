package importcsv

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16Bytes(s string, be bool) []byte {
	var b bytes.Buffer
	if be {
		b.Write([]byte{0xFE, 0xFF})
	} else {
		b.Write([]byte{0xFF, 0xFE})
	}
	for _, u := range utf16.Encode([]rune(s)) {
		if be {
			b.Write([]byte{byte(u >> 8), byte(u)})
		} else {
			b.Write([]byte{byte(u), byte(u >> 8)})
		}
	}
	return b.Bytes()
}

func TestSniff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		delim  rune
		header bool
		enc    string
		first  []string
	}{
		{"comma header", []byte("id,name,price\n1,ann,2.5\n2,bob,3\n"), ',', true, UTF8, []string{"1", "ann", "2.5"}},
		{"semicolon", []byte("id;name\n1;\"a;b\"\n2;c\n"), ';', true, UTF8, []string{"1", "a;b"}},
		{"tab no header", []byte("1\tann\n2\tbob\n3\tcid\n"), '\t', false, UTF8, []string{"1", "ann"}},
		{"pipe", []byte("a|b\nx|y\n"), '|', true, UTF8, []string{"x", "y"}},
		{"quoted newline", []byte("id,note\n1,\"two\nlines\"\n2,x\n"), ',', true, UTF8, []string{"1", "two\nlines"}},
		{"bom", append([]byte{0xEF, 0xBB, 0xBF}, "name,age\nann,31\n"...), ',', true, UTF8BOM, []string{"ann", "31"}},
		{"utf16le", utf16Bytes("name,city\nzoë,Kraków\n", false), ',', true, UTF16LE, []string{"zoë", "Kraków"}},
		{"utf16be", utf16Bytes("name,city\nzoë,Kraków\n", true), ',', true, UTF16BE, []string{"zoë", "Kraków"}},
		{"cp1252", []byte("name,price\ncaf\xe9,\x805\n"), ',', true, Windows1252, []string{"café", "€5"}},
	} {
		p, err := Sniff(bytes.NewReader(tc.data))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if p.Delimiter != tc.delim || p.HasHeader != tc.header || p.Encoding != tc.enc || len(p.Rows) == 0 || !slices.Equal(p.Rows[0], tc.first) {
			t.Errorf("%s: %+v", tc.name, p)
		}
	}
	p, _ := Sniff(strings.NewReader("1,2\n3,4\n"))
	if !slices.Equal(p.Header, []string{"column1", "column2"}) {
		t.Errorf("generated header %v", p.Header)
	}
	if _, err := Sniff(strings.NewReader(" \n")); err == nil {
		t.Error("empty input accepted")
	}
}

func TestReader(t *testing.T) {
	data := utf16Bytes("a;b\n1;\"x;y\"\n2;\n", false)
	r, err := NewReader(bytes.NewReader(data), Options{Delimiter: ';', HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, slices.Clone(rec))
	}
	if len(got) != 2 || !slices.Equal(got[0], []string{"1", "x;y"}) || !slices.Equal(got[1], []string{"2", ""}) {
		t.Fatalf("records %q", got)
	}

	// A forced encoding without a BOM.
	r, _ = NewReader(bytes.NewReader([]byte("caf\xe9\n")), Options{Encoding: Windows1252})
	if rec, err := r.Read(); err != nil || rec[0] != "café" {
		t.Fatalf("cp1252 %q %v", rec, err)
	}
}

func TestAutoMap(t *testing.T) {
	cols := []string{"id", "first_name", "Email"}
	for _, tc := range []struct {
		header, want []string
	}{
		{[]string{"ID", "First Name", "email", "extra"}, []string{"id", "first_name", "Email", ""}},
		{[]string{"first-name", "FIRSTNAME"}, []string{"first_name", ""}},
		{[]string{"a", "b", "c"}, []string{"id", "first_name", "Email"}},
		{[]string{"a", "b"}, []string{"", ""}},
	} {
		if got := AutoMap(tc.header, cols); !slices.Equal(got, tc.want) {
			t.Errorf("AutoMap(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}
