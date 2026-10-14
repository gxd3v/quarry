package importers

import (
	"bufio"
	"io"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// PgpassLine is one line of a password file; any field may be "*".
type PgpassLine struct {
	Host, Port, Database, User, Password string
}

// ParsePgpass reads a libpq password file. Malformed lines are skipped, as
// libpq does.
func ParsePgpass(r io.Reader) ([]PgpassLine, error) {
	var out []PgpassLine
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := splitPgpass(line)
		if len(f) != 5 {
			continue
		}
		out = append(out, PgpassLine{Host: f[0], Port: f[1], Database: f[2], User: f[3], Password: f[4]})
	}
	return out, sc.Err()
}

// splitPgpass splits a line on unescaped colons into at most five fields,
// the last taking the rest of the line; "\:" and "\\" are unescaped.
func splitPgpass(line string) []string {
	var (
		fields []string
		b      strings.Builder
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
		case c == ':' && len(fields) < 4:
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(fields, b.String())
}

func (l PgpassLine) matches(host string, port int, db, user string) bool {
	field := func(pat, v string) bool { return pat == "*" || pat == v }
	return field(l.Host, host) && field(l.Port, strconv.Itoa(port)) && field(l.Database, db) && field(l.User, user)
}

// PgpassPassword returns the password libpq would use for cfg: the first
// matching line. The database defaults to the user name, as in libpq.
func PgpassPassword(lines []PgpassLine, cfg driver.Config) (string, bool) {
	db := cfg.Database
	if db == "" {
		db = cfg.User
	}
	port := cfg.Port
	if port == 0 {
		port = pgDefaultPort
	}
	for _, l := range lines {
		if l.matches(cfg.Host, port, db, cfg.User) {
			return l.Password, true
		}
	}
	return "", false
}

// PgpassConnections turns password file lines into connections. Lines with
// a wildcard host or user cannot name a server and are left out; a wildcard
// port means the default one and a wildcard database none.
func PgpassConnections(lines []PgpassLine) []Entry {
	var out []Entry
	for _, l := range lines {
		if l.Host == "*" || l.Host == "" || l.User == "*" || l.User == "" {
			continue
		}
		cfg := driver.Config{Kind: driver.Postgres, Host: l.Host, Port: pgDefaultPort, User: l.User}
		if l.Port != "*" {
			n, err := strconv.Atoi(l.Port)
			if err != nil || n < 1 || n > 65535 {
				continue
			}
			cfg.Port = n
		}
		if l.Database != "*" {
			cfg.Database = l.Database
		}
		out = append(out, Entry{Config: cfg, Password: l.Password, HasPassword: true})
	}
	return out
}
