package importers

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestParsePgpass(t *testing.T) {
	lines, err := ParsePgpass(strings.NewReader(`# comment
db.example.com:5432:shop:ann:s3cr\:et\\x
*:*:*:bob:any
localhost:*:*:cid:pw:with:colons
broken line
other:6543:*:dan:d
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || lines[0].Password != `s3cr:et\x` || lines[2].Password != "pw:with:colons" {
		t.Fatalf("lines %+v", lines)
	}

	for _, tc := range []struct {
		cfg  driver.Config
		want string
		ok   bool
	}{
		{driver.Config{Host: "db.example.com", User: "ann", Database: "shop"}, `s3cr:et\x`, true},
		{driver.Config{Host: "db.example.com", Port: 5433, User: "ann", Database: "shop"}, "", false},
		{driver.Config{Host: "anything", User: "bob"}, "any", true},
		{driver.Config{Host: "localhost", User: "cid", Port: 1}, "pw:with:colons", true},
	} {
		got, ok := PgpassPassword(lines, tc.cfg)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%+v: %q %v", tc.cfg, got, ok)
		}
	}

	conns := PgpassConnections(lines)
	if len(conns) != 3 {
		t.Fatalf("connections %+v", conns)
	}
	if c := conns[2].Config; c.Host != "other" || c.Port != 6543 || c.Database != "" || c.User != "dan" || conns[2].Password != "d" {
		t.Errorf("wildcard database %+v", conns[2])
	}
}

func TestParsePgService(t *testing.T) {
	es, err := ParsePgService(strings.NewReader(`
# services
[prod]
host=db.prod
port=6432
dbname=app
user=svc
sslmode=verify-full
options=-c search_path=x

[local]
dbname=dev
password=pw
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("%+v", es)
	}
	p := es[0]
	if p.Name != "prod" || p.Config.Host != "db.prod" || p.Config.Port != 6432 || p.Config.Database != "app" || p.Config.User != "svc" ||
		!maps.Equal(p.Config.Options, map[string]string{"sslmode": "verify-full"}) || p.HasPassword {
		t.Errorf("prod %+v", p)
	}
	if l := es[1]; l.Config.Host != "localhost" || l.Config.Port != 5432 || l.Password != "pw" || !l.HasPassword {
		t.Errorf("local %+v", l)
	}

	for _, bad := range []string{"host=x", "[a\nport=x", "[a\nnonsense", "[a"} {
		if _, err := ParsePgService(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseTNSNames(t *testing.T) {
	es, err := ParseTNSNames(strings.NewReader(`
# Oracle names
ORCL, ORCL.WORLD =
  (DESCRIPTION =
    (ADDRESS_LIST =
      (ADDRESS = (PROTOCOL = TCP)(HOST = db1.example.com)(PORT = 1522))
      (ADDRESS = (PROTOCOL = TCP)(HOST = db2.example.com)(PORT = 1522))
    )
    (CONNECT_DATA = (SERVER = DEDICATED) (SERVICE_NAME = orcl.example.com))
  )
LEGACY=(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=old)(PORT=2484))(CONNECT_DATA=(SID=XE)))
IFILE = /etc/other.ora
EZ = dbhost:1530/FREEPDB1
`))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"ORCL", "ORCL.WORLD", "LEGACY", "EZ"}) {
		t.Fatalf("names %v", names)
	}
	if c := es[0].Config; c.Kind != driver.Oracle || c.Host != "db1.example.com" || c.Port != 1522 || c.Database != "orcl.example.com" {
		t.Errorf("ORCL %+v", c)
	}
	if c := es[2].Config; c.Host != "old" || c.Port != 2484 || c.Database != "" || !maps.Equal(c.Options, map[string]string{"sid": "XE", "tls": "true"}) {
		t.Errorf("LEGACY %+v", c)
	}
	if c := es[3].Config; c.Host != "dbhost" || c.Port != 1530 || c.Database != "FREEPDB1" {
		t.Errorf("EZ %+v", c)
	}

	for _, bad := range []string{"X = (DESCRIPTION = (ADDRESS = (HOST = h))", "X = (DESCRIPTION = (CONNECT_DATA = (SID = a)))", "= (A = 1)"} {
		if _, err := ParseTNSNames(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDetect(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       Format
	}{
		{"/home/a/.pgpass", "", Pgpass},
		{`C:\x\pgpass.conf`, "", Pgpass},
		{"pg_service.conf", "", PgService},
		{"TNSNAMES.ORA", "", TNSNames},
		{"x.txt", "# c\n[svc]\nhost=a", PgService},
		{"x.txt", "h:5432:db:u:p", Pgpass},
		{"x.txt", "A = (DESCRIPTION = (ADDRESS = (HOST = h)))", TNSNames},
		{"x.txt", "hello", Unknown},
	} {
		if got := Detect(tc.name, []byte(tc.data)); got != tc.want {
			t.Errorf("Detect(%q, %q) = %v, want %v", tc.name, tc.data, got, tc.want)
		}
	}
}

func TestPaths(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := pgpassPath("linux", "/home/a", env(nil)); got != filepath.Join("/home/a", ".pgpass") {
		t.Error(got)
	}
	if got := pgpassPath("windows", `C:\Users\a`, env(map[string]string{"APPDATA": `C:\AppData`})); got != filepath.Join(`C:\AppData`, "postgresql", "pgpass.conf") {
		t.Error(got)
	}
	if got := pgpassPath("linux", "/home/a", env(map[string]string{"PGPASSFILE": "/x"})); got != "/x" {
		t.Error(got)
	}
	got := pgServicePaths("linux", "/home/a", env(map[string]string{"PGSYSCONFDIR": "/etc/pg"}))
	if !slices.Equal(got, []string{filepath.Join("/home/a", ".pg_service.conf"), filepath.Join("/etc/pg", "pg_service.conf")}) {
		t.Error(got)
	}
	got = tnsNamesPaths(env(map[string]string{"TNS_ADMIN": "/tns", "ORACLE_HOME": "/oh"}))
	if !slices.Equal(got, []string{filepath.Join("/tns", "tnsnames.ora"), filepath.Join("/oh", "network", "admin", "tnsnames.ora")}) {
		t.Error(got)
	}
}
