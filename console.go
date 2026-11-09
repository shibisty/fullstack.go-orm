package database

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fullstack"
	ormstore "migration-orm"
	"migration/cli"
)

var commands = []fullstack.Command{
	{Name: "migrate", Description: "Run the pending migrations"},
	{Name: "migrate:rollback", Usage: "[--step N]", Description: "Roll back the last N batches of migrations"},
	{Name: "migrate:reset", Description: "Roll back every migration"},
	{Name: "migrate:refresh", Description: "Roll back every migration and migrate again"},
	{Name: "migrate:status", Description: "Show which migrations have run"},
	{Name: "make:migration", Usage: "<name>", Description: "Create a migration in database/migrations"},
	{Name: "db:seed", Usage: "[--only A,B]", Description: "Run the seeders"},
}

// addCommands adds the database commands to the App's console.
func (p *Provider) addCommands(app *fullstack.App) {
	for _, c := range commands {
		name := c.Name
		c.Run = func(_ context.Context, app *fullstack.App, args []string) error {
			if code, _ := ConsoleTo(app, append([]string{name}, args...), app.Stdout, app.Stderr); code != 0 {
				return fullstack.ExitError{Code: code}
			}
			return nil
		}
		app.AddCommand(c)
	}
}

// Console runs a database command when args start with one, and reports
// whether it did (App.Console runs them too, with the other commands):
//
//	migrate, migrate:rollback [--step N], migrate:reset, migrate:refresh,
//	migrate:status, make:migration <name>, db:seed [--only Name,...]
//
// In main, before serving:
//
//	if code, ok := database.Console(app, os.Args[1:]); ok {
//	    os.Exit(code)
//	}
func Console(app *fullstack.App, args []string) (code int, handled bool) {
	return ConsoleTo(app, args, os.Stdout, os.Stderr)
}

// ConsoleTo is Console with its output and errors written to the given writers.
func ConsoleTo(app *fullstack.App, args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	cmd := args[0]
	if cmd != "migrate" && !strings.HasPrefix(cmd, "migrate:") && cmd != "make:migration" && cmd != "db:seed" {
		return 0, false
	}
	p, err := fullstack.Make[*Provider](app)
	if err != nil {
		fmt.Fprintln(stderr, "database: register database.Provider first:", err)
		return 1, true
	}
	ctx := context.Background()
	if cmd == "db:seed" {
		if err := p.seed(ctx, app, args[1:], stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	}
	dir := p.MigrationsDir
	if dir == "" {
		dir = "database/migrations"
	}
	opts := cli.Options{Dir: dir, Package: "migrations", Template: ormstore.Template, Stdout: stdout, Stderr: stderr}
	if err := cli.Run(ctx, args, opts, func(ctx context.Context) (*ormstore.Migrator, error) {
		return p.migrator(ctx, app)
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1, true
	}
	return 0, true
}

func (p *Provider) seed(ctx context.Context, app *fullstack.App, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("db:seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	only := fs.String("only", "", "comma-separated seeders to run (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	known := map[string]bool{}
	for _, s := range p.Seeders {
		known[s.Name] = true
	}
	for n := range want {
		if !known[n] {
			return fmt.Errorf("db:seed: no seeder %q", n)
		}
	}
	ran := 0
	for _, s := range p.Seeders {
		if len(want) > 0 && !want[s.Name] {
			continue
		}
		if err := s.Run(ctx, app); err != nil {
			return fmt.Errorf("db:seed: %s: %w", s.Name, err)
		}
		fmt.Fprintf(stdout, "Seeded %s\n", s.Name)
		ran++
	}
	if ran == 0 {
		fmt.Fprintln(stdout, "Nothing to seed")
	}
	return nil
}
