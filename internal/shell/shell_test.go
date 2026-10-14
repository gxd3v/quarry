package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
)

// fakeBin creates stub executables in a fresh directory and makes it the
// only PATH entry.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		stub(t, dir, n)
	}
	t.Setenv("PATH", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE;.BAT;.CMD")
	}
	return dir
}

func stub(t *testing.T, dir, name string) string {
	t.Helper()
	path, body := filepath.Join(dir, name), "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		path, body = path+".bat", "@exit /b 0\r\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func getenv(env []string, key string) (string, bool) {
	var v string
	var ok bool
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); strings.EqualFold(k, key) {
			v, ok = val, true
		}
	}
	return v, ok
}

func mustCommand(t *testing.T, cfg driver.Config, pw string, scope driver.Scope) *Launch {
	t.Helper()
	l, err := Command(cfg, pw, scope)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	t.Cleanup(l.Cleanup)
	return l
}

// noSecret checks the password is neither in argv nor in Display.
func noSecret(t *testing.T, l *Launch, pw string) {
	t.Helper()
	for _, a := range l.Cmd.Args {
		if strings.Contains(a, pw) {
			t.Errorf("password in argv: %q", l.Cmd.Args)
		}
	}
	if strings.Contains(l.Display, pw) {
		t.Errorf("password in Display: %q", l.Display)
	}
}

func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != want {
		t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkCleanup checks Cleanup removes the file's directory and can run twice.
func checkCleanup(t *testing.T, l *Launch, file string) {
	t.Helper()
	dir := filepath.Dir(file)
	l.Cleanup()
	l.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s still exists after Cleanup (err=%v)", dir, err)
	}
}

func TestFind(t *testing.T) {
	dir := fakeBin(t, "sql", "sqlplus", "mongo")

	if p, err := Find(driver.Oracle, ""); err != nil || clientName(p) != "sql" {
		t.Errorf("oracle: %q, %v; want sql", p, err)
	}
	if p, err := Find(driver.MongoDB, ""); err != nil || clientName(p) != "mongo" {
		t.Errorf("mongodb fallback: %q, %v; want mongo", p, err)
	}
	stub(t, dir, "mongosh")
	if p, err := Find(driver.MongoDB, ""); err != nil || clientName(p) != "mongosh" {
		t.Errorf("mongodb: %q, %v; want mongosh", p, err)
	}

	_, err := Find(driver.Postgres, "")
	if err == nil || err.Error() != "psql not found in PATH; the shell_path connection option sets its location" {
		t.Errorf("missing psql: %v", err)
	}
	_, err = Find(driver.Redis, "")
	if err == nil || !strings.Contains(err.Error(), "redis-cli not found in PATH") {
		t.Errorf("missing redis-cli: %v", err)
	}
	if _, err := Find("sqlite", ""); err == nil {
		t.Error("unknown kind: want error")
	}
}

func TestFindOracleFallbackAndOverride(t *testing.T) {
	fakeBin(t, "sqlplus")
	if p, err := Find(driver.Oracle, ""); err != nil || clientName(p) != "sqlplus" {
		t.Errorf("oracle fallback: %q, %v; want sqlplus", p, err)
	}

	fakeBin(t)
	if _, err := Find(driver.Oracle, ""); err == nil || !strings.Contains(err.Error(), "sql or sqlplus not found in PATH") {
		t.Errorf("missing oracle clients: %v", err)
	}

	other := t.TempDir()
	custom := stub(t, other, "psql16")
	if p, err := Find(driver.Postgres, custom); err != nil || p != custom {
		t.Errorf("override by path: %q, %v; want %q", p, err, custom)
	}
	missing := filepath.Join(other, "nope")
	if _, err := Find(driver.Postgres, missing); err == nil || !strings.Contains(err.Error(), "shell_path") {
		t.Errorf("missing override: %v", err)
	}

	t.Setenv("PATH", other)
	if p, err := Find(driver.Postgres, "psql16"); err != nil || clientName(p) != "psql16" {
		t.Errorf("override by name: %q, %v", p, err)
	}
}

func TestCommandMissingBinary(t *testing.T) {
	fakeBin(t)
	if _, err := Command(driver.Config{Kind: driver.Postgres}, "pw", driver.Scope{}); err == nil {
		t.Error("want error when psql is missing")
	}
	if _, err := Command(driver.Config{Kind: "sqlite"}, "", driver.Scope{}); err == nil {
		t.Error("want error for unknown kind")
	}
}

func TestPostgres(t *testing.T) {
	fakeBin(t, "psql")
	t.Setenv("PGPASSWORD", "inherited-secret")
	t.Setenv("PGSERVICE", "elsewhere")
	const pw = `s3:cr\et`
	cfg := driver.Config{
		Kind: driver.Postgres, Host: "db", Port: 5433, User: "bob", Database: "app",
		Options: map[string]string{"sslmode": "verify-full", "sslrootcert": "/ca.pem", "sslcert": "/c.pem", "sslkey": "/k.pem"},
	}
	l := mustCommand(t, cfg, pw, driver.Scope{Database: "reports", Schema: `My Sch\ema`})
	noSecret(t, l, pw)

	if l.Display != "psql -h db -p 5433 -U bob reports" {
		t.Errorf("Display = %q", l.Display)
	}
	if len(l.Cmd.Args) != 1 {
		t.Errorf("args = %q, want none", l.Cmd.Args[1:])
	}
	env := l.Cmd.Env
	want := map[string]string{
		"PGHOST": "db", "PGPORT": "5433", "PGUSER": "bob", "PGDATABASE": "reports",
		"PGAPPNAME": "quarry-shell", "PGSSLMODE": "verify-full", "PGSSLROOTCERT": "/ca.pem",
		"PGSSLCERT": "/c.pem", "PGSSLKEY": "/k.pem",
		"PGOPTIONS": `-c search_path="My\ Sch\\ema",public`,
	}
	for k, v := range want {
		if got, _ := getenv(env, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"PGPASSWORD", "PGSERVICE"} {
		if v, ok := getenv(env, k); ok {
			t.Errorf("inherited %s=%q not removed", k, v)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, pw) {
			t.Errorf("password in environment: %q", kv)
		}
	}

	passfile, ok := getenv(env, "PGPASSFILE")
	if !ok {
		t.Fatal("PGPASSFILE not set")
	}
	if got, want := readFile(t, passfile), `db:5433:*:bob:s3\:cr\\et`+"\n"; got != want {
		t.Errorf("pgpass = %q, want %q", got, want)
	}
	checkMode(t, passfile, 0o600)
	checkMode(t, filepath.Dir(passfile), 0o700)
	checkCleanup(t, l, passfile)
}

func TestPostgresDefaultsAndReadOnly(t *testing.T) {
	fakeBin(t, "psql")
	l := mustCommand(t, driver.Config{Kind: driver.Postgres, ReadOnly: true}, "", driver.Scope{})
	if !slices.Equal(l.Cmd.Args[1:], []string{"-v", "ON_ERROR_STOP=1"}) {
		t.Errorf("args = %q", l.Cmd.Args[1:])
	}
	for k, v := range map[string]string{
		"PGHOST": "localhost", "PGPORT": "5432", "PGDATABASE": "postgres",
		"PGOPTIONS": "-c default_transaction_read_only=on",
	} {
		if got, _ := getenv(l.Cmd.Env, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"PGPASSFILE", "PGUSER", "PGSSLMODE"} {
		if _, ok := getenv(l.Cmd.Env, k); ok {
			t.Errorf("%s set without a value to give", k)
		}
	}
	l.Cleanup()

	l = mustCommand(t, driver.Config{Kind: driver.Postgres, ReadOnly: true}, "", driver.Scope{Schema: "s"})
	if got, _ := getenv(l.Cmd.Env, "PGOPTIONS"); got != `-c search_path="s",public -c default_transaction_read_only=on` {
		t.Errorf("PGOPTIONS = %q", got)
	}

	if _, err := Command(driver.Config{Kind: driver.Postgres}, "a\nb", driver.Scope{}); err == nil || strings.Contains(err.Error(), "a\nb") {
		t.Errorf("newline password: %v", err)
	}
}

func TestPgpass(t *testing.T) {
	for _, tc := range []struct{ host, user, pw, want string }{
		{"h", "u", "p", "h:5432:*:u:p\n"},
		{"h", "", `a:b\c`, `h:5432:*:*:a\:b\\c` + "\n"},
		{"::1", `d\om:u`, "*", `\:\:1:5432:*:d\\om\:u:*` + "\n"},
		{"h1, h2", "u", "p", "h1:5432:*:u:p\nh2:5432:*:u:p\n"},
	} {
		got, err := pgpass(tc.host, "5432", tc.user, tc.pw)
		if err != nil || got != tc.want {
			t.Errorf("pgpass(%q, %q, %q) = %q, %v; want %q", tc.host, tc.user, tc.pw, got, err, tc.want)
		}
	}
}

func TestPgOptions(t *testing.T) {
	got := pgOptions([]string{"-c", "search_path=\"a b\tc\\d\",public"})
	if want := `-c search_path="a\ b\` + "\t" + `c\\d",public`; got != want {
		t.Errorf("pgOptions = %q, want %q", got, want)
	}
}

func TestOracle(t *testing.T) {
	fakeBin(t, "sqlplus")
	const pw = `p@ss/w0rd&x`
	cfg := driver.Config{Kind: driver.Oracle, Host: "ora", Port: 1522, User: "scott", ReadOnly: true,
		Options: map[string]string{"service": "ORCLPDB1"}}
	l := mustCommand(t, cfg, pw, driver.Scope{Schema: "HR"})
	noSecret(t, l, pw)

	if l.Display != "sqlplus scott@//ora:1522/ORCLPDB1" {
		t.Errorf("Display = %q", l.Display)
	}
	args := l.Cmd.Args[1:]
	if len(args) != 3 || args[0] != "-L" || args[1] != "/nolog" || !strings.HasPrefix(args[2], "@") {
		t.Fatalf("args = %q", args)
	}
	script := strings.Trim(strings.TrimPrefix(args[2], "@"), `"`)
	want := "SET ECHO OFF\nSET DEFINE OFF\n" +
		`CONNECT scott/"p@ss/w0rd&x"@//ora:1522/ORCLPDB1` + "\n" +
		"SET DEFINE ON\n" +
		`ALTER SESSION SET CURRENT_SCHEMA = "HR";` + "\n"
	if got := readFile(t, script); got != want {
		t.Errorf("script =\n%s\nwant\n%s", got, want)
	}
	checkMode(t, script, 0o600)
	checkMode(t, filepath.Dir(script), 0o700)
	checkCleanup(t, l, script)
}

func TestOracleTargets(t *testing.T) {
	for _, tc := range []struct {
		cfg  driver.Config
		want string
	}{
		{driver.Config{Host: "h", Database: "svc"}, "//h:1521/svc"},
		{driver.Config{}, "//localhost:1521"},
		{driver.Config{Host: "::1", Port: 1, Options: map[string]string{"service": "s"}}, "//[::1]:1/s"},
		{driver.Config{Host: "h", Options: map[string]string{"service": "s", "tls": "true"}}, "tcps://h:1521/s"},
		{driver.Config{Host: "h", Port: 2, Options: map[string]string{"sid": "ORCL"}},
			"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=h)(PORT=2))(CONNECT_DATA=(SID=ORCL)))"},
		{driver.Config{Host: "h", Options: map[string]string{"tns": "PROD_HA", "service": "x"}}, "PROD_HA"},
	} {
		if got, err := oraTarget(tc.cfg); err != nil || got != tc.want {
			t.Errorf("oraTarget(%+v) = %q, %v; want %q", tc.cfg, got, err, tc.want)
		}
	}
	if _, err := oraTarget(driver.Config{Options: map[string]string{"tns": "a b"}}); err == nil {
		t.Error("tns with space: want error")
	}
}

func TestOracleLogonForms(t *testing.T) {
	fakeBin(t, "sql")
	script := func(cfg driver.Config, pw string) string {
		cfg.Kind = driver.Oracle
		l := mustCommand(t, cfg, pw, driver.Scope{})
		return readFile(t, strings.Trim(strings.TrimPrefix(l.Cmd.Args[3], "@"), `"`))
	}
	for _, tc := range []struct {
		cfg  driver.Config
		pw   string
		want string
	}{
		{driver.Config{User: "sys", Options: map[string]string{"role": "sysdba"}}, "x", `CONNECT sys/"x"@//localhost:1521 AS SYSDBA`},
		{driver.Config{User: "app[hr]"}, "x", `CONNECT app[hr]/"x"@//localhost:1521`},
		{driver.Config{User: "Mixed User"}, "x", `CONNECT "Mixed User"/"x"@//localhost:1521`},
		{driver.Config{User: "bob"}, "", `CONNECT bob@//localhost:1521`},
		{driver.Config{}, "", `CONNECT /@//localhost:1521`},
	} {
		if got := script(tc.cfg, tc.pw); !strings.Contains(got, tc.want+"\n") {
			t.Errorf("%+v: script\n%s\nwant line %q", tc.cfg, got, tc.want)
		}
	}
	for _, cfg := range []driver.Config{
		{Kind: driver.Oracle, User: "u", Options: map[string]string{"role": "god"}},
		{Kind: driver.Oracle, User: `a"b`},
	} {
		if _, err := Command(cfg, "x", driver.Scope{}); err == nil {
			t.Errorf("%+v: want error", cfg)
		}
	}
	if _, err := Command(driver.Config{Kind: driver.Oracle, User: "u"}, `pa"ss`, driver.Scope{}); err == nil || strings.Contains(err.Error(), `pa"ss`) {
		t.Errorf("quote in password: %v", err)
	}
}

func TestOraScriptArg(t *testing.T) {
	if got := oraScriptArg("/tmp/x/login.sql"); got != "@/tmp/x/login.sql" {
		t.Errorf("got %q", got)
	}
	if runtime.GOOS != "windows" {
		if got := oraScriptArg("/tmp/a b/login.sql"); got != `@"/tmp/a b/login.sql"` {
			t.Errorf("got %q", got)
		}
	}
}

func TestMongo(t *testing.T) {
	fakeBin(t, "mongosh")
	const pw = `p@ss:w/rd"`
	cfg := driver.Config{Kind: driver.MongoDB, Host: "m1:27017,m2:27017", User: "admin", Database: "app",
		Options: map[string]string{"authSource": "admin", "replicaSet": "rs0", "shell_path": "mongosh"}}
	l := mustCommand(t, cfg, pw, driver.Scope{Database: "logs"})
	noSecret(t, l, pw)

	if want := "mongosh mongodb://admin@m1:27017,m2:27017/logs?appName=quarry-shell&authSource=admin&replicaSet=rs0"; l.Display != want {
		t.Errorf("Display = %q, want %q", l.Display, want)
	}
	args := l.Cmd.Args[1:]
	if len(args) != 3 || args[0] != "--nodb" || args[1] != "--shell" {
		t.Fatalf("args = %q", args)
	}
	want := `db = connect("mongodb://admin:p%40ss%3Aw%2Frd%22@m1:27017,m2:27017/logs?appName=quarry-shell\u0026authSource=admin\u0026replicaSet=rs0");` + "\n"
	if got := readFile(t, args[2]); got != want {
		t.Errorf("script = %q, want %q", got, want)
	}
	checkMode(t, args[2], 0o600)
	checkMode(t, filepath.Dir(args[2]), 0o700)
	checkCleanup(t, l, args[2])
}

func TestMongoURI(t *testing.T) {
	for _, tc := range []struct {
		cfg   driver.Config
		pw    string
		scope driver.Scope
		want  string
	}{
		{driver.Config{}, "", driver.Scope{}, "mongodb://localhost/?appName=quarry-shell"},
		{driver.Config{Host: "h", Port: 27018, User: "u", Database: "d"}, "p", driver.Scope{},
			"mongodb://u:p@h:27018/d?appName=quarry-shell"},
		{driver.Config{Host: "cluster0.example.net", User: "u",
			Options: map[string]string{"srv": "true", "authSource": "admin", "appName": "mine"}}, "p w", driver.Scope{Database: "x"},
			"mongodb+srv://u:p%20w@cluster0.example.net/x?appName=mine&authSource=admin"},
		{driver.Config{Host: "h", Options: map[string]string{"tls": "true"}}, "", driver.Scope{},
			"mongodb://h/?appName=quarry-shell&tls=true"},
		{driver.Config{Host: "::1"}, "", driver.Scope{}, "mongodb://[::1]/?appName=quarry-shell"},
	} {
		if got := mongoURI(tc.cfg, tc.pw, tc.scope); got != tc.want {
			t.Errorf("mongoURI(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestRedis(t *testing.T) {
	fakeBin(t, "redis-cli")
	t.Setenv("REDISCLI_AUTH", "inherited")
	const pw = "r3dis"
	cfg := driver.Config{Kind: driver.Redis, Host: "cache", Port: 6380, User: "app", Database: "2",
		Options: map[string]string{"tls": "true"}}
	l := mustCommand(t, cfg, pw, driver.Scope{Database: "5"})
	noSecret(t, l, pw)
	if want := []string{"-h", "cache", "-p", "6380", "-n", "5", "--tls", "--user", "app"}; !slices.Equal(l.Cmd.Args[1:], want) {
		t.Errorf("args = %q, want %q", l.Cmd.Args[1:], want)
	}
	if l.Display != "redis-cli -h cache -p 6380 -n 5 --tls --user app" {
		t.Errorf("Display = %q", l.Display)
	}
	if v, _ := getenv(l.Cmd.Env, "REDISCLI_AUTH"); v != pw {
		t.Errorf("REDISCLI_AUTH = %q", v)
	}
	if n := slices.IndexFunc(l.Cmd.Env, func(kv string) bool { return kv == "REDISCLI_AUTH=inherited" }); n >= 0 {
		t.Error("inherited REDISCLI_AUTH kept")
	}

	l = mustCommand(t, driver.Config{Kind: driver.Redis, Database: "3", Options: map[string]string{"cluster": "true"}}, "", driver.Scope{})
	if !slices.Equal(l.Cmd.Args[1:], []string{"-c"}) {
		t.Errorf("cluster args = %q", l.Cmd.Args[1:])
	}
	if _, ok := getenv(l.Cmd.Env, "REDISCLI_AUTH"); ok {
		t.Error("REDISCLI_AUTH set without a password")
	}

	if _, err := Command(driver.Config{Kind: driver.Redis}, "", driver.Scope{Database: "x"}); err == nil {
		t.Error("invalid db: want error")
	}
}

func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"A=1", "pgpassword=x", "B=2", "=C:=C:\\"}, []string{"PGPASSWORD"}, []string{"b=3"})
	if want := []string{"A=1", "=C:=C:\\", "b=3"}; !slices.Equal(got, want) {
		t.Errorf("mergeEnv = %q, want %q", got, want)
	}
}

func TestClientName(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/psql": "psql", "C:/ora/sql.bat": "sql", "C:/ora/SQLPLUS.EXE": "SQLPLUS", "/x/mongo.sh": "mongo.sh",
	} {
		if got := clientName(filepath.FromSlash(in)); got != want {
			t.Errorf("clientName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPsqlIntegration runs psql with the launch environment against
// QUARRY_TEST_PG_DSN.
func TestPsqlIntegration(t *testing.T) {
	dsn := os.Getenv("QUARRY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_PG_DSN not set")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	cfg, pw, err := store.ParseURI(dsn)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Command(cfg, pw, driver.Scope{Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	l.Cmd.Args = append(l.Cmd.Args, "-X", "-A", "-t", "-c", "select 1")
	var out, stderr bytes.Buffer
	l.Cmd.Stdout, l.Cmd.Stderr = &out, &stderr
	if err := l.Cmd.Run(); err != nil {
		t.Fatalf("psql: %v: %s", err, stderr.String())
	}
	if strings.TrimSpace(out.String()) != "1" {
		t.Errorf("output = %q", out.String())
	}
}
