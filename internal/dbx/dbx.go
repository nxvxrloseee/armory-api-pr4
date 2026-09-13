package dbx

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// ApplySchema runs schema.sql statement by statement inside one transaction.
// Every statement in the file is written to be safe to repeat (CREATE TABLE
// IF NOT EXISTS, ON CONFLICT DO NOTHING), so this doubles as the project's
// migration mechanism — there is exactly one schema version, applied fresh
// on every start.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, stmt := range splitStatements(schemaSQL) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema statement failed: %w\n%s", err, stmt)
		}
	}
	return tx.Commit(ctx)
}

// Reset wipes every table and reapplies schema.sql, which reseeds them from
// scratch — the equivalent of mock-server.js's POST /api/__reset, used to
// get a clean slate between demo runs without restarting the process.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	const truncate = `TRUNCATE weapon_categories, weapon_designers, weapons, manufacturers, categories, designers, clients RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(ctx, truncate); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return ApplySchema(ctx, pool)
}

// splitStatements splits on a semicolon that ends a line, which schema.sql
// is written to guarantee (no embedded ';' in any literal). Simpler and more
// predictable here than pulling in a SQL-aware splitter for five tables.
func splitStatements(sql string) []string {
	raw := strings.Split(sql, ";\n")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
