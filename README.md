<p align="center"><img src="web/cactusdb.svg" alt="cactusDB icon" width="128" height="128"></p>

<h1 align="center">cactusdb</h1>

**One small program. No installation. Browse your databases in a web browser.**

cactusdb is a standalone app for **Linux, macOS and Windows** that lets you look at
**SQLite**, **MySQL**, **MariaDB** and **Firebird** databases. It also exports their data to
**Excel**, **JSON**, **XML** or **CSV**. You start it from a terminal and use it in your browser.

- No installer, no runtime, no database drivers or client libraries to install:
  everything is inside the one file.
- **Read-only:** it can never change, insert or delete data in your databases.
- Remembers your connections, so you only type them once.

```sh
cactusdb --port 8080
```
then open **http://localhost:8080** in your browser.

---

## 1. Download

Pick the file for your computer from the [`dist/`](dist) folder:

| Your computer | Download |
|---------------|----------|
| Windows (most PCs) | [cactusdb-windows-amd64.exe](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-windows-amd64.exe) |
| Windows on ARM (e.g. Surface Pro X, Snapdragon laptops) | [cactusdb-windows-arm64.exe](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-windows-arm64.exe) |
| Mac with Apple Silicon (M1/M2/M3/M4…) | [cactusdb-darwin-arm64](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-darwin-arm64) |
| Mac with Intel processor | [cactusdb-darwin-amd64](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-darwin-amd64) |
| Linux (most PCs and servers) | [cactusdb-linux-amd64](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-linux-amd64) |
| Linux on ARM (e.g. Raspberry Pi 4/5 64-bit, AWS Graviton) | [cactusdb-linux-arm64](https://github.com/albievan/cactusDB/raw/main/dist/cactusdb-linux-arm64) |

Put it in its own folder, e.g. `C:\cactusdb\` or `~/cactusdb/`. cactusdb keeps its saved
connections and exports next to itself (see [Files cactusdb creates](#5-files-cactusdb-creates)).
You can rename the file to just `cactusdb` (or `cactusdb.exe`).

## 2. Run it

### Windows
Open **Command Prompt** or **PowerShell** in the folder and run:
```bat
cactusdb-windows-amd64.exe --port 8080
```
If Windows SmartScreen warns about an unrecognised app, click **More info → Run anyway**.
If Windows Firewall asks, you can click **Cancel**: by default cactusdb only accepts
connections from your own computer.

### macOS
```sh
cd ~/cactusdb
chmod +x cactusdb-darwin-arm64
xattr -d com.apple.quarantine cactusdb-darwin-arm64   # allow the downloaded app to run
./cactusdb-darwin-arm64 --port 8080
```
If you skip the `xattr` step, macOS blocks the app. You can then allow it in
**System Settings → Privacy & Security → Open Anyway**.

### Linux
```sh
cd ~/cactusdb
chmod +x cactusdb-linux-amd64
./cactusdb-linux-amd64 --port 8080
```
The Linux builds are fully static, so they run on any distribution, including Alpine.

### What you'll see
```
cactusdb 1.0.0
  driver SQLite          modernc.org/sqlite v1.61.0 (bundled)
  driver MySQL, MariaDB  github.com/go-sql-driver/mysql v1.10.1 (bundled)
  driver Firebird        github.com/nakagami/firebirdsql v0.9.23 (bundled)
  saved connections: /home/me/cactusdb/cactus.conn.db
  export folder:     /home/me/cactusdb/export
  open http://127.0.0.1:8080
  press Ctrl+C to stop
```
Open the URL shown in your browser. Leave the terminal window open while you use
cactusdb, and press **Ctrl+C** in it to stop the app.

## 3. Using cactusdb

1. **Choose the database type** at the top left: SQLite, MySQL, MariaDB or Firebird.
2. **Fill in the connection details:**

   | Engine | What to enter |
   |--------|---------------|
   | SQLite | The path to the database file, e.g. `C:\data\shop.db` or `/home/me/shop.sqlite` |
   | MySQL / MariaDB | Host, port (default 3306), username, password, database name |
   | Firebird | Host, port (default 3050), username (often `SYSDBA`), password, and the database **path on the server** or an alias, e.g. `/var/lib/firebird/data/employee.fdb`, `C:\Data\EMPLOYEE.FDB` or `employee` |

3. *(Optional)* Tick **Save connection**, give it a name, and it will be remembered,
   including the password. Or click **Save** to store it without connecting.
4. Click **Connect**. All tables and views in the database appear under **Tables**.
   Type in the filter box to find one quickly.
5. **Click a table.** cactusdb writes the query that selects every column
   (`SELECT "id", "name", … FROM "customers"`) and shows the data in the grid.
   - Click a column heading to sort; click again to reverse.
   - Use **Show** to view 100, 1,000, 10,000 or 100,000 rows on screen.
   - Empty values are shown as *NULL*.
6. *(Optional)* Edit the SQL, e.g. add `WHERE` or `ORDER BY`, and press **Run** or
   **Ctrl+Enter**. Only read queries are accepted (see [Read-only](#read-only)).
7. **Export** with the **Excel**, **JSON**, **XML** or **CSV** buttons. The export
   always contains **all rows** of the query, not just the rows on screen. The file is
   saved in the export folder, and a bar shows the full path with a **Download** link.

**Saved connections** appear in their own list. Click one to connect, use **Edit** to
load it into the form (change it and save again under the same name to update it), or
**✕** to delete it.

### Export formats

| Format | File | Notes |
|--------|------|-------|
| Excel | `.xlsx` | Bold header row; numbers are stored as numbers. Up to 1,048,575 rows (Excel's limit). |
| JSON | `.json` | An array of objects, one per row, keys in column order. Numbers and decimals stay exact; NULL becomes `null`. |
| XML | `.xml` | `<table name="…"><row><column name="…">value</column></row></table>`; NULL becomes `<column name="…" null="true"/>` |
| CSV | `.csv` | UTF-8 with a BOM, so Excel shows accented characters correctly; NULL becomes an empty field |

Files are named `<table>_<date>-<time>.<ext>`, e.g. `customers_20261025-142233.xlsx`.
Free-form queries are named `query_…`. Existing files are never overwritten.

<a id="read-only"></a>
### Read-only: your data is safe

cactusdb **cannot modify your databases.** INSERT, UPDATE, DELETE, and changes to
tables (CREATE, ALTER, DROP, TRUNCATE…) are blocked twice:

1. **Query check:** only a single `SELECT`, `WITH`, `SHOW`, `DESCRIBE` or `EXPLAIN`
   statement is accepted. Anything that changes data is rejected, even when hidden
   inside a `WITH` query or after a `;`.
2. **Database lock:** even if a query got past the check, the database itself refuses
   to write:
   - SQLite files are opened in read-only mode.
   - MySQL and MariaDB sessions are switched to read-only, which also blocks
     `DROP TABLE` and similar commands.
   - MySQL, MariaDB and Firebird queries run in a read-only transaction that is
     always rolled back.

## 4. Startup options

```sh
cactusdb --port 8087 --exportto ./export
```

| Option | Default | What it does |
|--------|---------|--------------|
| `--port` | `8080` | The port to open in your browser: `http://localhost:<port>` |
| `--exportto` | folder `export` next to the cactusdb program | Where exported files are saved. Created automatically if missing. A relative path such as `./export` is relative to the folder you start cactusdb from. |
| `--conn-db` | `cactus.conn.db` next to the cactusdb program | The file saved connections are stored in |
| `--host` | `127.0.0.1` (this computer only) | Use `0.0.0.0` to let other computers on your network use cactusdb. See [Security](#6-security). |
| `--version` | | Print the version and exit |
| `--help` | | List the options |

Examples:
```sh
cactusdb --port 9000                                  # different port
cactusdb --port 8087 --exportto D:\Reports\exports    # exports to another drive (Windows)
cactusdb --port 8087 --exportto ~/Desktop/exports     # exports to your Desktop (macOS/Linux)
cactusdb --port 8080 --host 0.0.0.0                   # reachable from other machines
```

## 5. Files cactusdb creates

Next to the program (unless you chose other locations with the options above):

```
cactusdb/
├── cactusdb(.exe)       the program
├── cactus.conn.db       your saved connections (an SQLite database)
└── export/              your exported files
```

To move cactusdb to another computer and keep your connections, copy `cactus.conn.db`
along with it. To forget all saved connections, delete that file.

**Moving the program into a system folder** such as `/usr/local/bin` or
`C:\Program Files`? cactusdb may not be allowed to write next to itself there. In that
case, point the files somewhere writable:
```sh
cactusdb --port 8080 --conn-db ~/.cactus.conn.db --exportto ~/cactusdb-exports
```

## 6. Security

- **Passwords are saved in plain text** in `cactus.conn.db`. On macOS and Linux, cactusdb
  makes the file readable only by your user account. Don't share it, and don't commit it
  to git.
- By default cactusdb is only reachable **from your own computer**. If you start it with
  `--host 0.0.0.0`, anyone who can reach that port can use your saved connections and
  browse those databases. There is no login screen, so only do this on a trusted network.
- cactusdb never writes to your databases (see [Read-only](#read-only)). The only files it
  writes are `cactus.conn.db` and the files in the export folder.

## 7. Troubleshooting

| Problem | Fix |
|---------|-----|
| `cannot listen on 127.0.0.1:8080: …` | Another program uses that port. Pick another: `--port 8090`. |
| `cannot create export folder …` | The folder isn't writable. Use `--exportto` with a folder you own. |
| Browser shows *can't connect* | Is cactusdb still running in the terminal? Use the exact URL it printed. |
| `sqlite file not found` | Check the path. It's the path on the computer running cactusdb. |
| MySQL `Access denied for user` | Wrong username or password, or the user isn't allowed to connect from your machine's address. |
| Firebird `I/O error … CreateFile (open)` or `file … is not a valid database` | The database path must be the path **on the Firebird server**, not on your computer, or an alias defined in the server's `databases.conf`. |
| Firebird `Your user name and password are not defined` | Check the username and password. Both Srp and legacy authentication are supported. |
| `cactusdb is read-only: only SELECT queries are allowed` | Working as intended: cactusdb doesn't run statements that change data. |
| `result has more rows than Excel allows` | Excel holds at most 1,048,575 data rows. Add a `WHERE` clause, or export to CSV or JSON instead. |
| `not connected (session expired)` | cactusdb was restarted. Connect again. |

## 8. Supported databases

| Database | Versions |
|----------|----------|
| SQLite | 3.x files |
| MySQL | 5.6 and newer, including 8.x and 9.x |
| MariaDB | 10.x and 11.x |
| Firebird | 2.5, 3, 4 and 5, with Srp authentication and wire encryption |

All drivers are built into cactusdb and listed in the **Drivers** panel. Unlike tools such
as DBeaver, there is nothing to download, and no client library (libmysqlclient,
fbclient.dll, …) needs to be installed.

---

## For developers

cactusdb is written in Go and uses pure-Go drivers, so no CGO or C compiler is needed.

```sh
go build -o cactusdb .   # build for this computer
./build.sh               # build all six binaries into dist/
go test ./...            # unit tests
```

`integration_test.go` checks the database-level read-only protection against real
servers. Start test databases and set `CACTUSDB_IT=1`:

```sh
docker run -d --rm --name cdb-mysql -e MYSQL_ROOT_PASSWORD='p@ss:w/rd' -e MYSQL_DATABASE=shop -p 13306:3306 mysql
docker run -d --rm --name cdb-maria -e MARIADB_ROOT_PASSWORD=secret -e MARIADB_DATABASE=shop -p 13307:3306 mariadb:11
docker run -d --rm --name cdb-fb -e ISC_PASSWORD=masterkey -e FIREBIRD_DATABASE=test.fdb -p 13050:3050 jacobalberty/firebird
# create a 2-row `customers` table (`CUSTOMER` in Firebird) in each, then:
CACTUSDB_IT=1 go test ./...
```

| File | Contents |
|------|----------|
| `main.go` | Command-line options, web server, API |
| `db.go` | Connecting, listing tables and columns, running queries |
| `readonly.go` | The read-only query check |
| `export.go` | Excel / JSON / XML / CSV export |
| `store.go` | Saved connections (`cactus.conn.db`) |
| `web/index.html`, `web/cactusdb.svg` | The browser interface and icon, embedded into the binary |

## License

Public domain, under the same terms as SQLite: see [LICENSE](LICENSE).

> May you do good and not evil.
> May you find forgiveness for yourself and forgive others.
> May you share freely, never taking more than you give.

The third-party drivers built into the binary keep their own licenses (BSD-3-Clause,
MPL-2.0 and MIT), listed in [LICENSE](LICENSE).
