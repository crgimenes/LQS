package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"lqs/lua"

	_ "github.com/glebarez/go-sqlite"
	_ "github.com/lib/pq"
)

// Config holds the parameters extracted from the input file.
type Config struct {
	jsonOutput   bool   // Whether to output the result in JSON format
	dbURL        string // Database connection string
	luaScript    string // Lua script to execute the return values will be used as SQL parameters (if any)
	sqlForLua    string // SQL query to pass its result to Lua (values accessible as arg[1], arg[2], ...)
	sqlStatement string // Main SQL query to execute (can receive parameters from Lua and/or SQL query)
}

// parseFile reads the file content and extracts the parameters.
func parseFile(file []byte) Config {
	cfg := Config{}

	fileStr := strings.ReplaceAll(string(file), "\r\n", "\n")
	lines := strings.Split(fileStr, "\n")

	luaBrokenLine := false

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
			if strings.HasPrefix(cfg.dbURL, "$") {
				cfg.dbURL = os.Getenv(strings.TrimPrefix(cfg.dbURL, "$"))
			}
			continue
		}

		// Process JSON output parameter
		if strings.HasPrefix(upperLine, "-- JSON:") {
			param := strings.TrimSpace(strings.TrimPrefix(line, "-- JSON:"))
			cfg.jsonOutput = (strings.ToUpper(param) == "TRUE")
			continue
		}

		// Process Lua script parameter
		if strings.HasPrefix(upperLine, "-- LUA:") {
			part := strings.TrimSpace(strings.TrimPrefix(line, "-- LUA:"))
			// Handle Lua script broken line
			if strings.HasSuffix(line, "\\") {
				luaBrokenLine = true
				cfg.luaScript = strings.TrimSuffix(part, "\\") + "\n"
			} else {
				cfg.luaScript = part
			}
			continue
		}

		// Process new SQL parameter for Lua
		if strings.HasPrefix(upperLine, "-- SQL:") {
			cfg.sqlForLua = strings.TrimSpace(strings.TrimPrefix(line, "-- SQL:"))
			continue
		}

		// Continue Lua script from previous line
		if luaBrokenLine {
			part := strings.TrimPrefix(line, "-- ")
			cfg.luaScript += strings.TrimSuffix(part, "\\") + "\n"
			if strings.HasSuffix(line, "\\") {
				continue
			}
			luaBrokenLine = false
			continue
		}

		// Non recognized line, consider it as SQL statement
		cfg.sqlStatement += line + "\n"
	}

	cfg.sqlStatement = strings.TrimSpace(cfg.sqlStatement)
	cfg.luaScript = strings.TrimSpace(cfg.luaScript)
	return cfg
}

// open establishes a connection with the database.
func open(dbsource string) (db *sql.DB, err error) {
	if strings.HasPrefix(dbsource, "sqlite://") {
		dbsource = strings.TrimPrefix(dbsource, "sqlite://")
		db, err = sql.Open("sqlite", dbsource)
		if err != nil {
			err = fmt.Errorf("error open db: %v, %v", dbsource, err)
			return
		}
	} else if strings.HasPrefix(dbsource, "postgres://") {
		db, err = sql.Open("postgres", dbsource)
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
func query(db *sql.DB, sqlStmt string, params ...interface{}) (rows *sql.Rows, err error) {
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
	defer rows.Close()

	if !rows.Next() {
		return nil, fmt.Errorf("no rows returned by SQL query: %v", sqlStmt)
	}

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}

	err = rows.Scan(valuePtrs...)
	if err != nil {
		return nil, err
	}

	return values, nil
}

// runLuaScript executes the Lua script and, if provided, sets the SQL query result (sqlParams)
// as a global variable "arg" (accessible as arg[1], arg[2], ...).
func runLuaScript(luaScript string, sqlParams []any) ([]any, error) {
	l := lua.New()
	defer l.Close()

	l.SetGlobal("arg", sqlParams)

	err := l.DoString(luaScript)
	if err != nil {
		return nil, fmt.Errorf("error executing Lua script: %v", err)
	}
	return l.GetReturnValues(), nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Llongfile)

	var (
		inputFlag = ""
	)

	// Flags to override parameters.
	dbFlag := flag.String("db", "", "db connection string")
	luaScriptFlag := flag.String("luaScript", "", "luaScript script")
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
	if luaScriptFlag != nil && *luaScriptFlag != "" {
		cfg.luaScript = *luaScriptFlag
	}
	if dbFlag != nil && *dbFlag != "" {
		cfg.dbURL = *dbFlag
	}

	// Open the database connection.
	dbu, err := open(cfg.dbURL)
	if err != nil {
		log.Fatalln(err)
	}

	// Exec the SQL query to get the parameters for the Lua script.
	var sqlParamValues []interface{}
	if cfg.sqlForLua != "" {
		sqlParamValues, err = executeSQLParamQuery(dbu, cfg.sqlForLua)

		if err != nil {
			log.Fatalln(err)
		}
	}

	// Exec the Lua script, if provided.
	var luaReturnValues []interface{}
	if cfg.luaScript != "" {
		luaReturnValues, err = runLuaScript(cfg.luaScript, sqlParamValues)
		if err != nil {
			log.Fatalln(err)
		}
	}

	// Define the parameters to be used in the main SQL query.
	param := sqlParamValues
	if len(luaReturnValues) > 0 {
		param = luaReturnValues
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
		a := make([]map[string]interface{}, 0)
		columns, err := rows.Columns()
		if err != nil {
			log.Fatalln(err)
		}
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}
		for rows.Next() {
			m := make(map[string]interface{})
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
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
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
