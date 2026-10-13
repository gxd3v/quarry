package shell

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	// oraPlainUser is a user name SQL*Plus accepts unquoted, including the
	// proxy form user[target].
	oraPlainUser = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]*(\[[A-Za-z][A-Za-z0-9_$#]*\])?$`)
	oraRoles     = []string{"SYSDBA", "SYSOPER", "SYSASM", "SYSBACKUP", "SYSDG", "SYSKM", "SYSRAC"}
)

// SQLcl/SQL*Plus start with /nolog and run a temporary login script that
// connects and sets the current schema, so the password only lives in the
// 0600 script.
func oracle(p *plan, cfg driver.Config, password string, scope driver.Scope) error {
	if strings.ContainsAny(password, "\"\r\n") {
		return errors.New("Oracle passwords cannot contain double quotes or line breaks")
	}
	target, err := oraTarget(cfg)
	if err != nil {
		return err
	}
	logon := "/"
	if cfg.User != "" {
		if logon, err = oraUser(cfg.User); err != nil {
			return err
		}
		if password != "" {
			logon += `/"` + password + `"`
		}
	}
	var role string
	if r := strings.ToUpper(strings.TrimSpace(cfg.Options["role"])); r != "" {
		if !slices.Contains(oraRoles, r) {
			return fmt.Errorf("unknown Oracle role %q", cfg.Options["role"])
		}
		role = " AS " + r
	}

	var b strings.Builder
	// ECHO off keeps the CONNECT line (and password) off the screen even if
	// glogin.sql enabled it; DEFINE off stops '&' from being substituted.
	b.WriteString("SET ECHO OFF\nSET DEFINE OFF\n")
	fmt.Fprintf(&b, "CONNECT %s@%s%s\n", logon, target, role)
	b.WriteString("SET DEFINE ON\n")
	if scope.Schema != "" {
		if strings.ContainsAny(scope.Schema, "\"\r\n") {
			return fmt.Errorf("invalid schema name %q", scope.Schema)
		}
		fmt.Fprintf(&b, "ALTER SESSION SET CURRENT_SCHEMA = \"%s\";\n", scope.Schema)
	}
	f, err := p.writeSecret("login.sql", b.String())
	if err != nil {
		return err
	}
	p.args = []string{"-L", "/nolog", oraScriptArg(f)}

	user := cmp.Or(cfg.User, "/")
	p.display = []string{user + "@" + target + role}
	return nil
}

func oraUser(u string) (string, error) {
	if oraPlainUser.MatchString(u) {
		return u, nil
	}
	if strings.ContainsAny(u, "\"\r\n") {
		return "", fmt.Errorf("invalid Oracle user name %q", u)
	}
	return `"` + u + `"`, nil
}

// oraTarget is the connect identifier: a TNS alias (option tns), a SID
// descriptor (option sid) or an easy-connect service name (option service,
// else Database). Option tls=true switches to TCPS.
func oraTarget(cfg driver.Config) (string, error) {
	o := cfg.Options
	for _, k := range []string{"tns", "sid", "service"} {
		if strings.ContainsAny(o[k], " \t\r\n\"'") {
			return "", fmt.Errorf("invalid Oracle %s %q", k, o[k])
		}
	}
	if strings.ContainsAny(cfg.Host+cfg.Database, " \t\r\n\"'") {
		return "", errors.New("invalid Oracle host or service name")
	}
	if o["tns"] != "" {
		return o["tns"], nil
	}
	host := cmp.Or(cfg.Host, "localhost")
	port := strconv.Itoa(cmp.Or(cfg.Port, 1521))
	tls := o["tls"] == "true"
	if sid := o["sid"]; sid != "" {
		proto := "TCP"
		if tls {
			proto = "TCPS"
		}
		return fmt.Sprintf("(DESCRIPTION=(ADDRESS=(PROTOCOL=%s)(HOST=%s)(PORT=%s))(CONNECT_DATA=(SID=%s)))", proto, host, port, sid), nil
	}
	t := "//" + net.JoinHostPort(host, port)
	if tls {
		t = "tcps:" + t
	}
	if svc := cmp.Or(o["service"], cfg.Database); svc != "" {
		t += "/" + svc
	}
	return t, nil
}

// oraScriptArg is the @script argument. Both clients split their command line
// on spaces, so a path with spaces is shortened (Windows 8.3 names) or quoted.
func oraScriptArg(path string) string {
	if strings.ContainsRune(path, ' ') {
		path = shortPath(path)
	}
	if strings.ContainsRune(path, ' ') {
		return `@"` + path + `"`
	}
	return "@" + path
}
