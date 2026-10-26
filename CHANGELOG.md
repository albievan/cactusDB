# Changelog

## 1.4.0 — 2026-10-26

### Command line
- Several tables export **in parallel** (`--threads`, default 4), each with a live
  **progress bar** showing percentage, rows and time. Plain output when not on a terminal.
- A working command-line connection is **saved** as `cli-<host>` (password encrypted), so
  it's ready in the web UI. `--saveconn no` turns this off.
- `--exportddl all|table,…` writes the **DDL** to a `.sql` file.

### Web UI
- `--runas background` runs the web UI in the background, with a `cactusdb.pid` file;
  `--logfile` sets the **action log**, which records connections, queries, exports and
  every request (never passwords).
- **+ New** connection form with **Test connection**, which checks a connection
  (server version, number of tables) before it is saved. **Edit** uses the same form and
  can rename a connection while keeping its password.
- **Export all tables** icons (Excel, CSV, JSON, XML) under *Objects*, with a progress bar
  per table and download links.
- **DDL** button: the `CREATE` statements of every object in one `.sql` file.
- Animated data stream in the top bar, which speeds up while cactusdb is busy.

### DDL
- Firebird and SQL Server table definitions now include unique and check constraints,
  indexes and foreign keys; PostgreSQL table definitions now include indexes.
- DDL scripts add foreign keys after all tables and use each engine's statement
  separators, so they run unchanged in `isql`, `mysql`, `psql`, `sqlcmd` and `sqlite3`.

## 1.3.1 — 2026-10-26

- `--dbtable table1,table3,table88` exports several tables in one run, one file each.
  Every name is checked before anything is exported, and a table listed twice is exported once.

## 1.3.0 — 2026-10-26

### Command-line export
- Export tables without the browser:
  `cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima --dbtable orders --exportas excel --exportto ./export`.
- `--dbtable` takes one table, a comma-separated list, or `all`; `--exportas` is `csv`,
  `json`, `xml` or `excel`.
- `--showtable all` lists the tables and views; `--showtable <table>` prints its definition.
- The password is prompted for without echo, or taken from `--dbpassword` or the
  `CACTUSDB_DBPASSWORD` environment variable.
- Exit status 0/1/2 (success / failure / bad options) for scripts.

### Web UI
- `--web yes` starts the web UI explicitly (still the default without `--dbtype`).
- With `--host 0.0.0.0` the startup banner lists this computer's IP addresses to browse to.

## 1.2.0 — 2026-10-26

### Security
- Saved usernames and passwords in `cactus.conn.db` are now **encrypted** with AES-256-GCM.
  The key is a random `cactus.key` next to the connection file, or is derived from the
  `CACTUSDB_PASSPHRASE` environment variable so that nothing secret is stored on disk.
- Connections saved by older versions are encrypted automatically on first start, and the
  file is compacted so no plain-text copy remains.
- Saved passwords are no longer sent to the browser. After **Edit**, leave the password
  blank to keep the stored one.
- New `--key-file` option.

## 1.1.0 — 2026-10-26

### New databases
- **Microsoft SQL Server** (2012 and newer, tested with 2022), including named instances.
- **PostgreSQL** (10 and newer, tested with 17), including materialized views.

### Object browser
- Browse views, stored procedures, functions, packages, triggers, generators/sequences,
  domains, exceptions, events and synonyms, depending on the database.
- A **Definition** tab shows each object's source code with syntax colouring, plus a
  Copy button. Tables and views show their `CREATE` statement.
- Generators/sequences show their current value without advancing them.

### Interface
- Sidebar sections fold open and closed, and fold away automatically after connecting
  so the object list gets the full height.
- Objects are split into per-type tabs (Tables, Views, Procedures, Generators…) with
  counts. The filter searches all tabs.

### Read-only protection
- Queries that advance a sequence or generator (`nextval()`, `GEN_ID()`,
  `NEXT VALUE FOR`) are refused, because rollbacks can't undo them.
- MySQL/MariaDB/PostgreSQL sessions are read-only at the database level. SQL Server
  queries run in a transaction that is always rolled back.

### Fixes
- Timestamps with a time zone keep their UTC offset.

## 1.0.0 — 2026-10-25

- First release: browse **SQLite**, **MySQL**, **MariaDB** and **Firebird** databases
  from the browser.
- Click a table to select all of its columns into a sortable grid, or run your own
  `SELECT` queries.
- Export to Excel, JSON, XML and CSV in the `--exportto` folder.
- Saved connections in `cactus.conn.db`.
- Strictly read-only, single self-contained binary for Linux, macOS and Windows, with all
  drivers built in.
