package redis

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

// Integration tests run against a live server named by QUARRY_TEST_REDIS_DSN,
// e.g. redis://localhost:56379/0. Every test works under its own key prefix
// (in the DSN's database and in database itAltDB) and deletes it afterwards.

const itAltDB = 7

func itConfig(t *testing.T) (driver.Config, string) {
	t.Helper()
	dsn := os.Getenv("QUARRY_TEST_REDIS_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_REDIS_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return driver.Config{
		Kind:     driver.Redis,
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		Options:  map[string]string{},
	}, pw
}

// itPrefix returns a unique key prefix and removes its keys at the end.
func itPrefix(t *testing.T, cfg driver.Config, pw string) string {
	t.Helper()
	p := "qit:" + strings.ToLower(rand.Text()[:8]) + ":"
	t.Cleanup(func() {
		for _, db := range []int{dbOf(cfg), itAltDB} {
			cl := goredis.NewClient(&goredis.Options{Addr: addr(cfg), Password: pw, DB: db})
			ctx := context.Background()
			iter := cl.Scan(ctx, 0, globEscape(p)+"*", 1000).Iterator()
			for iter.Next(ctx) {
				cl.Del(ctx, iter.Val())
			}
			cl.Close()
		}
	})
	return p
}

func dbOf(cfg driver.Config) int {
	n, _ := strconv.Atoi(cfg.Database)
	return n
}

func addr(cfg driver.Config) string { return cfg.Host + ":" + strconv.Itoa(cfg.Port) }

func itOpen(t *testing.T, cfg driver.Config, pw string) *conn {
	t.Helper()
	c, err := New().Open(t.Context(), cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*conn)
}

func itSession(t *testing.T, c *conn, db string) *session {
	t.Helper()
	s, err := c.NewSession(t.Context(), driver.Scope{Database: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.(*session)
}

// exec runs line and returns the columns and every row.
func exec(t *testing.T, s driver.Session, line string) ([]string, [][]any) {
	t.Helper()
	cols, rows, err := tryExec(t.Context(), s, line)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return cols, rows
}

func tryExec(ctx context.Context, s driver.Session, line string) ([]string, [][]any, error) {
	r, err := s.Execute(ctx, driver.Statement{Text: line})
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	var cols []string
	for _, c := range r.Columns() {
		cols = append(cols, c.Name)
	}
	var rows [][]any
	for {
		page, err := r.Next(ctx, 300)
		if err != nil {
			return nil, nil, err
		}
		if len(page) == 0 {
			return cols, rows, nil
		}
		rows = append(rows, page...)
	}
}

func value(t *testing.T, s driver.Session, line string) any {
	t.Helper()
	_, rows := exec(t, s, line)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("%s: rows = %#v", line, rows)
	}
	return rows[0][0]
}

func TestITOpenAndAuth(t *testing.T) {
	cfg, pw := itConfig(t)
	c := itOpen(t, cfg, pw)
	if err := c.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	bad := cfg
	bad.Port = 1
	if _, err := New().Open(t.Context(), bad, pw); err == nil || errors.Is(err, driver.ErrAuth) ||
		!strings.HasPrefix(err.Error(), "cannot connect to") {
		t.Errorf("refused port: %v", err)
	}

	// An ACL user with a password stands in for a password-protected server.
	admin := goredis.NewClient(&goredis.Options{Addr: addr(cfg), Password: pw})
	defer admin.Close()
	user := "quarry_it_" + strings.ToLower(rand.Text()[:6])
	if err := admin.Do(t.Context(), "ACL", "SETUSER", user, "on", ">s3cret-pw", "~*", "+@all").Err(); err != nil {
		t.Fatal(err)
	}
	defer admin.Do(context.Background(), "ACL", "DELUSER", user)
	ucfg := cfg
	ucfg.User = user
	_, err := New().Open(t.Context(), ucfg, "wrong-pw-123")
	if !errors.Is(err, driver.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pw-123") {
		t.Errorf("error leaks the password: %v", err)
	}
	uc, err := New().Open(t.Context(), ucfg, "s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	uc.Close()
	ucfg.User, ucfg.Options = "", map[string]string{"username": user}
	uc, err = New().Open(t.Context(), ucfg, "s3cret-pw")
	if err != nil {
		t.Fatalf("username option: %v", err)
	}
	uc.Close()
}

func TestITExplorer(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	for _, line := range []string{
		"HSET " + p + "user:1 name ann age 30",
		"SET " + p + "user:2 bob EX 1000",
		"RPUSH " + p + "user:2:list a b c",
		"SET " + Quote(p+"bin\xff") + " x",
		"ZADD " + p + "rank 1 a 2.5 b",
	} {
		exec(t, s, line)
	}

	roots, err := c.Children(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	db := strconv.Itoa(dbOf(cfg))
	if !slices.ContainsFunc(roots, func(n *driver.Node) bool { return reflect.DeepEqual(n.Path, []string{db}) && n.Name == "db"+db }) {
		t.Fatalf("roots = %v", roots)
	}
	segs := strings.Split(strings.TrimSuffix(p, ":"), ":")
	base := &driver.Node{Path: append([]string{db}, segs...), Kind: driver.NodeFolder}
	level, err := c.Children(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	names := nodeNames(level)
	if want := []string{"user/", `"bin\xff"`, "rank"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("level = %q, want %q", names, want)
	}
	user := level[0]
	if user.Detail != "(3)" {
		t.Errorf("folder detail = %q", user.Detail)
	}
	kids, err := c.Children(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2/", "1", "2"}; !reflect.DeepEqual(nodeNames(kids), want) {
		t.Fatalf("user level = %q", nodeNames(kids))
	}
	if kids[1].Detail != "hash" || !strings.HasPrefix(kids[2].Detail, "string · ttl ") {
		t.Errorf("details = %q, %q", kids[1].Detail, kids[2].Detail)
	}
	if !reflect.DeepEqual(kids[1].Path, KeyPath(dbOf(cfg), p+"user:1", "")) {
		t.Errorf("key path = %q", kids[1].Path)
	}
	bin, err := c.Children(t.Context(), level[1])
	if err != nil || bin != nil {
		t.Errorf("a key has no children: %v %v", bin, err)
	}

	// Describe and replay the DDL of every key under a new name.
	for _, key := range []string{"user:1", "user:2", "user:2:list", "rank", "bin\xff"} {
		node := &driver.Node{Path: KeyPath(dbOf(cfg), p+key, ""), Kind: driver.NodeKey}
		d, err := c.Describe(t.Context(), node)
		if err != nil {
			t.Fatalf("describe %s: %v", key, err)
		}
		if stat(d, "Type") == "" || stat(d, "Memory usage") == "" || stat(d, "Encoding") == "" {
			t.Errorf("%s stats = %+v", key, d.Stats)
		}
		copyKey := p + "copy:" + key
		for line := range strings.Lines(d.DDL) {
			args, err := Fields(line)
			if err != nil {
				t.Fatal(err)
			}
			if Quote(args[1]) == Quote(p+key) {
				args[1] = copyKey
			}
			exec(t, s, command(args...))
		}
		if !sameValue(t, s, p+key, copyKey) {
			t.Errorf("%s: DDL does not recreate the key:\n%s", key, d.DDL)
		}
	}
	if d, _ := c.Describe(t.Context(), &driver.Node{Path: KeyPath(dbOf(cfg), p+"user:2", ""), Kind: driver.NodeKey}); !strings.Contains(d.DDL, "PEXPIRE") {
		t.Errorf("TTL missing from DDL: %s", d.DDL)
	}
	if d, err := c.Describe(t.Context(), user); err != nil || stat(d, "Keys") == "" {
		t.Errorf("describe folder = %+v, %v", d, err)
	}
	if d, err := c.Describe(t.Context(), &driver.Node{Path: []string{db}, Kind: driver.NodeDatabase}); err != nil || stat(d, "Keys") == "" {
		t.Errorf("describe db = %+v, %v", d, err)
	}

	objs, err := c.Objects(t.Context(), driver.Scope{Database: cfg.Database})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(objs, func(o driver.Object) bool { return o.Name == p+"user:1" })
	if i < 0 || objs[i].Detail != "hash" || objs[i].Schema != db || objs[i].Kind != driver.NodeKey {
		t.Errorf("objects lack %suser:1: %d", p, len(objs))
	}
	obj := &driver.Node{Path: []string{objs[i].Schema}, Kind: driver.NodeKey, Name: objs[i].Name}
	if q, err := c.DataQuery(obj, "", nil); err != nil || q != "HGETALL "+p+"user:1" {
		t.Errorf("data query of a catalog node = %q, %v", q, err)
	}
}

func nodeNames(ns []*driver.Node) []string {
	var out []string
	for _, n := range ns {
		name := n.Name
		if n.Kind == driver.NodeFolder {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

func stat(d *driver.ObjectDetails, name string) string {
	for _, r := range d.Stats {
		if r.Name == name {
			return r.Definition
		}
	}
	return ""
}

// sameValue compares two keys' type and content.
func sameValue(t *testing.T, s driver.Session, a, b string) bool {
	t.Helper()
	typ := value(t, s, "TYPE "+Quote(a))
	if value(t, s, "TYPE "+Quote(b)) != typ {
		return false
	}
	var q string
	switch typ {
	case "string":
		q = "GET %s"
	case "hash":
		q = "HGETALL %s"
	case "list":
		q = "LRANGE %s 0 -1"
	case "zset":
		q = "ZRANGE %s 0 -1 WITHSCORES"
	default:
		t.Fatalf("type %v", typ)
	}
	_, ra := exec(t, s, strings.Replace(q, "%s", Quote(a), 1))
	_, rb := exec(t, s, strings.Replace(q, "%s", Quote(b), 1))
	return reflect.DeepEqual(ra, rb)
}

func TestITSession(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)

	exec(t, s, "SET "+p+"n 41")
	if v := value(t, s, "INCR "+p+"n"); v != int64(42) {
		t.Errorf("INCR = %#v", v)
	}
	exec(t, s, `SET `+p+`bin "\xff\x00a"`)
	if v := value(t, s, "GET "+p+"bin"); !reflect.DeepEqual(v, []byte("\xff\x00a")) {
		t.Errorf("binary = %#v", v)
	}
	if v := value(t, s, "GET "+p+"missing"); v != nil {
		t.Errorf("missing = %#v", v)
	}
	exec(t, s, "ZADD "+p+"z 1.5 m")
	if v := value(t, s, "ZSCORE "+p+"z m"); v != 1.5 {
		t.Errorf("ZSCORE = %#v", v)
	}
	_, rows := exec(t, s, "HSET "+p+"h b 2 a 1")
	if rows[0][0] != int64(2) {
		t.Errorf("HSET = %#v", rows)
	}
	cols, rows := exec(t, s, "HGETALL "+p+"h")
	if !reflect.DeepEqual(cols, []string{"field", "value"}) || !reflect.DeepEqual(rows, [][]any{{"a", "1"}, {"b", "2"}}) {
		t.Errorf("HGETALL = %q %#v", cols, rows)
	}
	cols, _ = exec(t, s, "INFO server")
	if !reflect.DeepEqual(cols, []string{"section", "key", "value"}) {
		t.Errorf("INFO columns = %q", cols)
	}

	_, err := s.Execute(t.Context(), driver.Statement{Text: "LPUSH " + p + "h x"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "WRONGTYPE" {
		t.Errorf("WRONGTYPE = %#v", err)
	}
	_, err = s.Execute(t.Context(), driver.Statement{Text: "NOSUCHCMD"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "ERR" {
		t.Errorf("unknown command = %#v", err)
	}
	_, err = s.Execute(t.Context(), driver.Statement{Text: `SET k "open`})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Position != 6 {
		t.Errorf("syntax error = %#v", err)
	}
	_, err = s.Execute(t.Context(), driver.Statement{Text: "GET a\nGET b"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Position != 6 {
		t.Errorf("two commands = %#v", err)
	}
	if _, err := s.Execute(t.Context(), driver.Statement{Text: "SUBSCRIBE ch"}); err == nil {
		t.Error("SUBSCRIBE accepted")
	}

	// SELECT is per session.
	other := itSession(t, c, cfg.Database)
	alt := strconv.Itoa(itAltDB)
	exec(t, s, "SELECT "+alt)
	exec(t, s, "SET "+p+"where alt")
	if v := value(t, other, "GET "+p+"where"); v != nil {
		t.Errorf("SELECT leaked into another session: %#v", v)
	}
	if v := value(t, s, "GET "+p+"where"); v != "alt" {
		t.Errorf("selected db lost: %#v", v)
	}
	scoped := itSession(t, c, alt)
	if v := value(t, scoped, "GET "+p+"where"); v != "alt" {
		t.Errorf("Scope.Database ignored: %#v", v)
	}
	if !s.Alive() {
		t.Error("session not alive")
	}
	s.Close()
	if s.Alive() {
		t.Error("closed session alive")
	}
}

func TestITCancelBlocking(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	alt := strconv.Itoa(itAltDB)
	exec(t, s, "SELECT "+alt)
	exec(t, s, "SET "+p+"mark here")
	id := value(t, s, "CLIENT ID")

	cancelled := func() time.Duration {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := s.Execute(ctx, driver.Statement{Text: "BLPOP " + p + "never 0"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("BLPOP = %v", err)
		}
		return time.Since(start)
	}

	// CLIENT UNBLOCK ends the command and keeps the connection.
	if d := cancelled(); d > 2*time.Second {
		t.Errorf("cancel took %v", d)
	}
	if !s.Alive() {
		t.Error("session died")
	}
	if v := value(t, s, "CLIENT ID"); v != id {
		t.Errorf("connection replaced: %v -> %v", id, v)
	}
	if v := value(t, s, "GET "+p+"mark"); v != "here" {
		t.Errorf("selected db lost: %#v", v)
	}

	// Without a client id the socket is broken; the next command
	// reconnects and restores the selected database.
	s.id.Store(0)
	if d := cancelled(); d > 3*time.Second {
		t.Errorf("cancel took %v", d)
	}
	if !s.Alive() {
		t.Error("session not alive after a cancel")
	}
	s.DrainMessages()
	if v := value(t, s, "GET "+p+"mark"); v != "here" {
		t.Errorf("selected db lost after reconnect: %#v", v)
	}
	if v := value(t, s, "CLIENT ID"); v == id {
		t.Error("connection was not replaced")
	}
	if !slices.ContainsFunc(s.DrainMessages(), func(m driver.Message) bool { return strings.HasPrefix(m.Text, "Reconnected") }) {
		t.Error("no reconnect message")
	}
}

func TestITServerKill(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	exec(t, s, "SELECT "+strconv.Itoa(itAltDB))
	exec(t, s, "SET "+p+"k v")
	id := value(t, s, "CLIENT ID").(int64)
	cl, _ := c.client(c.defDB)
	if err := cl.ClientKillByFilter(t.Context(), "ID", strconv.FormatInt(id, 10)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(t.Context(), driver.Statement{Text: "GET " + p + "k"}); err == nil ||
		!strings.HasPrefix(err.Error(), "connection lost") {
		t.Fatalf("after kill: %v", err)
	}
	if !s.Alive() {
		t.Error("session should reconnect by itself")
	}
	if v := value(t, s, "GET "+p+"k"); v != "v" {
		t.Errorf("after reconnect: %#v", v)
	}

	// An idle connection is checked before use, so a server-side kill is
	// healed without failing the user's command.
	id = value(t, s, "CLIENT ID").(int64)
	cl.ClientKillByFilter(t.Context(), "ID", strconv.FormatInt(id, 10))
	s.lastUse = time.Now().Add(-2 * idleCheck)
	if v := value(t, s, "GET "+p+"k"); v != "v" {
		t.Errorf("after idle reconnect: %#v", v)
	}
}

func TestITReadOnly(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	rw := itSession(t, itOpen(t, cfg, pw), cfg.Database)
	exec(t, rw, "SET "+p+"k v")
	cfg.ReadOnly = true
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	if v := value(t, s, "GET "+p+"k"); v != "v" {
		t.Errorf("GET = %#v", v)
	}
	for _, line := range []string{"SET " + p + "k x", "DEL " + p + "k", "FLUSHDB", "SOMEMODULE.CMD x"} {
		if _, err := s.Execute(t.Context(), driver.Statement{Text: line}); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: %v", line, err)
		}
	}
	if err := s.SetTTL(t.Context(), p+"k", time.Minute); !errors.Is(err, errReadOnly) {
		t.Errorf("SetTTL = %v", err)
	}
}

func TestITTransactions(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	o := itSession(t, c, cfg.Database)

	if err := s.SetAutoCommit(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "GET "+p+"a")
	if s.InTx() {
		t.Error("a read opened a transaction")
	}
	if v := value(t, s, "SET "+p+"a 1"); v != "QUEUED" || !s.InTx() {
		t.Fatalf("SET in manual mode = %#v, inTx %v", v, s.InTx())
	}
	if v := value(t, o, "GET "+p+"a"); v != nil {
		t.Errorf("uncommitted write visible: %#v", v)
	}
	if err := s.Commit(t.Context()); err != nil || s.InTx() {
		t.Fatalf("commit: %v", err)
	}
	if v := value(t, o, "GET "+p+"a"); v != "1" {
		t.Errorf("committed write = %#v", v)
	}
	exec(t, s, "SET "+p+"a 2")
	if err := s.Rollback(t.Context()); err != nil || s.InTx() {
		t.Fatalf("rollback: %v", err)
	}
	if v := value(t, o, "GET "+p+"a"); v != "1" {
		t.Errorf("rolled back write = %#v", v)
	}
	exec(t, s, "LPUSH "+p+"a x") // WRONGTYPE at EXEC
	if err := s.Commit(t.Context()); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Errorf("commit with a failing command: %v", err)
	}
	if err := s.SetAutoCommit(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	// Typed MULTI/EXEC, with a SELECT queued inside.
	exec(t, s, "MULTI")
	if !s.InTx() {
		t.Error("MULTI not tracked")
	}
	exec(t, s, "SELECT "+strconv.Itoa(itAltDB))
	exec(t, s, "INCR "+p+"a")
	cols, rows := exec(t, s, "EXEC")
	if s.InTx() || !reflect.DeepEqual(cols, []string{"#", "value"}) || len(rows) != 2 || rows[1][1] != int64(1) {
		t.Errorf("EXEC = %q %#v", cols, rows)
	}
	if s.db != itAltDB {
		t.Errorf("SELECT inside MULTI not followed: db %d", s.db)
	}

	// WATCH aborts on a concurrent change.
	exec(t, s, "WATCH "+p+"w")
	exec(t, o, "SELECT "+strconv.Itoa(itAltDB))
	exec(t, o, "SET "+p+"w changed")
	exec(t, s, "MULTI")
	exec(t, s, "SET "+p+"w mine")
	if v := value(t, s, "EXEC"); v != nil {
		t.Errorf("EXEC after a WATCH conflict = %#v", v)
	}
	if !slices.ContainsFunc(s.DrainMessages(), func(m driver.Message) bool { return strings.Contains(m.Text, "WATCH") }) {
		t.Error("no WATCH warning")
	}
	if s.InTx() || s.watching {
		t.Error("transaction state not cleared")
	}
}

func TestITScanContinuation(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	cl, _ := c.client(dbOf(cfg))
	pipe := cl.Pipeline()
	for i := range 2500 {
		pipe.Set(t.Context(), p+"k:"+strconv.Itoa(i), i, 0)
		pipe.HSet(t.Context(), p+"h", "f"+strconv.Itoa(i), i)
	}
	if _, err := pipe.Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, rows := exec(t, s, "SCAN 0 MATCH "+p+"k:* COUNT 100")
	if !reflect.DeepEqual(cols, []string{"key"}) || len(rows) != 2500 || len(distinct(rows)) != 2500 {
		t.Errorf("SCAN = %q, %d rows, %d distinct", cols, len(rows), len(distinct(rows)))
	}
	_, rows = exec(t, s, "HSCAN "+p+"h 0 MATCH f1* COUNT 50")
	if len(rows) != 1111 || len(distinct(rows)) != 1111 {
		t.Errorf("HSCAN = %d rows", len(rows))
	}
	// A later command closes the open scan.
	r, err := s.Execute(t.Context(), driver.Statement{Text: "SCAN 0 MATCH " + p + "k:* COUNT 10"})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, "PING")
	if rows, _ := r.Next(t.Context(), 10); len(rows) != 0 {
		t.Errorf("closed scan returned %d rows", len(rows))
	}
}

func distinct(rows [][]any) map[string]bool {
	m := map[string]bool{}
	for _, r := range rows {
		m[text(r[0])] = true
	}
	return m
}

// view runs the data view of key and returns its RowKey, columns and rows.
func view(t *testing.T, c *conn, s driver.Session, key, filter string) ([]string, []string, [][]any) {
	t.Helper()
	node := &driver.Node{Path: KeyPath(c.defDB, key, ""), Kind: driver.NodeKey}
	q, err := c.DataQuery(node, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	cols, rows := exec(t, s, q)
	rk, err := c.RowKey(t.Context(), node)
	if err != nil {
		t.Fatal(err)
	}
	return rk, cols, rows
}

func apply(t *testing.T, c *conn, key string, changes ...driver.RowChange) error {
	t.Helper()
	return c.Apply(t.Context(), &driver.Node{Path: KeyPath(c.defDB, key, ""), Kind: driver.NodeKey}, changes)
}

func TestITEditing(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	ins := func(v map[string]any) driver.RowChange { return driver.RowChange{Op: driver.OpInsert, Values: v} }
	upd := func(k, v map[string]any) driver.RowChange {
		return driver.RowChange{Op: driver.OpUpdate, Key: k, Values: v}
	}
	del := func(k map[string]any) driver.RowChange { return driver.RowChange{Op: driver.OpDelete, Key: k} }
	type m = map[string]any

	// hash
	exec(t, s, "HSET "+p+"h a 1 b 2 c 3")
	exec(t, s, "EXPIRE "+p+"h 1000")
	if err := apply(t, c, p+"h", upd(m{"field": "a"}, m{"value": "10"}), upd(m{"field": "b"}, m{"field": "bb"}),
		del(m{"field": "c"}), ins(m{"field": "d", "value": "4"})); err != nil {
		t.Fatal(err)
	}
	rk, cols, rows := view(t, c, s, p+"h", "")
	if !reflect.DeepEqual(rk, []string{"field"}) || !reflect.DeepEqual(cols, []string{"field", "value"}) ||
		!reflect.DeepEqual(rows, [][]any{{"a", "10"}, {"bb", "2"}, {"d", "4"}}) {
		t.Errorf("hash = %q %q %#v", rk, cols, rows)
	}
	if ttl := value(t, s, "TTL "+p+"h"); ttl.(int64) <= 0 {
		t.Errorf("edit dropped the TTL: %v", ttl)
	}
	if _, _, rows := view(t, c, s, p+"h", "b*"); !reflect.DeepEqual(rows, [][]any{{"bb", "2"}}) {
		t.Errorf("hash filter = %#v", rows)
	}
	if err := apply(t, c, p+"h", ins(m{"field": "a", "value": "x"})); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate field: %v", err)
	}
	if err := apply(t, c, p+"h", upd(m{"field": "zz"}, m{"value": "x"})); err == nil || !strings.Contains(err.Error(), "change 1") {
		t.Errorf("missing field: %v", err)
	}

	// list: deletes keep displayed indexes stable
	exec(t, s, "RPUSH "+p+"l a b c d")
	if err := apply(t, c, p+"l", del(m{"#": int64(0)}), upd(m{"#": int64(1)}, m{"value": "B"}), del(m{"#": int64(2)}),
		ins(m{"value": "e"})); err != nil {
		t.Fatal(err)
	}
	rk, _, rows = view(t, c, s, p+"l", "")
	if !reflect.DeepEqual(rk, []string{"#"}) || !reflect.DeepEqual(rows, [][]any{{int64(0), "B"}, {int64(1), "d"}, {int64(2), "e"}}) {
		t.Errorf("list = %q %#v", rk, rows)
	}
	if _, _, rows := view(t, c, s, p+"l", "1 1"); !reflect.DeepEqual(rows, [][]any{{int64(1), "d"}}) {
		t.Errorf("list range = %#v", rows)
	}

	// set
	exec(t, s, "SADD "+p+"s a b c")
	if err := apply(t, c, p+"s", del(m{"member": "a"}), upd(m{"member": "b"}, m{"member": "B"}), ins(m{"member": "d"})); err != nil {
		t.Fatal(err)
	}
	rk, cols, rows = view(t, c, s, p+"s", "")
	if !reflect.DeepEqual(rk, []string{"member"}) || !reflect.DeepEqual(cols, []string{"member"}) ||
		!reflect.DeepEqual(rows, [][]any{{"B"}, {"c"}, {"d"}}) {
		t.Errorf("set = %q %q %#v", rk, cols, rows)
	}

	// zset
	exec(t, s, "ZADD "+p+"z 1 a 2 b 3 c")
	if err := apply(t, c, p+"z", upd(m{"member": "a"}, m{"score": 9.5}), upd(m{"member": "b"}, m{"member": "bb"}),
		del(m{"member": "c"}), ins(m{"member": "d"})); err != nil {
		t.Fatal(err)
	}
	rk, cols, rows = view(t, c, s, p+"z", "")
	if !reflect.DeepEqual(rk, []string{"member"}) || !reflect.DeepEqual(cols, []string{"member", "score"}) ||
		!reflect.DeepEqual(rows, [][]any{{"d", 0.0}, {"bb", 2.0}, {"a", 9.5}}) {
		t.Errorf("zset = %q %q %#v", rk, cols, rows)
	}

	// string
	exec(t, s, "SET "+p+"str old EX 1000")
	if err := apply(t, c, p+"str", upd(m{"value": "stale"}, m{"value": "new"})); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("stale string update: %v", err)
	}
	if err := apply(t, c, p+"str", upd(m{"value": "old"}, m{"value": "new"})); err != nil {
		t.Fatal(err)
	}
	rk, _, rows = view(t, c, s, p+"str", "")
	if !reflect.DeepEqual(rk, []string{"value"}) || !reflect.DeepEqual(rows, [][]any{{"new"}}) {
		t.Errorf("string = %q %#v", rk, rows)
	}
	if ttl := value(t, s, "TTL "+p+"str"); ttl.(int64) <= 0 {
		t.Errorf("SET lost the TTL: %v", ttl)
	}

	// stream
	exec(t, s, "XADD "+p+"x 1-0 a 1 b 2")
	exec(t, s, "XADD "+p+"x 2-0 a 3")
	if err := apply(t, c, p+"x", del(m{"id": "1-0"}), ins(m{"id": "3-0", "a": "5", "c": "6", "b": nil})); err != nil {
		t.Fatal(err)
	}
	rk, cols, rows = view(t, c, s, p+"x", "")
	if !reflect.DeepEqual(rk, []string{"id"}) || !reflect.DeepEqual(cols, []string{"id", "a", "c"}) ||
		!reflect.DeepEqual(rows, [][]any{{"2-0", "3", nil}, {"3-0", "5", "6"}}) {
		t.Errorf("stream = %q %q %#v", rk, cols, rows)
	}
	if err := apply(t, c, p+"x", upd(m{"id": "2-0"}, m{"a": "x"})); err == nil {
		t.Error("stream update accepted")
	}
	if err := apply(t, c, p+"x", ins(m{"a": "auto"})); err != nil {
		t.Errorf("XADD *: %v", err)
	}

	// An update of a row that no longer exists is refused.
	if err := apply(t, c, p+"h", upd(m{"field": "nope"}, nil)); err == nil {
		t.Error("missing row accepted")
	}
}

func TestITTTL(t *testing.T) {
	cfg, pw := itConfig(t)
	p := itPrefix(t, cfg, pw)
	c := itOpen(t, cfg, pw)
	s := itSession(t, c, cfg.Database)
	exec(t, s, "SET "+p+"k v")
	var ts TTLSetter = s
	if err := ts.SetTTL(t.Context(), p+"k", 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if ttl := value(t, s, "TTL "+p+"k").(int64); ttl < 85 || ttl > 90 {
		t.Errorf("TTL = %d", ttl)
	}
	if err := ts.SetTTL(t.Context(), p+"k", 0); err != nil {
		t.Fatal(err)
	}
	if ttl := value(t, s, "TTL "+p+"k"); ttl != int64(-1) {
		t.Errorf("TTL after persist = %v", ttl)
	}
	if err := ts.SetTTL(t.Context(), p+"k", 0); err != nil {
		t.Errorf("persist without expiry: %v", err)
	}
	if err := ts.SetTTL(t.Context(), p+"missing", time.Minute); err == nil {
		t.Error("missing key accepted")
	}
	if err := ts.SetTTL(t.Context(), p+"missing", -1); err == nil {
		t.Error("missing key accepted by persist")
	}

	var ks KeyTTLSetter = c
	node := &driver.Node{Path: KeyPath(dbOf(cfg), p+"k", ""), Kind: driver.NodeKey}
	if err := ks.SetKeyTTL(t.Context(), node, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if pttl := value(t, s, "PTTL "+p+"k").(int64); pttl <= 0 || pttl > 1500 {
		t.Errorf("PTTL = %d", pttl)
	}
	if err := ks.SetKeyTTL(t.Context(), &driver.Node{Path: []string{"0"}, Kind: driver.NodeDatabase}, time.Second); err == nil {
		t.Error("TTL on a database accepted")
	}
}
