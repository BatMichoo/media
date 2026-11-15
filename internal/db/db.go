// Package db is the media app's data access: downloads, links, progress and
// preferences in Postgres.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/BatMichoo/media/internal/config"
	"github.com/BatMichoo/media/internal/db/migrations"
	_ "github.com/lib/pq" // the "postgres" driver
	"github.com/pressly/goose/v3"
)

// DB wraps the connection pool.
type DB struct {
	SQL *sql.DB
}

// Open connects (retrying for up to 30 seconds while Postgres starts),
// and brings the schema up to date.
func Open(ctx context.Context, cfg config.DBConfig) (*DB, error) {
	conn, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	d := &DB{SQL: conn}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = d.Ping(ctx)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("reach database: %w", err)
	}
	if err := Migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Migrate applies the embedded migrations.
func Migrate(ctx context.Context, conn *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if err := goose.UpContext(ctx, conn, "."); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	return nil
}

// Ping checks the database answers.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return d.SQL.PingContext(ctx)
}

// Close closes the pool.
func (d *DB) Close() error { return d.SQL.Close() }
