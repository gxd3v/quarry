// Package driver defines the contract every database backend implements.
//
// Quarry targets both SQL (PostgreSQL, Oracle) and non-SQL (MongoDB, Redis)
// engines, so the abstraction is not about SQL: it is about browsing a tree of
// objects and running statements in a backend-specific language. Optional
// features (transactions, editing, catalog, describe, explain) are separate
// interfaces discovered with type assertions. Native CLIs are launched by
// package shell, not by drivers.
package driver

import (
	"context"
	"errors"
	"time"
	"uuid"
)

// ErrAuth is wrapped by Open errors caused by rejected credentials, so the UI
// can ask for the password again.
var ErrAuth = errors.New("authentication failed")

type Kind string

const (
	Postgres Kind = "postgres"
	Oracle   Kind = "oracle"
	MongoDB  Kind = "mongodb"
	Redis    Kind = "redis"
)

// Language is the statement language of an editor tab bound to a connection.
type Language string

const (
	LangSQL   Language = "sql"
	LangMongo Language = "mongo" // mongosh-like: db.coll.find({...})
	LangRedis Language = "redis" // one command per line
)

// Config is a saved connection. Secrets are never stored here; they live in
// the OS keyring (keyed by ID) and are passed separately to Open.
type Config struct {
	ID       uuid.UUID `toml:"id"` // stable across renames; scripts bind to it
	Name     string    `toml:"name"`
	Kind     Kind      `toml:"kind"`
	Env      string    `toml:"env"` // local | dev | qa | prod
	Host     string    `toml:"host"`
	Port     int       `toml:"port"`
	User     string    `toml:"user"`
	Database string    `toml:"database"`
	ReadOnly bool      `toml:"read_only"`
	Group    string    `toml:"group"`    // explorer folder; "" = top level
	Favorite bool      `toml:"favorite"` // pinned to the top of the explorer
	// AskPassword prompts on every connect instead of using the keyring.
	AskPassword bool              `toml:"ask_password"`
	Options     map[string]string `toml:"options"`
}

type Driver interface {
	Kind() Kind
	Language() Language
	Open(ctx context.Context, cfg Config, password string) (Conn, error)
}

// Conn is one live server connection. It serves metadata to the explorer;
// editor tabs each get their own Session.
type Conn interface {
	// Children lists the child nodes of parent, or the roots when parent is nil.
	Children(ctx context.Context, parent *Node) ([]*Node, error)
	// NewSession opens a dedicated session positioned at scope. An empty
	// scope means the connection's default database/schema.
	NewSession(ctx context.Context, scope Scope) (Session, error)
	Close() error
}

// Scope is where a session runs: database (PG, Mongo), schema (PG search_path,
// Oracle CURRENT_SCHEMA) or logical db index (Redis "0".."15").
type Scope struct {
	Database string
	Schema   string
}

type NodeKind string

const (
	NodeDatabase   NodeKind = "database"
	NodeSchema     NodeKind = "schema"
	NodeFolder     NodeKind = "folder"
	NodeTable      NodeKind = "table"
	NodeView       NodeKind = "view"
	NodeColumn     NodeKind = "column"
	NodeIndex      NodeKind = "index"
	NodeFunction   NodeKind = "function"
	NodeSequence   NodeKind = "sequence"
	NodeCollection NodeKind = "collection"
	NodeKey        NodeKind = "key"
)

type Node struct {
	Path   []string // stable identity, e.g. ["app", "public", "tables", "orders"]
	Kind   NodeKind
	Name   string
	Detail string // e.g. column type, row estimate
	Leaf   bool
}

// Session is a dedicated, stateful channel (one per editor tab). Transactions,
// current database/schema, session variables, etc. live here and never leak
// between tabs.
type Session interface {
	Split(script string) []Statement
	Execute(ctx context.Context, stmt Statement) (Result, error)
	Close() error
}

type Statement struct {
	Text   string
	Offset int // byte offset in the script, for error positions
}

// Result streams rows; the UI pulls pages on demand and never loads
// everything at once.
//
// Row values are normalised by drivers to a small set the UI knows how to
// display and edit: nil (SQL NULL), bool, int64, float64, Number (exact
// decimal), string, []byte, time.Time, JSON, and []any (arrays). Anything
// else must be converted to string by the driver.
type Result interface {
	Columns() []Column
	// Next returns up to n rows; an empty slice means the result is exhausted.
	Next(ctx context.Context, n int) ([][]any, error)
	Affected() int64 // -1 when not applicable
	Close() error
}

type Column struct {
	Name     string
	Type     string
	Nullable bool
}

// Number is an exact decimal kept as text (e.g. PG numeric, Oracle NUMBER).
type Number string

// JSON is a JSON document as raw text (PG json/jsonb, Mongo documents).
type JSON string

// QueryError is a server error with enough detail to point at the script.
type QueryError struct {
	Msg      string
	Detail   string
	Hint     string
	Code     string // SQLSTATE / ORA-nnnnn / server code
	Position int    // 0-based byte offset into Statement.Text, -1 if unknown
}

func (e *QueryError) Error() string { return e.Msg }

// Message is an out-of-band server message (PG NOTICE, Oracle DBMS_OUTPUT).
type Message struct {
	Severity string
	Text     string
}

// MessageSource is implemented by sessions that collect server messages.
type MessageSource interface {
	// DrainMessages returns and clears messages received since the last call.
	DrainMessages() []Message
}

// Transactional is implemented by sessions with explicit transaction control.
type Transactional interface {
	SetAutoCommit(ctx context.Context, on bool) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	InTx() bool
}

// Editable is implemented by connections that support inline data editing.
type Editable interface {
	// RowKey returns the columns that uniquely identify a row of target
	// (primary key, ROWID, _id, ...), or nil if rows cannot be edited.
	RowKey(ctx context.Context, target *Node) ([]string, error)
	Apply(ctx context.Context, target *Node, changes []RowChange) error
}

type ChangeOp int

const (
	OpInsert ChangeOp = iota
	OpUpdate
	OpDelete
)

type RowChange struct {
	Op     ChangeOp
	Key    map[string]any
	Values map[string]any
}

// Importer is implemented by connections that can bulk load rows (V-11).
type Importer interface {
	// Import inserts rows into the given columns of target in one
	// transaction. next returns the following row and io.EOF at the end;
	// cells are nil (NULL) or text, converted by the server to the column
	// types. progress, when set, is called with the rows sent so far,
	// possibly from another goroutine.
	Import(ctx context.Context, target *Node, columns []string, next func() ([]any, error), progress func(int64)) (int64, error)
}

// Listener is implemented by connections that deliver server notifications
// (PostgreSQL LISTEN/NOTIFY).
type Listener interface {
	// Listen subscribes to channels on a dedicated session in scope, calls
	// ready once subscribed, then fn for each notification, until ctx ends
	// (which returns nil) or the session fails. ready and fn run on the
	// caller's goroutine.
	Listen(ctx context.Context, scope Scope, channels []string, ready func(), fn func(Notification)) error
}

type Notification struct {
	At      time.Time
	Channel string
	Payload string
	PID     uint32 // server process that sent it
}

// Catalog is implemented by connections that can list a scope's objects in
// one call, for autocomplete and object search.
type Catalog interface {
	Objects(ctx context.Context, scope Scope) ([]Object, error)
}

// Object is a schema object as autocomplete sees it.
type Object struct {
	Schema  string
	Name    string
	Kind    NodeKind // NodeTable, NodeView, NodeFunction, NodeSequence, ...
	Detail  string   // e.g. function signature
	Columns []Column // tables and views
}

// DataViewer builds the statement behind a table's data view (V-01..V-03).
// filter is a backend expression (SQL WHERE body, Mongo JSON filter, Redis
// pattern); orderBy lists column names, each optionally suffixed " DESC".
type DataViewer interface {
	DataQuery(target *Node, filter string, orderBy []string) (string, error)
}

// Counter is implemented by connections that count the rows of a data view
// with a filter (see DataViewer), on demand since it may be slow.
type Counter interface {
	CountRows(ctx context.Context, target *Node, filter string) (int64, error)
}

// Describer returns what the object viewer shows for a table or view (E-05..E-08).
type Describer interface {
	Describe(ctx context.Context, target *Node) (*ObjectDetails, error)
}

type ObjectDetails struct {
	Columns     []ColumnInfo
	Indexes     []DetailRow
	Constraints []DetailRow
	Triggers    []DetailRow
	Stats       []DetailRow // e.g. "Estimated rows", "Total size", "Last analyze"
	DDL         string
}

type ColumnInfo struct {
	Name, Type, Default, Comment string
	Nullable, PrimaryKey         bool
}

type DetailRow struct{ Name, Definition string }

// Explainer is implemented by sessions that can show a statement's plan (X-15).
type Explainer interface {
	Explain(ctx context.Context, stmt Statement, analyze bool) (*PlanNode, error)
}

// PlanNode is one operator of an execution plan.
type PlanNode struct {
	Op       string            // e.g. "Seq Scan", "Hash Join"
	Target   string            // relation or index, if any
	Cost     float64           // planner total cost (0 when unknown)
	Rows     float64           // planner row estimate
	Actual   float64           // actual total time in ms (analyze only, -1 otherwise)
	ActRows  float64           // actual rows (analyze only, -1 otherwise)
	Props    map[string]string // other properties worth showing (filter, index cond, buffers…)
	Children []*PlanNode
}
