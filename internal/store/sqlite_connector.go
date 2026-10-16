package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
)

// SQLite's DSN PRAGMAs use context.Background inside Driver.Open. The caller
// owning native opening initializes each physical connection under its actual
// work context, before executing borrower SQL.
type sqliteConnector struct {
	driver        driver.Driver
	path          string
	busyTimeoutMs int
}

type connectionInitializationContextKey struct{}

// The pool's replacement opener has no caller context. Give it a connection
// without native resources, so handoff succeeds even with multiple waiters.
// The borrower opens it using its own context; a bounded borrower retains its
// operation worker. Synthetic ErrBadConn here consumes database/sql's finite
// acquisition retries while other waiters reserve the next replacement.
type callerOwnedConnector struct{ driver.Connector }

func (c callerOwnedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if ctx.Done() != nil {
		if _, owned := ctx.Value(connectionInitializationContextKey{}).(context.Context); !owned {
			return &lazySQLiteConn{connector: c.Connector}, nil
		}
	}
	return c.Connector.Connect(ctx)
}

// database/sql serializes connection methods. No caller context is retained:
// a fresh replacement can skip ResetSession, and an unused lease can return
// without ever opening SQLite. Only a method called by the borrower opens it.
type lazySQLiteConn struct {
	connector driver.Connector
	conn      driver.Conn
	err       error
	closed    bool
}

func (c *lazySQLiteConn) initialize(ctx context.Context) error {
	if operation, ok := ctx.Value(connectionInitializationContextKey{}).(context.Context); ok {
		ctx = operation
	}
	if c.closed {
		return driver.ErrBadConn
	}
	if c.err != nil {
		return c.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.conn == nil {
		c.conn, c.err = c.connector.Connect(ctx)
	}
	return c.err
}

func (c *lazySQLiteConn) Close() error {
	c.closed = true
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *lazySQLiteConn) IsValid() bool {
	if c.closed || c.err != nil {
		return false
	}
	if validator, ok := c.conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *lazySQLiteConn) ResetSession(ctx context.Context) error {
	if err := c.initialize(ctx); err != nil {
		return err
	}
	if resetter, ok := c.conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *lazySQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if execer, ok := c.conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *lazySQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if queryer, ok := c.conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *lazySQLiteConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *lazySQLiteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if preparer, ok := c.conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, query)
	}
	stmt, err := c.conn.Prepare(query)
	if err == nil && ctx.Err() != nil {
		_ = stmt.Close()
		return nil, ctx.Err()
	}
	return stmt, err
}

func (c *lazySQLiteConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *lazySQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if beginner, ok := c.conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	if opts.Isolation != driver.IsolationLevel(sql.LevelDefault) || opts.ReadOnly {
		return nil, fmt.Errorf("sqlite: driver does not support requested transaction options")
	}
	tx, err := c.conn.Begin()
	if err == nil && ctx.Err() != nil {
		_ = tx.Rollback()
		return nil, ctx.Err()
	}
	return tx, err
}

func (c *lazySQLiteConn) Ping(ctx context.Context) error {
	if err := c.initialize(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pinger, ok := c.conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c sqliteConnector) Driver() driver.Driver { return c.driver }

func (c sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	// Zero wait bounds pool admission to connGrab, while an uncontended
	// connection's initialization is SQL work in the fixed operation budget.
	if operation, ok := ctx.Value(connectionInitializationContextKey{}).(context.Context); ok {
		ctx = operation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", c.busyTimeoutMs),
		"PRAGMA foreign_keys = 1",
	} {
		if err = ctx.Err(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if _, err = conn.(driver.ExecerContext).ExecContext(ctx, statement, nil); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func openContextSQLite(path string, busyTimeoutMs int) (*sql.DB, error) {
	// Obtain the registered driver (including registered functions/hooks)
	// without opening a physical connection on this temporary pool.
	registered, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	drv := registered.Driver()
	if err := registered.Close(); err != nil {
		return nil, err
	}
	return sql.OpenDB(callerOwnedConnector{sqliteConnector{driver: drv, path: path, busyTimeoutMs: busyTimeoutMs}}), nil
}
