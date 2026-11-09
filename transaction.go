package database

import (
	"context"
	"testing"

	"fullstack"
	"orm"
)

// UseTransaction runs a test inside a database transaction that is rolled
// back when the test ends, so tests leave no data behind (Laravel's
// DatabaseTransactions). From the call on, the container's orm.SQLExecutor
// and orm.QueryExecutor are the transaction: repositories and BindModel
// made through the container use it. Call it before the code under test
// resolves the database; it needs an SQL connection.
//
//	func TestCreateUser(t *testing.T) {
//	    app := bootstrap.NewApp()
//	    database.UseTransaction(t, app)
//	    apptest.New(t, app).PostJSON("/api/users", in).AssertCreated()
//	}
func UseTransaction(t testing.TB, app *fullstack.App) {
	t.Helper()
	exec, err := fullstack.Make[orm.SQLExecutor](app)
	if err != nil {
		t.Fatalf("database: UseTransaction: %v", err)
	}
	qe, _ := fullstack.Make[orm.QueryExecutor](app)
	tx, err := exec.BeginTx(context.Background())
	if err != nil {
		t.Fatalf("database: UseTransaction: begin: %v", err)
	}
	fullstack.Set[orm.SQLExecutor](app, tx)
	fullstack.Set[orm.QueryExecutor](app, orm.AsQueryExecutor(tx))
	t.Cleanup(func() {
		fullstack.Set[orm.SQLExecutor](app, exec)
		fullstack.Set[orm.QueryExecutor](app, qe)
		if err := tx.Rollback(); err != nil {
			t.Errorf("database: UseTransaction: rollback: %v", err)
		}
	})
}
