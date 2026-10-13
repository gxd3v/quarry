package complete

import (
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

func suggestRedis(r Request) Result {
	s, cur := r.Text, r.Cursor
	ls := strings.LastIndexByte(s[:cur], '\n') + 1
	le := len(s)
	if i := strings.IndexByte(s[cur:], '\n'); i >= 0 {
		le = cur + i
	}
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }
	start, end := cur, cur
	for start > ls && !space(s[start-1]) {
		start--
	}
	for end < le && !space(s[end]) {
		end++
	}
	prefix := s[start:cur]
	head := strings.TrimSpace(s[ls:start])
	if strings.HasPrefix(head, "#") || head == "" && strings.HasPrefix(prefix, "#") {
		return Result{Start: cur, End: cur}
	}
	if head == "" {
		lower, _ := lowerTyped(prefix)
		kw := caser(lower)
		rk := newRanker(prefix, Command)
		for _, c := range redisCommands {
			rk.add(Item{Label: kw(c.name), Detail: c.detail, Kind: Command})
		}
		return rk.result(start, end)
	}
	rk := newRanker(prefix, Key)
	for _, o := range r.Objects {
		if o.Kind == driver.NodeKey {
			ins := o.Name
			if strings.ContainsAny(ins, " \t\"'\\") || ins == "" {
				ins = strconv.Quote(ins)
			}
			rk.add(Item{Label: o.Name, Insert: ins, Detail: o.Detail, Kind: Key})
		}
	}
	return rk.result(start, end)
}
