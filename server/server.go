// Package server exposes the Connect API to the webview over a loopback HTTP
// server guarded by a per-launch bearer token.
//
// The Wails v2 AssetServer is not used: on Windows it buffers the whole
// response body before handing it to WebView2, which breaks server streaming.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
)

type Server struct {
	URL   string // e.g. http://127.0.0.1:53211
	Token string
	srv   *http.Server
}

// Start listens on a random loopback port and serves h behind the token check
// and CORS for the webview origin.
func Start(h http.Handler) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		URL:   "http://" + ln.Addr().String(),
		Token: rand.Text(),
	}
	s.srv = &http.Server{
		Handler:           cors(auth(s.Token, h)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api server stopped", "err", err)
		}
	}()
	return s, nil
}

// Close stops the server, cancelling in-flight streams.
func (s *Server) Close() error { return s.srv.Close() }

// HandlerOptions are the options every Connect handler is mounted with.
func HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithRecover(func(_ context.Context, spec connect.Spec, _ http.Header, p any) error {
			slog.Error("panic in handler", "procedure", spec.Procedure, "panic", p)
			return connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}),
		connect.WithInterceptors(logging{}),
	}
}

func auth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var (
	allowMethods  = strings.Join(connectcors.AllowedMethods(), ", ")
	allowHeaders  = strings.Join(append(connectcors.AllowedHeaders(), "Authorization"), ", ")
	exposeHeaders = strings.Join(connectcors.ExposedHeaders(), ", ")
)

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !allowedOrigin(origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", allowMethods)
			h.Set("Access-Control-Allow-Headers", allowHeaders)
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("Access-Control-Expose-Headers", exposeHeaders)
		next.ServeHTTP(w, r)
	})
}

// allowedOrigin accepts the Wails webview origins (wails://wails on Linux and
// macOS, http://wails.localhost on Windows) and the loopback pages used by
// `wails dev`. The bearer token, not CORS, is what keeps other pages out.
func allowedOrigin(origin string) bool {
	switch origin {
	case "wails://wails", "wails://wails.localhost", "http://wails.localhost", "https://wails.localhost":
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

type logging struct{}

func (logging) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		res, err := next(ctx, req)
		logCall(req.Spec().Procedure, start, err)
		return res, err
	}
}

func (logging) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (logging) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		logCall(conn.Spec().Procedure, start, err)
		return err
	}
}

func logCall(procedure string, start time.Time, err error) {
	if err != nil && !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
		slog.Warn("rpc", "procedure", procedure, "elapsed", time.Since(start), "err", err)
		return
	}
	slog.Debug("rpc", "procedure", procedure, "elapsed", time.Since(start))
}
