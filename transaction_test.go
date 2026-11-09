package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"fullstack"
	"orm"
)

// sqlConn is a fake SQL connection whose transactions record what happens.
type sqlConn struct {
	memConn
	beginErr    error
	rollbacks   int
	rollbackErr error
	tx          *sqlTx
}

type sqlTx struct {
	conn *sqlConn
	sql  []string
}

func (s *sqlConn) Dialect() orm.Dialect { return nil }
func (s *sqlConn) ExecContext(context.Context, string, ...any) (orm.Result, error) {
	return orm.Result{}, errors.New("outside the transaction")
}
func (s *sqlConn) QueryContext(context.Context, string, ...any) (orm.Rows, error) { return nil, nil }
func (s *sqlConn) QueryRowContext(context.Context, string, ...any) orm.Row        { return nil }
func (s *sqlConn) BeginTx(context.Context) (orm.Tx, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	s.tx = &sqlTx{conn: s}
	return s.tx, nil
}

func (t *sqlTx) Driver() string             { return "sqltx" }
func (t *sqlTx) Ping(context.Context) error { return nil }
func (t *sqlTx) Close() error               { return nil }
func (t *sqlTx) Dialect() orm.Dialect       { return nil }
func (t *sqlTx) ExecContext(_ context.Context, q string, _ ...any) (orm.Result, error) {
	t.sql = append(t.sql, q)
	return orm.Result{RowsAffected: 1}, nil
}
func (t *sqlTx) QueryContext(context.Context, string, ...any) (orm.Rows, error) { return nil, nil }
func (t *sqlTx) QueryRowContext(context.Context, string, ...any) orm.Row        { return nil }
func (t *sqlTx) BeginTx(context.Context) (orm.Tx, error)                        { return nil, errors.New("nested") }
func (t *sqlTx) Commit() error                                                  { return errors.New("tests never commit") }
func (t *sqlTx) Rollback() error {
	t.conn.rollbacks++
	return t.conn.rollbackErr
}

func sqlApp(t *testing.T, conn *sqlConn) *fullstack.App {
	app := newApp(t, "DATABASE_DSN=x", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"sql": func(string) orm.Driver {
		return driverFunc(func(context.Context) (orm.Connection, error) { return conn, nil })
	}}})
	return app
}

type recT struct {
	testing.TB
	cleanups []func()
	errs     []string
}

func (r *recT) Helper()                   {}
func (r *recT) Cleanup(f func())          { r.cleanups = append(r.cleanups, f) }
func (r *recT) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }
func (r *recT) Fatalf(f string, a ...any) { r.Errorf(f, a...); panic(r) }
func (r *recT) run(fn func()) (fatal bool) {
	defer func() {
		if v := recover(); v != nil {
			fatal = true
		}
	}()
	fn()
	return false
}
func (r *recT) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestUseTransaction(t *testing.T) {
	conn := &sqlConn{}
	app := sqlApp(t, conn)
	rt := &recT{}
	if rt.run(func() { UseTransaction(rt, app) }) {
		t.Fatal(rt.errs)
	}
	exec, _ := fullstack.Make[orm.SQLExecutor](app)
	if exec != orm.SQLExecutor(conn.tx) {
		t.Fatal("the container does not give the transaction")
	}
	if _, err := exec.ExecContext(context.Background(), "INSERT"); err != nil || len(conn.tx.sql) != 1 {
		t.Fatal(err)
	}
	qe, _ := Exec(app)
	if qe == orm.QueryExecutor(conn) {
		t.Fatal("QueryExecutor not replaced")
	}
	rt.finish()
	if conn.rollbacks != 1 {
		t.Fatal("not rolled back")
	}
	if e, _ := fullstack.Make[orm.SQLExecutor](app); e != orm.SQLExecutor(conn) {
		t.Fatal("not restored")
	}

	// Failures.
	conn = &sqlConn{rollbackErr: errors.New("gone")}
	rt = &recT{}
	rt.run(func() { UseTransaction(rt, sqlApp(t, conn)) })
	rt.finish()
	if len(rt.errs) != 1 {
		t.Fatal(rt.errs)
	}
	rt = &recT{}
	if !rt.run(func() { UseTransaction(rt, sqlApp(t, &sqlConn{beginErr: errors.New("no")})) }) {
		t.Fatal("begin error")
	}
	rt = &recT{}
	mem := newApp(t, "DATABASE_DSN=x", "DB_PING=false")
	mem.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}})
	if !rt.run(func() { UseTransaction(rt, mem) }) {
		t.Fatal("not SQL")
	}
}
