// Package complete implements autocomplete for the editor: a pure suggestion
// engine for SQL, Mongo shell and Redis scripts, and the popup that shows its
// results.
package complete

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

// Kind classifies a suggestion; the popup shows it as a colored badge.
type Kind int

const (
	Keyword Kind = iota
	Schema
	Table
	View
	Column
	Function
	Alias
	Snippet
	Collection
	Command
	Key
)

var kindNames = [...]string{"keyword", "schema", "table", "view", "column",
	"function", "alias", "snippet", "collection", "command", "key"}

func (k Kind) String() string {
	if k >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Item is one suggestion. Insert replaces the Result's Start..End range.
type Item struct {
	Label, Insert, Detail string
	Kind                  Kind
}

// Request describes the text being edited and what the engine may suggest.
type Request struct {
	Lang       driver.Language
	Text       string
	Cursor     int             // byte offset
	Objects    []driver.Object // from driver.Catalog for the tab's scope (may be nil while loading)
	SearchPath []string        // schemas resolvable without qualification, e.g. ["shop","public"]
}

// Result is the byte range Start..End to replace with an Item's Insert.
// Prefix is the part of the word before the cursor that items were matched
// against (unquoted).
type Result struct {
	Start, End int
	Prefix     string
	Items      []Item
}

// MaxItems caps the number of suggestions returned.
const MaxItems = 50

// Suggest returns the suggestions for the cursor position in r.Text.
func Suggest(r Request) Result {
	r.Cursor = min(max(r.Cursor, 0), len(r.Text))
	switch r.Lang {
	case driver.LangMongo:
		return suggestMongo(r)
	case driver.LangRedis:
		return suggestRedis(r)
	}
	return suggestSQL(r)
}

// ranker collects candidates, filters them by prefix and sorts them.
type ranker struct {
	prefix string
	order  []Kind // kind relevance for the context, most relevant first
	cands  []scored
	seen   map[string]bool
}

type scored struct {
	item       Item
	tier, rank int
}

func newRanker(prefix string, order ...Kind) *ranker {
	return &ranker{prefix: prefix, order: order, seen: map[string]bool{}}
}

// add offers it, matched against its label and any extra keys.
func (rk *ranker) add(it Item, keys ...string) {
	if it.Insert == "" {
		it.Insert = it.Label
	}
	id := it.Kind.String() + "\x00" + it.Label
	if rk.seen[id] {
		return
	}
	tier := matchTier(it.Label, rk.prefix)
	for _, k := range keys {
		if t := matchTier(k, rk.prefix); t >= 0 && (tier < 0 || t < tier) {
			tier = t
		}
	}
	if tier < 0 {
		return
	}
	rk.seen[id] = true
	rank := slices.Index(rk.order, it.Kind)
	if rank < 0 {
		rank = len(rk.order)
	}
	rk.cands = append(rk.cands, scored{it, tier, rank})
}

func (rk *ranker) result(start, end int) Result {
	slices.SortStableFunc(rk.cands, func(a, b scored) int {
		return cmp.Or(
			cmp.Compare(a.tier, b.tier),
			cmp.Compare(a.rank, b.rank),
			cmp.Compare(len(a.item.Label), len(b.item.Label)),
			cmp.Compare(strings.ToLower(a.item.Label), strings.ToLower(b.item.Label)),
			cmp.Compare(a.item.Label, b.item.Label),
		)
	})
	items := make([]Item, 0, min(len(rk.cands), MaxItems))
	for _, c := range rk.cands[:min(len(rk.cands), MaxItems)] {
		items = append(items, c.item)
	}
	return Result{Start: start, End: end, Prefix: rk.prefix, Items: items}
}

// matchTier ranks how key matches the typed prefix p: 0 for a
// case-insensitive prefix, 1 for a match at a word boundary or on the
// initials, 2 for a subsequence starting at a word boundary, -1 for none.
func matchTier(key, p string) int {
	if p == "" {
		return 0
	}
	k, q := strings.ToLower(key), strings.ToLower(p)
	if strings.HasPrefix(k, q) {
		return 0
	}
	if len(k) != len(key) { // case mapping changed byte offsets; keep it simple
		if subsequence(k, q) {
			return 2
		}
		return -1
	}
	for i := 1; i < len(k); i++ {
		if boundary(key, i) && strings.HasPrefix(k[i:], q) {
			return 1
		}
	}
	if len(q) > 1 && strings.HasPrefix(initials(key), q) {
		return 1
	}
	for i := 0; i < len(k); i++ {
		if k[i] == q[0] && (i == 0 || boundary(key, i)) && subsequence(k[i+1:], q[1:]) {
			return 2
		}
	}
	return -1
}

// matchPositions returns which runes of label match prefix, for highlighting.
func matchPositions(label, prefix string) []bool {
	lr, pr := []rune(label), []rune(strings.ToLower(prefix))
	pos := make([]bool, len(lr))
	if len(pr) == 0 {
		return pos
	}
	low := []rune(strings.ToLower(label))
	if len(low) != len(lr) {
		return pos
	}
	at := func(i int) bool { // prefix found contiguously at i
		if i+len(pr) > len(low) {
			return false
		}
		return string(low[i:i+len(pr)]) == string(pr)
	}
	for i := range low {
		if (i == 0 || boundaryRunes(lr, i)) && at(i) {
			for j := i; j < i+len(pr); j++ {
				pos[j] = true
			}
			return pos
		}
	}
	j := 0
	for i := 0; i < len(low) && j < len(pr); i++ {
		if low[i] == pr[j] {
			pos[i] = true
			j++
		}
	}
	if j < len(pr) {
		return make([]bool, len(lr))
	}
	return pos
}

func boundary(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	p, _ := utf8.DecodeLastRuneInString(s[:i])
	return isBoundary(p, r)
}

func boundaryRunes(s []rune, i int) bool { return isBoundary(s[i-1], s[i]) }

func isBoundary(prev, r rune) bool {
	switch prev {
	case '_', '.', '$', ' ', '-', '"':
		return r != '_' && r != '.'
	}
	return unicode.IsLower(prev) && unicode.IsUpper(r)
}

func initials(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i == 0 || boundary(s, i) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func subsequence(s, q string) bool {
	for i := 0; i < len(s) && q != ""; i++ {
		if s[i] == q[0] {
			q = q[1:]
		}
	}
	return q == ""
}

// caser returns the case to apply to keywords: lower when the user typed in
// lower case, upper otherwise.
func caser(lower bool) func(string) string {
	if lower {
		return strings.ToLower
	}
	return strings.ToUpper
}

// lowerTyped reports whether s has letters and all of them are lower case.
func lowerTyped(s string) (lower, ok bool) {
	for _, r := range s {
		if unicode.IsLetter(r) {
			ok = true
			if unicode.IsUpper(r) {
				return false, true
			}
		}
	}
	return ok, ok
}
