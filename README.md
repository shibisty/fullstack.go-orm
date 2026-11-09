# fullstack-orm

[orm](https://github.com/shibisty/orm.go) and [migration](https://github.com/shibisty/migration.go) for
[fullstack](https://github.com/shibisty/fullstack.go): the database connection from the configuration,
repositories and route model binding from the container, and Laravel-style console commands.

## Installation

```json
{
    "dependencies": {
        "fullstack": "github:shibisty/fullstack.go",
        "fullstack-orm": "github:shibisty/fullstack.go-orm",
        "orm": "github:shibisty/orm.go",
        "orm-postgres": "github:shibisty/orm.go-postgres-driver",
        "migration": "github:shibisty/migration.go",
        "migration-orm": "github:shibisty/migration.go-orm-driver"
    }
}
```

```go
import "fullstack-orm" // package database
```

## Setup

```go
app.Register(&database.Provider{
    Drivers: map[string]func(string) orm.Driver{ // only these drivers are compiled in
        "postgres": postgres.Driver,
        "mysql":    mysql.Driver,
    },
    Migrations: migrations.All, // database/migrations
    Seeders: []database.Seeder{
        {Name: "Users", Run: seeders.Users},
    },
})

// main: console commands before serving (the Provider adds migrate*, make:migration, db:seed)
if code, ok := app.Console(os.Args[1:]); ok {
    os.Exit(code)
}
```

| Variable | Default | |
|---|---|---|
| `DB_DRIVER` | — | a key of `Provider.Drivers`; may be empty with a single driver |
| `DATABASE_DSN` | — | the driver's DSN |
| `DB_PING` | `true` | ping at boot: a wrong DSN fails at start, not on the first request |
| `DB_MIGRATE` | `false` | run pending migrations at boot (development, tests) |
| `DB_TIMEOUT` | `30s` | for opening, the ping and boot-time migrations |

The connection is in the container as `orm.Connection`, `orm.QueryExecutor` and, for SQL drivers,
`orm.SQLExecutor`; it is closed when the application stops.

## Using it

```go
users, err := database.Repo[models.User](app)          // *orm.Repository[User]
active, err := users.Where("active", "=", true).Latest().Get(ctx)

database.BindModel[models.User](app, app.Router, "user") // route model binding
app.Router.Get("/users/{user}", func(c *web.Ctx) error {
    user, err := web.Model[*models.User](c, "user")      // a missing user is a 404
    ...
})
```

Factories (orm-factory) take the executor: `factory.New(...).CreateMany(ctx, database.Exec(app), 10)`.

## Console

```bash
go run . migrate                       # or gtr run migrate with a script
go run . migrate:status
go run . migrate:rollback --step 2
go run . migrate:refresh
go run . make:migration create_posts_table   # database/migrations/2026_..._create_posts_table.go
go run . db:seed
go run . db:seed --only Users
```

`make:migration` writes a migration that adds itself with `register` in `init`; the package defines
`register` and `All` once:

```go
// database/migrations/migrations.go
package migrations

import (
    database "fullstack-orm"
    ormstore "migration-orm"
)

var registry database.Migrations

func register(m ormstore.Migration) { registry.Add(m) }

// All is Provider.Migrations: every migration, by date.
func All() []ormstore.Migration { return registry.All() }
```

`Provider.MigrationsDir` changes the folder; `Provider.Migrator` replaces the migrator (a memory
store in tests).

## Tests

```go
func TestCreatePost(t *testing.T) {
    app := bootstrap.NewApp()
    database.UseTransaction(t, app) // rolled back when the test ends
    apptest.New(t, app).ActingAs(user).PostJSON("/api/posts", in).AssertCreated()
}
```

From the call on, the container's `orm.SQLExecutor` and `orm.QueryExecutor` are the transaction, so
`Repo`, `BindModel` and anything resolved through the container use it.

## License

MIT
