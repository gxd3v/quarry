package complete

import (
	"slices"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func cols(names ...string) []driver.Column {
	var cs []driver.Column
	for _, n := range names {
		cs = append(cs, driver.Column{Name: n, Type: "int"})
	}
	return cs
}

var catalog = []driver.Object{
	{Schema: "shop", Name: "orders", Kind: driver.NodeTable, Columns: cols("id", "customer_id", "total", "created_at")},
	{Schema: "shop", Name: "customers", Kind: driver.NodeTable, Columns: cols("id", "name", "Email")},
	{Schema: "shop", Name: "calc_tax", Kind: driver.NodeFunction, Detail: "calc_tax(numeric)"},
	{Schema: "public", Name: "events", Kind: driver.NodeTable, Columns: cols("group", "CamelCol", "kind")},
	{Schema: "public", Name: "order_stats", Kind: driver.NodeView, Columns: cols("day", "n")},
	{Schema: "sales", Name: "orders", Kind: driver.NodeTable, Columns: cols("id", "amount")},
	{Schema: "sales", Name: "Invoices", Kind: driver.NodeTable, Columns: cols("no")},
	{Schema: "sales", Name: "v_daily", Kind: driver.NodeView},
}

// suggest runs Suggest on text where "|" marks the cursor.
func suggest(lang driver.Language, text string, objs []driver.Object) Result {
	cur := strings.Index(text, "|")
	return Suggest(Request{
		Lang: lang, Text: text[:cur] + text[cur+1:], Cursor: cur,
		Objects: objs, SearchPath: []string{"shop", "public"},
	})
}

func sqlSuggest(text string) Result { return suggest(driver.LangSQL, text, catalog) }

func labels(r Result) []string {
	var ls []string
	for _, it := range r.Items {
		ls = append(ls, it.Label)
	}
	return ls
}

func find(r Result, label string) (Item, bool) {
	for _, it := range r.Items {
		if it.Label == label {
			return it, true
		}
	}
	return Item{}, false
}

func kinds(r Result) map[Kind]bool {
	m := map[Kind]bool{}
	for _, it := range r.Items {
		m[it.Kind] = true
	}
	return m
}

func TestSQLContexts(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string // must be present
		absent     []string
		first      string // expected first label ("" = don't care)
		exact      []string
	}{
		{name: "statement start", text: "|", want: []string{"SELECT", "INSERT INTO", "DELETE FROM", "WITH", "BEGIN"}, absent: []string{"orders", "count"}},
		{name: "after semicolon", text: "SELECT 1;\nsel|", first: "select"},
		{name: "upper prefix", text: "SEL|", first: "SELECT"},
		{name: "from tables", text: "SELECT * FROM |",
			want:   []string{"orders", "customers", "events", "order_stats", "sales.orders", "sales.Invoices", "sales.v_daily", "shop", "public", "sales"},
			absent: []string{"SELECT", "count", "shop.orders"}},
		{name: "from prefix ranking", text: "SELECT * FROM ord|", first: "orders", want: []string{"order_stats", "sales.orders"}},
		{name: "join", text: "SELECT * FROM orders o JOIN cu|", first: "customers"},
		{name: "from comma", text: "SELECT * FROM orders o, |", want: []string{"customers"}},
		{name: "update", text: "UPDATE |", want: []string{"orders"}},
		{name: "describe", text: "DESCRIBE ev|", first: "events"},
		{name: "after table", text: "SELECT * FROM orders |", want: []string{"WHERE", "JOIN", "LEFT JOIN", "ORDER BY"}, absent: []string{"orders", "count"}},
		{name: "alias columns", text: "SELECT o.| FROM orders o", exact: []string{"id", "total", "created_at", "customer_id"}},
		{name: "alias with AS", text: "SELECT c.| FROM orders AS o JOIN customers AS c ON c.id = o.customer_id", exact: []string{"id", "name", "Email"}},
		{name: "alias declared later in join", text: "SELECT * FROM orders o JOIN customers c ON c.id = o.| ", want: []string{"customer_id"}},
		{name: "table name qualifier", text: "SELECT orders.| FROM orders", exact: []string{"id", "total", "created_at", "customer_id"}},
		{name: "schema qualifier", text: "SELECT * FROM sales.|", exact: []string{"orders", "v_daily", "Invoices"}},
		{name: "schema.table qualifier", text: "SELECT sales.orders.| FROM sales.orders", exact: []string{"id", "amount"}},
		{name: "schema-qualified alias", text: "SELECT * FROM sales.orders s WHERE s.am|", exact: []string{"amount"}},
		{name: "unknown qualifier", text: "SELECT zz.| FROM orders o", exact: nil},
		{name: "where columns", text: "SELECT * FROM orders WHERE |", want: []string{"total", "customer_id", "COUNT", "calc_tax"}, absent: []string{"orders"}},
		{name: "where keywords", text: "SELECT * FROM orders WHERE total > 1 an|", first: "and"},
		{name: "ambiguous columns", text: "SELECT | FROM orders o JOIN customers c ON true",
			want: []string{"o.id", "c.id", "total", "name", "Email", "o", "c"}, absent: []string{"id"}},
		{name: "group by", text: "SELECT * FROM orders GROUP BY |", want: []string{"total"}},
		{name: "order by", text: "SELECT * FROM orders ORDER BY cr|", first: "created_at"},
		{name: "having", text: "SELECT 1 FROM orders HAVING to|", first: "total"},
		{name: "set", text: "UPDATE orders SET to|", first: "total"},
		{name: "insert columns", text: "INSERT INTO orders (|", want: []string{"total", "id"}},
		{name: "subquery from", text: "SELECT * FROM events WHERE kind IN (SELECT id FROM |", want: []string{"orders"}},
		{name: "otherwise", text: "INSERT INTO orders VALUES (|", want: []string{"NULL", "DEFAULT"}},
		{name: "otherwise functions", text: "INSERT INTO orders VALUES (co|", want: []string{"count", "coalesce"}},
		{name: "otherwise tables", text: "INSERT INTO orders VALUES (ord|", want: []string{"order by", "orders", "order_stats"}},
		{name: "cte body", text: "WITH x AS (|", want: []string{"SELECT"}, absent: []string{"orders"}},
		{name: "explain", text: "EXPLAIN |", want: []string{"SELECT"}},
		{name: "other statement alias", text: "SELECT c.| FROM orders o; SELECT * FROM customers c", exact: nil},
		{name: "statement boundaries", text: "SELECT * FROM customers c; SELECT c.| FROM orders c", exact: []string{"id", "total", "created_at", "customer_id"}},
		{name: "semicolon in string", text: "SELECT ';', o.| FROM orders o", want: []string{"total"}},
		{name: "comment between", text: "SELECT o.| /* x; */ FROM orders o", want: []string{"total"}},
		{name: "catalog function", text: "SELECT calc|", first: "calc_tax"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := sqlSuggest(tt.text)
			got := labels(r)
			for _, w := range tt.want {
				if !slices.Contains(got, w) {
					t.Errorf("missing %q in %v", w, got)
				}
			}
			for _, a := range tt.absent {
				if slices.Contains(got, a) {
					t.Errorf("unexpected %q in %v", a, got)
				}
			}
			if tt.first != "" && (len(got) == 0 || got[0] != tt.first) {
				t.Errorf("first = %v, want %q", got, tt.first)
			}
			if tt.exact != nil || strings.Contains(tt.name, "unknown") || strings.Contains(tt.name, "other statement") {
				a, b := slices.Clone(got), slices.Clone(tt.exact)
				slices.Sort(a)
				slices.Sort(b)
				if !slices.Equal(a, b) {
					t.Errorf("got %v, want exactly %v", got, tt.exact)
				}
			}
		})
	}
}

func TestSQLIgnoredSpans(t *testing.T) {
	for _, text := range []string{
		"SELECT 'FROM |' FROM orders",
		"SELECT 'unterminated |",
		"-- sel|",
		"SELECT 1 /* from | */",
		"SELECT 1 /* unterminated |",
		"SELECT $$ sel| $$",
		"SELECT * FROM orders LIMIT 10|",
	} {
		r := sqlSuggest(text)
		if len(r.Items) != 0 {
			t.Errorf("%q: got %v, want nothing", text, labels(r))
		}
	}
	// Just after a closed string or comment the cursor is outside it.
	if r := sqlSuggest("SELECT 'x' |"); len(r.Items) == 0 {
		t.Error("no items after a closed string")
	}
	if r := sqlSuggest("/* c */ sel|"); len(r.Items) == 0 || r.Items[0].Label != "select" {
		t.Errorf("after comment: %v", labels(r))
	}
}

func TestSQLReplaceRange(t *testing.T) {
	text := "SELECT * FROM ord|ers WHERE"
	r := sqlSuggest(text)
	if r.Start != 14 || r.End != 20 || r.Prefix != "ord" {
		t.Fatalf("range = %d..%d prefix %q", r.Start, r.End, r.Prefix)
	}
	if r.Items[0].Label != "orders" {
		t.Fatalf("items = %v", labels(r))
	}

	r = sqlSuggest("SELECT o.cu|stomer FROM orders o")
	if r.Start != 9 || r.End != 17 || r.Prefix != "cu" || r.Items[0].Insert != "customer_id" {
		t.Fatalf("qualified range = %d..%d %q %v", r.Start, r.End, r.Prefix, labels(r))
	}

	// No word under the cursor: empty range at the cursor.
	r = sqlSuggest("SELECT * FROM |")
	if r.Start != 14 || r.End != 14 || r.Prefix != "" {
		t.Fatalf("empty range = %d..%d", r.Start, r.End)
	}

	// Cursor at the start of a word still replaces it.
	r = sqlSuggest("SELECT * FROM |orders")
	if r.Start != 14 || r.End != 20 || r.Prefix != "" {
		t.Fatalf("start-of-word range = %d..%d", r.Start, r.End)
	}
}

func TestSQLQuoting(t *testing.T) {
	r := sqlSuggest("SELECT * FROM events WHERE |")
	for label, ins := range map[string]string{"group": `"group"`, "CamelCol": `"CamelCol"`, "kind": "kind"} {
		it, ok := find(r, label)
		if !ok || it.Insert != ins {
			t.Errorf("%s: insert %q, want %q", label, it.Insert, ins)
		}
	}
	r = sqlSuggest("SELECT * FROM |")
	if it, _ := find(r, "sales.Invoices"); it.Insert != `sales."Invoices"` {
		t.Errorf("qualified insert = %q", it.Insert)
	}
	if it, _ := find(r, "sales.orders"); it.Insert != "sales.orders" || it.Kind != Table {
		t.Errorf("shadowed table = %+v", it)
	}
	if it, _ := find(r, "order_stats"); it.Kind != View {
		t.Errorf("view kind = %v", it.Kind)
	}

	// A word started with a double quote is completed quoted, replacing the
	// opening quote too.
	r = sqlSuggest(`SELECT "Em| FROM customers`)
	if r.Start != 7 || r.Prefix != "Em" || len(r.Items) == 0 || r.Items[0].Insert != `"Email"` {
		t.Fatalf("quoted prefix: %d %q %+v", r.Start, r.Prefix, r.Items)
	}
	r = sqlSuggest(`SELECT "na|" FROM customers`)
	if r.End != 11 || r.Items[0].Insert != `"name"` || kinds(r)[Keyword] {
		t.Fatalf("quoted word: end %d %+v", r.End, r.Items)
	}

	ambiguous := sqlSuggest(`SELECT | FROM orders JOIN "customers" ON true`)
	if it, ok := find(ambiguous, "orders.id"); !ok || it.Insert != "orders.id" {
		t.Errorf("ambiguous without alias: %v", labels(ambiguous))
	}
	if it, ok := find(ambiguous, `customers.Email`); ok {
		t.Errorf("Email is not ambiguous: %+v", it)
	}
}

func TestSQLKeywordCase(t *testing.T) {
	cases := map[string]string{
		"sel|":                         "select",
		"SEL|":                         "SELECT",
		"Sel|":                         "SELECT",
		"select * from orders |":       "where",
		"SELECT * FROM orders |":       "WHERE",
		"select * from orders wh|":     "where",
		"select * from orders WH|":     "WHERE",
		"select 1;\n|":                 "select",
		"select * from orders order |": "order by",
	}
	for text, want := range cases {
		r := sqlSuggest(text)
		if !slices.Contains(labels(r), want) {
			t.Errorf("%q: %q not in %v", text, want, labels(r))
		}
	}
	r := sqlSuggest("select * from orders where cou|")
	if it, _ := find(r, "count"); it.Insert != "count" {
		t.Errorf("lower function: %+v", it)
	}
}

func TestSQLRanking(t *testing.T) {
	r := sqlSuggest("SELECT * FROM orders WHERE c|")
	got := labels(r)
	idx := func(l string) int { return slices.Index(got, l) }
	// Columns before functions before keywords within the prefix tier.
	if !(idx("created_at") >= 0 && idx("created_at") < idx("count") && idx("count") < idx("case")) {
		t.Errorf("kind order: %v", got)
	}
	// Shorter first within a kind.
	if idx("created_at") > idx("customer_id") {
		t.Errorf("length order: %v", got)
	}

	// Word-boundary and initials matches come after prefix matches.
	r = sqlSuggest("SELECT * FROM orders WHERE id|")
	got = labels(r)
	if got[0] != "id" || slices.Index(got, "customer_id") < 0 {
		t.Fatalf("boundary: %v", got)
	}
	r = sqlSuggest("SELECT * FROM orders WHERE ci|")
	if r.Items[0].Label != "customer_id" {
		t.Fatalf("initials: %v", labels(r))
	}
	// Subsequence matches anchored at a word start.
	r = sqlSuggest("SELECT * FROM orders WHERE crat|")
	if r.Items[0].Label != "created_at" {
		t.Fatalf("subsequence: %v", labels(r))
	}
	if r := sqlSuggest("SELECT * FROM orders WHERE rdr|"); slices.Contains(labels(r), "order_stats") {
		t.Fatalf("subsequence must start at a word boundary: %v", labels(r))
	}

	if r := sqlSuggest("SELECT |"); len(r.Items) != MaxItems {
		t.Fatalf("cap: %d items", len(r.Items))
	}
}

func TestSQLNilObjects(t *testing.T) {
	for _, text := range []string{"SELECT * FROM |", "SELECT o.| FROM orders o", "SELECT * FROM sales.|"} {
		if r := suggest(driver.LangSQL, text, nil); len(r.Items) != 0 {
			t.Errorf("%q: %v", text, labels(r))
		}
	}
	r := suggest(driver.LangSQL, "SELECT s| FROM orders", nil)
	if k := kinds(r); !k[Function] || !k[Keyword] || k[Column] {
		t.Errorf("nil objects columns ctx: %v", labels(r))
	}
	// Out-of-range cursors are clamped.
	Suggest(Request{Text: "SELECT", Cursor: 99})
	Suggest(Request{Text: "SELECT", Cursor: -3})
}

func TestMatchPositions(t *testing.T) {
	cases := []struct {
		label, prefix, want string
	}{
		{"orders", "ord", "xxx..."},
		{"o.id", "id", "..xx"},
		{"customer_id", "ci", "x........x."},
		{"created_at", "crat", "xx.xx....."},
		{"name", "", "...."},
		{"name", "zz", "...."},
	}
	for _, c := range cases {
		var b strings.Builder
		for _, m := range matchPositions(c.label, c.prefix) {
			if m {
				b.WriteByte('x')
			} else {
				b.WriteByte('.')
			}
		}
		if b.String() != c.want {
			t.Errorf("%s/%s = %s, want %s", c.label, c.prefix, b.String(), c.want)
		}
	}
}

var mongoCatalog = []driver.Object{
	{Name: "users", Kind: driver.NodeCollection, Columns: cols("name", "age", "two words")},
	{Name: "order-items", Kind: driver.NodeCollection},
	{Name: "logs", Kind: driver.NodeTable},
}

func TestMongo(t *testing.T) {
	ms := func(text string) Result { return suggest(driver.LangMongo, text, mongoCatalog) }

	r := ms("db.|")
	if got := labels(r); len(got) < 3 || got[0] != "logs" || got[1] != "users" || got[2] != "order-items" {
		t.Fatalf("collections: %v", got)
	}
	if it, _ := find(r, "order-items"); it.Insert != `getCollection("order-items")` || it.Kind != Collection {
		t.Errorf("non-identifier collection: %+v", it)
	}
	if !slices.Contains(labels(r), "getCollectionNames") {
		t.Errorf("db methods missing: %v", labels(r))
	}

	r = ms("db.us|ers.find()")
	if r.Items[0].Label != "users" || r.Start != 3 || r.End != 8 {
		t.Fatalf("collection prefix: %v %d..%d", labels(r), r.Start, r.End)
	}

	r = ms("db.users.f|")
	if got := labels(r); got[0] != "find" || !slices.Contains(got, "findOne") || r.Items[0].Kind != Function {
		t.Fatalf("methods: %v", got)
	}
	if r = ms("db.users.|"); !slices.Contains(labels(r), "aggregate") || !slices.Contains(labels(r), "insertMany") {
		t.Fatalf("all methods: %v", labels(r))
	}
	if r = ms(`db.getCollection("x").fi|`); r.Items[0].Label != "find" {
		t.Fatalf("getCollection methods: %v", labels(r))
	}
	if r = ms("db.users.find({}).so|"); r.Items[0].Label != "sort" {
		t.Fatalf("cursor methods: %v", labels(r))
	}

	r = ms("db.users.find({ age: { $g| } })")
	if got := labels(r); len(got) < 2 || got[0] != "$gt" || got[1] != "$gte" || r.Prefix != "$g" {
		t.Fatalf("operators: %v", got)
	}
	if r = ms("db.users.aggregate([{ $| }])"); !slices.Contains(labels(r), "$match") || !slices.Contains(labels(r), "$group") {
		t.Fatalf("stages: %v", labels(r))
	}
	if r = ms("db.users.find({ na| })"); len(r.Items) == 0 || r.Items[0].Label != "name" {
		t.Fatalf("fields: %v", labels(r))
	}
	if r = ms("db.users.find({ tw| })"); r.Items[0].Insert != `"two words"` {
		t.Fatalf("quoted field: %+v", r.Items)
	}

	for _, text := range []string{
		`db.users.find({ "$g|" })`,
		"db.users.find({}) // db.|",
		"$g|",
		"x = db.users.count() + |",
	} {
		if r := ms(text); len(r.Items) != 0 {
			t.Errorf("%q: %v", text, labels(r))
		}
	}

	if r = ms("|"); !slices.Contains(labels(r), "db") || !slices.Contains(labels(r), "show collections") {
		t.Fatalf("statement start: %v", labels(r))
	}
	if r = ms("db.users.find()\nsh|"); r.Items[0].Label != "show dbs" {
		t.Fatalf("second line: %v", labels(r))
	}

	r = suggest(driver.LangMongo, "db.|", nil)
	if k := kinds(r); k[Collection] || !k[Function] {
		t.Fatalf("nil objects: %v", labels(r))
	}
}

func TestRedis(t *testing.T) {
	keys := []driver.Object{
		{Name: "user:1", Kind: driver.NodeKey, Detail: "hash"},
		{Name: "my key", Kind: driver.NodeKey},
		{Name: "other", Kind: driver.NodeTable},
	}
	rs := func(text string, objs []driver.Object) Result { return suggest(driver.LangRedis, text, objs) }

	r := rs("ge|", nil)
	if r.Items[0].Label != "get" || r.Items[0].Detail != "key" || r.Items[0].Kind != Command {
		t.Fatalf("lower command: %+v", r.Items[0])
	}
	if r = rs("HG|", nil); r.Items[0].Label != "HGET" {
		t.Fatalf("upper command: %v", labels(r))
	}
	if r = rs("|", nil); len(r.Items) != MaxItems {
		t.Fatalf("all commands capped: %d", len(r.Items))
	}
	if it, ok := find(rs("SE|", nil), "SET"); !ok || !strings.Contains(it.Detail, "key value") {
		t.Fatalf("SET detail: %+v", it)
	}
	if r = rs("G|ET foo", nil); r.Start != 0 || r.End != 3 {
		t.Fatalf("range: %d..%d", r.Start, r.End)
	}
	if r = rs("SET a 1\n  hge|", nil); r.Items[0].Label != "hget" || r.Start != 10 || r.End != 13 {
		t.Fatalf("second line: %v %d..%d", labels(r), r.Start, r.End)
	}

	if r = rs("GET |", nil); len(r.Items) != 0 {
		t.Fatalf("args without keys: %v", labels(r))
	}
	r = rs("GET us|", keys)
	if len(r.Items) != 1 || r.Items[0].Label != "user:1" || r.Items[0].Kind != Key {
		t.Fatalf("keys: %+v", r.Items)
	}
	if r = rs("GET my|", keys); r.Items[0].Insert != `"my key"` {
		t.Fatalf("quoted key: %+v", r.Items)
	}
	if r = rs("# GE|", nil); len(r.Items) != 0 {
		t.Fatalf("comment: %v", labels(r))
	}
}
