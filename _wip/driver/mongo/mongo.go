// Package mongo implements the MongoDB driver on top of the official Go
// driver. Statements are a subset of mongosh syntax (db.coll.find({...}),
// show dbs, use x, ...) parsed by a package-private lexer and parser.
package mongo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"

	"github.com/gxd3v/quarry/internal/driver"
)

const (
	defaultHost    = "localhost"
	defaultPort    = 27017
	defaultDB      = "test"
	defaultTimeout = 10 * time.Second
	defaultAppName = "quarry"
	closeTimeout   = 3 * time.Second
)

var (
	errClosed   = errors.New("connection is closed")
	errReadOnly = errors.New("connection is read-only: changes cannot be saved")
)

type mongoDriver struct{}

// New returns the MongoDB driver.
func New() driver.Driver { return mongoDriver{} }

func (mongoDriver) Kind() driver.Kind         { return driver.MongoDB }
func (mongoDriver) Language() driver.Language { return driver.LangMongo }

func (mongoDriver) Open(ctx context.Context, cfg driver.Config, password string) (driver.Conn, error) {
	uri, err := buildURI(cfg, password)
	if err != nil {
		return nil, err
	}
	opts := options.Client().ApplyURI(uri)
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid connection settings: %s", redact(err.Error(), password))
	}
	if opts.ConnectTimeout == nil {
		opts.SetConnectTimeout(defaultTimeout)
	}
	if opts.ServerSelectionTimeout == nil {
		opts.SetServerSelectionTimeout(defaultTimeout)
	}
	if opts.AppName == nil {
		opts.SetAppName(defaultAppName)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, connectError(err, cfg, password)
	}
	c := &conn{client: client, cfg: cfg, defaultDB: cmp.Or(cfg.Database, defaultDB), readOnly: cfg.ReadOnly}
	if err := c.hello(ctx); err != nil {
		c.Close()
		return nil, connectError(err, cfg, password)
	}
	return c, nil
}

// buildURI turns a saved connection into a mongodb:// URI. Host may be a
// comma-separated seed list; Options["srv"]="true" selects mongodb+srv; the
// other options become URI query parameters (authSource, replicaSet, tls,
// directConnection, ...).
func buildURI(cfg driver.Config, password string) (string, error) {
	srv := strings.EqualFold(cfg.Options["srv"], "true")
	scheme := "mongodb"
	if srv {
		scheme = "mongodb+srv"
	}
	host := strings.TrimSpace(cmp.Or(cfg.Host, defaultHost))
	var hosts string
	switch {
	case strings.Contains(host, ","):
		var list []string
		for h := range strings.SplitSeq(host, ",") {
			if h = strings.TrimSpace(h); h != "" {
				list = append(list, h)
			}
		}
		if len(list) == 0 {
			return "", errors.New("invalid host list")
		}
		if srv {
			return "", errors.New("mongodb+srv takes a single host name")
		}
		hosts = strings.Join(list, ",")
	case srv:
		if cfg.Port != 0 {
			return "", errors.New("mongodb+srv connections cannot have a port")
		}
		hosts = host
	default:
		port := cfg.Port
		if port == 0 {
			if h, p, err := net.SplitHostPort(host); err == nil {
				host = h
				if port, err = strconv.Atoi(p); err != nil {
					return "", fmt.Errorf("invalid port %q", p)
				}
			} else {
				port = defaultPort
			}
		}
		hosts = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
	}
	var b strings.Builder
	b.WriteString(scheme + "://")
	if cfg.User != "" {
		if password != "" {
			b.WriteString(url.UserPassword(cfg.User, password).String())
		} else {
			b.WriteString(url.User(cfg.User).String())
		}
		b.WriteByte('@')
	}
	b.WriteString(hosts)
	b.WriteString("/")
	b.WriteString(url.PathEscape(cfg.Database))
	q := url.Values{}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range cfg.Options {
			if !yield(k) {
				return
			}
		}
	})
	for _, k := range keys {
		if k == "srv" || cfg.Options[k] == "" {
			continue
		}
		q.Set(k, cfg.Options[k])
	}
	if len(q) > 0 {
		b.WriteString("?" + q.Encode())
	}
	return b.String(), nil
}

// conn is one MongoDB client. The client pools connections, so explorer
// calls and sessions share it.
type conn struct {
	client    *mongo.Client
	cfg       driver.Config
	defaultDB string
	readOnly  bool
	closed    atomic.Bool

	// Learned from hello at Open.
	replicaSet bool // replica set member or mongos: transactions available
	version    string

	sessMu   sync.Mutex
	sessions map[*session]struct{}
}

// hello pings the server and learns its topology.
func (c *conn) hello(ctx context.Context) error {
	var reply struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&reply); err != nil {
		// Very old servers only know isMaster.
		var ce mongo.CommandError
		if !errors.As(err, &ce) {
			return err
		}
		if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "isMaster", Value: 1}}).Decode(&reply); err != nil {
			return err
		}
	}
	c.replicaSet = reply.SetName != "" || reply.Msg == "isdbgrid"
	var bi struct {
		Version string `bson:"version"`
	}
	if c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi) == nil {
		c.version = bi.Version
	}
	return nil
}

func (c *conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.sessMu.Lock()
	sessions := c.sessions
	c.sessions = nil
	c.sessMu.Unlock()
	for s := range sessions {
		s.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return c.client.Disconnect(ctx)
}

// database returns db, or the connection's default database.
func (c *conn) database(name string) *mongo.Database {
	return c.client.Database(cmp.Or(name, c.defaultDB))
}

// friendlyError is an error with a message fit for a status bar; the
// original stays reachable with errors.As.
type friendlyError struct {
	msg string
	err error
}

func (e *friendlyError) Error() string { return e.msg }
func (e *friendlyError) Unwrap() error { return e.err }

func redact(s, password string) string {
	if password == "" {
		return s
	}
	s = strings.ReplaceAll(s, password, "****")
	return strings.ReplaceAll(s, url.QueryEscape(password), "****")
}

// connectError rewrites driver connection failures into short messages.
func connectError(err error, cfg driver.Config, password string) error {
	where := cmp.Or(cfg.Host, defaultHost)
	if cfg.Port != 0 && !strings.Contains(where, ",") {
		where = net.JoinHostPort(where, strconv.Itoa(cfg.Port))
	}
	if errors.Is(err, context.Canceled) {
		return &friendlyError{msg: "connection attempt canceled", err: err}
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 18:
			return &friendlyError{msg: fmt.Sprintf("authentication failed for user %q", cfg.User), err: err}
		case 13:
			return &friendlyError{msg: "not authorized: " + ce.Message, err: err}
		}
		if ce.Message != "" {
			return &friendlyError{msg: redact(ce.Message, password), err: err}
		}
	}
	var sse topology.ServerSelectionError
	if errors.As(err, &sse) {
		var causes []string
		for _, s := range sse.Desc.Servers {
			if s.LastError != nil {
				causes = append(causes, fmt.Sprintf("%s: %s", s.Addr, innermost(s.LastError)))
			}
		}
		if len(causes) > 0 {
			return &friendlyError{msg: "cannot connect to " + where + ": " + redact(strings.Join(causes, "; "), password), err: err}
		}
		return &friendlyError{msg: "timed out connecting to " + where + " (no reachable server)", err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) {
		return &friendlyError{msg: "timed out connecting to " + where, err: err}
	}
	msg := redact(err.Error(), password)
	if strings.Contains(msg, "auth error") || strings.Contains(msg, "Authentication failed") {
		return &friendlyError{msg: fmt.Sprintf("authentication failed for user %q", cfg.User), err: err}
	}
	return &friendlyError{msg: "cannot connect to " + where + ": " + msg, err: err}
}

func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// queryError converts server errors to *driver.QueryError; parse errors
// keep their position, server errors have none.
func queryError(err error) error {
	if err == nil {
		return nil
	}
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		return qe
	}
	if pe, ok := errors.AsType[*parseError](err); ok {
		return &driver.QueryError{Msg: pe.msg, Position: pe.pos}
	}
	if fe, ok := errors.AsType[*friendlyError](err); ok {
		return fe
	}
	if errors.Is(err, errClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var we mongo.WriteException
	if errors.As(err, &we) {
		if len(we.WriteErrors) > 0 {
			w := we.WriteErrors[0]
			qe := &driver.QueryError{Msg: w.Message, Code: strconv.Itoa(w.Code), Position: -1}
			if len(we.WriteErrors) > 1 {
				qe.Detail = fmt.Sprintf("%d write errors; the first was at index %d", len(we.WriteErrors), w.Index)
			}
			return qe
		}
		if we.WriteConcernError != nil {
			return &driver.QueryError{Msg: "write concern error: " + we.WriteConcernError.Message,
				Code: strconv.Itoa(we.WriteConcernError.Code), Position: -1}
		}
	}
	var be mongo.BulkWriteException
	if errors.As(err, &be) {
		if len(be.WriteErrors) > 0 {
			w := be.WriteErrors[0]
			return &driver.QueryError{Msg: w.Message, Code: strconv.Itoa(w.Code), Position: -1,
				Detail: fmt.Sprintf("operation %d failed", w.Index)}
		}
		if be.WriteConcernError != nil {
			return &driver.QueryError{Msg: "write concern error: " + be.WriteConcernError.Message,
				Code: strconv.Itoa(be.WriteConcernError.Code), Position: -1}
		}
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) && (ce.Code != 0 || ce.Name != "") {
		qe := &driver.QueryError{Msg: ce.Message, Code: strconv.Itoa(int(ce.Code)), Detail: ce.Name, Position: -1}
		if qe.Msg == "" {
			qe.Msg = ce.Error()
		}
		return qe
	}
	if mongo.IsNetworkError(err) {
		return &friendlyError{msg: "connection lost: " + innermost(err).Error(), err: err}
	}
	return fmt.Errorf("mongodb: %w", err)
}
