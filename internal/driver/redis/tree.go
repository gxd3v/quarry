package redis

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

// Path markers, see the package documentation. Path elements travel as
// protobuf strings, so names that are not valid UTF-8 are hex-encoded.
const (
	keyMarker    = "\x00k:"
	keyHex       = "\x00x:"
	folderEscape = "\x00f:"
	folderHex    = "\x00h:"
	moreMarker   = "\x00more"
)

const (
	levelCap    = 10_000          // keys scanned per tree level
	catalogCap  = 2_000           // keys sampled for autocomplete
	scanCount   = 1_000           // SCAN COUNT hint
	scanBudget  = 3 * time.Second // time spent scanning one tree level
	pipeBatch   = 500             // commands per metadata pipeline
	maxListedDB = 64              // above this many databases only non-empty ones are listed
)

var _ driver.Catalog = (*conn)(nil)

func folderElem(seg string) string {
	switch {
	case !utf8.ValidString(seg):
		return folderHex + hex.EncodeToString([]byte(seg))
	case strings.HasPrefix(seg, "\x00"):
		return folderEscape + seg
	}
	return seg
}

func folderSeg(elem string) (string, error) {
	if h, ok := strings.CutPrefix(elem, folderHex); ok {
		b, err := hex.DecodeString(h)
		return string(b), err
	}
	return strings.TrimPrefix(elem, folderEscape), nil
}

func keyElem(key string) string {
	if utf8.ValidString(key) {
		return keyMarker + key
	}
	return keyHex + hex.EncodeToString([]byte(key))
}

// KeyPath returns the explorer Path of key in database db for separator sep
// (empty = ":"), for callers that build key nodes themselves.
func KeyPath(db int, key, sep string) []string {
	sep = cmp.Or(sep, defaultSeparator)
	p := []string{strconv.Itoa(db)}
	segs := strings.Split(key, sep)
	for _, s := range segs[:len(segs)-1] {
		p = append(p, folderElem(s))
	}
	return append(p, keyElem(key))
}

// target is what a node designates.
type target struct {
	db     int
	key    string // set for key nodes
	isKey  bool
	more   bool
	prefix string // folder prefix including the trailing separator; "" for a database
}

var errNoNode = errors.New("no Redis object selected")

// locate resolves a node. Besides tree nodes it accepts key nodes built from
// a Catalog Object (Kind NodeKey, Name = the key, Path[0] = Object.Schema).
func (c *conn) locate(n *driver.Node) (target, error) {
	if n == nil || len(n.Path) == 0 {
		return target{}, errNoNode
	}
	db, err := c.dbIndex(n.Path[0])
	if err != nil {
		return target{}, err
	}
	t := target{db: db}
	last := n.Path[len(n.Path)-1]
	switch {
	case len(n.Path) > 1 && strings.HasPrefix(last, keyMarker):
		t.isKey, t.key = true, strings.TrimPrefix(last, keyMarker)
	case len(n.Path) > 1 && strings.HasPrefix(last, keyHex):
		b, err := hex.DecodeString(strings.TrimPrefix(last, keyHex))
		if err != nil {
			return target{}, errors.New("invalid key path")
		}
		t.isKey, t.key = true, string(b)
	case len(n.Path) > 1 && last == moreMarker:
		t.more = true
	case n.Kind == driver.NodeKey:
		t.isKey, t.key = true, n.Name
	case len(n.Path) > 1:
		segs := make([]string, 0, len(n.Path)-1)
		for _, e := range n.Path[1:] {
			s, err := folderSeg(e)
			if err != nil {
				return target{}, errors.New("invalid folder path")
			}
			segs = append(segs, s)
		}
		t.prefix = strings.Join(segs, c.sep) + c.sep
	}
	return t, nil
}

func (c *conn) Children(ctx context.Context, parent *driver.Node) ([]*driver.Node, error) {
	if parent == nil {
		return c.databases(ctx)
	}
	t, err := c.locate(parent)
	if err != nil {
		return nil, err
	}
	if t.isKey || t.more {
		return nil, nil
	}
	return c.level(ctx, t, parent.Path)
}

func (c *conn) databases(ctx context.Context) ([]*driver.Node, error) {
	if c.cluster {
		n, err := c.base.DBSize(ctx).Result()
		if err != nil {
			return nil, queryError(err)
		}
		return []*driver.Node{dbNode(0, n)}, nil
	}
	cl, err := c.client(c.defDB)
	if err != nil {
		return nil, err
	}
	count := 16
	// CONFIG is often disabled on managed servers; 16 is the default.
	if m, err := cl.ConfigGet(ctx, "databases").Result(); err == nil {
		if n, err := strconv.Atoi(m["databases"]); err == nil && n > 0 {
			count = n
		}
	}
	info, err := cl.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, queryError(err)
	}
	keys := keyspace(info)
	var idx []int
	if count <= maxListedDB {
		for i := range count {
			idx = append(idx, i)
		}
	} else {
		idx = append(idx, 0)
		for i := range keys {
			idx = append(idx, i)
		}
	}
	idx = append(idx, c.defDB)
	slices.Sort(idx)
	idx = slices.Compact(idx)
	out := make([]*driver.Node, 0, len(idx))
	for _, i := range idx {
		out = append(out, dbNode(i, keys[i]))
	}
	return out, nil
}

func dbNode(i int, keys int64) *driver.Node {
	detail := ""
	switch {
	case keys == 1:
		detail = "1 key"
	case keys > 1:
		detail = fmt.Sprintf("%d keys", keys)
	}
	s := strconv.Itoa(i)
	return &driver.Node{Path: []string{s}, Kind: driver.NodeDatabase, Name: "db" + s, Detail: detail}
}

// keyspace parses INFO keyspace ("db3:keys=5,expires=0,avg_ttl=0").
func keyspace(info string) map[int]int64 {
	out := map[int]int64{}
	for line := range strings.Lines(info) {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(name, "db") {
			continue
		}
		i, err := strconv.Atoi(name[2:])
		if err != nil {
			continue
		}
		for kv := range strings.SplitSeq(rest, ",") {
			if v, ok := strings.CutPrefix(kv, "keys="); ok {
				out[i], _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	return out
}

// group is one tree level: folders with their key counts and leaf keys.
type group struct {
	folders map[string]int
	keys    []string
}

// groupKeys splits keys (which all start with prefix) at the next separator.
func groupKeys(keys []string, prefix, sep string) group {
	g := group{folders: map[string]int{}}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, prefix)
		if i := strings.Index(rest, sep); i >= 0 {
			g.folders[rest[:i]]++
		} else {
			g.keys = append(g.keys, k)
		}
	}
	slices.Sort(g.keys)
	return g
}

func (c *conn) level(ctx context.Context, t target, parent []string) ([]*driver.Node, error) {
	cl, err := c.client(t.db)
	if err != nil {
		return nil, err
	}
	keys, more, err := c.scan(ctx, cl, globEscape(t.prefix)+"*", levelCap)
	if err != nil {
		return nil, err
	}
	g := groupKeys(keys, t.prefix, c.sep)
	plus := ""
	if more {
		plus = "+"
	}
	names := make([]string, 0, len(g.folders))
	for f := range g.folders {
		names = append(names, f)
	}
	slices.Sort(names)
	child := func(elem string) []string { return append(parent[:len(parent):len(parent)], elem) }
	out := make([]*driver.Node, 0, len(names)+len(g.keys)+1)
	for _, f := range names {
		name := label(f)
		if f == "" {
			name = "(empty)"
		}
		out = append(out, &driver.Node{Path: child(folderElem(f)), Kind: driver.NodeFolder, Name: name,
			Detail: fmt.Sprintf("(%d%s)", g.folders[f], plus)})
	}
	metas, err := keyMeta(ctx, cl, g.keys, true)
	if err != nil {
		return nil, err
	}
	for i, k := range g.keys {
		m := metas[i]
		if m.typ == "none" {
			continue // deleted since SCAN
		}
		name := cmp.Or(strings.TrimPrefix(k, t.prefix), k)
		out = append(out, &driver.Node{Path: child(keyElem(k)), Kind: driver.NodeKey, Name: label(name),
			Detail: m.detail(), Leaf: true})
	}
	if more {
		out = append(out, &driver.Node{Path: child(moreMarker), Kind: driver.NodeFolder, Name: "(+more)",
			Detail: fmt.Sprintf("only the first %d keys are shown; filter the data view to see others", len(keys)), Leaf: true})
	}
	return out, nil
}

// globEscape quotes the glob metacharacters of s for SCAN MATCH.
func globEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// scan collects up to limit distinct keys matching match, spending at most
// scanBudget; more reports that the keyspace may hold others. A Cluster is
// scanned on every primary.
func (c *conn) scan(ctx context.Context, cl goredis.UniversalClient, match string, limit int) (keys []string, more bool, err error) {
	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		start = time.Now()
	)
	loop := func(ctx context.Context, node goredis.Cmdable) error {
		var cursor uint64
		for {
			batch, next, err := node.Scan(ctx, cursor, match, scanCount).Result()
			if err != nil {
				return err
			}
			mu.Lock()
			for _, k := range batch {
				if !seen[k] {
					seen[k] = true
					keys = append(keys, k)
				}
			}
			stop := len(keys) >= limit || time.Since(start) > scanBudget
			if stop && next != 0 {
				more = true
			}
			mu.Unlock()
			if next == 0 || stop {
				return nil
			}
			cursor = next
		}
	}
	if cc, ok := cl.(*goredis.ClusterClient); ok {
		err = cc.ForEachMaster(ctx, func(ctx context.Context, m *goredis.Client) error { return loop(ctx, m) })
	} else {
		err = loop(ctx, cl)
	}
	if err != nil {
		return nil, false, queryError(err)
	}
	if len(keys) > limit {
		keys, more = keys[:limit], true
	}
	return keys, more, nil
}

// meta is what the tree shows about a key.
type meta struct {
	typ string // TYPE reply, "none" when missing
	ttl int64  // seconds; -1 without expiry
}

func (m meta) detail() string {
	d := normType(m.typ)
	if m.ttl >= 0 {
		d += fmt.Sprintf(" · ttl %ds", m.ttl)
	}
	return d
}

// normType maps TYPE replies to the driver's names (RedisJSON reports
// "ReJSON-RL").
func normType(t string) string {
	if t == "ReJSON-RL" {
		return "json"
	}
	return t
}

// detailType extracts the type from a key node's Detail ("hash · ttl 5s").
func detailType(detail string) string {
	t, _, _ := strings.Cut(detail, "·")
	return normType(strings.TrimSpace(t))
}

// keyMeta pipelines TYPE (and TTL when ttl is set) for keys.
func keyMeta(ctx context.Context, cl goredis.UniversalClient, keys []string, ttl bool) ([]meta, error) {
	out := make([]meta, len(keys))
	for start := 0; start < len(keys); start += pipeBatch {
		end := min(start+pipeBatch, len(keys))
		var (
			types []*goredis.StatusCmd
			ttls  []*goredis.DurationCmd
		)
		_, err := cl.Pipelined(ctx, func(p goredis.Pipeliner) error {
			for _, k := range keys[start:end] {
				types = append(types, p.Type(ctx, k))
				if ttl {
					ttls = append(ttls, p.TTL(ctx, k))
				}
			}
			return nil
		})
		if err != nil {
			return nil, queryError(err)
		}
		for i := range types {
			m := meta{typ: types[i].Val(), ttl: -1}
			if ttl {
				switch d := ttls[i].Val(); {
				case d == -2:
					m.typ = "none"
				case d >= 0:
					m.ttl = int64(d / time.Second)
				}
			}
			out[start+i] = m
		}
	}
	return out, nil
}

// Objects returns a sample of up to 2,000 keys of a database for
// autocomplete: Kind NodeKey, Name the key, Schema the database index (so a
// node can be built for it, see locate) and Detail its type. Keys that are
// not valid UTF-8 are left out.
func (c *conn) Objects(ctx context.Context, scope driver.Scope) ([]driver.Object, error) {
	db, err := c.dbIndex(scope.Database)
	if err != nil {
		return nil, err
	}
	cl, err := c.client(db)
	if err != nil {
		return nil, err
	}
	keys, _, err := c.scan(ctx, cl, "*", catalogCap)
	if err != nil {
		return nil, err
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return !utf8.ValidString(k) })
	slices.Sort(keys)
	metas, err := keyMeta(ctx, cl, keys, false)
	if err != nil {
		return nil, err
	}
	out := make([]driver.Object, 0, len(keys))
	schema := strconv.Itoa(db)
	for i, k := range keys {
		if metas[i].typ == "none" {
			continue
		}
		out = append(out, driver.Object{Schema: schema, Name: k, Kind: driver.NodeKey, Detail: normType(metas[i].typ)})
	}
	return out, nil
}
