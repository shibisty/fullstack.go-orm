// Package database wires orm and migrations into fullstack (gtr package
// fullstack-orm): a service provider that opens the connection named by
// DB_DRIVER and DATABASE_DSN, repositories and route model binding from the
// container, and the console commands migrate, migrate:rollback,
// migrate:status, migrate:reset, migrate:refresh, make:migration and db:seed.
//
//	app.Register(&database.Provider{
//	    Drivers: map[string]func(string) orm.Driver{
//	        "postgres": postgres.Driver,
//	        "mysql":    mysql.Driver,
//	    },
//	    Migrations: migrations.All,
//	    Seeders:    []database.Seeder{seeders.Users},
//	})
//	if code, ok := database.Console(app, os.Args[1:]); ok { // go run . migrate
//	    os.Exit(code)
//	}
//	users, _ := database.Repo[User](app)
package database

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fullstack"
	"fullstack/env"
	"fullstack/web"
	ormstore "migration-orm"
	"orm"
)

// Config is read from the environment by Provider.
type Config struct {
	// Driver names one of Provider.Drivers ("postgres", "mysql"); it may be
	// empty when Provider has a single driver.
	Driver string `env:"DB_DRIVER"`
	DSN    string `env:"DATABASE_DSN"`
	// Ping checks the connection when the application boots (fail fast).
	Ping bool `env:"DB_PING" default:"true"`
	// Migrate runs pending migrations when the application boots: handy in
	// development and tests, not for several instances deploying at once.
	Migrate bool `env:"DB_MIGRATE" default:"false"`
	// Timeout bounds opening, pinging and boot-time migrations.
	Timeout time.Duration `env:"DB_TIMEOUT" default:"30s"`
}

// Seeder fills the database with data (db:seed).
type Seeder struct {
	Name string
	Run  func(ctx context.Context, app *fullstack.App) error
}

// Provider opens the database connection and registers it in the
// container as orm.Connection, orm.QueryExecutor and (for SQL drivers)
// orm.SQLExecutor, plus the Config.
type Provider struct {
	// Drivers maps DB_DRIVER values to driver constructors
	// (postgres.Driver, mysql.Driver, sqlite.Driver…). Drivers are plain Go
	// values, so only the ones listed here are compiled in.
	Drivers map[string]func(dsn string) orm.Driver
	// Migrations lists the application's migrations (usually migrations.All
	// from database/migrations).
	Migrations func() []ormstore.Migration
	// MigrationsDir is where make:migration writes (default
	// "database/migrations", package "migrations").
	MigrationsDir string
	// Seeders run by db:seed, in order.
	Seeders []Seeder
	// Migrator replaces the default migrator (migration-orm on the SQL
	// connection), e.g. with a memory store in tests.
	Migrator func(ctx context.Context, app *fullstack.App) (*ormstore.Migrator, error)

	cfg Config
}

// Register reads Config and provides the connection; it is opened on first
// use (or at Boot when DB_PING or DB_MIGRATE is on) and closed on shutdown.
func (p *Provider) Register(app *fullstack.App) error {
	if err := env.Decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 30 * time.Second
	}
	fullstack.Set(app, p.cfg)
	fullstack.Set(app, p)
	p.addCommands(app)
	fullstack.Provide(app, func(app *fullstack.App) (orm.Connection, error) {
		ctor, err := p.driver()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
		defer cancel()
		conn, err := orm.New(ctx, ctor(p.cfg.DSN))
		if err != nil {
			return nil, fmt.Errorf("database: opening %s: %w", p.driverName(), err)
		}
		app.OnShutdown(func(context.Context) error { return conn.Close() })
		return conn, nil
	})
	fullstack.Provide(app, func(app *fullstack.App) (orm.QueryExecutor, error) {
		conn, err := fullstack.Make[orm.Connection](app)
		if err != nil {
			return nil, err
		}
		if qe, ok := conn.(orm.QueryExecutor); ok {
			return qe, nil
		}
		return nil, fmt.Errorf("database: the %s connection is not an orm.QueryExecutor", conn.Driver())
	})
	fullstack.Provide(app, func(app *fullstack.App) (orm.SQLExecutor, error) {
		conn, err := fullstack.Make[orm.Connection](app)
		if err != nil {
			return nil, err
		}
		if se, ok := conn.(orm.SQLExecutor); ok {
			return se, nil
		}
		return nil, fmt.Errorf("database: the %s connection is not an SQL database", conn.Driver())
	})
	return nil
}

// Boot pings the database (DB_PING) and runs pending migrations
// (DB_MIGRATE).
func (p *Provider) Boot(app *fullstack.App) error {
	if !p.cfg.Ping && !p.cfg.Migrate {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()
	conn, err := fullstack.Make[orm.Connection](app)
	if err != nil {
		return err
	}
	if p.cfg.Ping {
		if err := conn.Ping(ctx); err != nil {
			return fmt.Errorf("database: ping %s: %w", p.driverName(), err)
		}
	}
	if p.cfg.Migrate {
		m, err := p.migrator(ctx, app)
		if err != nil {
			return err
		}
		ran, err := m.Migrate(ctx)
		for _, name := range ran {
			app.Log.Info("migrated", "migration", name)
		}
		return err
	}
	return nil
}

func (p *Provider) driverName() string {
	if p.cfg.Driver != "" {
		return p.cfg.Driver
	}
	for name := range p.Drivers {
		return name
	}
	return ""
}

func (p *Provider) driver() (func(string) orm.Driver, error) {
	names := make([]string, 0, len(p.Drivers))
	for n := range p.Drivers {
		names = append(names, n)
	}
	sort.Strings(names)
	if p.cfg.DSN == "" {
		return nil, errors.New("database: DATABASE_DSN is not set")
	}
	name := p.cfg.Driver
	if name == "" {
		if len(names) != 1 {
			return nil, fmt.Errorf("database: set DB_DRIVER to one of %s", strings.Join(names, ", "))
		}
		name = names[0]
	}
	d, ok := p.Drivers[name]
	if !ok {
		if len(names) == 0 {
			return nil, errors.New("database: Provider.Drivers is empty")
		}
		return nil, fmt.Errorf("database: unknown DB_DRIVER %q: one of %s", name, strings.Join(names, ", "))
	}
	return d, nil
}

func (p *Provider) migrator(ctx context.Context, app *fullstack.App) (*ormstore.Migrator, error) {
	if p.Migrator != nil {
		return p.Migrator(ctx, app)
	}
	exec, err := fullstack.Make[orm.SQLExecutor](app)
	if err != nil {
		return nil, err
	}
	var list []ormstore.Migration
	if p.Migrations != nil {
		list = p.Migrations()
	}
	return ormstore.New(exec, list...)
}

// Exec returns the query executor of the application's database.
func Exec(app *fullstack.App) (orm.QueryExecutor, error) {
	return fullstack.Make[orm.QueryExecutor](app)
}

// Repo returns a repository of model T on the application's database.
func Repo[T orm.Model](app *fullstack.App) (*orm.Repository[T], error) {
	exec, err := Exec(app)
	if err != nil {
		return nil, err
	}
	return orm.NewRepository[T](exec), nil
}

// BindModel binds the route parameter name to model T by its primary key,
// like Laravel's route model binding: in "/users/{user}" the handler gets
// the *T with web.Model[*T](c, "user"); a missing record is a 404. A value
// that is an integer is looked up as one (int64), others as strings.
func BindModel[T orm.Model](app *fullstack.App, r *web.Router, name string) {
	r.Bind(name, func(req *http.Request, value string) (any, error) {
		repo, err := Repo[T](app)
		if err != nil {
			return nil, err
		}
		var key any = value
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			key = n
		}
		m, err := repo.Find(req.Context(), key)
		if errors.Is(err, orm.ErrNotFound) {
			return nil, web.ErrNotFound
		}
		return m, err
	})
}

// Migrations is a helper for database/migrations packages: it collects
// migrations registered with Add (usually from init in each file).
type Migrations struct{ list []ormstore.Migration }

// Add registers a migration.
func (m *Migrations) Add(mig ormstore.Migration) { m.list = append(m.list, mig) }

// All returns the migrations sorted by name (their date prefix).
func (m *Migrations) All() []ormstore.Migration {
	out := append([]ormstore.Migration(nil), m.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
