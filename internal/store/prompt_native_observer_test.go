package store

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
)

// ObservePromptRowsForTest observes real rows and transaction statements; its
// optional close hook runs after Scan copies destinations, before rows cancellation.
func ObservePromptRowsForTest(t *testing.T, s *Store, path string, before func(context.Context, string) error, after func(context.Context, string, error), beforeClose ...func(context.Context, string)) func() {
	b, d := installNativeBoundary(t, s, path)
	if len(beforeClose) > 0 && beforeClose[0] != nil {
		d.Driver = &promptCloseDriver{Driver: d.Driver, before: beforeClose[0]}
	}
	b.before, b.afterNext = before, after
	return func() { b.stopObservingAfterCleanup(t, s) }
}

type promptCloseDriver struct {
	driver.Driver
	before func(context.Context, string)
}

func (d *promptCloseDriver) Open(path string) (driver.Conn, error) {
	conn, err := d.Driver.Open(path)
	if err != nil {
		return nil, err
	}
	return &promptCloseConn{Conn: sqliteobserve.WrapConn(conn, sqliteobserve.Hooks{}), before: d.before}, nil
}

type promptCloseConn struct {
	*sqliteobserve.Conn
	before func(context.Context, string)
}

func (c *promptCloseConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &promptCloseRows{Rows: rows, before: func() { c.before(ctx, q) }}, nil
}

type promptCloseRows struct {
	driver.Rows
	before func()
}

func (r *promptCloseRows) Close() error {
	r.before()
	return r.Rows.Close()
}
