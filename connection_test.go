package pgfx

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"
)

func TestWithName(t *testing.T) {
	cfg := Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}}

	named, err := MakeDB(context.Background(), cfg, WithName("node3"))
	if err != nil {
		t.Fatal(err)
	}
	defer named.Close()

	unnamed, err := MakeDB(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer unnamed.Close()

	if named.Name() != "node3" || len(unnamed.Name()) != 0 || len((&DB{}).Name()) != 0 {
		t.Fatalf("names = %q, %q, %q", named.Name(), unnamed.Name(), (&DB{}).Name())
	}
}

func TestQueryMetricConnection(t *testing.T) {
	var got QueryMetric

	tracer := makeOptions(WithName("node3"), WithQueryMetrics(func(metric QueryMetric) { got = metric })).buildTracer()
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got.Connection != "node3" {
		t.Fatalf("Connection = %q", got.Connection)
	}
}

func TestMakeModuleForNameAndGroup(t *testing.T) {
	cfg := Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}}

	var names []string

	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			fx.Annotate(cfg, fx.ResultTags(`name:"a"`)),
			fx.Annotate(cfg, fx.ResultTags(`name:"b"`)),
		),
		MakeModuleFor("a", WithName("ignored")),
		MakeModuleFor("b"),
		fx.Invoke(fx.Annotate(
			func(dbs []*DB) {
				for _, db := range dbs {
					names = append(names, db.Name())
				}
			},
			fx.ParamTags(`group:"pgfx"`),
		)),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("names = %v", names)
	}
}
