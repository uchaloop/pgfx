package pgfx

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func validConfig() Config {
	return Config{Host: "127.0.0.1:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}
}

// TestModuleGraphValid verifies the default Module consumes an untagged Config
// and provides an untagged *pgxpool.Pool. ValidateApp checks the graph without
// running constructors, so no database is opened.
func TestModuleGraphValid(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(validConfig()),
		Module(),
		fx.Invoke(func(*pgxpool.Pool) {}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// TestModuleForGraphValid verifies ModuleFor consumes a Config tagged
// name:"<name>" and provides a *pgxpool.Pool tagged the same.
func TestModuleForGraphValid(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(fx.Annotate(validConfig(), fx.ResultTags(`name:"replica"`))),
		ModuleFor("replica"),
		fx.Invoke(fx.Annotate(
			func(*pgxpool.Pool) {},
			fx.ParamTags(`name:"replica"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// TestMultiplePoolsGraphNoConflict verifies that the untagged default pool and
// several tagged pools coexist in one container. fx distinguishes them by tag,
// so there is no duplicate-type conflict; ValidateApp fails if there were one.
func TestMultiplePoolsGraphNoConflict(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(validConfig()),
		fx.Supply(fx.Annotate(validConfig(), fx.ResultTags(`name:"replica"`))),
		fx.Supply(fx.Annotate(validConfig(), fx.ResultTags(`name:"analytics"`))),
		Module(),
		ModuleFor("replica"),
		ModuleFor("analytics"),
		fx.Invoke(fx.Annotate(
			func(primary, replica, analytics *pgxpool.Pool) {},
			fx.ParamTags(``, `name:"replica"`, `name:"analytics"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp with three pools: %v", err)
	}
}

// TestMultiplePoolsAreDistinct builds a default and a named connection for real
// and checks they resolve to different pools. pgxpool does not dial until first
// use, so no database is needed.
func TestMultiplePoolsAreDistinct(t *testing.T) {
	primaryCfg := Config{Host: "primary:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}
	replicaCfg := Config{Host: "replica:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}

	var primary, replica *pgxpool.Pool
	app := fx.New(
		fx.NopLogger,
		fx.Supply(primaryCfg),
		fx.Supply(fx.Annotate(replicaCfg, fx.ResultTags(`name:"replica"`))),
		Module(),
		ModuleFor("replica"),
		fx.Invoke(fx.Annotate(
			func(p, r *pgxpool.Pool) { primary, replica = p, r },
			fx.ParamTags(``, `name:"replica"`),
		)),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build two pools: %v", err)
	}
	t.Cleanup(func() {
		if primary != nil {
			primary.Close()
		}
		if replica != nil {
			replica.Close()
		}
	})

	if primary == nil || replica == nil {
		t.Fatal("a pool was not provided")
	}
	if primary == replica {
		t.Fatal("the default and named connection resolved to the same pool")
	}
	if got := primary.Config().ConnConfig.Host; got != "primary" {
		t.Errorf("primary host = %q, want primary", got)
	}
	if got := replica.Config().ConnConfig.Host; got != "replica" {
		t.Errorf("replica host = %q, want replica", got)
	}
}

// TestModuleBuildsWithoutPassword verifies a passwordless Config builds a pool
// (Password is optional). pgxpool does not dial until first use.
func TestModuleBuildsWithoutPassword(t *testing.T) {
	cfg := Config{Host: "h", Database: "app", TLS: TLSConfig{Mode: "disable"}} // no password

	var pool *pgxpool.Pool
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		Module(),
		fx.Invoke(func(p *pgxpool.Pool) { pool = p }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("passwordless build: %v", err)
	}
	if pool != nil {
		pool.Close()
	}
}

// TestModuleRequiresConfig verifies the graph fails when no Config is provided.
func TestModuleRequiresConfig(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		Module(),
		fx.Invoke(func(*pgxpool.Pool) {}),
	)
	if err == nil {
		t.Fatal("expected ValidateApp to fail without a Config")
	}
}
