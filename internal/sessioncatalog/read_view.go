package sessioncatalog

import (
	"context"
	"database/sql"
	"time"
)

type readViewKey struct{}
type readView struct {
	owner    *Catalog
	tx       *sql.Tx
	revision uint64
	now      time.Time
}
type queryReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// WithReadView pins all nested list reads to one SQLite read transaction. No
// writer mutex is held and callers must finish materialization before return.
func (c *Catalog) WithReadView(ctx context.Context, visit func(context.Context) error) error {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return visit(ctx)
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The first read establishes the WAL view, even for an empty result.
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_sessions`).Scan(&count); err != nil {
		return err
	}
	v := &readView{c, tx, c.revision.Load(), c.opts.Now()}
	return visit(context.WithValue(ctx, readViewKey{}, v))
}

func (c *Catalog) readDB(ctx context.Context) queryReader {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return v.tx
	}
	return c.db
}
func (c *Catalog) readRevision(ctx context.Context) uint64 {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return v.revision
	}
	return c.revision.Load()
}
func (c *Catalog) readTime(ctx context.Context) time.Time {
	if v, _ := ctx.Value(readViewKey{}).(*readView); v != nil && v.owner == c {
		return v.now
	}
	return c.opts.Now()
}
