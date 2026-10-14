// Package mongo implements the MongoDB driver on top of the official Go
// driver (v2). Statements are a subset of mongosh syntax, parsed by a
// package-private lexer and parser and sent to the server as database
// commands.
//
// # Connection settings
//
// Host is one host or a comma-separated seed list ("h1,h2:27018"); entries
// without a port use Port (default 27017). Database is the default database
// (default "test"). User and the password authenticate against auth_source,
// which defaults to "admin" for SCRAM. Options:
//
//	uri=<uri>               a full mongodb:// or mongodb+srv:// URI that replaces
//	                        Host, Port and srv; the password still comes from the
//	                        keyring (a password in the URI is used only when none
//	                        is given)
//	srv=true                mongodb+srv:// with Host as the SRV name
//	auth_source=<db>        authentication database
//	auth_mechanism=<m>      SCRAM-SHA-256, SCRAM-SHA-1, MONGODB-X509, PLAIN, ...
//	replica_set=<name>      required replica set name
//	tls=true                TLS with the system roots
//	direct_connection=true  talk to the given host only (no topology discovery)
//	connect_timeout=<n>     seconds or a Go duration (default 10s)
//
// Other options pass through as URI options (authSource, readPreference,
// tlsCAFile, ...), so connections pasted as a URI keep their settings.
//
// # Explorer tree
//
// Roots are databases, Path [db] (Detail: size on disk). Below are the
// collections and views, Path [db, name], Kind NodeCollection or NodeView
// (views are leaves). Below a collection are its indexes, Path [db, coll,
// index], Kind NodeIndex, Detail the key pattern.
//
// # Statements and results
//
// See Split for statement boundaries and session.Execute for the supported
// methods. Documents are returned as rows: one column per top-level field of
// the first batch, then DocumentColumn with the whole document. Values are
// normalized as described in normalize.
package mongo

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
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
	metaTimeout    = 30 * time.Second
)

var (
	errClosed   = errors.New("connection is closed")
	errReadOnly = errors.New("connection is read-only: changes cannot be saved")
)

// quarryOptions are the Options keys interpreted by the driver itself; the
// others are passed through as URI options.
var quarryOptions = map[string]bool{
	"uri": true, "srv": true, "shell_path": true, "auth_source": true, "auth_mechanism": true,
	"replica_set": true, "tls": true, "direct_connection": true, "connect_timeout": true,
}

type mongoDriver struct{}

// New returns the MongoDB driver.
func New() driver.Driver { return mongoDriver{} }

func (mongoDriver) Kind() driver.Kind         { return driver.MongoDB }
func (mongoDriver) Language() driver.Language { return driver.LangMongo }

func (mongoDriver) Open(ctx context.Context, cfg driver.Config, password string) (driver.Conn, error) {
	opts, err := clientOptions(cfg, password)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, connectError(err, cfg, password)
	}
	c := &conn{client: client, defaultDB: cmp.Or(cfg.Database, defaultDB), readOnly: cfg.ReadOnly}
	if err := c.hello(ctx); err != nil {
		c.Close()
		return nil, connectError(err, cfg, password)
	}
	return c, nil
}

// clientOptions turns a saved connection into client options. The password
// never goes into a URI, so parse errors cannot leak it.
func clientOptions(cfg driver.Config, password string) (*options.ClientOptions, error) {
	opt := func(k string) string { return strings.TrimSpace(cfg.Options[k]) }
	uri, err := buildURI(cfg)
	if err != nil {
		return nil, err
	}
	opts := options.Client().ApplyURI(uri)
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid connection settings: %s", redact(err.Error(), password))
	}
	if v := opt("replica_set"); v != "" {
		opts.SetReplicaSet(v)
	}
	if v := opt("direct_connection"); v != "" {
		opts.SetDirect(isTrue(v))
	}
	if isTrue(opt("tls")) && opts.TLSConfig == nil {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	timeout := defaultTimeout
	if v := opt("connect_timeout"); v != "" {
		if timeout, err = parseTimeout(v); err != nil {
			return nil, err
		}
	}
	if opts.ConnectTimeout == nil || opt("connect_timeout") != "" {
		opts.SetConnectTimeout(timeout)
	}
	if opts.ServerSelectionTimeout == nil || opt("connect_timeout") != "" {
		opts.SetServerSelectionTimeout(timeout)
	}
	if opts.AppName == nil {
		opts.SetAppName(defaultAppName)
	}
	if cred, ok := credential(cfg, password, opts.Auth, uriHasOption(uri, "authSource")); ok {
		opts.SetAuth(cred)
	}
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid connection settings: %s", redact(err.Error(), password))
	}
	return opts, nil
}

// buildURI returns the uri option, or a mongodb:// URI without credentials
// built from Host, Port and the pass-through options.
func buildURI(cfg driver.Config) (string, error) {
	if u := strings.TrimSpace(cfg.Options["uri"]); u != "" {
		if !strings.HasPrefix(u, "mongodb://") && !strings.HasPrefix(u, "mongodb+srv://") {
			return "", errors.New("invalid uri option: want a mongodb:// or mongodb+srv:// URI")
		}
		return u, nil
	}
	scheme := "mongodb"
	srv := isTrue(cfg.Options["srv"])
	if srv {
		scheme = "mongodb+srv"
	}
	hosts, err := hostList(cfg.Host, cfg.Port, srv)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	for k, v := range cfg.Options {
		if !quarryOptions[k] && v != "" {
			q.Set(k, v)
		}
	}
	uri := scheme + "://" + hosts + "/"
	if len(q) > 0 {
		uri += "?" + q.Encode()
	}
	return uri, nil
}

// hostList expands Host ("h1,h2:27018,[::1]") into a URI host list.
func hostList(host string, port int, srv bool) (string, error) {
	if port == 0 {
		port = defaultPort
	}
	var out []string
	for h := range strings.SplitSeq(host, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if srv {
			out = append(out, h)
			continue
		}
		name, p := h, ""
		switch {
		case strings.HasPrefix(h, "["):
			end := strings.Index(h, "]")
			if end < 0 {
				return "", fmt.Errorf("missing ']' in host %q", h)
			}
			name, p = h[1:end], strings.TrimPrefix(h[end+1:], ":")
		case strings.Count(h, ":") == 1:
			name, p, _ = strings.Cut(h, ":")
		}
		if p == "" {
			p = strconv.Itoa(port)
		} else if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid port %q", p)
		}
		out = append(out, net.JoinHostPort(cmp.Or(name, defaultHost), p))
	}
	switch {
	case len(out) == 0 && srv:
		return "", errors.New("mongodb+srv needs a host name")
	case len(out) == 0:
		out = []string{net.JoinHostPort(defaultHost, strconv.Itoa(port))}
	case srv && len(out) > 1:
		return "", errors.New("mongodb+srv takes a single host name")
	}
	return strings.Join(out, ","), nil
}

func uriHasOption(uri, key string) bool {
	_, query, ok := strings.Cut(uri, "?")
	if !ok {
		return false
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return false
	}
	for k := range q {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// credential merges the configured user, password and auth options with
// those of the URI. It reports false when the connection does not
// authenticate.
func credential(cfg driver.Config, password string, fromURI *options.Credential, uriAuthSource bool) (options.Credential, bool) {
	var cred options.Credential
	if fromURI != nil {
		cred = *fromURI
	}
	if cfg.User != "" {
		cred.Username = cfg.User
	}
	if m := strings.TrimSpace(cfg.Options["auth_mechanism"]); m != "" {
		cred.AuthMechanism = m
	}
	if cred.Username == "" && cred.AuthMechanism == "" {
		return cred, false
	}
	if password != "" {
		cred.Password, cred.PasswordSet = password, true
	}
	switch src := strings.TrimSpace(cfg.Options["auth_source"]); {
	case src != "":
		cred.AuthSource = src
	case uriAuthSource:
	case cred.AuthMechanism == "" || strings.HasPrefix(strings.ToUpper(cred.AuthMechanism), "SCRAM"):
		cred.AuthSource = "admin"
	}
	return cred, true
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "require", "required":
		return true
	}
	return false
}

func parseTimeout(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("invalid connect_timeout %q", v)
}

// conn is one MongoDB client. The client pools connections, so explorer
// calls and every session share it; sessions only keep state (current
// database, server session, transaction) per tab.
type conn struct {
	client    *mongo.Client
	defaultDB string
	readOnly  bool
	closed    atomic.Bool
	// replicaSet: a replica set member or mongos, so transactions work.
	replicaSet bool
}

// hello checks the server answers and learns its topology.
func (c *conn) hello(ctx context.Context) error {
	var reply struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&reply)
	if err != nil {
		return err
	}
	c.replicaSet = reply.SetName != "" || reply.Msg == "isdbgrid"
	return nil
}

func (c *conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return c.client.Disconnect(ctx)
}

func (c *conn) check() error {
	if c.closed.Load() {
		return errClosed
	}
	return nil
}

// bound gives metadata calls a deadline when ctx has none.
func bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, metaTimeout)
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
	for _, p := range []string{password, url.QueryEscape(password), url.PathEscape(password)} {
		s = strings.ReplaceAll(s, p, "****")
	}
	return s
}

// connectError rewrites connection failures into short messages. Rejected
// credentials wrap driver.ErrAuth.
func connectError(err error, cfg driver.Config, password string) error {
	if fe, ok := errors.AsType[*friendlyError](err); ok {
		return fe
	}
	where := cmp.Or(cfg.Host, defaultHost)
	if cfg.Options["uri"] != "" {
		where = "the server"
	} else if cfg.Port != 0 && !strings.Contains(where, ",") {
		where = net.JoinHostPort(where, strconv.Itoa(cfg.Port))
	}
	if isAuthError(err) {
		msg := "authentication failed"
		if cfg.User != "" {
			msg = fmt.Sprintf("authentication failed for user %q", cfg.User)
		}
		return &friendlyError{msg: msg, err: fmt.Errorf("%w: %s", driver.ErrAuth, redact(err.Error(), password))}
	}
	if errors.Is(err, context.Canceled) {
		return &friendlyError{msg: "connection attempt canceled", err: err}
	}
	if se, ok := errors.AsType[mongo.ServerError](err); ok && !mongo.IsNetworkError(err) {
		return &friendlyError{msg: redact(serverMessage(se), password), err: err}
	}
	if sse, ok := errors.AsType[topology.ServerSelectionError](err); ok {
		var causes []string
		for _, s := range sse.Desc.Servers {
			if s.LastError != nil {
				causes = append(causes, innermost(s.LastError).Error())
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
	return &friendlyError{msg: "cannot connect to " + where + ": " + redact(innermost(err).Error(), password), err: err}
}

// isAuthError recognises rejected credentials (AuthenticationFailed, code
// 18), also when they come wrapped in a handshake or server selection error.
func isAuthError(err error) bool {
	if se, ok := errors.AsType[mongo.ServerError](err); ok && se.HasErrorCode(18) {
		return true
	}
	if sse, ok := errors.AsType[topology.ServerSelectionError](err); ok {
		for _, s := range sse.Desc.Servers {
			if s.LastError != nil && isAuthError(s.LastError) {
				return true
			}
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "AuthenticationFailed") || strings.Contains(msg, "auth error")
}

func serverMessage(se mongo.ServerError) string {
	if ce, ok := se.(mongo.CommandError); ok && ce.Message != "" {
		return ce.Message
	}
	return se.Error()
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

// queryError converts a failure to *driver.QueryError: parse errors keep
// their position, server errors have Code set to the server's code name
// (or number) and no position. Other errors pass through.
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
	if mongo.IsNetworkError(err) {
		return &friendlyError{msg: "connection lost: " + innermost(err).Error(), err: err}
	}
	if we, ok := errors.AsType[mongo.WriteException](err); ok {
		if len(we.WriteErrors) > 0 {
			w := we.WriteErrors[0]
			qe := &driver.QueryError{Msg: w.Message, Code: codeName(w.Code, ""), Position: -1}
			if len(w.Details) > 0 {
				qe.Detail = extJSON(w.Details)
			}
			return qe
		}
		if wce := we.WriteConcernError; wce != nil {
			return &driver.QueryError{Msg: "write concern error: " + wce.Message, Code: codeName(wce.Code, wce.Name), Position: -1}
		}
	}
	if ce, ok := errors.AsType[mongo.CommandError](err); ok && (ce.Code != 0 || ce.Name != "") {
		return &driver.QueryError{Msg: cmp.Or(ce.Message, ce.Error()), Code: codeName(int(ce.Code), ce.Name), Position: -1}
	}
	if se, ok := errors.AsType[mongo.ServerError](err); ok {
		codes := se.ErrorCodes()
		code := ""
		if len(codes) > 0 {
			code = codeName(codes[0], "")
		}
		return &driver.QueryError{Msg: se.Error(), Code: code, Position: -1}
	}
	return fmt.Errorf("mongodb: %w", err)
}

// codeNames names the server error codes that write errors report without a
// codeName.
var codeNames = map[int]string{
	2: "BadValue", 9: "FailedToParse", 11: "UserNotFound", 13: "Unauthorized", 14: "TypeMismatch",
	18: "AuthenticationFailed", 20: "IllegalOperation", 26: "NamespaceNotFound", 27: "IndexNotFound",
	40: "ConflictingUpdateOperators", 48: "NamespaceExists", 50: "MaxTimeMSExpired",
	52: "DollarPrefixedFieldName", 55: "InvalidDBRef", 56: "EmptyFieldName", 57: "DottedFieldName",
	61: "ShardKeyNotFound", 66: "ImmutableField", 67: "CannotCreateIndex", 72: "InvalidOptions",
	85: "IndexOptionsConflict", 86: "IndexKeySpecsConflict", 112: "WriteConflict",
	121: "DocumentValidationFailure", 166: "CommandNotSupportedOnView", 251: "NoSuchTransaction",
	11000: "DuplicateKey", 11600: "InterruptedAtShutdown", 11601: "Interrupted",
	17280: "KeyTooLong",
}

func codeName(code int, name string) string {
	if name != "" {
		return name
	}
	if n, ok := codeNames[code]; ok {
		return n
	}
	if code == 0 {
		return ""
	}
	return strconv.Itoa(code)
}
