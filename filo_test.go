package main

import (
	"reflect"
	"testing"
)

// TestParseFileFilo verifies that the -- FILO: metadata tag (and the rest of the
// embedded metadata) is parsed into the Config.
func TestParseFileFilo(t *testing.T) {
	input := []byte(`-- DB: sqlite://:memory:
-- FILO: (list 42 "Alice" 1.99)
SELECT * FROM test
WHERE id = $1 AND user = $2 AND value < $3;`)

	cfg := parseFile(input)

	if cfg.dbURL != "sqlite://:memory:" {
		t.Fatalf("dbURL = %q", cfg.dbURL)
	}
	if cfg.filoScript != `(list 42 "Alice" 1.99)` {
		t.Fatalf("filoScript = %q", cfg.filoScript)
	}
	wantSQL := "SELECT * FROM test\nWHERE id = $1 AND user = $2 AND value < $3;"
	if cfg.sqlStatement != wantSQL {
		t.Fatalf("sqlStatement = %q, want %q", cfg.sqlStatement, wantSQL)
	}
}

// TestRunFiloScript covers the three result shapes: a literal parameter list,
// parameters computed from the -- SQL: result (exposed as arg), and a single
// non-list value treated as one parameter.
func TestRunFiloScript(t *testing.T) {
	// 1) literal list of parameters
	got, err := runFiloScript(`(list 42 "Alice" 1.99)`, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{42.0, "Alice", 1.99}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 1: got %#v, want %#v", got, want)
	}

	// 2) parameters computed from arg (the -- SQL: result); nth is 0-based
	got, err = runFiloScript(`(list (+ (nth arg 0) 1) (nth arg 1))`, []any{int64(10), "Bob"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{11.0, "Bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 2: got %#v, want %#v", got, want)
	}

	// 3) a single non-list value becomes a single parameter
	got, err = runFiloScript(`(+ 2 40)`, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{42.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 3: got %#v, want %#v", got, want)
	}
}

// TestEndToEndSQLite proves that Filo-produced parameters flow through into a real
// query against an in-memory SQLite database.
func TestEndToEndSQLite(t *testing.T) {
	db, err := open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	params, err := runFiloScript(`(list 42 "Alice")`, nil)
	if err != nil {
		t.Fatalf("runFiloScript: %v", err)
	}

	rows, err := query(db, "SELECT ? AS id, ? AS name", params...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		t.Fatal("no rows returned")
	}
	var id float64
	var name string
	if err := rows.Scan(&id, &name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 42 || name != "Alice" {
		t.Fatalf("got id=%v name=%q, want 42/Alice", id, name)
	}
}
