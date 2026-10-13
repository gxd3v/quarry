package shell

import (
	"cmp"
	"errors"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// pgVars are inherited libpq variables that could redirect or authenticate
// the connection differently from the saved one.
var pgVars = []string{
	"PGHOST", "PGHOSTADDR", "PGPORT", "PGUSER", "PGDATABASE", "PGPASSWORD",
	"PGPASSFILE", "PGSERVICE", "PGOPTIONS", "PGAPPNAME", "PGSSLMODE",
	"PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY",
}

// psql connects through libpq environment variables and a temporary pgpass
// file, so neither argv nor the environment holds the password.
func postgres(p *plan, cfg driver.Config, password string, scope driver.Scope) error {
	host := cmp.Or(cfg.Host, "localhost")
	port := strconv.Itoa(cmp.Or(cfg.Port, 5432))
	db := cmp.Or(scope.Database, cfg.Database, "postgres")

	p.drop = pgVars
	p.setenv("PGHOST", host)
	p.setenv("PGPORT", port)
	if cfg.User != "" {
		p.setenv("PGUSER", cfg.User)
	}
	p.setenv("PGDATABASE", db)
	p.setenv("PGAPPNAME", "quarry-shell")
	for _, o := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey"} {
		if v := cfg.Options[o]; v != "" {
			p.setenv("PG"+strings.ToUpper(o), v)
		}
	}

	if password != "" {
		pass, err := pgpass(host, port, cfg.User, password)
		if err != nil {
			return err
		}
		f, err := p.writeSecret("pgpass", pass)
		if err != nil {
			return err
		}
		p.setenv("PGPASSFILE", f)
	}

	var opts []string
	if scope.Schema != "" {
		opts = append(opts, "-c", "search_path="+pgIdent(scope.Schema)+",public")
	}
	if cfg.ReadOnly {
		opts = append(opts, "-c", "default_transaction_read_only=on")
		p.args = append(p.args, "-v", "ON_ERROR_STOP=1")
	}
	if len(opts) > 0 {
		p.setenv("PGOPTIONS", pgOptions(opts))
	}

	p.display = append(p.display, "-h", host, "-p", port)
	if cfg.User != "" {
		p.display = append(p.display, "-U", cfg.User)
	}
	p.display = append(p.display, db)
	return nil
}

// pgpass renders pgpass lines for each host of a (possibly multi-host)
// PGHOST, matching any database so \c keeps working.
func pgpass(host, port, user, password string) (string, error) {
	if strings.ContainsAny(user+password, "\r\n") {
		return "", errors.New("user names and passwords with line breaks cannot be passed to psql")
	}
	u := "*"
	if user != "" {
		u = pgpassEscape(user)
	}
	var b strings.Builder
	for h := range strings.SplitSeq(host, ",") {
		b.WriteString(pgpassEscape(strings.TrimSpace(h)) + ":" + port + ":*:" + u + ":" + pgpassEscape(password) + "\n")
	}
	return b.String(), nil
}

func pgpassEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(s)
}

// pgIdent quotes a schema name for search_path.
func pgIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgOptions joins backend arguments as libpq parses PGOPTIONS: whitespace
// separates arguments, and a backslash escapes whitespace or itself.
func pgOptions(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		for _, r := range a {
			switch r {
			case '\\', ' ', '\t', '\n', '\r', '\f', '\v':
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
