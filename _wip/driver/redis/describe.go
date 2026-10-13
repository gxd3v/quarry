package redis

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Describer = (*conn)(nil)

const (
	ddlCap   = 10_000 // elements exported to DDL
	ddlChunk = 100    // elements per generated command
)

// Describe returns what the object viewer shows. For a key: Stats (type,
// TTL, memory usage, encoding, length), the data view's Columns and, as DDL,
// a command sequence that recreates the key (SET / HSET / RPUSH / SADD /
// ZADD / XADD / JSON.SET, then EXPIRE); at most 10,000 elements are
// exported. For a database: its key counts. For a folder: its prefix and
// key count.
func (c *conn) Describe(ctx context.Context, target *driver.Node) (*driver.ObjectDetails, error) {
	t, err := c.locate(target)
	if err != nil {
		return nil, err
	}
	if t.more {
		return nil, errors.New("not a key, folder or database")
	}
	cl, err := c.client(t.db)
	if err != nil {
		return nil, err
	}
	if !t.isKey {
		return c.describePrefix(ctx, cl, t)
	}
	typ, err := c.lookupType(ctx, t.db, t.key)
	if err != nil {
		return nil, err
	}
	if typ == "none" {
		return nil, fmt.Errorf("key %q does not exist", t.key)
	}
	d := &driver.ObjectDetails{}
	add := func(name, def string) { d.Stats = append(d.Stats, driver.DetailRow{Name: name, Definition: def}) }
	add("Key", t.key)
	add("Type", typ)
	ttl, err := cl.Do(ctx, "TTL", t.key).Int64()
	if err != nil {
		return nil, queryError(err)
	}
	if ttl >= 0 {
		add("TTL", fmt.Sprintf("%d s", ttl))
	} else {
		add("TTL", "no expiry")
	}
	if n, err := cl.MemoryUsage(ctx, t.key).Result(); err == nil {
		add("Memory usage", size(n))
	}
	if enc, err := cl.ObjectEncoding(ctx, t.key).Result(); err == nil {
		add("Encoding", enc)
	}
	if label, cmd := lengthCmd(typ); cmd != "" {
		if n, err := cl.Do(ctx, cmd, t.key).Int64(); err == nil {
			add(label, strconv.FormatInt(n, 10))
		}
	}
	d.Columns = columnInfo(typ)
	ddl, err := recreate(ctx, cl, typ, t.key)
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		ddl += command("EXPIRE", t.key, strconv.FormatInt(ttl, 10)) + "\n"
	}
	d.DDL = ddl
	return d, nil
}

func (c *conn) describePrefix(ctx context.Context, cl goredis.UniversalClient, t target) (*driver.ObjectDetails, error) {
	d := &driver.ObjectDetails{}
	add := func(name, def string) { d.Stats = append(d.Stats, driver.DetailRow{Name: name, Definition: def}) }
	if t.prefix == "" {
		add("Database", "db"+strconv.Itoa(t.db))
		n, err := cl.DBSize(ctx).Result()
		if err != nil {
			return nil, queryError(err)
		}
		add("Keys", strconv.FormatInt(n, 10))
		if !c.cluster {
			if info, err := cl.Info(ctx, "keyspace").Result(); err == nil {
				for _, line := range strings.Fields(info) {
					name, rest, _ := strings.Cut(line, ":")
					if name != "db"+strconv.Itoa(t.db) {
						continue
					}
					for _, kv := range strings.Split(rest, ",") {
						switch k, v, _ := strings.Cut(kv, "="); k {
						case "expires":
							add("Keys with expiry", v)
						case "avg_ttl":
							add("Average TTL", v+" ms")
						}
					}
				}
			}
		}
		return d, nil
	}
	keys, more, err := c.scan(ctx, cl, globEscape(t.prefix)+"*", levelCap)
	if err != nil {
		return nil, err
	}
	add("Prefix", t.prefix)
	n := strconv.Itoa(len(keys))
	if more {
		n = "more than " + n
	}
	add("Keys", n)
	return d, nil
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB (%d bytes)", float64(n)/(1<<20), n)
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB (%d bytes)", float64(n)/(1<<10), n)
	}
	return fmt.Sprintf("%d bytes", n)
}

func lengthCmd(typ string) (label, cmd string) {
	switch typ {
	case "string":
		return "Length (bytes)", "STRLEN"
	case "hash":
		return "Fields", "HLEN"
	case "list":
		return "Elements", "LLEN"
	case "set":
		return "Members", "SCARD"
	case "zset":
		return "Members", "ZCARD"
	case "stream":
		return "Entries", "XLEN"
	}
	return "", ""
}

// columnInfo describes the columns of a key's data view.
func columnInfo(typ string) []driver.ColumnInfo {
	col := func(name, t string, pk bool) driver.ColumnInfo {
		return driver.ColumnInfo{Name: name, Type: t, PrimaryKey: pk}
	}
	switch typ {
	case "string", "json":
		return []driver.ColumnInfo{col("value", typ, false)}
	case "hash":
		return []driver.ColumnInfo{col("field", "string", true), col("value", "string", false)}
	case "list":
		return []driver.ColumnInfo{col("#", "integer", true), col("value", "string", false)}
	case "set":
		return []driver.ColumnInfo{col("value", "string", true)}
	case "zset":
		return []driver.ColumnInfo{col("member", "string", true), col("score", "double", false)}
	case "stream":
		return []driver.ColumnInfo{col("id", "string", false), col("field", "string", false), col("value", "string", false)}
	}
	return nil
}

// recreate renders commands that rebuild key from scratch.
func recreate(ctx context.Context, cl goredis.UniversalClient, typ, key string) (string, error) {
	var b strings.Builder
	total := int64(-1)
	// chunked writes cmd key + items, ddlChunk items (of width w) per line.
	chunked := func(cmd string, items []string, w int) {
		per := ddlChunk * w
		for i := 0; i < len(items); i += per {
			args := append([]string{cmd, key}, items[i:min(i+per, len(items))]...)
			b.WriteString(command(args...) + "\n")
		}
	}
	truncated := func(n int) {
		if total > int64(n) {
			fmt.Fprintf(&b, "# truncated: only the first %d of %d elements are included\n", n, total)
		}
	}
	switch typ {
	case "string":
		v, err := cl.Get(ctx, key).Result()
		if err != nil && err != goredis.Nil {
			return "", queryError(err)
		}
		b.WriteString(command("SET", key, v) + "\n")
	case "hash":
		total, _ = cl.HLen(ctx, key).Result()
		var items []string
		var cursor uint64
		for {
			kv, next, err := cl.HScan(ctx, key, cursor, "*", scanCount).Result()
			if err != nil {
				return "", queryError(err)
			}
			items = append(items, kv...)
			cursor = next
			if cursor == 0 || len(items)/2 >= ddlCap {
				break
			}
		}
		items = dedupPairs(items, ddlCap)
		truncated(len(items) / 2)
		chunked("HSET", items, 2)
	case "list":
		total, _ = cl.LLen(ctx, key).Result()
		items, err := cl.LRange(ctx, key, 0, ddlCap-1).Result()
		if err != nil {
			return "", queryError(err)
		}
		truncated(len(items))
		chunked("RPUSH", items, 1)
	case "set":
		total, _ = cl.SCard(ctx, key).Result()
		seen := map[string]bool{}
		var items []string
		var cursor uint64
		for {
			ms, next, err := cl.SScan(ctx, key, cursor, "*", scanCount).Result()
			if err != nil {
				return "", queryError(err)
			}
			for _, m := range ms {
				if !seen[m] && len(items) < ddlCap {
					seen[m] = true
					items = append(items, m)
				}
			}
			cursor = next
			if cursor == 0 || len(items) >= ddlCap {
				break
			}
		}
		slices.Sort(items)
		truncated(len(items))
		chunked("SADD", items, 1)
	case "zset":
		total, _ = cl.ZCard(ctx, key).Result()
		zs, err := cl.ZRangeWithScores(ctx, key, 0, ddlCap-1).Result()
		if err != nil {
			return "", queryError(err)
		}
		items := make([]string, 0, 2*len(zs))
		for _, z := range zs {
			items = append(items, strconv.FormatFloat(z.Score, 'g', -1, 64), fmt.Sprint(z.Member))
		}
		truncated(len(zs))
		chunked("ZADD", items, 2)
	case "stream":
		total, _ = cl.XLen(ctx, key).Result()
		msgs, err := cl.Do(ctx, "XRANGE", key, "-", "+", "COUNT", ddlCap).Slice()
		if err != nil {
			return "", queryError(err)
		}
		truncated(len(msgs))
		if len(msgs) == 0 {
			b.WriteString("# empty stream: XGROUP CREATE … MKSTREAM creates one\n")
		}
		for _, m := range msgs {
			ent, ok := m.([]any)
			if !ok || len(ent) != 2 {
				continue
			}
			args := []string{"XADD", key, text(ent[0])}
			fv, _ := ent[1].([]any)
			for _, x := range fv {
				args = append(args, text(x))
			}
			b.WriteString(command(args...) + "\n")
		}
	case "json":
		v, err := cl.Do(ctx, "JSON.GET", key).Text()
		if err != nil {
			return "", queryError(err)
		}
		b.WriteString(command("JSON.SET", key, "$", v) + "\n")
	default:
		fmt.Fprintf(&b, "# keys of type %s cannot be exported as commands\n", typ)
	}
	return b.String(), nil
}

// dedupPairs drops repeated fields of a flat field/value list (HSCAN may
// return a field twice) and keeps at most n pairs.
func dedupPairs(kv []string, n int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv) && len(out)/2 < n; i += 2 {
		if seen[kv[i]] {
			continue
		}
		seen[kv[i]] = true
		out = append(out, kv[i], kv[i+1])
	}
	return out
}
