package importers

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// tnsParam is one "(KEY = value)" of a descriptor; a value is either text or
// a list of nested parameters.
type tnsParam struct {
	key   string
	text  string
	items []tnsParam
}

// find returns the first parameter named key, searching depth first.
func (p tnsParam) find(key string) (tnsParam, bool) {
	for _, it := range p.items {
		if it.key == key {
			return it, true
		}
		if f, ok := it.find(key); ok {
			return f, true
		}
	}
	return tnsParam{}, false
}

func (p tnsParam) get(key string) string {
	f, _ := p.find(key)
	return f.text
}

type tnsLexer struct {
	s   string
	pos int
}

// next returns the next token: one of "(", ")", "=", ",", a word, or "" at
// the end. Comments run from # to the end of the line.
func (l *tnsLexer) next() string {
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		switch {
		case c == '#':
			for l.pos < len(l.s) && l.s[l.pos] != '\n' {
				l.pos++
			}
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.pos++
		case strings.IndexByte("()=,", c) >= 0:
			l.pos++
			return string(c)
		case c == '"' || c == '\'':
			end := strings.IndexByte(l.s[l.pos+1:], c)
			if end < 0 {
				w := l.s[l.pos+1:]
				l.pos = len(l.s)
				return w
			}
			w := l.s[l.pos+1 : l.pos+1+end]
			l.pos += end + 2
			return w
		default:
			start := l.pos
			for l.pos < len(l.s) && strings.IndexByte("()=,# \t\r\n", l.s[l.pos]) < 0 {
				l.pos++
			}
			return l.s[start:l.pos]
		}
	}
	return ""
}

func (l *tnsLexer) peek() string {
	save := l.pos
	t := l.next()
	l.pos = save
	return t
}

func (l *tnsLexer) expect(tok string) error {
	if t := l.next(); t != tok {
		return fmt.Errorf("offset %d: expected %q, got %q", l.pos, tok, t)
	}
	return nil
}

// value parses what follows "=": nested parameters or one word.
func (l *tnsLexer) value() (tnsParam, error) {
	var p tnsParam
	if l.peek() != "(" {
		if t := l.peek(); t != ")" && t != "=" && t != "," {
			p.text = l.next()
		}
		return p, nil
	}
	for l.peek() == "(" {
		l.next()
		key := strings.ToUpper(l.next())
		if err := l.expect("="); err != nil {
			return p, err
		}
		v, err := l.value()
		if err != nil {
			return p, err
		}
		if err := l.expect(")"); err != nil {
			return p, err
		}
		v.key = key
		p.items = append(p.items, v)
	}
	return p, nil
}

// ParseTNSNames reads a tnsnames.ora file. Entries are "alias[, alias] =
// descriptor"; each alias becomes a connection. IFILE includes are ignored.
func ParseTNSNames(r io.Reader) ([]Entry, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	l := &tnsLexer{s: string(data)}
	var out []Entry
	for l.peek() != "" {
		var names []string
		for {
			n := l.next()
			if n == "" || strings.IndexByte("()=,", n[0]) >= 0 {
				return nil, fmt.Errorf("offset %d: expected an alias, got %q", l.pos, n)
			}
			names = append(names, n)
			if l.peek() != "," {
				break
			}
			l.next()
		}
		if err := l.expect("="); err != nil {
			return nil, err
		}
		v, err := l.value()
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(names[0], "IFILE") {
			continue
		}
		cfg, err := tnsConfig(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", names[0], err)
		}
		for _, n := range names {
			out = append(out, Entry{Name: n, Config: cfg})
		}
	}
	return out, nil
}

// tnsConfig reads the first address and the service of a descriptor, or an
// EZConnect "host[:port]/service" string.
func tnsConfig(v tnsParam) (driver.Config, error) {
	cfg := driver.Config{Kind: driver.Oracle, Port: oracleDefaultPort}
	if v.items == nil {
		host, service, _ := strings.Cut(strings.TrimPrefix(v.text, "//"), "/")
		if h, p, ok := strings.Cut(host, ":"); ok {
			n, err := strconv.Atoi(p)
			if err != nil {
				return cfg, fmt.Errorf("invalid port %q", p)
			}
			host, cfg.Port = h, n
		}
		if host == "" || service == "" {
			return cfg, fmt.Errorf("cannot read %q", v.text)
		}
		cfg.Host, cfg.Database = host, service
		return cfg, nil
	}
	addr, ok := v.find("ADDRESS")
	if !ok {
		return cfg, fmt.Errorf("no ADDRESS")
	}
	cfg.Host = addr.get("HOST")
	if cfg.Host == "" {
		return cfg, fmt.Errorf("no HOST in ADDRESS")
	}
	if p := addr.get("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return cfg, fmt.Errorf("invalid PORT %q", p)
		}
		cfg.Port = n
	}
	if strings.EqualFold(addr.get("PROTOCOL"), "TCPS") {
		cfg.Options = map[string]string{"tls": "true"}
	}
	data, _ := v.find("CONNECT_DATA")
	switch {
	case data.get("SERVICE_NAME") != "":
		cfg.Database = data.get("SERVICE_NAME")
	case data.get("SID") != "":
		if cfg.Options == nil {
			cfg.Options = map[string]string{}
		}
		cfg.Options["sid"] = data.get("SID")
	default:
		return cfg, fmt.Errorf("no SERVICE_NAME or SID in CONNECT_DATA")
	}
	return cfg, nil
}
