// Package importers reads connections from the files other tools keep them
// in: libpq's password and service files and Oracle's tnsnames.ora.
package importers

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Format is the kind of a connection file.
type Format int

const (
	Unknown Format = iota
	Pgpass
	PgService
	TNSNames
)

const (
	pgDefaultPort     = 5432
	oracleDefaultPort = 1521
)

var tnsRe = regexp.MustCompile(`(?i)\(\s*description(_list)?\s*=`)

// Detect tells the format of a file from its name, or failing that from its
// content.
func Detect(name string, data []byte) Format {
	base := strings.ToLower(filepath.Base(name))
	switch {
	case strings.Contains(base, "pgpass"):
		return Pgpass
	case strings.Contains(base, "pg_service"):
		return PgService
	case strings.Contains(base, "tnsnames"):
		return TNSNames
	}
	if tnsRe.Match(data) {
		return TNSNames
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			return PgService
		}
		if len(splitPgpass(line)) >= 5 {
			return Pgpass
		}
		break
	}
	return Unknown
}

// PgpassPath is where libpq looks for the password file, or "" when unknown.
func PgpassPath() string {
	return pgpassPath(runtime.GOOS, homeDir(), os.Getenv)
}

// PgServicePaths are the service files libpq reads, the user's first.
func PgServicePaths() []string {
	return pgServicePaths(runtime.GOOS, homeDir(), os.Getenv)
}

// TNSNamesPaths are the tnsnames.ora files Oracle clients read, in order.
func TNSNamesPaths() []string {
	return tnsNamesPaths(os.Getenv)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func pgpassPath(goos, home string, getenv func(string) string) string {
	if p := getenv("PGPASSFILE"); p != "" {
		return p
	}
	if goos == "windows" {
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "postgresql", "pgpass.conf")
		}
		return ""
	}
	if home != "" {
		return filepath.Join(home, ".pgpass")
	}
	return ""
}

func pgServicePaths(goos, home string, getenv func(string) string) []string {
	var out []string
	switch {
	case getenv("PGSERVICEFILE") != "":
		out = append(out, getenv("PGSERVICEFILE"))
	case goos == "windows":
		if d := getenv("APPDATA"); d != "" {
			out = append(out, filepath.Join(d, "postgresql", ".pg_service.conf"))
		}
	case home != "":
		out = append(out, filepath.Join(home, ".pg_service.conf"))
	}
	if d := getenv("PGSYSCONFDIR"); d != "" {
		out = append(out, filepath.Join(d, "pg_service.conf"))
	}
	return out
}

func tnsNamesPaths(getenv func(string) string) []string {
	var out []string
	if d := getenv("TNS_ADMIN"); d != "" {
		out = append(out, filepath.Join(d, "tnsnames.ora"))
	}
	if d := getenv("ORACLE_HOME"); d != "" {
		p := filepath.Join(d, "network", "admin", "tnsnames.ora")
		if len(out) == 0 || out[0] != p {
			out = append(out, p)
		}
	}
	return out
}
