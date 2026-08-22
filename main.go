package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/crgimenes/filo"

	_ "github.com/glebarez/go-sqlite"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Config holds the parameters extracted from the input file.
type Config struct {
	jsonOutput   bool   // Whether to output the result in JSON format
	dbURL        string // Database connection string
	filoScript   string // Filo script to execute; its return value (a list) is used as SQL parameters (if any)
	sqlForFilo   string // SQL query to pass its result to Filo (values accessible as (nth arg 0), (nth arg 1), ...)
	sqlStatement string // Main SQL query to execute (can receive parameters from Filo and/or SQL query)
}

// parseFile reads the file content and extracts the parameters.
func parseFile(file []byte) Config {
	cfg := Config{}

	fileStr := strings.ReplaceAll(string(file), "\r\n", "\n")
	lines := strings.Split(fileStr, "\n")

	filoBrokenLine := false

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Ignore shebang line
		if i == 0 && strings.HasPrefix(line, "#!") {
			continue
		}

		upperLine := strings.ToUpper(line)

		// Process DB parameter
		if strings.HasPrefix(upperLine, "-- DB:") {
			cfg.dbURL = strings.TrimSpace(strings.TrimPrefix(line, "-- DB:"))
			if after, ok := strings.CutPrefix(cfg.dbURL, "$"); ok {
				cfg.dbURL = os.Getenv(after)
			}
			continue
		}

		// Process JSON output parameter
		if strings.HasPrefix(upperLine, "-- JSON:") {
			param := strings.TrimSpace(strings.TrimPrefix(line, "-- JSON:"))
			cfg.jsonOutput = (strings.ToUpper(param) == "TRUE")
			continue
		}

		// Process Filo script parameter
		if strings.HasPrefix(upperLine, "-- FILO:") {
			part := strings.TrimSpace(strings.TrimPrefix(line, "-- FILO:"))
			// Handle Filo script broken line
			if strings.HasSuffix(line, "\\") {
				filoBrokenLine = true
				cfg.filoScript = strings.TrimSuffix(part, "\\") + "\n"
			} else {
				cfg.filoScript = part
			}
			continue
		}

		// Process new SQL parameter for Filo
		if strings.HasPrefix(upperLine, "-- SQL:") {
			cfg.sqlForFilo = strings.TrimSpace(strings.TrimPrefix(line, "-- SQL:"))
			continue
		}

		// Continue Filo script from previous line
		if filoBrokenLine {
			part := strings.TrimPrefix(line, "-- ")
			cfg.filoScript += strings.TrimSuffix(part, "\\") + "\n"
			if strings.HasSuffix(line, "\\") {
				continue
			}
			filoBrokenLine = false
			continue
		}

		// Non recognized line, consider it as SQL statement
		cfg.sqlStatement += line + "\n"
	}

	cfg.sqlStatement = strings.TrimSpace(cfg.sqlStatement)
	cfg.filoScript = strings.TrimSpace(cfg.filoScript)
	return cfg
}

// open establishes a connection with the database.
func open(dbsource string) (db *sql.DB, err error) {
	if after, ok := strings.CutPrefix(dbsource, "sqlite://"); ok {
		dbsource = after
		db, err = sql.Open("sqlite", dbsource)
		if err != nil {
			err = fmt.Errorf("error open db: %v, %v", dbsource, err)
			return
		}
	} else if strings.HasPrefix(dbsource, "postgres://") {
		db, err = sql.Open("pgx", dbsource)
		if err != nil {
			err = fmt.Errorf("error open db: %v, %v", dbsource, err)
			return
		}
	} else {
		err = fmt.Errorf("error open db: unknown db source %v", dbsource)
		return
	}

	err = db.Ping()
	if err != nil {
		log.Fatalln(err)
	}
	return
}

// query executes a SQL query with given parameters.
func query(db *sql.DB, sqlStmt string, params ...any) (rows *sql.Rows, err error) {
	rows, err = db.Query(sqlStmt, params...)
	if err != nil {
		err = fmt.Errorf("error query db: %v", err)
		return
	}
	return
}

// executeSQLParamQuery executes the SQL query specified by -- SQL: and returns its single-row result.
func executeSQLParamQuery(db *sql.DB, sqlStmt string) ([]any, error) {
	rows, err := query(db, sqlStmt)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return nil, fmt.Errorf("no rows returned by SQL query: %v", sqlStmt)
	}

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]any, len(columns))
	valuePtrs := make([]any, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}

	err = rows.Scan(valuePtrs...)
	if err != nil {
		return nil, err
	}

	return values, nil
}

// anyToValue converts a Go value (typically a SQL column value) into a Filo Value.
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

// valueToAny converts a Filo Value back into a Go value usable as a SQL parameter.
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

// runFiloScript executes the Filo script and, if provided, exposes the SQL query
// result (sqlParams) as the global "arg" (accessible as (nth arg 0), (nth arg 1), ...).
// The script's final expression is its result: a (list ...) becomes the list of SQL
// parameters, and any other single value becomes a single parameter.
func runFiloScript(script string, sqlParams []any) ([]any, error) {
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

	result, _, err := eng.RunScript(context.Background(), script, globals, cfg)
	if err != nil {
		return nil, fmt.Errorf("error executing Filo script: %w", err)
	}

	if result.Kind == filo.KList {
		out := make([]any, len(result.List))
		for i, e := range result.List {
			out[i] = valueToAny(e)
		}
		return out, nil
	}

	return []any{valueToAny(result)}, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Llongfile)

	var (
		inputFlag = ""
	)

	// Flags to override parameters.
	dbFlag := flag.String("db", "", "db connection string")
	filoScriptFlag := flag.String("filoScript", "", "Filo script")
	flag.Parse()

	if flag.NArg() > 0 {
		inputFlag = flag.Args()[0]
	}

	if inputFlag == "" {
		fmt.Println("missing input file")
		flag.Usage()
		return
	}

	file, err := os.ReadFile(inputFlag)
	if err != nil {
		log.Fatalln(err)
	}

	// Parse the file to extract parameters.
	cfg := parseFile(file)

	// Allow flags to override parameters.
	if filoScriptFlag != nil && *filoScriptFlag != "" {
		cfg.filoScript = *filoScriptFlag
	}
	if dbFlag != nil && *dbFlag != "" {
		cfg.dbURL = *dbFlag
	}

	// Open the database connection.
	dbu, err := open(cfg.dbURL)
	if err != nil {
		log.Fatalln(err)
	}

	// Exec the SQL query to get the parameters for the Filo script.
	var sqlParamValues []any
	if cfg.sqlForFilo != "" {
		sqlParamValues, err = executeSQLParamQuery(dbu, cfg.sqlForFilo)

		if err != nil {
			log.Fatalln(err)
		}
	}

	// Exec the Filo script, if provided.
	var filoReturnValues []any
	if cfg.filoScript != "" {
		filoReturnValues, err = runFiloScript(cfg.filoScript, sqlParamValues)
		if err != nil {
			log.Fatalln(err)
		}
	}

	// Define the parameters to be used in the main SQL query.
	param := sqlParamValues
	if len(filoReturnValues) > 0 {
		param = filoReturnValues
	}

	// Exec the main SQL query.
	cfg.sqlStatement = strings.TrimSpace(cfg.sqlStatement)
	rows, err := query(dbu, cfg.sqlStatement, param...)
	if err != nil {
		err = fmt.Errorf("error query\ndb: %v\nSQL: %v\nerror: %v", cfg.dbURL, cfg.sqlStatement, err)
		log.Fatalln(err)
	}
	defer func() {
		err := rows.Close()
		if err != nil {
			log.Fatalln(err)
		}
	}()

	// Print the result in JSON format.
	if cfg.jsonOutput {
		a := make([]map[string]any, 0)
		columns, err := rows.Columns()
		if err != nil {
			log.Fatalln(err)
		}
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}
		for rows.Next() {
			m := make(map[string]any)
			err = rows.Scan(valuePtrs...)
			if err != nil {
				log.Fatalln(err)
			}
			for i, col := range columns {
				m[col] = values[i]
			}
			a = append(a, m)
		}
		j, err := json.MarshalIndent(a, "", "    ")
		if err != nil {
			log.Fatalln(err)
		}
		fmt.Println(string(j))
		return
	}

	// Print the result in a human-readable format.
	count := 0
	for rows.Next() {
		columns, err := rows.Columns()
		if err != nil {
			log.Fatalln(err)
		}
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}
		err = rows.Scan(valuePtrs...)
		if err != nil {
			log.Fatalln(err)
		}
		if count > 0 {
			fmt.Printf("-=-=-=-= record %d =-=-=-=-\n", count)
		}
		for i, col := range columns {
			fmt.Printf("%s: %v\n", col, values[i])
		}
		count++
	}
}
