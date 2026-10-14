package importers

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// Entry is a connection read from a file. Config has no ID or Env.
type Entry struct {
	Name        string // service or TNS alias; "" for password file lines
	Config      driver.Config
	Password    string
	HasPassword bool
}

// pgServiceOptions are the libpq keys kept as connection options.
var pgServiceOptions = map[string]bool{
	"sslmode": true, "sslrootcert": true, "sslcert": true, "sslkey": true,
	"application_name": true, "connect_timeout": true,
}

// ParsePgService reads a libpq connection service file.
func ParsePgService(r io.Reader) ([]Entry, error) {
	var (
		out []Entry
		cur *Entry
	)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: unterminated section %q", n, line)
			}
			out = append(out, Entry{Name: strings.TrimSpace(line[1 : len(line)-1]), Config: driver.Config{Kind: driver.Postgres, Port: pgDefaultPort}})
			cur = &out[len(out)-1]
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key=value, got %q", n, line)
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: %q is outside a [service] section", n, line)
		}
		key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
		cfg := &cur.Config
		switch key {
		case "host":
			cfg.Host = val
		case "hostaddr":
			if cfg.Host == "" {
				cfg.Host = val
			}
		case "port":
			p, err := strconv.Atoi(val)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("line %d: invalid port %q", n, val)
			}
			cfg.Port = p
		case "dbname":
			cfg.Database = val
		case "user":
			cfg.User = val
		case "password":
			cur.Password, cur.HasPassword = val, true
		default:
			if pgServiceOptions[key] {
				if cfg.Options == nil {
					cfg.Options = map[string]string{}
				}
				cfg.Options[key] = val
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Config.Host == "" {
			out[i].Config.Host = "localhost"
		}
	}
	return out, nil
}
