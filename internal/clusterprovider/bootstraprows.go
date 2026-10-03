package clusterprovider

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const bootstrapRowPattern = "/bootstrap/%"

var ErrNoPsql = errors.New(
	"clusterprovider: reading whether the store already holds k3s's bootstrap row needs psql, " +
		"the same binary this package already uses to create the store, and it is not on PATH. " +
		"The answer decides whether k3s exits at boot with \"bootstrap data already found and " +
		"encrypted with different token\", so it is refused rather than guessed")

func (p *Postgres) psql(ctx context.Context, sql string) (string, error) {
	bin := "psql"
	if p.spec.BinDir != "" {
		bin = p.spec.BinDir + "/psql"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%w (%s)", ErrNoPsql, bin)
	}
	cmd := exec.CommandContext(ctx, bin, p.DSN(), "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("clusterprovider: %s against this cluster's store: %w: %s",
			sql, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Postgres) HasBootstrapRow(ctx context.Context) (bool, error) {
	n, err := p.psql(ctx, "select count(*) from kine where name like '"+bootstrapRowPattern+"'")
	if err != nil {
		return false, err
	}
	count, err := strconv.Atoi(n)
	if err != nil {
		return false, fmt.Errorf("clusterprovider: %q is not a row count: %w", n, err)
	}
	return count > 0, nil
}

func (p *Postgres) DeleteBootstrapRows(ctx context.Context) error {
	if _, err := p.psql(ctx, "delete from kine where name like '"+bootstrapRowPattern+"'"); err != nil {
		return err
	}
	return nil
}
