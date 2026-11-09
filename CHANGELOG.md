# Changelog

## Unreleased

### Added
- `database.Provider`: opens the connection named by `DB_DRIVER` (one of `Provider.Drivers`;
  optional with a single driver) and `DATABASE_DSN`, registers `orm.Connection`,
  `orm.QueryExecutor` and `orm.SQLExecutor`, closes it on shutdown. Boot pings it (`DB_PING`,
  default on) and can run pending migrations (`DB_MIGRATE`); `DB_TIMEOUT` bounds both.
- `database.Repo[T](app)`, `database.Exec(app)` and `database.BindModel[T](app, router, "user")`
  (route model binding by primary key; a missing record is a 404).
- `database.Console(app, os.Args[1:])`: `migrate`, `migrate:rollback [--step N]`,
  `migrate:reset`, `migrate:refresh`, `migrate:status`, `make:migration <name>`
  (into `database/migrations`) and `db:seed [--only A,B]` with `Provider.Seeders`; the Provider
  also adds them to `App.Console`, so `make:model -m` creates the migration.
- `database.Migrations`: a registry for a `database/migrations` package (`Add` in each file's
  `init`, `All` sorted by name).
- `database.UseTransaction(t, app)`: the test runs in a transaction rolled back at its end; the
  container's executors are the transaction meanwhile.
