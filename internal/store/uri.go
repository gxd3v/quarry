package store

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// ParseURI parses a connection URI (postgres, postgresql, mongodb,
// mongodb+srv, redis, rediss, oracle). Query parameters go to Options,
// except "password", which is returned like the userinfo password. Name and
// Env are left empty; a missing port is left at 0.
//
// MongoDB seed lists (h1:27017,h2:27017) are kept verbatim in Host with
// Port 0.
func ParseURI(s string) (cfg driver.Config, password string, err error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(s), "://")
	if !ok {
		return cfg, "", errors.New("Not a connection URI")
	}
	scheme = strings.ToLower(scheme)
	opts := map[string]string{}
	switch scheme {
	case "postgres", "postgresql":
		cfg.Kind = driver.Postgres
	case "mongodb":
		cfg.Kind = driver.MongoDB
	case "mongodb+srv":
		cfg.Kind = driver.MongoDB
		opts["srv"] = "true"
	case "redis":
		cfg.Kind = driver.Redis
	case "rediss":
		cfg.Kind = driver.Redis
		opts["tls"] = "true"
	case "oracle":
		cfg.Kind = driver.Oracle
	default:
		return cfg, "", fmt.Errorf("Unsupported URI scheme %q", scheme)
	}

	authority, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}
	tail, _, _ = strings.Cut(tail, "#")
	path, query, _ := strings.Cut(tail, "?")

	// The last '@' ends the userinfo: unescaped '@' in passwords is common.
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		user, pass, hasPass := strings.Cut(authority[:i], ":")
		if cfg.User, err = url.PathUnescape(user); err != nil {
			return cfg, "", errors.New("Invalid user name encoding")
		}
		if hasPass {
			if password, err = url.PathUnescape(pass); err != nil {
				return cfg, "", errors.New("Invalid password encoding")
			}
		}
		authority = authority[i+1:]
	}

	if cfg.Kind == driver.MongoDB && strings.Contains(authority, ",") {
		cfg.Host = authority
	} else {
		host, port, err := splitHostPort(authority)
		if err != nil {
			return cfg, "", err
		}
		cfg.Host = host
		if port != "" {
			if cfg.Port, err = strconv.Atoi(port); err != nil || cfg.Port < 1 || cfg.Port > 65535 {
				return cfg, "", fmt.Errorf("Invalid port %q", port)
			}
		}
	}
	if opts["srv"] == "true" && cfg.Port != 0 {
		return cfg, "", errors.New("mongodb+srv URIs cannot have a port")
	}

	if path, err = url.PathUnescape(strings.TrimPrefix(path, "/")); err != nil {
		return cfg, "", errors.New("Invalid path encoding")
	}
	switch cfg.Kind {
	case driver.Oracle:
		if path != "" {
			opts["service"] = path
		}
	case driver.Redis:
		if path != "" {
			if n, err := strconv.Atoi(path); err != nil || n < 0 {
				return cfg, "", fmt.Errorf("Invalid Redis database %q", path)
			}
		}
		cfg.Database = path
	default:
		cfg.Database = path
	}

	q, err := url.ParseQuery(query)
	if err != nil {
		return cfg, "", errors.New("Invalid query string")
	}
	for k, v := range q {
		if k == "password" {
			if password == "" {
				password = v[len(v)-1]
			}
			continue
		}
		opts[k] = v[len(v)-1]
	}
	if len(opts) > 0 {
		cfg.Options = opts
	}
	return cfg, password, nil
}

// splitHostPort splits "host", "host:port", "[v6]" or "[v6]:port". A bare
// IPv6 address without brackets is taken as a host.
func splitHostPort(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", errors.New("Missing ']' in host")
		}
		host, rest := s[1:end], s[end+1:]
		if rest == "" {
			return host, "", nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("Invalid host %q", s)
		}
		return host, rest[1:], nil
	}
	if strings.Count(s, ":") == 1 {
		host, port, _ = strings.Cut(s, ":")
		return host, port, nil
	}
	return s, "", nil
}

// FormatURI renders cfg as a URI without the password, suitable for display
// and copying. It is the inverse of ParseURI.
func FormatURI(cfg driver.Config) string {
	opts := make(url.Values, len(cfg.Options))
	for k, v := range cfg.Options {
		opts.Set(k, v)
	}
	var scheme, path string
	switch cfg.Kind {
	case driver.Postgres:
		scheme, path = "postgres", cfg.Database
	case driver.MongoDB:
		scheme, path = "mongodb", cfg.Database
		if opts.Get("srv") == "true" {
			scheme = "mongodb+srv"
			opts.Del("srv")
		}
	case driver.Redis:
		scheme, path = "redis", cfg.Database
		if opts.Get("tls") == "true" {
			scheme = "rediss"
			opts.Del("tls")
		}
	case driver.Oracle:
		scheme, path = "oracle", opts.Get("service")
		opts.Del("service")
	default:
		return ""
	}

	var b strings.Builder
	b.WriteString(scheme + "://")
	if cfg.User != "" {
		b.WriteString(url.User(cfg.User).String() + "@")
	}
	switch {
	case strings.Contains(cfg.Host, ","):
		b.WriteString(cfg.Host)
	case cfg.Port > 0:
		b.WriteString(net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	case strings.Contains(cfg.Host, ":"):
		b.WriteString("[" + cfg.Host + "]")
	default:
		b.WriteString(cfg.Host)
	}
	if path != "" || len(opts) > 0 {
		b.WriteString("/" + url.PathEscape(path))
	}
	if len(opts) > 0 {
		b.WriteString("?" + opts.Encode())
	}
	return b.String()
}
