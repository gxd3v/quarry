package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	_ TTLSetter         = (*conn)(nil)
)

// TTLSetter is implemented by connections that can change a key's expiry;
// the UI discovers it with a type assertion on driver.Conn.
type TTLSetter interface {
	// SetTTL makes target expire in seconds (> 0); seconds < 0 removes the
	// expiry (PERSIST). 0 is rejected, since EXPIRE 0 deletes the key.
	SetTTL(ctx context.Context, target *driver.Node, seconds int64) error
}

const lookupTimeout = 5 * time.Second

// normType maps TYPE replies and Detail types to the driver's names.
func normType(t string) string {
	switch t {
	case "ReJSON-RL", "json":
		return "json"
	}
	return t
}

// lookupType runs TYPE; a key that does not exist reports "none".
func (c *conn) lookupType(ctx context.Context, db int, key string) (string, error) {
	cl, err := c.client(db)
	if err != nil {
		return "", err
	}
	t, err := cl.Type(ctx, key).Result()
	if err != nil {
		return "", queryError(err)
	}
	return normType(t), nil
}

// DataQuery builds the command behind a node's data view.
//
// Key nodes show their whole value by type: GET, HGETALL, LRANGE 0 -1,
// SMEMBERS, ZRANGE 0 -1 WITHSCORES, XRANGE - + COUNT 1000, JSON.GET. For
// hashes, sets and sorted sets a filter is a MATCH pattern
// (HSCAN/SSCAN/ZSCAN key 0 MATCH filter COUNT 1000); for JSON keys it is a
// JSONPath. orderBy "score DESC" (sorted sets) and "id DESC" (streams)
// reverse the order; other sort terms are ignored (sort client-side).
//
// Database and folder nodes run SCAN 0 MATCH <filter or prefix*> COUNT 1000.
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
		match := filter
		if match == "" {
			match = globEscape(t.prefix) + "*"
		}
		return command("SCAN", "0", "MATCH", match, "COUNT", strconv.Itoa(scanCount)), nil
	}
	typ := ""
	if target.Kind == driver.NodeKey {
		typ = normType(detailType(target.Detail))
	}
	if !viewable(typ) {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		if typ, err = c.lookupType(ctx, t.db, t.key); err != nil {
			return "", err
		}
	}
	return dataQuery(typ, t.key, filter, orderBy)
}

func viewable(typ string) bool {
	switch typ {
	case "string", "hash", "list", "set", "zset", "stream", "json":
		return true
	}
	return false
}

func descending(orderBy []string, col string) (desc, ok bool) {
	for _, o := range orderBy {
		f := strings.Fields(o)
		if len(f) == 0 || !strings.EqualFold(f[0], col) {
			continue
		}
		return len(f) > 1 && strings.EqualFold(f[1], "DESC"), true
	}
	return false, false
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
		return command("LRANGE", key, "0", "-1"), nil
	case "set":
		if filter != "" {
			return command("SSCAN", key, "0", "MATCH", filter, "COUNT", count), nil
		}
		return command("SMEMBERS", key), nil
	case "zset":
		if filter != "" {
			return command("ZSCAN", key, "0", "MATCH", filter, "COUNT", count), nil
		}
		if desc, _ := descending(orderBy, "score"); desc {
			return command("ZREVRANGE", key, "0", "-1", "WITHSCORES"), nil
		}
		return command("ZRANGE", key, "0", "-1", "WITHSCORES"), nil
	case "stream":
		if desc, _ := descending(orderBy, "id"); desc {
			return command("XREVRANGE", key, "+", "-", "COUNT", count), nil
		}
		return command("XRANGE", key, "-", "+", "COUNT", count), nil
	case "json":
		if filter != "" {
			return command("JSON.GET", key, filter), nil
		}
		return command("JSON.GET", key), nil
	case "none", "":
		return "", fmt.Errorf("key %q does not exist", key)
	}
	return "", fmt.Errorf("keys of type %s cannot be shown in a data view", typ)
}

// rowKeys maps a key type to the columns identifying a row of its data view.
var rowKeys = map[string][]string{
	"hash":   {"field"},
	"list":   {"#"},
	"set":    {"value"},
	"zset":   {"member"},
	"string": {"value"},
}

// RowKey returns the row identity of a key's data view: hash ["field"],
// list ["#"] (the element index), set ["value"], sorted set ["member"],
// string ["value"] (a single row). Streams, JSON and other types are not
// editable (nil).
func (c *conn) RowKey(ctx context.Context, target *driver.Node) ([]string, error) {
	t, err := c.locate(target)
	if err != nil {
		return nil, err
	}
	if !t.isKey {
		return nil, nil
	}
	typ, err := c.editType(ctx, t, target)
	if err != nil {
		return nil, err
	}
	return slices.Clone(rowKeys[typ]), nil
}

// editType is the live TYPE, or the node's Detail type when the key does
// not exist (e.g. every element was deleted).
func (c *conn) editType(ctx context.Context, t target, n *driver.Node) (string, error) {
	typ, err := c.lookupType(ctx, t.db, t.key)
	if err != nil {
		return "", err
	}
	if typ == "none" {
		typ = normType(detailType(n.Detail))
		if typ == "" {
			return "", fmt.Errorf("key %q does not exist", t.key)
		}
	}
	return typ, nil
}

// Apply saves data view edits of one key atomically: the key is WATCHed,
// changes are validated against its current content, then sent in one
// MULTI/EXEC. Values are strings (or values decoded by this driver); NULL
// is rejected.
//
//   - hash: insert {field, value} (HSET; the field must be new); update by
//     field: value and/or field (rename: HDEL + HSET); delete: HDEL.
//   - list: insert {value} appends (RPUSH); update by # (index): LSET;
//     delete by #: LSET to a unique marker, then one LREM of the marker,
//     so indexes always refer to the list as it was displayed.
//   - set: insert SADD; update by value: SREM old + SADD new; delete SREM.
//   - zset: insert {member, score} (ZADD; score defaults to 0); update by
//     member: score and/or member (rename: ZREM + ZADD); delete ZREM.
//   - string: update value: SET … KEEPTTL; insert only when the key does
//     not exist; delete removes the key (DEL).
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
	typ, err := c.editType(ctx, t, target)
	if err != nil {
		return err
	}
	if rowKeys[typ] == nil {
		return fmt.Errorf("keys of type %s cannot be edited", typ)
	}
	err = cl.Watch(ctx, func(tx *goredis.Tx) error {
		cmds, err := plan(ctx, tx, typ, t.key, changes)
		if err != nil {
			return err
		}
		if len(cmds) == 0 {
			return nil
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
		return fmt.Errorf("key %q was modified while saving; no changes were saved", t.key)
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

// plan validates changes against the key's content (read through rd) and
// returns the write commands to send.
func plan(ctx context.Context, rd goredis.Cmdable, typ, key string, changes []driver.RowChange) ([][]any, error) {
	var (
		cmds   [][]any
		tail   [][]any // list deletes and appends run after the LSETs
		marker string
		llen   int64 = -1
	)
	emit := func(args ...any) { cmds = append(cmds, args) }
	exists := func(cmd *goredis.IntCmd) (bool, error) {
		n, err := cmd.Result()
		return n > 0, err
	}
	var rows *rowSet
	switch typ {
	case "hash":
		rows = newRowSet(func(f string) (bool, error) { return rd.HExists(ctx, key, f).Result() })
	case "set":
		rows = newRowSet(func(m string) (bool, error) { return rd.SIsMember(ctx, key, m).Result() })
	case "zset":
		rows = newRowSet(func(m string) (bool, error) {
			err := rd.ZScore(ctx, key, m).Err()
			if err == goredis.Nil {
				return false, nil
			}
			return err == nil, err
		})
	case "string":
		rows = newRowSet(func(string) (bool, error) { return exists(rd.Exists(ctx, key)) })
	}

	for i, ch := range changes {
		fail := func(format string, a ...any) error {
			return fmt.Errorf("change %d: %s", i+1, fmt.Sprintf(format, a...))
		}
		if err := knownColumns(typ, ch); err != nil {
			return nil, fail("%v", err)
		}
		switch typ {
		case "hash":
			switch ch.Op {
			case driver.OpInsert:
				f, err := need(ch.Values, "field")
				if err != nil {
					return nil, fail("%v", err)
				}
				v, err := optional(ch.Values, "value", "")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(f); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("field %q already exists", f)
				}
				rows.add(f)
				emit("HSET", key, f, v)
			case driver.OpUpdate, driver.OpDelete:
				f, err := need(ch.Key, "field")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(f); err != nil {
					return nil, err
				} else if !ok {
					return nil, fail("field %q no longer exists", f)
				}
				if ch.Op == driver.OpDelete {
					rows.del(f)
					emit("HDEL", key, f)
					continue
				}
				nf, renamed := ch.Values["field"]
				_, hasValue := ch.Values["value"]
				newName, err := optional(ch.Values, "field", f)
				if err != nil {
					return nil, fail("%v", err)
				}
				if renamed && nf != nil && newName != f {
					value, err := optional(ch.Values, "value", "")
					if err != nil {
						return nil, fail("%v", err)
					}
					if !hasValue {
						if value, err = rd.HGet(ctx, key, f).Result(); err != nil {
							return nil, err
						}
					}
					if ok, err := rows.has(newName); err != nil {
						return nil, err
					} else if ok {
						return nil, fail("field %q already exists", newName)
					}
					rows.del(f)
					rows.add(newName)
					emit("HDEL", key, f)
					emit("HSET", key, newName, value)
				} else if hasValue {
					v, err := need(ch.Values, "value")
					if err != nil {
						return nil, fail("%v", err)
					}
					emit("HSET", key, f, v)
				}
			}

		case "list":
			if llen < 0 {
				n, err := rd.LLen(ctx, key).Result()
				if err != nil {
					return nil, err
				}
				llen = n
			}
			switch ch.Op {
			case driver.OpInsert:
				v, err := need(ch.Values, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				tail = append(tail, []any{"RPUSH", key, v})
			case driver.OpUpdate, driver.OpDelete:
				idx, err := index(ch.Key["#"])
				if err != nil {
					return nil, fail("%v", err)
				}
				if idx < 0 || idx >= llen {
					return nil, fail("element #%d no longer exists (the list has %d elements)", idx, llen)
				}
				if ch.Op == driver.OpDelete {
					if marker == "" {
						marker = deleteMarker()
					}
					emit("LSET", key, idx, marker)
					continue
				}
				if _, ok := ch.Values["value"]; !ok {
					continue
				}
				v, err := need(ch.Values, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				emit("LSET", key, idx, v)
			}

		case "set":
			switch ch.Op {
			case driver.OpInsert:
				v, err := need(ch.Values, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(v); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("%q is already in the set", v)
				}
				rows.add(v)
				emit("SADD", key, v)
			case driver.OpUpdate, driver.OpDelete:
				old, err := need(ch.Key, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(old); err != nil {
					return nil, err
				} else if !ok {
					return nil, fail("%q is no longer in the set", old)
				}
				if ch.Op == driver.OpDelete {
					rows.del(old)
					emit("SREM", key, old)
					continue
				}
				v, err := optional(ch.Values, "value", old)
				if err != nil {
					return nil, fail("%v", err)
				}
				if v == old {
					continue
				}
				if ok, err := rows.has(v); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("%q is already in the set", v)
				}
				rows.del(old)
				rows.add(v)
				emit("SREM", key, old)
				emit("SADD", key, v)
			}

		case "zset":
			switch ch.Op {
			case driver.OpInsert:
				m, err := need(ch.Values, "member")
				if err != nil {
					return nil, fail("%v", err)
				}
				s, err := optional(ch.Values, "score", "0")
				if err != nil {
					return nil, fail("%v", err)
				}
				if err := checkScore(s); err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(m); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("member %q already exists", m)
				}
				rows.add(m)
				emit("ZADD", key, s, m)
			case driver.OpUpdate, driver.OpDelete:
				m, err := need(ch.Key, "member")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(m); err != nil {
					return nil, err
				} else if !ok {
					return nil, fail("member %q no longer exists", m)
				}
				if ch.Op == driver.OpDelete {
					rows.del(m)
					emit("ZREM", key, m)
					continue
				}
				nm, err := optional(ch.Values, "member", m)
				if err != nil {
					return nil, fail("%v", err)
				}
				_, hasScore := ch.Values["score"]
				s, err := optional(ch.Values, "score", "")
				if err != nil {
					return nil, fail("%v", err)
				}
				if hasScore {
					if err := checkScore(s); err != nil {
						return nil, fail("%v", err)
					}
				}
				if nm == m {
					if hasScore {
						emit("ZADD", key, "XX", s, m)
					}
					continue
				}
				if !hasScore {
					f, err := rd.ZScore(ctx, key, m).Result()
					if err != nil {
						return nil, err
					}
					s = strconv.FormatFloat(f, 'g', -1, 64)
				}
				if ok, err := rows.has(nm); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("member %q already exists", nm)
				}
				rows.del(m)
				rows.add(nm)
				emit("ZREM", key, m)
				emit("ZADD", key, s, nm)
			}

		case "string":
			switch ch.Op {
			case driver.OpInsert:
				v, err := need(ch.Values, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(""); err != nil {
					return nil, err
				} else if ok {
					return nil, fail("a string key holds a single value: edit it instead of adding a row")
				}
				rows.add("")
				emit("SET", key, v)
			case driver.OpUpdate:
				if _, ok := ch.Values["value"]; !ok {
					continue
				}
				v, err := need(ch.Values, "value")
				if err != nil {
					return nil, fail("%v", err)
				}
				if ok, err := rows.has(""); err != nil {
					return nil, err
				} else if !ok {
					return nil, fail("key %q no longer exists", key)
				}
				emit("SET", key, v, "KEEPTTL")
			case driver.OpDelete:
				rows.del("")
				emit("DEL", key)
			}
		}
		if ch.Op != driver.OpInsert && ch.Op != driver.OpUpdate && ch.Op != driver.OpDelete {
			return nil, fail("unknown operation %d", ch.Op)
		}
	}
	if marker != "" {
		cmds = append(cmds, []any{"LREM", key, 0, marker})
	}
	return append(cmds, tail...), nil
}

// columns per key type, for validating change maps.
var editColumns = map[string][]string{
	"hash":   {"field", "value"},
	"list":   {"#", "value"},
	"set":    {"value"},
	"zset":   {"member", "score"},
	"string": {"value"},
}

func knownColumns(typ string, ch driver.RowChange) error {
	cols := editColumns[typ]
	for _, m := range []map[string]any{ch.Key, ch.Values} {
		for k := range m {
			if !slices.Contains(cols, k) {
				return fmt.Errorf("unknown column %q", k)
			}
		}
	}
	return nil
}

func need(m map[string]any, col string) (string, error) {
	v, ok := m[col]
	if !ok {
		return "", fmt.Errorf("missing %s", col)
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
		return "", fmt.Errorf("%s cannot be NULL: Redis has no NULL values", col)
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
	return "", fmt.Errorf("%s: unsupported value type %T", col, v)
}

func index(v any) (int64, error) {
	s, err := argText(v, "#")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid list index %q", s)
	}
	return n, nil
}

func checkScore(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "inf", "+inf", "-inf":
		return nil
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		return fmt.Errorf("invalid score %q: want a number", s)
	}
	return nil
}

func deleteMarker() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "\x00quarry-deleted:" + hex.EncodeToString(b)
}

// SetTTL implements TTLSetter.
func (c *conn) SetTTL(ctx context.Context, target *driver.Node, seconds int64) error {
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
	if seconds == 0 {
		return errors.New("TTL must be positive (a negative value removes the expiry)")
	}
	cl, err := c.client(t.db)
	if err != nil {
		return err
	}
	var ok bool
	if seconds < 0 {
		n, err := cl.Exists(ctx, t.key).Result()
		if err != nil {
			return queryError(err)
		}
		if n == 0 {
			return fmt.Errorf("key %q does not exist", t.key)
		}
		if err := cl.Persist(ctx, t.key).Err(); err != nil {
			return queryError(err)
		}
		return nil
	}
	ok, err = cl.Expire(ctx, t.key, time.Duration(seconds)*time.Second).Result()
	if err != nil {
		return queryError(err)
	}
	if !ok {
		return fmt.Errorf("key %q does not exist", t.key)
	}
	return nil
}
