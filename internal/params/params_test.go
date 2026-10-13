package params

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestFind(t *testing.T) {
	tests := []struct {
		stmt string
		want []Param
	}{
		{"", nil},
		{"select 1", nil},
		{"select * from t where id = :id", []Param{{"id", []int{27}}}},
		{"select :a, :b, :a", []Param{{"a", []int{7, 15}}, {"b", []int{11}}}},
		{"select $1, $2, $1", []Param{{"1", []int{7, 15}}, {"2", []int{11}}}},
		{"select $10", []Param{{"10", []int{7}}}},
		{"select :1, :2 from dual", []Param{{"1", []int{7}}, {"2", []int{11}}}},
		{"select x::text from t", nil},
		{"select x::text, :p::int", []Param{{"p", []int{16}}}},
		{"select x :: text", nil},
		{"v := 1", nil},
		{"v := :p", []Param{{"p", []int{5}}}},
		{"select ':no' from t", nil},
		{`select ":no" from t`, nil},
		{"select 1 -- :no\n, :yes", []Param{{"yes", []int{18}}}},
		{"select /* :no /* :nested */ :still */ :yes", []Param{{"yes", []int{38}}}},
		{"select $$ :no $1 $$, :yes", []Param{{"yes", []int{21}}}},
		{"select $f$ :no $f$ || $1", []Param{{"1", []int{22}}}},
		{"select $$", nil},
		{`select E'\' :no', :yes`, []Param{{"yes", []int{18}}}},
		{"select q'[ it's :no ]', :yes from dual", []Param{{"yes", []int{24}}}},
		{"select a[1:2], a[lo:hi], a[:n] from t", nil},
		{"select a[$1] from t", []Param{{"1", []int{9}}}},
		{"begin :new.id := 1; end;", nil},
		{"select :ação from t", []Param{{"ação", []int{7}}}},
		{"select a$1 from t", nil},
		{"select to_date(:d, 'HH24:MI') from dual", []Param{{"d", []int{15}}}},
		{"insert into t values (:a,:b)", []Param{{"a", []int{22}}, {"b", []int{25}}}},
		{"select :", nil},
		{"select :;", nil},
		{"select $", nil},
		{"select 'unterminated :x", nil},
	}
	for _, tt := range tests {
		t.Run(tt.stmt, func(t *testing.T) {
			got := Find(driver.LangSQL, tt.stmt)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Find = %v, want %v", got, tt.want)
			}
			for _, p := range got {
				for _, pos := range p.Positions {
					if c := tt.stmt[pos]; c != ':' && c != '$' {
						t.Errorf("position %d is %q, not a sigil", pos, c)
					}
				}
			}
		})
	}
}

func TestFindNonSQL(t *testing.T) {
	if p := Find(driver.LangMongo, "db.c.find({a: :x})"); p != nil {
		t.Errorf("mongo: %v", p)
	}
	if p := Find(driver.LangRedis, "GET :x"); p != nil {
		t.Errorf("redis: %v", p)
	}
	s, err := Substitute(driver.LangRedis, "GET :x", nil, "")
	if err != nil || s != "GET :x" {
		t.Errorf("redis Substitute = %q, %v", s, err)
	}
}

func TestSubstitute(t *testing.T) {
	tests := []struct {
		stmt   string
		values map[string]string
		want   string
	}{
		{"select 1", nil, "select 1"},
		{"select :n", map[string]string{"n": "42"}, "select 42"},
		{"select :n", map[string]string{"n": " 42 "}, "select 42"},
		{"select :n", map[string]string{"n": "3.14"}, "select 3.14"},
		{"select :n", map[string]string{"n": ".5"}, "select .5"},
		{"select :n", map[string]string{"n": "1e10"}, "select 1e10"},
		{"select :n", map[string]string{"n": "+7"}, "select 7"},
		{"select x-:n", map[string]string{"n": "-5"}, "select x-(-5)"},
		{"select :n", map[string]string{"n": "0"}, "select 0"},
		{"select :n", map[string]string{"n": "0.25"}, "select 0.25"},
		{"select :n", map[string]string{"n": "007"}, "select '007'"},
		{"select :n", map[string]string{"n": "'42'"}, "select '42'"},
		{"select :n", map[string]string{"n": "1,5"}, "select '1,5'"},
		{"select :n", map[string]string{"n": "0x10"}, "select '0x10'"},
		{"select :n", map[string]string{"n": "NaN"}, "select 'NaN'"},
		{"select :n", map[string]string{"n": "Inf"}, "select 'Inf'"},
		{"select :n", map[string]string{"n": "1e"}, "select '1e'"},
		{"select :n", map[string]string{"n": "."}, "select '.'"},
		{"select :n", map[string]string{"n": "-"}, "select '-'"},
		{"select :n", map[string]string{"n": "NULL"}, "select NULL"},
		{"select :n", map[string]string{"n": "null"}, "select NULL"},
		{"select :n", map[string]string{"n": "'NULL'"}, "select 'NULL'"},
		{"select :n", map[string]string{"n": "nullable"}, "select 'nullable'"},
		{"select :n", map[string]string{"n": ""}, "select ''"},
		{"select :n", map[string]string{"n": "bob"}, "select 'bob'"},
		{"select :n", map[string]string{"n": "O'Brien"}, "select 'O''Brien'"},
		{"select :n", map[string]string{"n": "'O''Brien'"}, "select 'O''Brien'"},
		{"select :n", map[string]string{"n": "'O'Brien'"}, "select 'O''Brien'"},
		{"select :n", map[string]string{"n": "'; drop table t; --"}, "select '''; drop table t; --'"},
		{"select :n", map[string]string{"n": `a\b`}, `select 'a\b'`},
		{"select :n", map[string]string{"n": "ação"}, "select 'ação'"},
		{"select :a, :b, :a", map[string]string{"a": "1", "b": "x"}, "select 1, 'x', 1"},
		{"select $1, $2", map[string]string{"1": "x", "2": "2"}, "select 'x', 2"},
		{"select :p::int", map[string]string{"p": "5"}, "select 5::int"},
		{"select x::text, ':p', :p", map[string]string{"p": "v"}, "select x::text, ':p', 'v'"},
		{"select :long, :l", map[string]string{"long": "1", "l": "2"}, "select 1, 2"},
		{"select :n", map[string]string{"n": "x", "unused": "y"}, "select 'x'"},
	}
	for _, tt := range tests {
		t.Run(tt.stmt+"="+tt.values["n"], func(t *testing.T) {
			got, err := Substitute(driver.LangSQL, tt.stmt, tt.values, "postgres")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubstituteErrors(t *testing.T) {
	_, err := Substitute(driver.LangSQL, "select :a, $1, :b", map[string]string{"b": "1"}, "postgres")
	if err == nil || !strings.Contains(err.Error(), ":a, $1") {
		t.Errorf("missing values: err = %v", err)
	}
	_, err = Substitute(driver.LangSQL, "select :a", map[string]string{"a": "x\x00y"}, "postgres")
	if err == nil {
		t.Error("NUL byte accepted for postgres")
	}
	got, err := Substitute(driver.LangSQL, "select :a", map[string]string{"a": "x\x00y"}, "oracle")
	if err != nil || got != "select 'x\x00y'" {
		t.Errorf("oracle NUL: %q, %v", got, err)
	}
}

func FuzzFind(f *testing.F) {
	for _, s := range []string{
		"select :a, $1", "x::int", "v := :p", "$$ :x $$", "E'\\", "q'[", "/* /*", "a[:n]",
		":new.x", "$", ":", "$tag$", "'", `"`, "select :ação",
	} {
		f.Add(s, "1")
	}
	f.Fuzz(func(t *testing.T, s, v string) {
		ps := Find(driver.LangSQL, s)
		values := map[string]string{}
		for _, p := range ps {
			if p.Name == "" || len(p.Positions) == 0 {
				t.Fatalf("bad param %+v", p)
			}
			for _, pos := range p.Positions {
				if pos < 0 || pos+1+len(p.Name) > len(s) || s[pos+1:pos+1+len(p.Name)] != p.Name {
					t.Fatalf("bad position %d for %q in %q", pos, p.Name, s)
				}
			}
			values[p.Name] = v
		}
		if _, err := Substitute(driver.LangSQL, s, values, "oracle"); err != nil {
			t.Fatalf("Substitute: %v", err)
		}
	})
}
