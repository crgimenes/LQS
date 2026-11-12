---
name: lqs
description: Run a SQL file or snippet that carries lqs metadata comments (-- DB:, -- SQL:, -- FILO:, -- JSON:) against SQLite or PostgreSQL and read the rows, with parameters computed by a Filo script. Use to test a query the user wrote with lqs comments, to try SQL against a throwaway SQLite database (sqlite://:memory: or a file), or to check that placeholders and Filo parameters line up — not to browse the user's configured databases (that is dbv).
---

# lqs

`lqs [-db URL] [-filoScript SCRIPT] [-json] [FILE | -]` runs one SQL
statement and exits. With `-` or no FILE it reads stdin. It executes the SQL
exactly as written: **there is no read-only guard**, an INSERT, UPDATE,
DELETE or DDL changes the database.

## Metadata comments

```
-- DB: sqlite://:memory: | sqlite://file.db | postgres://... | $ENV_VAR
-- SQL: SELECT ...      runs first; its first row is `arg` in Filo
-- FILO: (list ...)     result bound to $1, $2, ... (a non-list is one parameter)
-- JSON: TRUE           same as -json
```

Keys are case-insensitive. A `-- FILO:` line ending in `\` continues on the
next `-- ` line. `(nth arg 0)` is the first column of the `-- SQL:` row.
Any other `-- ...` line stays in the SQL as a comment. `-db` and
`-filoScript` override the file; flags go before the file name.

## How to use it as an agent

- Pipe the SQL in rather than writing a temp file:
  `printf -- '-- DB: sqlite://:memory:\n-- FILO: (list 42)\nSELECT $1 AS n;\n' | lqs -json`
- **Read results with `-json`:** an array of objects, one per row, keys are
  the column names (sorted). Without rows: `[]`. The text format is
  `column: value` lines with `-=-=-=-= record N =-=-=-=-` between rows.
- SQLite placeholders are `?` or `$1`; PostgreSQL uses `$1`.
- Exit 0 success; 1 runtime failure (message on stderr, `lqs: ...`, with the
  SQL and the database, password masked); 2 usage error.
- **Never put a password on the command line or in a file you write.** For
  PostgreSQL use `-- DB: $ENV_VAR` with a variable the user already has, or
  use the dbv skill, which keeps credentials in its own config.
- Anything that writes (INSERT/UPDATE/DELETE/DDL) against a database that is
  not a throwaway SQLite one runs only after the user approved that exact
  statement in this conversation.
- Ctrl-C (SIGINT) cancels the connection and the query; a hung connection
  attempt is otherwise unbounded, so run it with a timeout of your own.
