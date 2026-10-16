package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
)

// SQLite's DSN PRAGMAs use context.Background inside Driver.Open. Keep native
// opening on the bounded operation owner, then initialize each connection
// under its actual acquisition context, before the pool can lend it out.
type sqliteConnector struct {
	driver        driver.Driver
	path          string
	busyTimeoutMs int
}

type connectionInitializationContextKey struct{}

// database/sql's replacement opener uses its own cancelable pool context,
// never the context of the caller waiting in DB.Conn. Refuse that opener
// before any native work. ErrBadConn makes database/sql retry acquisition
// on the waiting caller, whose marked context and operation slot own the
// physical open through initialization and disposal. Synchronous legacy
// pool calls use noncancelable contexts and keep their existing behavior.
type callerOwnedConnector struct{ driver.Connector }

func (c callerOwnedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if ctx.Done() != nil {
		if _, owned := ctx.Value(connectionInitializationContextKey{}).(context.Context); !owned {
			return nil, driver.ErrBadConn
		}
	}
	return c.Connector.Connect(ctx)
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
