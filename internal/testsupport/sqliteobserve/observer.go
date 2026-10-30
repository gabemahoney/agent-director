// Package sqliteobserve shares the real SQLite driver boundary used by store
// and controller tests. Hooks observe actual statements/rows/commit results;
// they never synthesize a successful SQLite operation.
package sqliteobserve

import (
	"context"
	"database/sql/driver"
)

// Hooks must remain immutable, or synchronize their own state, while installed.
// AfterCommit runs only after SQLite successfully commits, before its result
// returns to the store worker. Blocking it models an uncooperative native result.
type Hooks struct {
	Before      func(context.Context, string) error
	AfterNext   func(context.Context, string, error)
	AfterExec   func(context.Context, string, error)
	AfterCommit func(context.Context) error
	Close       func()
}

type observedDriver struct {
	driver.Driver
	hooks Hooks
}

// Wrap retains the real driver and delegates connection initialization to the
// store's production connector. Each returned connection uses the same hooks.
func Wrap(d driver.Driver, hooks Hooks) driver.Driver {
	return &observedDriver{Driver: d, hooks: hooks}
}

func (d *observedDriver) Open(path string) (driver.Conn, error) {
	c, err := d.Driver.Open(path)
	if err != nil {
		return nil, err
	}
	return WrapConn(c, d.hooks), nil
}

// Conn preserves the context-aware SQLite interfaces when embedded by a fixture.
type Conn struct {
	driver.Conn
	hooks Hooks
}

// WrapConn also serves existing store fixtures which observe connection open
// and initialization separately before installing the shared statement hooks.
func WrapConn(c driver.Conn, hooks Hooks) *Conn {
	return &Conn{Conn: c, hooks: hooks}
}

func (c *Conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.hooks.Before != nil {
		if err := c.hooks.Before(ctx, q); err != nil {
			return nil, err
		}
	}
	r, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if c.hooks.AfterExec != nil {
		c.hooks.AfterExec(ctx, q, err)
	}
	if err == nil && q == "COMMIT" && c.hooks.AfterCommit != nil {
		err = c.hooks.AfterCommit(ctx)
	}
	return r, err
}

func (c *Conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.hooks.Before != nil {
		if err := c.hooks.Before(ctx, q); err != nil {
			return nil, err
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && c.hooks.AfterNext != nil {
		return &observedRows{Rows: rows, afterNext: func(err error) { c.hooks.AfterNext(ctx, q, err) }}, nil
	}
	return rows, err
}

func (c *Conn) Close() error {
	if c.hooks.Close != nil {
		c.hooks.Close()
	}
	return c.Conn.Close()
}

type observedRows struct {
	driver.Rows
	afterNext func(error)
}

func (r *observedRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	r.afterNext(err)
	return err
}
