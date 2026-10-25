# Changelog

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
