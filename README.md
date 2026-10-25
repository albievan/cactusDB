# cactusdb

A single, self-contained binary that lets you browse **SQLite**, **MySQL**, **MariaDB**
and **Firebird** databases from your web browser, and export results to
**Excel (.xlsx)**, **JSON**, **XML** and **CSV**.

```sh
cactusdb --port 8080                       # exports go to ./export next to the binary
cactusdb --port 8087 --exportto ./export   # or choose the export folder
# then open http://localhost:8080
```

## Download

Ready-to-run binaries are in [`dist/`](dist):

| OS | Intel / AMD | ARM |
|----|-------------|-----|
| Linux | `cactusdb-linux-amd64` | `cactusdb-linux-arm64` |
| macOS | `cactusdb-darwin-amd64` | `cactusdb-darwin-arm64` (Apple Silicon) |
| Windows | `cactusdb-windows-amd64.exe` | `cactusdb-windows-arm64.exe` |

On Linux and macOS, run `chmod +x` on the file. macOS may block an unsigned download
the first time: run `xattr -d com.apple.quarantine cactusdb-darwin-arm64` or allow it in
System Settings, then Privacy & Security.

## Features

- Pick an engine, connect, and every table and view in the database is listed.
- Click a table: cactusdb builds `SELECT <every column> FROM <table>` and shows the
  result in a sortable grid. You can also edit the SQL and re-run it (Ctrl+Enter).
- Export the full result (not just the rows on screen) to Excel, JSON, XML or CSV.
  Files are written to the export folder as `<table>_<yyyymmdd-hhmmss>.<ext>`. The UI
  shows the saved path, with a link to download the file to your browser too.
- Save connections, including username and password, to `cactus.conn.db`, a local
  SQLite file. Click a saved connection to reconnect.
- **Read-only.** INSERT, UPDATE, DELETE and schema changes are blocked in two layers:
  1. Every query is checked: it must be a single SELECT / WITH / SHOW / DESCRIBE /
     EXPLAIN statement, with no data-changing keywords (comments and string literals
     are stripped before the check).
  2. The database enforces it too, even if a query got past layer 1:
     - SQLite files are opened with `mode=ro` and `query_only`.
     - MySQL / MariaDB sessions are set to `transaction_read_only` (or `tx_read_only`
       on older servers), which also blocks DDL such as `DROP TABLE`.
     - MySQL / MariaDB / Firebird queries also run inside a `READ ONLY` transaction
       that is always rolled back.

  The only file cactusdb writes is its own `cactus.conn.db`, plus the files in the
  export folder.
- **No drivers to install.** All database drivers are pure Go and compiled into the
  binary (shown in the *Drivers* panel and on startup). Unlike JDBC tools such as
  DBeaver, nothing is downloaded at runtime, and no client libraries
  (libmysqlclient, fbclient) are needed.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | HTTP port to serve the UI on |
| `--host` | `127.0.0.1` | Listen address. Use `0.0.0.0` to allow other machines. Saved passwords are then reachable over the network, so only do this on a trusted network. |
| `--exportto` | `export` folder next to the binary | Folder exports are written to; created if missing. Relative paths are relative to the current directory. |
| `--conn-db` | `cactus.conn.db` next to the binary | File used to store saved connections |
| `--version` | | Print version and exit |

## Connection fields

| Engine | Fields |
|--------|--------|
| SQLite | Path to the database file |
| MySQL / MariaDB | Host, port (3306), username, password, database name |
| Firebird | Host, port (3050), username, password, database path or alias, e.g. `/var/lib/firebird/data/employee.fdb`, `C:\data\EMPLOYEE.FDB` or `employee` |

Firebird 2.5 to 5 are supported, including Srp authentication and wire encryption.

## Building

Requires Go 1.22+. No CGO and no C toolchain are needed.

```sh
go build -o cactusdb .   # current platform
./build.sh               # linux/mac/windows x amd64/arm64 into ./dist
go test ./...
```

`integration_test.go` checks the database-level read-only layer against real servers.
To run it, start the test databases and set `CACTUSDB_IT=1`:

```sh
docker run -d --rm --name cdb-mysql -e MYSQL_ROOT_PASSWORD='p@ss:w/rd' -e MYSQL_DATABASE=shop -p 13306:3306 mysql
docker run -d --rm --name cdb-maria -e MARIADB_ROOT_PASSWORD=secret -e MARIADB_DATABASE=shop -p 13307:3306 mariadb:11
docker run -d --rm --name cdb-fb -e ISC_PASSWORD=masterkey -e FIREBIRD_DATABASE=test.fdb -p 13050:3050 jacobalberty/firebird
# create a 2-row `customers` table (`CUSTOMER` in Firebird) in each, then:
CACTUSDB_IT=1 go test ./...
```

## Security note

Saved passwords are stored **in plain text** in `cactus.conn.db`, as requested.
cactusdb restricts the file to your user (mode 600) on Linux and macOS. Protect it accordingly.

## License

Public domain, under the same terms as SQLite: see [LICENSE](LICENSE). The bundled
third-party drivers keep their own licenses, which are listed there too.
