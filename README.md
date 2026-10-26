<p align="center"><img src="web/cactusdb.svg" alt="cactusDB icon" width="128" height="128"></p>

<h1 align="center">cactusdb</h1>

**One small program. No installation. Browse your databases in a web browser, or export them straight from the command line.**

cactusdb is a standalone app for **Linux, macOS and Windows** that lets you look at
**SQLite**, **MySQL**, **MariaDB**, **PostgreSQL**, **Firebird** and **Microsoft SQL Server** databases,
including their views, stored procedures, functions, triggers and generators/sequences. It
also exports their data to
**Excel**, **JSON**, **XML** or **CSV**. You start it from a terminal and use it in your browser,
or export tables with a single command without opening a browser at all.

- No installer, no runtime, no database drivers or client libraries to install:
  everything is inside the one file.
- **Read-only:** it can never change, insert or delete data in your databases.
- Remembers your connections, so you only type them once.

```sh
cactusdb --port 8080 --web yes
```
then open **http://localhost:8080** in your browser. Or export without the browser:
```sh
cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima --dbtable all --exportas excel
```

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
connections and exports next to itself (see [Files cactusdb creates](#6-files-cactusdb-creates)).
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
cactusdb 1.4.0
  driver SQLite          modernc.org/sqlite v1.61.0 (bundled)
  driver MySQL, MariaDB  github.com/go-sql-driver/mysql v1.10.1 (bundled)
  driver Firebird        github.com/nakagami/firebirdsql v0.9.23 (bundled)
  driver SQL Server      github.com/microsoft/go-mssqldb v1.11.2 (bundled)
  driver PostgreSQL      github.com/jackc/pgx/v5 v5.11.0 (bundled)
  saved connections: /home/me/cactusdb/cactus.conn.db (credentials encrypted with key file /home/me/cactusdb/cactus.key)
  export folder:     /home/me/cactusdb/export
  open http://127.0.0.1:8080
  (only this computer can connect; start with --host 0.0.0.0 to allow others)
  press Ctrl+C to stop
```
Open the URL shown in your browser. Leave the terminal window open while you use
cactusdb, and press **Ctrl+C** in it to stop the app.

## 3. Using cactusdb

1. **Choose the database type** at the top left: SQLite, MySQL, MariaDB, Firebird, SQL Server or PostgreSQL.
2. **Fill in the connection details:**

   | Engine | What to enter |
   |--------|---------------|
   | SQLite | The path to the database file, e.g. `C:\data\shop.db` or `/home/me/shop.sqlite` |
   | MySQL / MariaDB | Host, port (default 3306), username, password, database name |
   | Firebird | Host, port (default 3050), username (often `SYSDBA`), password, and the database **path on the server** or an alias, e.g. `/var/lib/firebird/data/employee.fdb`, `C:\Data\EMPLOYEE.FDB` or `employee` |
   | PostgreSQL | Host, port (default 5432), username (often `postgres`), password, database name (leave blank to use the database named after the user) |
   | SQL Server | Host, port (default 1433), SQL Server login (e.g. `sa`) and password, database name (leave blank for the login's default database). For a named instance, enter the host as `server\INSTANCE` and clear the port. |

3. *(Optional)* Tick **Save connection**, give it a name, and it will be remembered,
   including the password, which is stored encrypted (see [Security](#7-security)).
   Or click **Save** to store it without connecting.
4. Click **Connect**. The Connection, Saved connections and Drivers sections fold away so
   the **Objects** list gets the whole sidebar. Click any section heading (▸/▾) to open or
   close it again. Objects are split into tabs (Tables, Views, Procedures, Functions,
   Triggers, Generators/Sequences…), see [Browsing objects](#browsing-objects).
5. **Click a table or view.** cactusdb writes the query that selects every column
   (`SELECT "id", "name", … FROM "customers"`) and shows the data in the grid.
   - Click a column heading to sort; click again to reverse.
   - Use **Show** to view 100, 1,000, 10,000 or 100,000 rows on screen.
   - Empty values are shown as *NULL*.
6. *(Optional)* Edit the SQL, e.g. add `WHERE` or `ORDER BY`, and press **Run** or
   **Ctrl+Enter**. Only read queries are accepted (see [Read-only](#read-only)).
7. **Export** with the **Excel**, **JSON**, **XML** or **CSV** buttons. The export
   always contains **all rows** of the query, not just the rows on screen. The file is
   saved in the export folder, and a bar shows the full path with a **Download** link.

**Saved connections** appear in their own list. Click one to connect, or **✕** to delete it.

**Adding and editing connections.** Click **+ New** next to *Saved connections* to open the
connection form: a name, the database type and the connection details. Click
**Test connection** to check them before saving. cactusdb connects, reports the server
version and the number of tables (or the exact error), and disconnects again. Nothing is
saved until you click **Save**. **Edit** on a saved connection opens the same form, where
you can test it, change it or rename it. Saved passwords are never sent to the browser:
the password box shows *saved – leave blank to keep*. Leave it empty to keep the stored
password (testing uses it too), or type a new one to replace it.

**Export all tables.** Under the *Objects* heading, the four file icons (**XLS**, **CSV**,
**JSON**, **XML**) export every table of the database, one file per table, four tables at a
time. Each table gets a progress bar with its row count, and a download link when it's
done. Disconnecting stops an export that is still running.

**Export the DDL.** The **DDL** button next to the filter writes the `CREATE` statements of
every object in the database to one `.sql` file (see [DDL export](#ddl-export)).

While cactusdb is working (connecting, running a query, exporting), the animated data
stream in the top bar speeds up.

<a id="browsing-objects"></a>
### Browsing objects: views, stored procedures, generators…

The **Objects** section has one tab per object type that exists in the database, each with
a count, e.g. `Tables 42` `Views 7` `Procedures 12` `Generators 9`. Click a tab to list
those objects.

- The **filter** box searches every tab at once. Each tab shows its number of matches,
  and the list jumps to a tab that has matches.
- **⟳** reloads the list after the database has changed.
- Every sidebar section (Connection, Saved connections, Drivers, Objects) folds open and
  closed from its heading, and cactusdb remembers your choice.

| Engine | Object groups |
|--------|---------------|
| SQLite | Tables, Views, Triggers, Indexes |
| MySQL / MariaDB | Tables, Views, Procedures, Functions, Triggers, Events, Sequences (MariaDB 10.3+) |
| Firebird | Tables, Views, Procedures, Functions, Packages (Firebird 3+), Triggers, Generators, Domains, Exceptions |
| PostgreSQL | Tables, Views, Materialized views, Functions, Procedures, Triggers, Sequences, Domains (shown as `schema.name`; functions include their argument types, so overloads are listed separately) |
| SQL Server | Tables, Views, Procedures, Functions, Triggers, Sequences, Synonyms (shown as `schema.name`) |

- **Tables, views and materialized views** open in the **Data** tab with their rows. Switch to the
  **Definition** tab to see their `CREATE` statement.
- **Procedures, functions, triggers, generators and all other objects** open in the
  **Definition** tab, which shows their full source code with syntax colouring. Use
  **Copy** to copy it to the clipboard.
- **Generators / sequences** also show their **current value**. Reading it never
  advances the generator. Queries that would advance one (`nextval()`, `GEN_ID()`,
  `NEXT VALUE FOR`) are refused, because a sequence change can't be undone.
- Procedures and functions are only shown, never executed, so cactusdb stays read-only.
- If a definition is hidden (encrypted SQL Server modules, or a MySQL user without
  privileges), cactusdb says so instead of showing the source.

### Export formats

| Format | File | Notes |
|--------|------|-------|
| Excel | `.xlsx` | Bold header row; numbers are stored as numbers. Up to 1,048,575 rows (Excel's limit). |
| JSON | `.json` | An array of objects, one per row, keys in column order. Numbers and decimals stay exact; NULL becomes `null`. |
| XML | `.xml` | `<table name="…"><row><column name="…">value</column></row></table>`; NULL becomes `<column name="…" null="true"/>` |
| CSV | `.csv` | UTF-8 with a BOM, so Excel shows accented characters correctly; NULL becomes an empty field |

Files are named `<table>_<date>-<time>.<ext>`, e.g. `customers_20261025-142233.xlsx`.
Free-form queries are named `query_…`. Existing files are never overwritten.

<a id="ddl-export"></a>
### DDL export

**DDL** in the web UI, or `--exportddl all` on the command line, writes a script that
recreates the database structure, without data: `<database>_ddl_<date>-<time>.sql`.

- **Order:** domains, sequences/generators and exceptions come first, then tables with
  their indexes, then views, functions, procedures, packages, triggers and events.
  Foreign keys are added with `ALTER TABLE` after the last table, so tables can reference
  each other in any order. On MySQL/MariaDB the script switches foreign key checks off
  while it runs, as `mysqldump` does.
- **Tables** include their primary key, unique and check constraints, plain indexes
  and foreign keys (with `ON DELETE`/`ON UPDATE` rules).
- **Ready to run** in each database's own tool: Firebird routines are wrapped in
  `SET TERM ^ ;` for `isql`, MySQL/MariaDB routines in `DELIMITER $$` for the `mysql` client,
  and SQL Server objects are separated by `GO` for `sqlcmd` and SSMS.
- If an object's source can't be read (an encrypted SQL Server module, missing
  privileges), the script says so in a comment and continues.

The SQLite, MariaDB, PostgreSQL and Firebird scripts were tested by running them into an
empty database and exporting again: the second script is identical to the first.

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
   - MySQL, MariaDB and PostgreSQL sessions are switched to read-only, which also blocks
     `DROP TABLE` and similar commands.
   - MySQL, MariaDB, PostgreSQL and Firebird queries run in a read-only transaction
     that is always rolled back.
   - SQL Server queries run in a transaction that is always rolled back. SQL Server
     can undo both data and table changes this way, and commands that can't run in
     a transaction (`BACKUP`, `RESTORE`, `DBCC`, …) are refused.

## 4. Exporting from the command line (no browser)

Give cactusdb the database details and the table, and it exports straight to a file and
exits. No web server is started, and nothing is saved to `cactus.conn.db`.

```sh
cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima \
         --dbtable orders --exportas excel --exportto ./export
```
```
Password for root@10.66.1.136:3306:
Exporting 1 table(s) as xlsx to /home/me/cactusdb/export
  orders                             15234 rows  /home/me/cactusdb/export/orders_20261026-141502.xlsx
```

The password is **asked for** and not shown while you type. For scripts, pass it with
`--dbpassword xyz` or, better, the `CACTUSDB_DBPASSWORD` environment variable (see
[Security](#7-security)). SQLite needs no password.

| Option | What it does |
|--------|--------------|
| `--dbtype` | `sqlite`, `mysql`, `mariadb`, `firebird`, `mssql` (or `sqlserver`) or `postgres` (or `postgresql`). Required. |
| `--host` | The database server. Default `localhost`. For a SQL Server named instance use `server\INSTANCE`. |
| `--dbport` | The database port. Default: the standard port (3306, 3050, 1433 or 5432). |
| `--user` | The database user. Required, except for SQLite. |
| `--dbpassword` | The password. Leave it out to be prompted. |
| `--dbname` | The database. For SQLite the database file; for Firebird the database path or alias on the server. Required. |
| `--dbtable` | The table or view to export; several separated by commas (`--dbtable orders,customers`); or `all` for every table. |
| `--showtable` | `all` lists the tables and views; a table name prints its definition (`CREATE TABLE …`). |
| `--exportas` | `csv` (default), `json`, `xml` or `excel`. |
| `--exportto` | Where the files go. Default: the `export` folder next to cactusdb. |
| `--threads` | How many tables to export at the same time. Default `4`. |
| `--exportddl` | `all` writes the DDL of every object to one `.sql` file; table names (comma-separated) write just those tables. See [DDL export](#ddl-export). |
| `--saveconn` | `yes` (default) saves the connection; `no` doesn't. See below. |

Each table is saved as its own file named `<table>_<date>-<time>.<ext>`, with every
row and every column, exactly as the **Export** button in the browser does it.

**Parallel export with progress bars.** When you export several tables, cactusdb exports
up to four at the same time (change it with `--threads`), each over its own database
connection, and shows a live progress bar per table:
```
Exporting 5 table(s) as xlsx to /home/me/cactusdb/export (4 at a time)
  big1       [████████████████░░░░░░░░]  67%  202,900/300,000 rows  1.3s
  big2       [██████████░░░░░░░░░░░░░░]  41%  82,100/200,000 rows  1.1s
  big3       [████████████████████████] ✔ 100,000 rows  0.6s
  customer   [████████████████████████] ✔ 1,204 rows  0.1s
  orders     [                        ]  waiting
```
When the output isn't a terminal (a cron job, or `> log.txt`), cactusdb prints one plain
line per finished table instead.

**The connection is saved** in `cactus.conn.db` once it works, named `cli-<host>` (for
SQLite `cli-<file name>`), e.g. `cli-10.66.1.136`, with the password encrypted like any
other saved connection. Open the web UI and it's in *Saved connections*, ready to click.
A later export to the same host replaces it. Use `--saveconn no` to not save it, and
`--conn-db`/`--key-file` to use another connection file.

**Several tables at once:** separate the names with commas, e.g.
`--dbtable table1,table3,table88`. If you put spaces after the commas, put quotes around the
list: `--dbtable "table1, table3, table88"`. All names are checked first, so a misspelt
table stops the run before any file is written. A table named twice is exported once.

Table names are matched exactly, then ignoring upper/lower case, then without the schema:
on PostgreSQL and SQL Server, `--dbtable orders` finds `public.orders` or `dbo.orders`. If
two schemas both have an `orders` table, cactusdb asks you to name it in full
(`sales.orders`).

More examples:
```sh
# what's in the database?
cactusdb --dbtype postgres --host db.local --user report --dbname shop --showtable all

# the definition of one table
cactusdb --dbtype firebird --host fbserver --user SYSDBA --dbname /data/shop.fdb --showtable CUSTOMER

# three tables as CSV, one file each
cactusdb --dbtype mariadb --host 10.66.1.136 --user root --dbname optima --dbtable table1,table3,table88

# the DDL of the whole database, ready to run in isql
cactusdb --dbtype firebird --host fbserver --user SYSDBA --dbname /data/shop.fdb --exportddl all

# every table of an SQLite file, as JSON
cactusdb --dbtype sqlite --dbname ./shop.db --dbtable all --exportas json

# a scheduled export (Linux cron / macOS): no prompt, password from the environment
CACTUSDB_DBPASSWORD='s3cret' cactusdb --dbtype mssql --host sql01 --user reader --dbname Sales \
    --dbtable dbo.Invoices --exportas csv --exportto /srv/exports
```
On Windows put the whole command on one line (the `\` line breaks are for macOS and
Linux), and set the password with `set CACTUSDB_DBPASSWORD=s3cret` (Command Prompt) or
`$env:CACTUSDB_DBPASSWORD='s3cret'` (PowerShell).

cactusdb exits with status **0** when everything was exported, **1** when connecting or
an export failed (the other tables are still exported), and **2** for a mistake in the
options, so scripts can check the result. The same [read-only](#read-only) protection
applies as in the browser.

## 5. Startup options

These options are for the web UI. The options for exporting without a browser are in
[section 4](#4-exporting-from-the-command-line-no-browser).

```sh
cactusdb --port 8087 --web yes --exportto ./export
```

| Option | Default | What it does |
|--------|---------|--------------|
| `--web` | `yes` when no `--dbtype` is given | `--web yes` starts the web UI. It's the default, so `cactusdb --port 8087` does the same. |
| `--port` | `8080` | The port to open in your browser: `http://localhost:<port>` |
| `--exportto` | folder `export` next to the cactusdb program | Where exported files are saved. Created automatically if missing. A relative path such as `./export` is relative to the folder you start cactusdb from. |
| `--conn-db` | `cactus.conn.db` next to the cactusdb program | The file saved connections are stored in |
| `--key-file` | `cactus.key` next to the connection file | The key that encrypts saved usernames and passwords. Created automatically. Not used when `CACTUSDB_PASSPHRASE` is set. |
| `--host` | `127.0.0.1` (this computer only) | Use `0.0.0.0` to let other computers on your network use cactusdb at `http://<this computer's IP address>:<port>`; the addresses are printed at startup. See [Security](#7-security). |
| `--runas` | `foreground` | `background` starts the web UI in the background: it keeps running after you close the terminal and logs to `--logfile`. See below. |
| `--logfile` | `cactusdb.log` next to the program, with `--runas background` | Every action is logged here: connections (never passwords), tables opened, queries, exports, saved connections, and every request with its result. In the foreground, the log goes to the terminal and, if you give `--logfile`, to that file too. |
| `--version` | | Print the version and exit |
| `--help` | | List the options |

Examples:
```sh
cactusdb --port 9000                                  # different port
cactusdb --port 8087 --exportto D:\Reports\exports    # exports to another drive (Windows)
cactusdb --port 8087 --exportto ~/Desktop/exports     # exports to your Desktop (macOS/Linux)
cactusdb --port 8087 --web yes --host 0.0.0.0         # open from other machines: http://<ip address>:8087
CACTUSDB_PASSPHRASE='my long passphrase' cactusdb --port 8080   # encrypt with a passphrase (macOS/Linux)
```

### Running in the background

```sh
cactusdb --port 8087 --runas background --host 0.0.0.0
```
```
cactusdb 1.4.0 is running in the background (process id 48211)
  open http://localhost:8087
  open http://192.168.1.20:8087
  log file: /home/me/cactusdb/cactusdb.log
  stop it with: kill 48211
```
cactusdb starts a copy of itself without a terminal, waits until it answers, and tells
you where to open it. If it can't start (the port is in use, the connection file can't be
opened…), you get the error straight away. The process id is also written to
`cactusdb.pid` next to the log file, so you can stop it later:

- macOS/Linux: `kill $(cat cactusdb.pid)`
- Windows: `taskkill /PID <id> /F` (the id is in `cactusdb.pid`)

The log looks like this:
```
2026/10/26 17:26:30 started in the background, process id 70769, listening on 127.0.0.1:8087
2026/10/26 17:26:30 127.0.0.1 connect mariadb: root@10.66.1.136:3306/optima as "cli-10.66.1.136", session f1fc0d2d
2026/10/26 17:26:30 127.0.0.1 [cli-10.66.1.136] open table orders
2026/10/26 17:26:30 127.0.0.1 [cli-10.66.1.136] 1000 row(s) (more not shown) in 41ms
2026/10/26 17:26:31 127.0.0.1 [cli-10.66.1.136] query: delete from orders
2026/10/26 17:26:31 127.0.0.1 [cli-10.66.1.136] query FAILED: cactusdb is read-only: only SELECT queries are allowed …
2026/10/26 17:26:35 127.0.0.1 [cli-10.66.1.136] export xlsx: 15234 row(s) to /home/me/cactusdb/export/orders_20261026-172635.xlsx
```

| Environment variable | What it does |
|----------------------|--------------|
| `CACTUSDB_DBPASSWORD` | The database password for [command-line exports](#4-exporting-from-the-command-line-no-browser), so you aren't prompted for it. |
| `CACTUSDB_PASSPHRASE` | Derives the encryption key for saved usernames and passwords from this passphrase instead of using `cactus.key`. Nothing secret is then stored on disk, but you must set the same passphrase every time. On Windows: `set CACTUSDB_PASSPHRASE=…` (Command Prompt) or `$env:CACTUSDB_PASSPHRASE='…'` (PowerShell). |

## 6. Files cactusdb creates

Next to the program (unless you chose other locations with the options above):

```
cactusdb/
├── cactusdb(.exe)       the program
├── cactus.conn.db       your saved connections (an SQLite database)
├── cactus.key           the key that encrypts saved usernames and passwords
├── cactusdb.log         the action log (with --runas background or --logfile)
├── cactusdb.pid         the process id of a background cactusdb, while it runs
└── export/              your exported files
```

To move cactusdb to another computer and keep your connections, copy `cactus.conn.db`
**and `cactus.key`** along with it. You don't need the key if you use `CACTUSDB_PASSPHRASE`;
use the same passphrase on the new computer. To forget all saved connections, delete
both files. If you lose the key or forget the passphrase, the saved connections can't be
decrypted. Delete `cactus.conn.db` and save them again.

**Moving the program into a system folder** such as `/usr/local/bin` or
`C:\Program Files`? cactusdb may not be allowed to write next to itself there. In that
case, point the files somewhere writable:
```sh
cactusdb --port 8080 --conn-db ~/.cactusdb/cactus.conn.db --exportto ~/cactusdb-exports
```

## 7. Security

- **Saved usernames and passwords are encrypted** in `cactus.conn.db` with AES-256-GCM.
  Each value has its own random nonce and is tied to its column. The other connection
  details (name, engine, host, port, database) are not secret and stay readable.
- **The key** is a random 256-bit key in `cactus.key`, created on first start. You can
  also set `CACTUSDB_PASSPHRASE`, and the key is then derived from your passphrase
  (PBKDF2-SHA256, 600,000 rounds, random salt) and never written to disk.
  - With the **key file**, someone who copies only `cactus.conn.db` can't read the
    passwords. Anyone who gets *both* files can, so keep `cactus.key` private and store
    or back it up separately.
  - With a **passphrase**, the file on its own is never enough.

  On macOS and Linux both files are readable only by your user account. Don't commit
  either one to git.
- **Upgrading from an older cactusdb** that stored passwords in plain text: on first start
  the saved credentials are encrypted automatically, and the file is compacted so no
  plain-text copy is left behind.
- Saved passwords never leave the cactusdb process. The browser only sees whether a
  connection has one.
- By default cactusdb is only reachable **from your own computer**. If you start it with
  `--host 0.0.0.0`, anyone who can reach that port can use your saved connections and
  browse those databases. There is no login screen, so only do this on a trusted network.
- **Command-line passwords:** a password given with `--dbpassword` can be seen by other
  users of the computer (in the process list) and is kept in your shell history. Leave it
  out to be prompted (nothing is shown as you type), or set `CACTUSDB_DBPASSWORD` in scripts.
- cactusdb never writes to your databases (see [Read-only](#read-only)). The only files it
  writes are `cactus.conn.db`, `cactus.key` and the files in the export folder.

## 8. Troubleshooting

| Problem | Fix |
|---------|-----|
| `cannot listen on 127.0.0.1:8080: …` | Another program uses that port. Pick another: `--port 8090`. |
| `the background server stopped:` followed by log lines | The background copy couldn't start; the lines say why (often the port or the connection file). |
| `warning: the connection won't be saved` | A command-line export couldn't open `cactus.conn.db` (e.g. a different `cactus.key`). The export still runs. |
| `cannot decrypt the saved connections in … with …` | The key doesn't match the one the connections were saved with. Put the original `cactus.key` back, set the same `CACTUSDB_PASSPHRASE` as before (or unset it if you used the key file), or start fresh with `--conn-db new.conn.db`. |
| `cannot read password: no password given` | A command-line export needs a password but isn't running in a terminal (e.g. from cron). Use `CACTUSDB_DBPASSWORD` or `--dbpassword`. |
| `table "orders" is ambiguous: public.orders, sales.orders` | Name the table with its schema: `--dbtable sales.orders`. |
| `--port only applies to the web UI` | In a command-line export the database port is `--dbport`. |
| `cannot create export folder …` | The folder isn't writable. Use `--exportto` with a folder you own. |
| Browser shows *can't connect* | Is cactusdb still running in the terminal? Use the exact URL it printed. |
| `sqlite file not found` | Check the path. It's the path on the computer running cactusdb. |
| MySQL `Access denied for user` | Wrong username or password, or the user isn't allowed to connect from your machine's address. |
| Firebird `I/O error … CreateFile (open)` or `file … is not a valid database` | The database path must be the path **on the Firebird server**, not on your computer, or an alias defined in the server's `databases.conf`. |
| Firebird `Your user name and password are not defined` | Check the username and password. Both Srp and legacy authentication are supported. |
| PostgreSQL `password authentication failed` / `no pg_hba.conf entry` | Check the username and password, and that the server's `pg_hba.conf` allows connections from your machine. |
| PostgreSQL `database "…" does not exist` | Fill in the database name. When it's blank, PostgreSQL uses a database with the same name as the user. |
| SQL Server `Login failed for user` | Check the login and password, and that the server allows **SQL Server authentication** (mixed mode). Windows/Active Directory logins aren't supported. |
| SQL Server `unable to open tcp connection` / timeout | Check that TCP/IP is enabled in SQL Server Configuration Manager and the port (default 1433) isn't blocked by a firewall. For a named instance use `server\INSTANCE` with an empty port, and make sure the SQL Server Browser service is running. |
| `cactusdb is read-only: only SELECT queries are allowed` | Working as intended: cactusdb doesn't run statements that change data. |
| `result has more rows than Excel allows` | Excel holds at most 1,048,575 data rows. Add a `WHERE` clause, or export to CSV or JSON instead. |
| `not connected (session expired)` | cactusdb was restarted. Connect again. |

## 9. Supported databases

| Database | Versions |
|----------|----------|
| SQLite | 3.x files |
| MySQL | 5.6 and newer, including 8.x and 9.x |
| MariaDB | 10.x and 11.x |
| Firebird | 2.5, 3, 4 and 5, with Srp authentication and wire encryption |
| PostgreSQL | 10 and newer (tested with PostgreSQL 17), and compatible servers such as Amazon RDS/Aurora, Azure Database for PostgreSQL and Supabase. TLS is used when the server offers it. |
| Microsoft SQL Server | 2012 and newer (tested with SQL Server 2022); Azure SQL Database should work too. Uses SQL Server authentication; TLS encryption is used when the server offers it, and the server certificate is trusted like SSMS's *Trust server certificate* option. |

All drivers are built into cactusdb and listed in the **Drivers** panel. Unlike tools such
as DBeaver, there is nothing to download, and no client library (libmysqlclient,
fbclient.dll, libpq, ODBC drivers, …) needs to be installed.

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
docker run -d --rm --name cdb-pg -e POSTGRES_PASSWORD='pg p@ss:/w' -e POSTGRES_DB=shop -p 15432:5432 postgres:17-alpine
docker run -d --rm --name cdb-mssql --platform linux/amd64 -e ACCEPT_EULA=Y -e 'MSSQL_SA_PASSWORD=Cactus#Pass123' -p 11433:1433 mcr.microsoft.com/mssql/server:2022-latest
# create a 2-row `customers` table (`CUSTOMER` in Firebird, `dbo.customers` in a `shop` database on SQL Server, `public.customers` on PostgreSQL) in each, then:
CACTUSDB_IT=1 go test ./...
```

| File | Contents |
|------|----------|
| `main.go` | Command-line options, web server, API, action log |
| `cli.go` | Exporting from the command line (`--dbtype`, `--dbtable`, `--showtable`, password prompt) |
| `progress.go` | Parallel export of several tables, and the terminal progress bars |
| `webexport.go` | *Export all tables* in the web UI |
| `ddl.go`, `tables.go` | DDL export; constraints, indexes and foreign keys of tables |
| `background.go` | `--runas background` |
| `sys_windows.go`, `sys_other.go` | Platform specifics: detaching from the terminal, console colours |
| `db.go` | Connecting, listing columns, running queries |
| `objects.go` | Listing objects (tables, views, procedures, …) and building their definitions |
| `readonly.go` | The read-only query check |
| `export.go` | Excel / JSON / XML / CSV export |
| `store.go` | Saved connections (`cactus.conn.db`) |
| `crypt.go` | Encryption of saved usernames and passwords |
| `web/index.html`, `web/cactusdb.svg` | The browser interface and icon, embedded into the binary |

## Changes

See [CHANGELOG.md](CHANGELOG.md) for what's new in each version.

## License

Public domain, under the same terms as SQLite: see [LICENSE](LICENSE).

> May you do good and not evil.
> May you find forgiveness for yourself and forgive others.
> May you share freely, never taking more than you give.

The third-party drivers built into the binary keep their own licenses (BSD-3-Clause,
MPL-2.0 and MIT), listed in [LICENSE](LICENSE).
