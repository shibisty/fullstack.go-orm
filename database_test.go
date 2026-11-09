package database

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fullstack"
	"fullstack/web"
	"migration"
	ormstore "migration-orm"
	"orm"
)

type User struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func (User) TableName() string { return "users" }

// memConn is an in-memory orm.QueryExecutor with users 1 and 2.
type memConn struct {
	pings, closed int
	pingErr       error
}

func (m *memConn) Driver() string { return "mem" }
func (m *memConn) Ping(context.Context) error {
	m.pings++
	return m.pingErr
}
func (m *memConn) Close() error { m.closed++; return nil }
func (m *memConn) Select(_ context.Context, q *orm.Query, dest any) error {
	rows := map[int64]map[string]any{1: {"id": int64(1), "name": "Ann"}, 2: {"id": int64(2), "name": "Bob"}}
	for _, w := range q.Wheres {
		if w.Column == "id" {
			id, ok := w.Value.(int64)
			if !ok {
				if _, isString := w.Value.(string); isString {
					return orm.ErrNotFound // a string key: no such user
				}
				return errors.New("unexpected key type")
			}
			if r, ok := rows[id]; ok {
				return orm.FillStruct(dest, r)
			}
		}
	}
	return orm.ErrNotFound
}
func (m *memConn) Insert(context.Context, *orm.Query) (orm.Result, error) { return orm.Result{}, nil }
func (m *memConn) Update(context.Context, *orm.Query) (orm.Result, error) { return orm.Result{}, nil }
func (m *memConn) Delete(context.Context, *orm.Query) (orm.Result, error) { return orm.Result{}, nil }

// onlyConn is a Connection without the QueryExecutor methods.
type onlyConn struct{ c *memConn }

func (o onlyConn) Driver() string                 { return "plain" }
func (o onlyConn) Ping(ctx context.Context) error { return o.c.Ping(ctx) }
func (o onlyConn) Close() error                   { return o.c.Close() }

type driverFunc func(ctx context.Context) (orm.Connection, error)

func (f driverFunc) Open(ctx context.Context) (orm.Connection, error) { return f(ctx) }

func clearEnv(t *testing.T) {
	for _, k := range []string{"APP_ENV", "DB_DRIVER", "DATABASE_DSN", "DB_PING", "DB_MIGRATE", "DB_TIMEOUT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func newApp(t *testing.T, env ...string) *fullstack.App {
	t.Helper()
	clearEnv(t)
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte(strings.Join(env, "\n")), 0o600)
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{f}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func memDriver(c *memConn) func(string) orm.Driver {
	return func(dsn string) orm.Driver {
		return driverFunc(func(context.Context) (orm.Connection, error) {
			if dsn == "bad" {
				return nil, errors.New("refused")
			}
			return c, nil
		})
	}
}

func TestProviderAndRepo(t *testing.T) {
	conn := &memConn{}
	app := newApp(t, "DATABASE_DSN=mem://x")
	if err := app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(conn)}}); err != nil {
		t.Fatal(err)
	}
	if err := app.Boot(); err != nil || conn.pings != 1 {
		t.Fatal(err, conn.pings)
	}
	repo, err := Repo[User](app)
	if err != nil {
		t.Fatal(err)
	}
	u, err := repo.Find(context.Background(), int64(2))
	if err != nil || u.Name != "Bob" {
		t.Fatal(u, err)
	}
	if cfg := fullstack.MustMake[Config](app); !cfg.Ping || cfg.Migrate {
		t.Fatal(cfg)
	}
	if _, err := fullstack.Make[orm.SQLExecutor](app); err == nil || !strings.Contains(err.Error(), "not an SQL database") {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil || conn.closed != 1 {
		t.Fatal(err, conn.closed)
	}
}

func TestBindModel(t *testing.T) {
	app := newApp(t, "DATABASE_DSN=x", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}})
	r := app.Router
	r.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	BindModel[User](app, r, "user")
	r.Get("/users/{user}", func(c *web.Ctx) error {
		u, err := web.Model[*User](c, "user")
		if err != nil {
			return err
		}
		return c.Text(200, u.Name)
	})
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/users/1": 200, "/users/9": 404, "/users/abc": 404} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	// A connection that cannot be opened is a 500.
	bad := newApp(t, "DATABASE_DSN=bad", "DB_PING=false")
	bad.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}})
	BindModel[User](bad, bad.Router, "user")
	bad.Router.Get("/users/{user}", func(c *web.Ctx) error { return nil })
	hb, _ := bad.Handler()
	w := httptest.NewRecorder()
	hb.ServeHTTP(w, httptest.NewRequest("GET", "/users/1", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

func TestDriverSelection(t *testing.T) {
	two := map[string]func(string) orm.Driver{"a": memDriver(&memConn{}), "b": memDriver(&memConn{})}
	cases := []struct {
		env     []string
		drivers map[string]func(string) orm.Driver
		want    string
	}{
		{nil, two, "DATABASE_DSN is not set"},
		{[]string{"DATABASE_DSN=x"}, two, "set DB_DRIVER to one of a, b"},
		{[]string{"DATABASE_DSN=x", "DB_DRIVER=c"}, two, `unknown DB_DRIVER "c": one of a, b`},
		{[]string{"DATABASE_DSN=x", "DB_DRIVER=c"}, nil, "Provider.Drivers is empty"},
		{[]string{"DATABASE_DSN=bad", "DB_DRIVER=a"}, two, "opening a: refused"},
	}
	for _, c := range cases {
		app := newApp(t, append(c.env, "DB_PING=false")...)
		app.Register(&Provider{Drivers: c.drivers})
		if _, err := fullstack.Make[orm.Connection](app); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.env, err)
		}
		if _, err := fullstack.Make[orm.QueryExecutor](app); err == nil {
			t.Errorf("%v: QueryExecutor", c.env)
		}
		if _, err := fullstack.Make[orm.SQLExecutor](app); err == nil {
			t.Errorf("%v: SQLExecutor", c.env)
		}
		if _, err := Repo[User](app); err == nil {
			t.Errorf("%v: Repo", c.env)
		}
	}
	// Boot fails on a failing ping, and on a connection that does not open.
	conn := &memConn{pingErr: errors.New("down")}
	app := newApp(t, "DATABASE_DSN=x")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(conn)}})
	if err := app.Boot(); err == nil || !strings.Contains(err.Error(), "ping mem: down") {
		t.Fatal(err)
	}
	app = newApp(t, "DATABASE_DSN=bad")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(conn)}})
	if err := app.Boot(); err == nil {
		t.Fatal("boot with a broken connection")
	}
	// A connection that is not a QueryExecutor.
	app = newApp(t, "DATABASE_DSN=x", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"p": func(string) orm.Driver {
		return driverFunc(func(context.Context) (orm.Connection, error) { return onlyConn{c: &memConn{}}, nil })
	}}})
	if _, err := Exec(app); err == nil || !strings.Contains(err.Error(), "not an orm.QueryExecutor") {
		t.Fatal(err)
	}
	if err := newApp(t, "DB_TIMEOUT=never").Register(&Provider{}); err == nil {
		t.Fatal("bad DB_TIMEOUT")
	}
}

// journal is a memory migration store whose DB records the migrations' steps.
func memMigrator(log *[]string, store *migration.MemoryStore[*ormstore.DB]) func(context.Context, *fullstack.App) (*ormstore.Migrator, error) {
	return func(context.Context, *fullstack.App) (*ormstore.Migrator, error) {
		mk := func(name string) ormstore.Migration {
			return ormstore.Migration{Name: name,
				Up:   func(context.Context, *ormstore.DB) error { *log = append(*log, "up "+name); return nil },
				Down: func(context.Context, *ormstore.DB) error { *log = append(*log, "down "+name); return nil }}
		}
		var ms Migrations
		ms.Add(mk("2026_01_02_000000_b"))
		ms.Add(mk("2026_01_01_000000_a"))
		return migration.New[*ormstore.DB](store, ms.All()...)
	}
}

func TestConsole(t *testing.T) {
	var steps []string
	store := migration.NewMemoryStore[*ormstore.DB](&ormstore.DB{})
	seeded := []string{}
	dir := t.TempDir()
	app := newApp(t, "DATABASE_DSN=x", "DB_PING=false")
	app.Register(&Provider{
		Drivers:       map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})},
		Migrator:      memMigrator(&steps, store),
		MigrationsDir: dir,
		Seeders: []Seeder{
			{Name: "Users", Run: func(context.Context, *fullstack.App) error { seeded = append(seeded, "Users"); return nil }},
			{Name: "Posts", Run: func(context.Context, *fullstack.App) error { seeded = append(seeded, "Posts"); return nil }},
			{Name: "Broken", Run: func(context.Context, *fullstack.App) error { return errors.New("boom") }},
		},
	})
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code, ok := ConsoleTo(app, args, &out, &errb)
		if !ok {
			t.Fatalf("%v not handled", args)
		}
		return code, out.String(), errb.String()
	}
	// Through the App's console too.
	var aout bytes.Buffer
	app.Stdout, app.Stderr = &aout, io.Discard
	if code, ok := app.Console([]string{"migrate:status"}); !ok || code != 0 || !strings.Contains(aout.String(), "2026_01_01_000000_a") {
		t.Fatal(code, aout.String())
	}
	if code, _ := app.Console([]string{"db:seed", "--only", "Nope"}); code != 1 {
		t.Fatal("failing command through the App", code)
	}
	if _, ok := Console(app, []string{"serve"}); ok {
		t.Fatal("serve handled")
	}
	if _, ok := Console(app, nil); ok {
		t.Fatal("no args handled")
	}
	if code, out, _ := run("migrate"); code != 0 || !strings.Contains(out, "2026_01_01_000000_a") || strings.Join(steps, ",") != "up 2026_01_01_000000_a,up 2026_01_02_000000_b" {
		t.Fatalf("%d %s %v", code, out, steps)
	}
	if code, out, _ := run("migrate:status"); code != 0 || !strings.Contains(out, "batch 1") {
		t.Fatal(code, out)
	}
	if code, out, _ := run("migrate:rollback"); code != 0 || !strings.Contains(out, "Rolled back") {
		t.Fatal(code, out)
	}
	if code, _, errs := run("migrate:nope"); code != 1 || !strings.Contains(errs, "unknown command") {
		t.Fatal(code, errs)
	}
	if code, out, _ := run("make:migration", "create_posts_table"); code != 0 || !strings.Contains(out, "create_posts_table.go") {
		t.Fatal(code, out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*_create_posts_table.go"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "package migrations") {
		t.Fatalf("%s", data)
	}

	if code, out, _ := run("db:seed", "--only", "Posts"); code != 0 || out != "Seeded Posts\n" {
		t.Fatal(code, out)
	}
	if code, _, errs := run("db:seed"); code != 1 || !strings.Contains(errs, "Broken: boom") || strings.Join(seeded, ",") != "Posts,Users,Posts" {
		t.Fatal(code, errs, seeded)
	}
	if code, _, errs := run("db:seed", "--only", "Nope"); code != 1 || !strings.Contains(errs, `no seeder "Nope"`) {
		t.Fatal(code, errs)
	}
	if code, _, _ := run("db:seed", "--bad"); code != 1 {
		t.Fatal("bad flag")
	}

	// Without seeders, and without a Provider.
	empty := newApp(t, "DATABASE_DSN=x")
	empty.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}})
	var out bytes.Buffer
	if code, _ := ConsoleTo(empty, []string{"db:seed"}, &out, io.Discard); code != 0 || out.String() != "Nothing to seed\n" {
		t.Fatal(code, out.String())
	}
	// The default migrator needs an SQL connection.
	var errb bytes.Buffer
	if code, _ := ConsoleTo(empty, []string{"migrate"}, io.Discard, &errb); code != 1 || !strings.Contains(errb.String(), "not an SQL database") {
		t.Fatal(code, errb.String())
	}
	none := newApp(t)
	errb.Reset()
	if code, ok := ConsoleTo(none, []string{"migrate"}, io.Discard, &errb); !ok || code != 1 || !strings.Contains(errb.String(), "database.Provider") {
		t.Fatal(code, errb.String())
	}
}

func TestMigrateOnBoot(t *testing.T) {
	var steps []string
	store := migration.NewMemoryStore[*ormstore.DB](&ormstore.DB{})
	app := newApp(t, "DATABASE_DSN=x", "DB_MIGRATE=true", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}, Migrator: memMigrator(&steps, store)})
	if err := app.Boot(); err != nil || len(steps) != 2 || len(store.Records()) != 2 {
		t.Fatal(err, steps)
	}
	// The default migrator on a connection that is not SQL.
	app = newApp(t, "DATABASE_DSN=x", "DB_MIGRATE=true", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}, Migrations: func() []ormstore.Migration { return nil }})
	if err := app.Boot(); err == nil {
		t.Fatal("migrations on a non-SQL connection")
	}
	// Ping and migrate off: Boot does nothing (the connection is not even opened).
	app = newApp(t, "DATABASE_DSN=bad", "DB_PING=false")
	app.Register(&Provider{Drivers: map[string]func(string) orm.Driver{"mem": memDriver(&memConn{})}})
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
}
