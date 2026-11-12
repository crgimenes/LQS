package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/crgimenes/filo"

	_ "github.com/glebarez/go-sqlite"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var GitTag = "dev"

const usage = `Usage: lqs [-db URL] [-filoScript SCRIPT] [-json] [FILE | -]

Runs the SQL in FILE (or stdin, with - or no argument) against the database
named in its metadata comments, and prints the rows.

Flags:
  -db URL              database, overrides -- DB: (default: from the file)
  -filoScript SCRIPT   Filo script, overrides -- FILO: (default: from the file)
  -json                rows as a JSON array, same as -- JSON: TRUE

Metadata comments in the SQL:
  -- DB: sqlite://file.db | sqlite://:memory: | postgres://... | $ENV_VAR
  -- SQL: SELECT ...     runs first; its row is arg in Filo, (nth arg 0) ...
  -- FILO: (list ...)    result is bound to $1, $2, ... (end a line with \ to continue)
  -- JSON: TRUE

Output: one "column: value" line per column, records separated by
"-=-=-=-= record N =-=-=-=-"; with -json, an array of objects.

Example:
  printf -- '-- DB: sqlite://:memory:\n-- FILO: (list 42)\nSELECT $1 AS n;\n' | lqs

lqs %s
`

type Config struct {
	jsonOutput   bool
	dbURL        string
	filoScript   string
	sqlForFilo   string
	sqlStatement string
}

// directive splits "-- KEY: value" into the upper-cased key and the value; an
// unknown key is returned too, and the caller keeps that line as SQL.
func directive(line string) (string, string) {
	rest, ok := strings.CutPrefix(line, "-- ")
	if !ok {
		return "", ""
	}
	key, value, ok := strings.Cut(rest, ":")
	if !ok {
		return "", ""
	}
	return strings.ToUpper(key), strings.TrimSpace(value)
}

func parseFile(file []byte) Config {
	cfg := Config{}

	fileStr := strings.TrimPrefix(string(file), "\uFEFF")
	fileStr = strings.ReplaceAll(fileStr, "\r\n", "\n")
	lines := strings.Split(fileStr, "\n")

	filoBrokenLine := false

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if i == 0 && strings.HasPrefix(line, "#!") {
			continue
		}

		key, value := directive(line)
		switch key {
		case "DB":
			cfg.dbURL = value
			continue
		case "JSON":
			cfg.jsonOutput = strings.EqualFold(value, "TRUE")
			continue
		case "FILO":
			cfg.filoScript = value
			if strings.HasSuffix(value, "\\") {
				filoBrokenLine = true
				cfg.filoScript = strings.TrimSuffix(value, "\\") + "\n"
			}
			continue
		case "SQL":
			cfg.sqlForFilo = value
			continue
		}

		if filoBrokenLine {
			part := strings.TrimPrefix(line, "-- ")
			cfg.filoScript += strings.TrimSuffix(part, "\\") + "\n"
			filoBrokenLine = strings.HasSuffix(line, "\\")
			continue
		}

		cfg.sqlStatement += line + "\n"
	}

	cfg.sqlStatement = strings.TrimSpace(cfg.sqlStatement)
	cfg.filoScript = strings.TrimSpace(cfg.filoScript)
	return cfg
}

// redactURL keeps passwords out of error messages, which end up in terminals
// and editor notifications.
func redactURL(s string) string {
	if strings.HasPrefix(s, "sqlite://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		scheme, _, _ := strings.Cut(s, "://")
		return scheme + "://(unparsable URL)"
	}
	return u.Redacted()
}

func open(ctx context.Context, dbsource string) (*sql.DB, error) {
	driver := ""
	dsn := dbsource
	switch {
	case strings.HasPrefix(dbsource, "sqlite://"):
		driver = "sqlite"
		dsn = strings.TrimPrefix(dbsource, "sqlite://")
	case strings.HasPrefix(dbsource, "postgres://"), strings.HasPrefix(dbsource, "postgresql://"):
		driver = "pgx"
	case dbsource == "":
		return nil, errors.New("no database: add -- DB: to the file or use -db")
	default:
		return nil, fmt.Errorf("unknown database URL %q: use sqlite:// or postgres://", redactURL(dbsource))
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", redactURL(dbsource), err)
	}
	err = db.PingContext(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect %s: %w", redactURL(dbsource), err)
	}
	return db, nil
}

func scanRow(rows *sql.Rows, n int) ([]any, error) {
	values := make([]any, n)
	ptrs := make([]any, n)
	for i := range values {
		ptrs[i] = &values[i]
	}
	err := rows.Scan(ptrs...)
	return values, err
}

// executeSQLParamQuery runs the -- SQL: query and returns its first row.
func executeSQLParamQuery(ctx context.Context, db *sql.DB, sqlStmt string) ([]any, error) {
	rows, err := db.QueryContext(ctx, sqlStmt)
	if err != nil {
		return nil, fmt.Errorf("-- SQL: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		err = rows.Err()
		if err != nil {
			return nil, fmt.Errorf("-- SQL: %w", err)
		}
		return nil, fmt.Errorf("-- SQL: no rows returned by %v", sqlStmt)
	}

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	return scanRow(rows, len(columns))
}

func anyToValue(p any) filo.Value {
	switch v := p.(type) {
	case nil:
		return filo.VString("")
	case string:
		return filo.VString(v)
	case bool:
		return filo.VBool(v)
	case int:
		return filo.VNum(float64(v))
	case int64:
		return filo.VNum(float64(v))
	case float64:
		return filo.VNum(v)
	case []byte:
		return filo.VString(string(v))
	default:
		return filo.VString(fmt.Sprintf("%v", v))
	}
}

func valueToAny(v filo.Value) any {
	switch v.Kind {
	case filo.KNumber:
		return v.Num
	case filo.KBool:
		return v.Bool
	case filo.KString:
		return v.Str
	default:
		return v.String()
	}
}

// runFiloScript exposes sqlParams to the script as the global "arg". A (list ...)
// result becomes the SQL parameters; any other value becomes a single parameter.
func runFiloScript(ctx context.Context, script string, sqlParams []any) ([]any, error) {
	eng := filo.NewEngine()

	argValues := make([]filo.Value, len(sqlParams))
	for i, p := range sqlParams {
		argValues[i] = anyToValue(p)
	}

	globals := map[string]filo.Value{
		"arg": filo.VList(argValues),
	}

	cfg := filo.EvalConfig{
		StepLimit:      100000,
		RecursionLimit: 128,
		Timeout:        5 * time.Second,
	}

	result, _, err := eng.RunScript(ctx, script, globals, cfg)
	if err != nil {
		return nil, fmt.Errorf("-- FILO: %w", err)
	}

	if result.Kind != filo.KList {
		return []any{valueToAny(result)}, nil
	}
	out := make([]any, len(result.List))
	for i, e := range result.List {
		out[i] = valueToAny(e)
	}
	return out, nil
}

func printJSON(w io.Writer, rows *sql.Rows, columns []string) error {
	a := make([]map[string]any, 0)
	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return err
		}
		m := make(map[string]any, len(columns))
		for i, col := range columns {
			m[col] = values[i]
		}
		a = append(a, m)
	}
	err := rows.Err()
	if err != nil {
		return err
	}
	j, err := json.MarshalIndent(a, "", "    ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(j))
	return err
}

func printText(w io.Writer, rows *sql.Rows, columns []string) error {
	count := 0
	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return err
		}
		if count > 0 {
			_, _ = fmt.Fprintf(w, "-=-=-=-= record %d =-=-=-=-\n", count)
		}
		for i, col := range columns {
			_, _ = fmt.Fprintf(w, "%s: %v\n", col, values[i])
		}
		count++
	}
	return rows.Err()
}

func readInput(name string) ([]byte, error) {
	if name != "" && name != "-" {
		return os.ReadFile(name) // #nosec G304 -- the file to run is the user's argument
	}
	return io.ReadAll(os.Stdin)
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func run(ctx context.Context, cfg Config, w io.Writer) error {
	envVar, isEnv := strings.CutPrefix(cfg.dbURL, "$")
	if isEnv {
		cfg.dbURL = os.Getenv(envVar)
	}
	if isEnv && cfg.dbURL == "" {
		return fmt.Errorf("-- DB: environment variable %s is empty", envVar)
	}

	db, err := open(ctx, cfg.dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var param []any
	if cfg.sqlForFilo != "" {
		param, err = executeSQLParamQuery(ctx, db, cfg.sqlForFilo)
		if err != nil {
			return err
		}
	}

	if cfg.filoScript != "" {
		var filoParams []any
		filoParams, err = runFiloScript(ctx, cfg.filoScript, param)
		if err != nil {
			return err
		}
		if len(filoParams) > 0 {
			param = filoParams
		}
	}

	rows, err := db.QueryContext(ctx, cfg.sqlStatement, param...)
	if err != nil {
		return fmt.Errorf("query\ndb: %v\nSQL: %v\nerror: %w", redactURL(cfg.dbURL), cfg.sqlStatement, err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if cfg.jsonOutput {
		return printJSON(w, rows, columns)
	}
	return printText(w, rows, columns)
}

func main() {
	fs := flag.NewFlagSet("lqs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbFlag := fs.String("db", "", "")
	filoScriptFlag := fs.String("filoScript", "", "")
	jsonFlag := fs.Bool("json", false, "")

	err := fs.Parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Printf(usage, GitTag)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lqs: %v\nRun lqs -h for usage.\n", err)
		os.Exit(2)
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "lqs: one file at a time\nRun lqs -h for usage.")
		os.Exit(2)
	}

	name := fs.Arg(0)
	if name == "" && stdinIsTerminal() {
		fmt.Fprintln(os.Stderr, "lqs: missing input file\nRun lqs -h for usage.")
		os.Exit(2)
	}

	file, err := readInput(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lqs:", err)
		os.Exit(1)
	}

	cfg := parseFile(file)
	if *filoScriptFlag != "" {
		cfg.filoScript = *filoScriptFlag
	}
	if *dbFlag != "" {
		cfg.dbURL = *dbFlag
	}
	cfg.jsonOutput = cfg.jsonOutput || *jsonFlag

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err = run(ctx, cfg, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lqs:", err)
		os.Exit(1)
	}
}
