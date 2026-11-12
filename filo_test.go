package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

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

func TestParseFileDirectives(t *testing.T) {
	input := []byte("\uFEFF#!/usr/bin/env lqs\r\n" +
		"-- db: sqlite://x.db\r\n" +
		"-- Json: true\r\n" +
		"-- sql: SELECT 1\r\n" +
		"-- FILO: (list \\\r\n" +
		"--   (nth arg 0))\r\n" +
		"-- a plain comment: kept\r\n" +
		"SELECT $1;\r\n")

	cfg := parseFile(input)

	want := Config{
		jsonOutput:   true,
		dbURL:        "sqlite://x.db",
		filoScript:   "(list \n  (nth arg 0))",
		sqlForFilo:   "SELECT 1",
		sqlStatement: "-- a plain comment: kept\nSELECT $1;",
	}
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"postgres://u:secret@h/db": "postgres://u:xxxxx@h/db",
		"sqlite://:memory:":        "sqlite://:memory:",
		"postgres://u@h/db":        "postgres://u@h/db",
	}
	for in, want := range cases {
		got := redactURL(in)
		if got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRunFiloScript covers the three result shapes: a literal parameter list,
// parameters computed from the -- SQL: result (exposed as arg), and a single
// non-list value treated as one parameter.
func TestRunFiloScript(t *testing.T) {
	ctx := context.Background()

	got, err := runFiloScript(ctx, `(list 42 "Alice" 1.99)`, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{42.0, "Alice", 1.99}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 1: got %#v, want %#v", got, want)
	}

	got, err = runFiloScript(ctx, `(list (+ (nth arg 0) 1) (nth arg 1))`, []any{int64(10), "Bob"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{11.0, "Bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 2: got %#v, want %#v", got, want)
	}

	got, err = runFiloScript(ctx, `(+ 2 40)`, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := []any{42.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 3: got %#v, want %#v", got, want)
	}
}

func TestRunSQLite(t *testing.T) {
	cfg := parseFile([]byte(`-- DB: sqlite://:memory:
-- SQL: SELECT 10, 'Bob'
-- FILO: (list (+ (nth arg 0) 32) (nth arg 1))
SELECT ? AS id, ? AS name`))

	var out bytes.Buffer
	err := run(context.Background(), cfg, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "id: 42\nname: Bob\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}

	cfg.jsonOutput = true
	out.Reset()
	err = run(context.Background(), cfg, &out)
	if err != nil {
		t.Fatalf("run json: %v", err)
	}
	if !strings.Contains(out.String(), `"name": "Bob"`) {
		t.Fatalf("json output: %s", out.String())
	}
}

func TestRunErrors(t *testing.T) {
	t.Setenv("LQS_TEST_EMPTY", "")
	cases := map[string]string{
		"":                        "no database",
		"$LQS_TEST_EMPTY":         "LQS_TEST_EMPTY is empty",
		"mysql://u:secret@h/db":   "unknown database URL",
		"postgres://u:secret@h:x": "unparsable",
	}
	for db, want := range cases {
		err := run(context.Background(), Config{dbURL: db, sqlStatement: "SELECT 1"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("db %q: err = %v, want %q", db, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("db %q: password in error: %v", db, err)
		}
	}
}
