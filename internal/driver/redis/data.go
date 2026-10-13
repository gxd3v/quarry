package redis

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	_ driver.DataViewer = (*conn)(nil)
	_ driver.Editable   = (*conn)(nil)
	_ TTLSetter         = (*session)(nil)
	_ KeyTTLSetter      = (*conn)(nil)
)

// TTLSetter is implemented by sessions that can change a key's expiry in
// their current database; ttl <= 0 removes the expiry (PERSIST). The UI
// discovers it with a type assertion on driver.Session.
type TTLSetter interface {
	SetTTL(ctx context.Context, key string, ttl time.Duration) error
}

// KeyTTLSetter is the explorer-side variant, for a key node of a
// driver.Conn (the node's Path names the database).
type KeyTTLSetter interface {
	SetKeyTTL(ctx context.Context, target *driver.Node, ttl time.Duration) error
}

const lookupTimeout = 5 * time.Second

// DataQuery builds the command behind a node's data view. It runs on a
// session whose Scope.Database is the node's Path[0].
//
// Key nodes show their value by type, and filter means:
//
//	string  GET key                              (no filter)
//	hash    HGETALL key | HSCAN key 0 MATCH <filter> COUNT 1000
//	list    LRANGE key 0 -1 | LRANGE key <start> [<stop>]   (filter "start [stop]")
//	set     SMEMBERS key | SSCAN key 0 MATCH <filter> COUNT 1000
//	zset    ZRANGE key 0 -1 WITHSCORES | ZSCAN key 0 MATCH <filter> COUNT 1000
//	stream  XRANGE key <start> <end> COUNT 1000   (filter "start [end]" entry IDs)
//	json    JSON.GET key [<filter as a JSONPath>]
//
// orderBy "score DESC" (sorted sets) and "id DESC" (streams) reverse the
// order; other terms are ignored. SCAN-family results keep following their
// cursor as the UI pages through them.
//
// Database and folder nodes run SCAN 0 MATCH <pattern> COUNT 1000, where the
// pattern is the folder's prefix followed by filter (default "*").
//
// The key's type is read from Node.Detail ("hash · ttl 5s", as produced by
// Children); when absent it is looked up with TYPE.
func (c *conn) DataQuery(target *driver.Node, filter string, orderBy []string) (string, error) {
	t, err := c.locate(target)
	if err != nil {
		return "", err
	}
	filter = strings.TrimSpace(filter)
	if t.more {
		return "", errors.New("not a key or folder")
	}
	if !t.isKey {
		return command("SCAN", "0", "MATCH", scanPattern(t.prefix, filter), "COUNT", strconv.Itoa(scanCount)), nil
	}
	typ := ""
	if target.Kind == driver.NodeKey {
		typ = detailType(target.Detail)
	}
	if !viewable(typ) {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		cl, err := c.client(t.db)
		if err != nil {
			return "", err
		}
		if typ, err = lookupType(ctx, cl, t.key); err != nil {
			return "", err
		}
	}
	return dataQuery(typ, t.key, filter, orderBy)
}

func scanPattern(prefix, filter string) string {
	p := globEscape(prefix)
	switch {
	case filter == "":
		return p + "*"
	case strings.HasPrefix(filter, p):
		return filter
	}
	return p + filter
}

func viewable(typ string) bool {
	switch typ {
	case "string", "hash", "list", "set", "zset", "stream", "json":
		return true
	}
	return false
}

func descending(orderBy []string, col string) bool {
	for _, o := range orderBy {
		f := strings.Fields(o)
		if len(f) > 0 && strings.EqualFold(f[0], col) {
			return len(f) > 1 && strings.EqualFold(f[1], "DESC")
		}
	}
	return false
}

func dataQuery(typ, key, filter string, orderBy []string) (string, error) {
	count := strconv.Itoa(scanCount)
	switch typ {
	case "string":
		return command("GET", key), nil
	case "hash":
		if filter != "" {
			return command("HSCAN", key, "0", "MATCH", filter, "COUNT", count), nil
		}
		return command("HGETALL", key), nil
	case "list":
		start, stop, err := bounds(filter, "0", "-1", isInt)
		if err != nil {
			return "", fmt.Errorf("list filter: want a start index and an optional stop index: %w", err)
		}
		return command("LRANGE", key, start, stop), nil
	case "set":
		if filter != "" {
			return command("SSCAN", key, "0", "MATCH", filter, "COUNT", count), nil
		}
		return command("SMEMBERS", key), nil
	case "zset":
		if filter != "" {
			return command("ZSCAN", key, "0", "MATCH", filter, "COUNT", count), nil
		}
		if descending(orderBy, "score") {
			return command("ZREVRANGE", key, "0", "-1", "WITHSCORES"), nil
		}
		return command("ZRANGE", key, "0", "-1", "WITHSCORES"), nil
	case "stream":
		start, end, err := bounds(filter, "-", "+", func(string) bool { return true })
		if err != nil {
			return "", fmt.Errorf("stream filter: want a start ID and an optional end ID: %w", err)
		}
		if descending(orderBy, "id") {
			return command("XREVRANGE", key, end, start, "COUNT", count), nil
		}
		return command("XRANGE", key, start, end, "COUNT", count), nil
	case "json":
		if filter != "" {
			return command("JSON.GET", key, filter), nil
		}
		return command("JSON.GET", key), nil
	case "none", "":
		return "", fmt.Errorf("key %s does not exist", label(key))
	}
	return "", fmt.Errorf("keys of type %s cannot be shown in a data view", typ)
}

// bounds reads a "from [to]" filter.
func bounds(filter, from, to string, valid func(string) bool) (string, string, error) {
	f := strings.Fields(filter)
	switch {
	case len(f) > 2:
		return "", "", errors.New("too many values")
	case len(f) == 2:
		from, to = f[0], f[1]
	case len(f) == 1:
		from = f[0]
	}
	for _, v := range []string{from, to} {
		if !valid(v) {
			return "", "", fmt.Errorf("invalid value %q", v)
		}
	}
	return from, to, nil
}

func isInt(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// rowKeys maps a key type to the columns identifying a row of its data view.
var rowKeys = map[string][]string{
	"string": {"value"},
	"hash":   {"field"},
	"list":   {"#"},
	"set":    {"member"},
	"zset":   {"member"},
	"stream": {"id"},
}

// RowKey returns the row identity of a key's data view: string ["value"]
// (a single row), hash ["field"], list ["#"] (the element index), set and
// sorted set ["member"], stream ["id"]. JSON, module types, databases and
// folders are not editable (nil).
func (c *conn) RowKey(ctx context.Context, target *driver.Node) ([]string, error) {
	t, err := c.locate(target)
	if err != nil {
		return nil, err
	}
	if !t.isKey {
		return nil, nil
	}
	cl, err := c.client(t.db)
	if err != nil {
		return nil, err
	}
	typ, err := editType(ctx, cl, t.key, target)
	if err != nil {
		return nil, err
	}
	return slices.Clone(rowKeys[typ]), nil
}

// editType is the live TYPE, or the node's Detail type when the key does
// not exist (e.g. every element was deleted).
func editType(ctx context.Context, cl goredis.UniversalClient, key string, n *driver.Node) (string, error) {
	typ, err := lookupType(ctx, cl, key)
	if err != nil {
		return "", err
	}
	if typ == "none" {
		typ = detailType(n.Detail)
		if typ == "" {
			return "", fmt.Errorf("key %s does not exist", label(key))
		}
	}
	return typ, nil
}

// Apply saves data view edits of one key atomically: the key is WATCHed,
// changes are validated against its current content, then sent in one
// MULTI/EXEC. Values are strings (or values decoded by this driver); NULL
// is rejected.
//
//   - string: update value: SET … KEEPTTL (the old value must still be
//     there); insert only when the key does not exist; delete removes the
//     key (DEL).
//   - hash: insert {field, value} (HSET; the field must be new); update by
//     field: value and/or field (rename: HDEL + HSET); delete: HDEL.
//   - list: insert {value} appends (RPUSH); update by # (index): LSET;
//     delete by #: LSET to a unique marker, then one LREM of the marker,
//     so indexes always refer to the list as it was displayed.
//   - set: insert SADD; update by member: SREM old + SADD new; delete SREM.
//   - zset: insert {member, score} (ZADD; score defaults to 0); update by
//     member: score and/or member (rename: ZREM + ZADD); delete ZREM.
//   - stream: insert {id (optional, default "*"), field columns…}: XADD
//     with every non-NULL field; delete by id: XDEL. Entries cannot be
//     updated.
//
// A change whose row no longer exists, or a concurrent modification of the
// key, aborts everything.
func (c *conn) Apply(ctx context.Context, target *driver.Node, changes []driver.RowChange) error {
	if c.readOnly {
		return errReadOnly
	}
	t, err := c.locate(target)
	if err != nil {
		return err
	}
	if !t.isKey {
		return errors.New("only keys can be edited")
	}
	if len(changes) == 0 {
		return nil
	}
	cl, err := c.client(t.db)
	if err != nil {
		return err
	}
	typ, err := editType(ctx, cl, t.key, target)
	if err != nil {
		return err
	}
	if rowKeys[typ] == nil {
		return fmt.Errorf("keys of type %s cannot be edited", typ)
	}
	err = cl.Watch(ctx, func(tx *goredis.Tx) error {
		cmds, err := plan(ctx, tx, typ, t.key, changes)
		if err != nil || len(cmds) == 0 {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			for _, args := range cmds {
				p.Do(ctx, args...)
			}
			return nil
		})
		return err
	}, t.key)
	if errors.Is(err, goredis.TxFailedErr) {
		return fmt.Errorf("key %s was modified while saving; no changes were saved", label(t.key))
	}
	return queryError(err)
}

// rowSet tracks which members exist while a batch of changes is planned.
type rowSet struct {
	added, removed map[string]bool
	check          func(string) (bool, error)
}

func newRowSet(check func(string) (bool, error)) *rowSet {
	return &rowSet{added: map[string]bool{}, removed: map[string]bool{}, check: check}
}

func (r *rowSet) has(x string) (bool, error) {
	switch {
	case r.added[x]:
		return true, nil
	case r.removed[x]:
		return false, nil
	}
	return r.check(x)
}

func (r *rowSet) add(x string) { r.added[x] = true; delete(r.removed, x) }
func (r *rowSet) del(x string) { r.removed[x] = true; delete(r.added, x) }

// planner turns data view changes into write commands, reading the key's
// current content through rd.
type planner struct {
	ctx    context.Context
	rd     goredis.Cmdable
	key    string
	rows   *rowSet
	cmds   [][]any
	tail   [][]any // list appends run after the LSETs and the LREM
	marker string
	llen   int64
}

func plan(ctx context.Context, rd goredis.Cmdable, typ, key string, changes []driver.RowChange) ([][]any, error) {
	p := &planner{ctx: ctx, rd: rd, key: key, llen: -1}
	switch typ {
	case "hash":
		p.rows = newRowSet(func(f string) (bool, error) { return rd.HExists(ctx, key, f).Result() })
	case "set":
		p.rows = newRowSet(func(m string) (bool, error) { return rd.SIsMember(ctx, key, m).Result() })
	case "zset":
		p.rows = newRowSet(func(m string) (bool, error) {
			err := rd.ZScore(ctx, key, m).Err()
			if errors.Is(err, goredis.Nil) {
				return false, nil
			}
			return err == nil, err
		})
	case "string":
		p.rows = newRowSet(func(string) (bool, error) {
			n, err := rd.Exists(ctx, key).Result()
			return n > 0, err
		})
	}
	for i, ch := range changes {
		if err := p.change(typ, ch); err != nil {
			if _, ok := errors.AsType[changeError](err); ok {
				return nil, fmt.Errorf("change %d: %w", i+1, err)
			}
			return nil, err
		}
	}
	if p.marker != "" {
		p.cmds = append(p.cmds, []any{"LREM", key, 0, p.marker})
	}
	return append(p.cmds, p.tail...), nil
}

// changeError is a change that cannot be applied, as opposed to a failure
// reading the key.
type changeError struct{ msg string }

func (e changeError) Error() string { return e.msg }

func bad(format string, a ...any) error { return changeError{fmt.Sprintf(format, a...)} }

func (p *planner) emit(args ...any) { p.cmds = append(p.cmds, args) }

func (p *planner) change(typ string, ch driver.RowChange) error {
	if ch.Op != driver.OpInsert && ch.Op != driver.OpUpdate && ch.Op != driver.OpDelete {
		return bad("unknown operation %d", ch.Op)
	}
	if err := knownColumns(typ, ch); err != nil {
		return err
	}
	switch typ {
	case "hash":
		return p.hash(ch)
	case "list":
		return p.list(ch)
	case "set":
		return p.set(ch)
	case "zset":
		return p.zset(ch)
	case "string":
		return p.str(ch)
	case "stream":
		return p.stream(ch)
	}
	return bad("keys of type %s cannot be edited", typ)
}

// mustHave and mustLack check row existence.
func (p *planner) mustHave(x, what string) error {
	ok, err := p.rows.has(x)
	if err == nil && !ok {
		return bad("%s %s no longer exists", what, label(x))
	}
	return err
}

func (p *planner) mustLack(x, what string) error {
	ok, err := p.rows.has(x)
	if err == nil && ok {
		return bad("%s %s already exists", what, label(x))
	}
	return err
}

func (p *planner) hash(ch driver.RowChange) error {
	if ch.Op == driver.OpInsert {
		f, err := need(ch.Values, "field")
		if err != nil {
			return err
		}
		v, err := optional(ch.Values, "value", "")
		if err != nil {
			return err
		}
		if err := p.mustLack(f, "field"); err != nil {
			return err
		}
		p.rows.add(f)
		p.emit("HSET", p.key, f, v)
		return nil
	}
	f, err := need(ch.Key, "field")
	if err != nil {
		return err
	}
	if err := p.mustHave(f, "field"); err != nil {
		return err
	}
	if ch.Op == driver.OpDelete {
		p.rows.del(f)
		p.emit("HDEL", p.key, f)
		return nil
	}
	nf, err := optional(ch.Values, "field", f)
	if err != nil {
		return err
	}
	_, hasValue := ch.Values["value"]
	if nf == f {
		if hasValue {
			v, err := need(ch.Values, "value")
			if err != nil {
				return err
			}
			p.emit("HSET", p.key, f, v)
		}
		return nil
	}
	v, err := optional(ch.Values, "value", "")
	if err != nil {
		return err
	}
	if !hasValue {
		if v, err = p.rd.HGet(p.ctx, p.key, f).Result(); err != nil {
			return err
		}
	}
	if err := p.mustLack(nf, "field"); err != nil {
		return err
	}
	p.rows.del(f)
	p.rows.add(nf)
	p.emit("HDEL", p.key, f)
	p.emit("HSET", p.key, nf, v)
	return nil
}

func (p *planner) list(ch driver.RowChange) error {
	if p.llen < 0 {
		n, err := p.rd.LLen(p.ctx, p.key).Result()
		if err != nil {
			return err
		}
		p.llen = n
	}
	if ch.Op == driver.OpInsert {
		v, err := need(ch.Values, "value")
		if err != nil {
			return err
		}
		p.tail = append(p.tail, []any{"RPUSH", p.key, v})
		return nil
	}
	idx, err := index(ch.Key["#"])
	if err != nil {
		return err
	}
	if idx < 0 || idx >= p.llen {
		return bad("element #%d no longer exists (the list has %d elements)", idx, p.llen)
	}
	if ch.Op == driver.OpDelete {
		if p.marker == "" {
			p.marker = deleteMarker()
		}
		p.emit("LSET", p.key, idx, p.marker)
		return nil
	}
	if _, ok := ch.Values["value"]; !ok {
		return nil
	}
	v, err := need(ch.Values, "value")
	if err != nil {
		return err
	}
	p.emit("LSET", p.key, idx, v)
	return nil
}

func (p *planner) set(ch driver.RowChange) error {
	if ch.Op == driver.OpInsert {
		m, err := need(ch.Values, "member")
		if err != nil {
			return err
		}
		if err := p.mustLack(m, "member"); err != nil {
			return err
		}
		p.rows.add(m)
		p.emit("SADD", p.key, m)
		return nil
	}
	old, err := need(ch.Key, "member")
	if err != nil {
		return err
	}
	if err := p.mustHave(old, "member"); err != nil {
		return err
	}
	if ch.Op == driver.OpDelete {
		p.rows.del(old)
		p.emit("SREM", p.key, old)
		return nil
	}
	m, err := optional(ch.Values, "member", old)
	if err != nil || m == old {
		return err
	}
	if err := p.mustLack(m, "member"); err != nil {
		return err
	}
	p.rows.del(old)
	p.rows.add(m)
	p.emit("SREM", p.key, old)
	p.emit("SADD", p.key, m)
	return nil
}

func (p *planner) zset(ch driver.RowChange) error {
	if ch.Op == driver.OpInsert {
		m, err := need(ch.Values, "member")
		if err != nil {
			return err
		}
		s, err := optional(ch.Values, "score", "0")
		if err != nil {
			return err
		}
		if err := checkScore(s); err != nil {
			return err
		}
		if err := p.mustLack(m, "member"); err != nil {
			return err
		}
		p.rows.add(m)
		p.emit("ZADD", p.key, s, m)
		return nil
	}
	m, err := need(ch.Key, "member")
	if err != nil {
		return err
	}
	if err := p.mustHave(m, "member"); err != nil {
		return err
	}
	if ch.Op == driver.OpDelete {
		p.rows.del(m)
		p.emit("ZREM", p.key, m)
		return nil
	}
	nm, err := optional(ch.Values, "member", m)
	if err != nil {
		return err
	}
	_, hasScore := ch.Values["score"]
	s, err := optional(ch.Values, "score", "")
	if err != nil {
		return err
	}
	if hasScore {
		if err := checkScore(s); err != nil {
			return err
		}
	}
	if nm == m {
		if hasScore {
			p.emit("ZADD", p.key, "XX", s, m)
		}
		return nil
	}
	if !hasScore {
		f, err := p.rd.ZScore(p.ctx, p.key, m).Result()
		if err != nil {
			return err
		}
		s = strconv.FormatFloat(f, 'g', -1, 64)
	}
	if err := p.mustLack(nm, "member"); err != nil {
		return err
	}
	p.rows.del(m)
	p.rows.add(nm)
	p.emit("ZREM", p.key, m)
	p.emit("ZADD", p.key, s, nm)
	return nil
}

func (p *planner) str(ch driver.RowChange) error {
	switch ch.Op {
	case driver.OpInsert:
		v, err := need(ch.Values, "value")
		if err != nil {
			return err
		}
		if ok, err := p.rows.has(""); err != nil {
			return err
		} else if ok {
			return bad("a string key holds a single value: edit it instead of adding a row")
		}
		p.rows.add("")
		p.emit("SET", p.key, v)
	case driver.OpUpdate:
		if _, ok := ch.Values["value"]; !ok {
			return nil
		}
		v, err := need(ch.Values, "value")
		if err != nil {
			return err
		}
		if err := p.sameString(ch.Key); err != nil {
			return err
		}
		p.emit("SET", p.key, v, "KEEPTTL")
	case driver.OpDelete:
		if err := p.sameString(ch.Key); err != nil {
			return err
		}
		p.rows.del("")
		p.emit("DEL", p.key)
	}
	return nil
}

// sameString checks the string still holds the displayed value (when the
// change carries it), so an edit never overwrites someone else's.
func (p *planner) sameString(key map[string]any) error {
	if ok, err := p.rows.has(""); err != nil {
		return err
	} else if !ok {
		return bad("key %s no longer exists", label(p.key))
	}
	if _, ok := key["value"]; !ok || len(p.rows.added) > 0 {
		return nil
	}
	old, err := need(key, "value")
	if err != nil {
		return err
	}
	cur, err := p.rd.Get(p.ctx, p.key).Result()
	if err != nil {
		return err
	}
	if cur != old {
		return bad("the value of %s changed since it was loaded", label(p.key))
	}
	return nil
}

func (p *planner) stream(ch driver.RowChange) error {
	switch ch.Op {
	case driver.OpUpdate:
		return bad("stream entries cannot be modified: delete the entry and add a new one")
	case driver.OpDelete:
		id, err := need(ch.Key, "id")
		if err != nil {
			return err
		}
		msgs, err := p.rd.XRange(p.ctx, p.key, id, id).Result()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return bad("entry %s no longer exists", id)
		}
		p.emit("XDEL", p.key, id)
		return nil
	}
	id := "*"
	if v, ok := ch.Values["id"]; ok && v != nil {
		s, err := argText(v, "id")
		if err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s != "" {
			id = s
		}
	}
	var names []string
	for col, v := range ch.Values {
		if col != "id" && v != nil {
			names = append(names, col)
		}
	}
	if len(names) == 0 {
		return bad("a stream entry needs at least one field")
	}
	slices.Sort(names)
	args := []any{"XADD", p.key, id}
	for _, col := range names {
		v, err := argText(ch.Values[col], col)
		if err != nil {
			return err
		}
		args = append(args, strings.TrimSuffix(col, fieldSuffix), v)
	}
	p.emit(args...)
	return nil
}

// editColumns lists the columns of each editable type; streams take any.
var editColumns = map[string][]string{
	"hash":   {"field", "value"},
	"list":   {"#", "value"},
	"set":    {"member"},
	"zset":   {"member", "score"},
	"string": {"value"},
}

func knownColumns(typ string, ch driver.RowChange) error {
	cols, ok := editColumns[typ]
	if !ok {
		return nil
	}
	for _, m := range []map[string]any{ch.Key, ch.Values} {
		for k := range m {
			if !slices.Contains(cols, k) {
				return bad("unknown column %q", k)
			}
		}
	}
	return nil
}

func need(m map[string]any, col string) (string, error) {
	v, ok := m[col]
	if !ok {
		return "", bad("missing %s", col)
	}
	return argText(v, col)
}

func optional(m map[string]any, col, def string) (string, error) {
	v, ok := m[col]
	if !ok {
		return def, nil
	}
	return argText(v, col)
}

// argText renders a cell value as a command argument.
func argText(v any, col string) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", bad("%s cannot be NULL: Redis has no NULL values", col)
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case driver.Number:
		return string(x), nil
	case driver.JSON:
		return string(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case int:
		return strconv.Itoa(x), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), nil
	case bool:
		return strconv.FormatBool(x), nil
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	case fmt.Stringer:
		return x.String(), nil
	}
	return "", bad("%s: unsupported value type %T", col, v)
}

func index(v any) (int64, error) {
	s, err := argText(v, "#")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, bad("invalid list index %q", s)
	}
	return n, nil
}

func checkScore(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "inf", "+inf", "-inf":
		return nil
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		return bad("invalid score %q: want a number", s)
	}
	return nil
}

func deleteMarker() string {
	return "\x00quarry-deleted:" + rand.Text()
}

// SetKeyTTL implements KeyTTLSetter: target is a key node; ttl is rounded
// to milliseconds and ttl <= 0 removes the expiry (PERSIST).
func (c *conn) SetKeyTTL(ctx context.Context, target *driver.Node, ttl time.Duration) error {
	if c.readOnly {
		return errReadOnly
	}
	t, err := c.locate(target)
	if err != nil {
		return err
	}
	if !t.isKey {
		return errors.New("only keys have a TTL")
	}
	cl, err := c.client(t.db)
	if err != nil {
		return err
	}
	return setTTL(ctx, func(ctx context.Context, args []string) (any, error) {
		a := make([]any, len(args))
		for i, x := range args {
			a[i] = x
		}
		v, err := cl.Do(ctx, a...).Result()
		return v, queryError(err)
	}, t.key, ttl)
}

// setTTL sends PEXPIRE (or PERSIST) through run and reports a missing key.
func setTTL(ctx context.Context, run func(context.Context, []string) (any, error), key string, ttl time.Duration) error {
	args := []string{"PERSIST", key}
	if ttl > 0 {
		args = []string{"PEXPIRE", key, strconv.FormatInt(max(ttl.Milliseconds(), 1), 10)}
	}
	v, err := run(ctx, args)
	if err != nil {
		return err
	}
	if n, _ := v.(int64); n == 1 {
		return nil
	}
	if args[0] == "PERSIST" {
		// 0 also means "had no expiry".
		v, err := run(ctx, []string{"EXISTS", key})
		if err != nil {
			return err
		}
		if n, _ := v.(int64); n == 1 {
			return nil
		}
	}
	return fmt.Errorf("key %s does not exist", label(key))
}
