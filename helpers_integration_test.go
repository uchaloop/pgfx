//go:build integration

package pgfx_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/uchaloop/pgfx"
)

func TestErrorClassificationPostgres(t *testing.T) {
	ctx, db, cleanup := makeIntegrationDB(t, 10*time.Second)
	defer cleanup()

	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	const query = `
		CREATE TEMP TABLE pgfx_error_parent (
			id int PRIMARY KEY
		);
		CREATE TEMP TABLE pgfx_error_child (
			id int CONSTRAINT pgfx_error_pk PRIMARY KEY,
			parent_id int REFERENCES pgfx_error_parent(id),
			value int NOT NULL CHECK (value > 0)
		);
		INSERT INTO pgfx_error_child
		VALUES (
			1,
			NULL,
			1
		)
	`

	_, err = conn.Exec(ctx, query)
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		sql, code string
		isMatch   func(error) bool
	}{
		{sql: `
			INSERT INTO pgfx_error_child
			VALUES (
				1,
				NULL,
				1
			)
		`, code: "23505", isMatch: pgfx.IsUniqueViolation},
		{sql: `
			INSERT INTO pgfx_error_child
			VALUES (
				2,
				99,
				1
			)
		`, code: "23503", isMatch: pgfx.IsForeignKeyViolation},
		{sql: `
			INSERT INTO pgfx_error_child
			VALUES (
				2,
				NULL,
				NULL
			)
		`, code: "23502", isMatch: pgfx.IsNotNullViolation},
		{sql: `
			INSERT INTO pgfx_error_child
			VALUES (
				2,
				NULL,
				-1
			)
		`, code: "23514", isMatch: pgfx.IsCheckViolation},
		{sql: "SELECT 'oops'::integer", code: "22P02", isMatch: pgfx.IsInvalidTextRepresentation},
		{sql: "SELECT 32768::smallint", code: "22003", isMatch: pgfx.IsNumericValueOutOfRange},
		{sql: "SELECT * FROM pg_temp.pgfx_missing_table", code: "42P01", isMatch: pgfx.IsUndefinedTable},
		{sql: "SELECT missing_column FROM pgfx_error_child", code: "42703", isMatch: pgfx.IsUndefinedColumn},
	} {
		t.Run(item.code, func(t *testing.T) {
			_, err := conn.Exec(ctx, item.sql)
			if err == nil {
				t.Fatal("expected PostgreSQL error")
			}

			errWrapped := fmt.Errorf("query: %w", err)
			if !item.isMatch(errWrapped) || pgfx.SQLState(errWrapped) != item.code {
				t.Fatalf("classification: %v", errWrapped)
			}

			if item.code == "23505" && !pgfx.IsConstraint(errWrapped, "23505", "pgfx_error_pk") {
				t.Fatal("constraint lost")
			}
		})
	}

	for _, value := range []string{`50%_off\sale`, "O'Reilly", "Иван", ""} {
		var isExact, isOther bool
		const query = `
			SELECT
				$1::text LIKE $2,
				($1::text || 'x') LIKE $2
		`

		err := conn.QueryRow(
			ctx,
			query,
			value,
			pgfx.EscapeLike(value),
		).Scan(&isExact, &isOther)
		if err != nil || !isExact || isOther {
			t.Fatalf(
				"literal LIKE %q: %v %v %v",
				value,
				isExact,
				isOther,
				err,
			)
		}
	}

	var isMatch bool

	if err := conn.QueryRow(ctx, "SELECT 'prefix 50%_OFF suffix' ILIKE $1", "%"+pgfx.EscapeLike("50%_off")+"%").Scan(&isMatch); err != nil || !isMatch {
		t.Fatalf("ILIKE: %v, %v", isMatch, err)
	}
}
