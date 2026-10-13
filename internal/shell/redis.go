package shell

import (
	"cmp"
	"fmt"
	"strconv"

	"github.com/gxd3v/quarry/internal/driver"
)

// redis-cli takes the connection as flags and the password from
// REDISCLI_AUTH, which it reads when neither -a nor --pass is given.
func redis(p *plan, cfg driver.Config, password string, scope driver.Scope) error {
	cluster := cfg.Options["cluster"] == "true"
	if cfg.Host != "" {
		p.args = append(p.args, "-h", cfg.Host)
	}
	if cfg.Port > 0 {
		p.args = append(p.args, "-p", strconv.Itoa(cfg.Port))
	}
	if db := cmp.Or(scope.Database, cfg.Database); db != "" && !cluster {
		if n, err := strconv.Atoi(db); err != nil || n < 0 {
			return fmt.Errorf("invalid Redis database %q", db)
		}
		p.args = append(p.args, "-n", db)
	}
	if cfg.Options["tls"] == "true" {
		p.args = append(p.args, "--tls")
	}
	if cfg.User != "" {
		p.args = append(p.args, "--user", cfg.User)
	}
	if cluster {
		p.args = append(p.args, "-c")
	}
	p.drop = []string{"REDISCLI_AUTH"}
	if password != "" {
		p.setenv("REDISCLI_AUTH", password)
	}
	p.display = p.args
	return nil
}
