# lqs

lqs is a lightweight, cross-platform CLI tool written in Go for quickly testing and debugging SQL statements that include embedded metadata and optional [Filo](https://github.com/crgimenes/filo) scripts. It helps detect issues in SQL strings early, saving time and reducing errors.

## Key Features

- **Embedded Metadata**
  Use special comments to define parameters. Examples:
  - `-- DB: postgres://user:pass@host/dbname`
  - `-- FILO: (list 1 2 3)` (Its result is mapped to SQL placeholders.)
  - `-- SQL: SELECT id, name FROM users WHERE active = true` (Use results as parameters.)
  - `-- JSON: TRUE` (Print query results in JSON.)

- **Filo Integration**
  - The result of your Filo script — the value of its final expression, typically a `(list ...)` — is mapped to SQL placeholders (`$1`, `$2`, etc.).
  - Accept input parameters from another SQL query defined by `-- SQL:` and access them in Filo via `(nth arg 0)`, `(nth arg 1)`, etc. (zero-based).

- **Database Support**
  - SQLite (`sqlite://:memory:` or `sqlite://file.db`)
  - PostgreSQL (`postgres://...`)

- **No CGO Required**
  Pure Go SQLite and PostgreSQL drivers. Builds with `CGO_ENABLED=0` on any Go-supported platform.

- **Neovim Integration**
  Easily test SQL within Neovim by sending the selected text to `lqs` in a temporary file. See [lqs.lua](lqs.lua) for a quick reference.

## Installation

```bash
go install github.com/crgimenes/LQS@latest   # installs the binary as LQS
```

or, from a clone:

```bash
go build -o lqs
cp lqs /usr/local/bin   # or any directory in your PATH
```

## Command line

```
lqs [-db URL] [-filoScript SCRIPT] [-json] [FILE | -]
```

With `-` or no file, the SQL is read from stdin. `-db` and `-filoScript` override the
`-- DB:` and `-- FILO:` comments; `-json` is the same as `-- JSON: TRUE`. `lqs -h` prints
the full usage. Errors go to stderr with a non-zero exit status, and passwords in database
URLs are masked.

An agent skill lives in [skills/lqs/SKILL.md](skills/lqs/SKILL.md).

## Usage

1. **Create a SQL File**
   Use a shebang or call `lqs` directly:
   ```sql
   #!./lqs
   -- DB: sqlite://:memory:
   -- JSON: TRUE
   SELECT sqlite_version();
   ```
   Make it executable, then run `./script.sql` or `lqs script.sql`.

2. **Use Filo Scripts**
   ```sql
   -- DB: $ENV_DB_CONNECTION_STRING
   -- FILO: (list 42 "Alice" 1.99)
   SELECT * FROM test
   WHERE id = $1
     AND user = $2
     AND value < $3;
   ```
   The Filo script's result list maps to `$1`, `$2`, `$3`, etc.

3. **Combine `-- SQL:` and Filo**
   ```sql
   -- DB: sqlite://:memory:
   -- SQL: SELECT 10, "Bob"
   -- FILO: (list (nth arg 0) (nth arg 1))
   SELECT * FROM users WHERE id = $1 AND name = $2;
   ```
   The `-- SQL:` line runs first, and its result is available to Filo as `arg` (zero-based: `(nth arg 0)`, `(nth arg 1)`, ...).

4. **JSON Output**
   ```sql
   -- DB: postgres://user:pass@localhost/testdb
   -- JSON: TRUE
   SELECT id, username FROM auth_user;
   ```
   If `-- JSON: TRUE` is present, results are printed in JSON.

## Integration with Neovim

[lqs.lua](lqs.lua) defines `:LQS`: it sends the selected text to `lqs` on stdin and shows the output in a new buffer. Place it in your Neovim config (for example `~/.config/nvim/lua/lqs.lua`) and load it with `require "lqs"`.

## Contributing

Feel free to submit issues or pull requests for bug fixes and improvements.

## License

Licensed under the MIT License. See [LICENSE](LICENSE) for details.

