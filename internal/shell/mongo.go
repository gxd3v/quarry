package shell

import (
	"cmp"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// mongoInternal are Quarry options that are not MongoDB URI options.
var mongoInternal = map[string]bool{"srv": true, "shell_path": true}

// mongosh (or the legacy mongo shell) starts without a connection and runs a
// temporary script that connects with the full URI.
func mongo(p *plan, cfg driver.Config, password string, scope driver.Scope) error {
	uri, err := json.Marshal(mongoURI(cfg, password, scope))
	if err != nil {
		return err
	}
	f, err := p.writeSecret("login.js", "db = connect("+string(uri)+");\n")
	if err != nil {
		return err
	}
	p.args = []string{"--nodb", "--shell", f}
	p.display = []string{mongoURI(cfg, "", scope)}
	return nil
}

// mongoURI builds the connection string: seed lists and SRV hosts are taken
// from Host, other options pass through as query parameters.
func mongoURI(cfg driver.Config, password string, scope driver.Scope) string {
	scheme := "mongodb"
	if cfg.Options["srv"] == "true" {
		scheme = "mongodb+srv"
	}
	var userinfo string
	switch {
	case cfg.User != "" && password != "":
		userinfo = url.UserPassword(cfg.User, password).String() + "@"
	case cfg.User != "":
		userinfo = url.User(cfg.User).String() + "@"
	}

	host := cmp.Or(cfg.Host, "localhost")
	switch {
	case strings.Contains(host, ","), scheme == "mongodb+srv":
	case cfg.Port > 0:
		host = net.JoinHostPort(host, strconv.Itoa(cfg.Port))
	case strings.Contains(host, ":"):
		host = "[" + host + "]"
	}

	q := url.Values{}
	hasApp := false
	for k, v := range cfg.Options {
		if mongoInternal[k] {
			continue
		}
		hasApp = hasApp || strings.EqualFold(k, "appName")
		q.Set(k, v)
	}
	if !hasApp {
		q.Set("appName", "quarry-shell")
	}
	db := cmp.Or(scope.Database, cfg.Database)
	return scheme + "://" + userinfo + host + "/" + url.PathEscape(db) + "?" + q.Encode()
}
