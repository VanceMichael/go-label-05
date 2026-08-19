package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go-base/migrations"
	"strings"
)

type Database struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Database, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 8
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db := &Database{Pool: pool}
	if err = db.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err = db.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}
func (d *Database) Close() { d.Pool.Close() }
func (d *Database) Ping(ctx context.Context) error {
	if err := d.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
func (d *Database) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
func (d *Database) Migrate(ctx context.Context) error {
	all, err := migrations.All()
	if err != nil {
		return err
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	return d.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x484552444359434c)); err != nil {
			return err
		}
		for _, migration := range all {
			var applied bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", migration.Version).Scan(&applied); err != nil {
				return err
			}
			if applied {
				continue
			}
			if _, err := tx.Exec(ctx, migration.SQL); err != nil {
				return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", migration.Version); err != nil {
				return err
			}
		}
		return nil
	})
}
