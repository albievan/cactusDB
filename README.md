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
then open **http://localhost:8080** in your browser, or **http://<this computer's IP address>:8080**
from any other computer on your network. Or export without the browser:
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
cactusdb 1.12.0
  driver SQLite          modernc.org/sqlite v1.61.0 (bundled)
  driver MySQL, MariaDB  github.com/go-sql-driver/mysql v1.10.1 (bundled)
  driver Firebird        github.com/nakagami/firebirdsql v0.9.23 (bundled)
  driver SQL Server      github.com/microsoft/go-mssqldb v1.11.2 (bundled)
  driver PostgreSQL      github.com/jackc/pgx/v5 v5.11.0 (bundled)
  saved connections: /home/me/cactusdb/cactus.conn.db (credentials encrypted with key file /home/me/cactusdb/cactus.key)
  export folder:     /home/me/cactusdb/export
  open http://localhost:8080
  open http://192.168.1.20:8080
  (listening on every network interface; --host 127.0.0.1 limits it to this computer)
  press Ctrl+C to stop
```
Open one of the URLs shown in your browser. cactusdb listens on every network interface,
so other computers on your network can use it too, at `http://<this computer's IP address>:<port>`
(the addresses are listed at startup). If they can't connect, allow the port in this
computer's firewall (see [Troubleshooting](#8-troubleshooting)). Leave the terminal window open while you use
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
   saved in a subfolder of the export folder named after its type (`json`, `csv`,
   `xlsx` or `xml`), and a bar shows the full path with a **Download** link.
   **Clear** removes that bar again.

**Saved connections** appear in their own list. Click one to connect, or **✕** to delete it.

**Adding and editing connections.** Click **+ New** next to *Saved connections* to open the
connection form: a name, the database type and the connection details. Click
**Test connection** to check them before saving. cactusdb connects, reports the server
version and the number of tables (or the exact error), and disconnects again. Nothing is
saved until you click **Save**. While you type a **new** connection, the eye button in
the password field shows what you typed. When you edit or open a saved connection the
password can't be shown: the button isn't there, and the saved password never reaches
the browser anyway (leave the field blank to keep it). **Edit** on a saved connection opens the same form, where
you can test it, change it or rename it. Saved passwords are never sent to the browser:
the password box shows *saved – leave blank to keep*. Leave it empty to keep the stored
password (testing uses it too), or type a new one to replace it.

**Export all tables.** Under the *Objects* heading, the four file icons (**XLS**, **CSV**,
**JSON**, **XML**) export every table of the database, one file per table, four tables at a
time. Each table gets a progress bar with its row count, and a download link when it's
done. The list fills the whole data area (in several columns on wide screens), so you see
as many tables as possible. Opening a table brings the grid back, and **✕** closes the
list while the export carries on. When the export is finished, **Clear** removes the
export status. Disconnecting stops an export that is still running.

**Exported files.** The **Exported files** tab lists every file in the export folder: data
exports, DDL scripts and backups, with when each was created and its size (uncompressed).
- **Sort:** click a column heading to sort by file name, type, date or size. Click it again
  to reverse the order.
- **Filter by type:** the **Type** buttons show how many files of each type there are
  (Excel, CSV, JSON, XML, DDL, Backup). Click one to show only that type, or click several
  to combine them, for example CSV and JSON. **All** shows every type again.
- **Search by name:** the **Name** box shows files whose name contains what you type,
  ignoring case. With several words, for example `order 2026`, a name must contain all of
  them, in any order. The matching text is highlighted. **Esc** or **✕** clears the box.
- Type, name and search filters combine. The footer shows how many files are shown and
  their size. If nothing matches, **Clear the filters** resets them. The filters and the
  sort order are remembered in your browser.
- **Select:** tick the files you want, or click their rows. The heading checkbox selects every
  file shown.
- **Preview** (the 👁 button after a file's checkbox) opens the file in a window:
  - CSV and Excel files (the first sheet) as a table; JSON exports as a table of their objects
  - XML, DDL scripts and other text as text
  - a backup as the list of files inside, followed by the start of its SQL script

  A preview shows at most the first 200 rows or 256 KB, and says when there is more.
  From the preview you can also download or delete that file.
- **Delete:** the 🗑 button deletes one file, and **Delete selected** deletes every ticked file.
  cactusdb always asks first, with **Cancel** selected, and shows the files and their total
  size. Deleted files are gone from the export folder for good.
- **Download selected:** cactusdb first compresses the chosen files into one `.zip` at the
  highest compression level, each file in a folder for its type. Only then does the download
  start, and the page shows the original and the compressed size.

The prepared `.zip` can be downloaded again for 15 minutes. After that, or when cactusdb
stops, it's deleted from the server's temporary folder. Your exports themselves are never
changed.

**Export folder.** The *Export folder* section in the sidebar shows where exports are saved.
**Change** lets you type another folder: a full path on the computer cactusdb runs on (a
leading `~` means your home folder). cactusdb creates it if needed, checks it can write
there, and saves the choice in `cactus.conn.db`, so it's used again after a restart, by the
web UI and by command-line exports. **Use default** goes back to the `export` folder next to
the program. Files exported before the change can still be downloaded until cactusdb stops.
`--exportto` on the command line always wins over the saved folder.

**Overall progress.** While an export runs, a progress bar below the **Run** button shows
how far it has got. It stays visible while you open other tables.
- **Export all tables:** the bar shows the percentage, tables done out of the total, rows
  exported, time elapsed and about how long is left. Any failures appear in red.
- **Single exports, DDL and backups:** the server doesn't report how far these have got,
  so the bar moves to show that the work is in progress, with the elapsed time.
- **When it's finished:** the bar turns into a summary, for example *✔ Exported 60 of 60
  tables as Excel · 1,800,000 rows in 0:42*. **Clear** removes it.

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
recreates the database structure, without data: `<database>_ddl_<date>-<time>.sql`, in the
`sql` subfolder of the export folder.

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

<a id="backup"></a>
### Database backup

**Backup** (next to **DDL** under *Objects*), or `--backup yes` on the command line, backs
up the whole connected database to `<database>_backup_<date>-<time>.zip` in the `backup`
subfolder of the export folder. The web UI shows a **Download** link when it's done.

The `.zip` is compressed at the maximum Deflate level and holds:

- **`<database>.sql`**, a script that restores everything: the [DDL](#ddl-export) of every
  object with **every row of every table** as `INSERT` statements. The rows are inserted after
  the tables are created, but before foreign keys, indexes, views and triggers, so loading
  them never fires a trigger or trips over a foreign key. Afterwards sequences, generators
  and identity columns are set to where they were, so new rows don't reuse existing keys.
  Computed/generated columns are left out (the database computes them again).
- **SQLite only:** `<database>.db`, an exact copy of the database file, made with
  `VACUUM INTO` (consistent even while others use the database).

Restore into an **empty** database with the database's own tool:

| Database | Restore |
|----------|---------|
| SQLite | use the `.db` file as it is, or `sqlite3 new.db < shop.sql` |
| MySQL / MariaDB | `mysql -u root -p newdb < shop.sql` |
| PostgreSQL | `psql -v ON_ERROR_STOP=1 -U postgres newdb < shop.sql` |
| Firebird | create the database, then `isql -ch UTF8 -user SYSDBA -password … host:/path/new.fdb < shop.sql` |
| SQL Server | `sqlcmd -S host -d newdb -i shop.sql` (the script uses `GO` and `SET IDENTITY_INSERT`) |

Backing up only reads the database; like everything else in cactusdb, nothing is written to
it. A table whose rows can't be read is noted in the script and named in the result, and
the rest is still backed up. The backup runs while the page waits; for very large
databases the command line (`--backup yes`) shows the progress per table.

Backups of SQLite, MariaDB, PostgreSQL and Firebird databases (with quotes, backslashes,
Unicode, binary data, decimals, dates, NULLs, identity, generated columns, foreign keys and
triggers) were tested by restoring them into an empty database: the data, the next
identity/sequence value and the triggers matched the original. SQL Server backups are
built the same way but weren't tested against a live server.

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
Exporting 1 table(s) as xlsx to /home/me/cactusdb/export/xlsx
  orders                             15234 rows  /home/me/cactusdb/export/xlsx/orders_20261026-141502.xlsx
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
| `--exportto` | The base folder for exports. Default: the `export` folder next to cactusdb. Each type goes to its own subfolder: `json`, `csv`, `xlsx` (Excel), `xml`, `sql` for DDL and `backup` for backups. |
| `--threads` | How many tables to export at the same time. Default `4`. |
| `--exportddl` | `all` writes the DDL of every object to one `.sql` file; table names (comma-separated) write just those tables. See [DDL export](#ddl-export). |
| `--backup` | `yes` backs up the whole database (DDL and every row) to a compressed `.zip` in the `backup` subfolder. See [Database backup](#backup). |
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
| `--exportto` | folder `export` next to the cactusdb program | The base folder for exported files. Each type is saved in its own subfolder (`json`, `csv`, `xlsx`, `xml`, `sql` for DDL and `backup` for backups). Created automatically if missing. A relative path such as `./export` is relative to the folder you start cactusdb from. |
| `--conn-db` | `cactus.conn.db` next to the cactusdb program | The file saved connections are stored in |
| `--key-file` | `cactus.key` next to the connection file | The key that encrypts saved usernames and passwords. Created automatically. Not used when `CACTUSDB_PASSPHRASE` is set. |
| `--host` | `0.0.0.0` (every network interface) | The address to listen on. By default other computers on your network can use cactusdb at `http://<this computer's IP address>:<port>`; the addresses are printed at startup. Use `127.0.0.1` to allow only this computer, `::` to listen on IPv6 too, or one of this computer's IP addresses to listen on that network only. See [Security](#7-security). |
| `--runas` | `foreground` | `background` starts the web UI in the background: it keeps running after you close the terminal and logs to `--logfile`. See below. |
| `--logfile` | `cactusdb.log` next to the program, with `--runas background` | Every action is logged here: connections (never passwords), tables opened, queries, exports, saved connections, and every request with its result. In the foreground, the log goes to the terminal and, if you give `--logfile`, to that file too. |
| `--version` | | Print the version and exit |
| `--help` | | List the options |

Examples:
```sh
cactusdb --port 9000                                  # different port
cactusdb --port 8087 --exportto D:\Reports\exports    # exports to another drive (Windows)
cactusdb --port 8087 --exportto ~/Desktop/exports     # exports to your Desktop (macOS/Linux)
cactusdb --port 8087 --web yes                        # open from any machine: http://<ip address>:8087
cactusdb --port 8087 --host 127.0.0.1                 # this computer only
CACTUSDB_PASSPHRASE='my long passphrase' cactusdb --port 8080   # encrypt with a passphrase (macOS/Linux)
```

### Running in the background

```sh
cactusdb --port 8087 --runas background
```
```
cactusdb 1.12.0 is running in the background (process id 48211)
  open http://localhost:8087
  open http://192.168.1.20:8087
  log file: /home/me/cactusdb/cactusdb.log
  stop it with: kill 48211
```
cactusdb starts a copy of itself without a terminal, waits until it answers, and tells
you where to open it. If it can't start (the port is in use, the connection file can't be
opened…), you get the error straight away. The process id is also written to
`cactusdb.pid` next to the log file, so you can stop it later:

- **From the browser:** a background cactusdb shows a **⏻ Shutdown** button at the top
  right, next to *Disconnect*. Click it and confirm. cactusdb cancels running exports,
  closes every database connection and the connection file, removes `cactusdb.pid`,
  logs `stopped` and exits. The page then says it has shut down, with the command to
  start it again. (The button only appears in background mode; in a terminal, press
  Ctrl+C.)
- macOS/Linux: `kill $(cat cactusdb.pid)`
- Windows: `taskkill /PID <id> /F` (the id is in `cactusdb.pid`)

The log looks like this:
```
2026/10/26 17:26:30 started in the background, process id 70769, listening on 0.0.0.0:8087
2026/10/26 17:26:30 127.0.0.1 connect mariadb: root@10.66.1.136:3306/optima as "cli-10.66.1.136", session f1fc0d2d
2026/10/26 17:26:30 127.0.0.1 [cli-10.66.1.136] open table orders
2026/10/26 17:26:30 127.0.0.1 [cli-10.66.1.136] 1000 row(s) (more not shown) in 41ms
2026/10/26 17:26:31 127.0.0.1 [cli-10.66.1.136] query: delete from orders
2026/10/26 17:26:31 127.0.0.1 [cli-10.66.1.136] query FAILED: cactusdb is read-only: only SELECT queries are allowed …
2026/10/26 17:26:35 127.0.0.1 [cli-10.66.1.136] export xlsx: 15234 row(s) to /home/me/cactusdb/export/xlsx/orders_20261026-172635.xlsx
```

| Environment variable | What it does |
|----------------------|--------------|
| `CACTUSDB_DBPASSWORD` | The database password for [command-line exports](#4-exporting-from-the-command-line-no-browser), so you aren't prompted for it. |
| `CACTUSDB_PASSPHRASE` | Derives the encryption key for saved usernames and passwords from this passphrase instead of using `cactus.key`. Nothing secret is then stored on disk, but you must set the same passphrase every time. On Windows: `set CACTUSDB_PASSPHRASE=…` (Command Prompt) or `$env:CACTUSDB_PASSPHRASE='…'` (PowerShell). |

### Deploying to Docker, k3s or Kubernetes — `deploy.sh`

`deploy.sh` installs cactusdb as a container on **Docker** (Docker Desktop, Docker Engine or
Podman), **Docker CE** on a Linux server (this one, or another one over SSH), **k3s** or any
**Kubernetes** cluster, with all settings in an env file:

```sh
cp .env.example .env             # then edit .env
./deploy.sh --docker dd  --env .env --install
./deploy.sh --docker ce  --env .env --install     # CE_HOST=admin@server in .env, or empty for this machine
./deploy.sh --docker k3s --env .env --install
./deploy.sh --docker k8s --env .env --install
./deploy.sh --env .env --status
./deploy.sh --env .env --uninstall            # keeps the data
./deploy.sh --env .env --uninstall --purge    # also deletes the data volumes (asks first)
```

`.env.example` lists every setting with its default and an explanation. Lines are read as
`KEY=VALUE` (the file isn't run), so it's safe to keep secrets such as
`CACTUSDB_PASSPHRASE` in `.env`, which git ignores. `--install` again deploys a new build
over the running one and keeps the data. Without `--docker`, the target comes from
`DEPLOY_TARGET` in the env file, or for `--uninstall`/`--status` from what's running.

| Target | What `--install` does | Data |
|--------|-----------------------|------|
| `dd` | Builds the image and runs a container (restart `unless-stopped`, health check) as your user, publishing `CACTUSDB_PORT` on `DD_BIND` (every address by default) | Host folders `DD_CONN_DIR` and `DD_EXPORT_DIR` (default `~/cactusdb/conn` and `~/cactusdb/export`) |
| `ce` | Docker CE (Docker Engine) on a Linux server. With `CE_HOST=user@server` every docker command runs on the server over SSH: the image is built there for its CPU, and the container runs as the SSH user. `CE_INSTALL_DOCKER=yes` first installs Docker CE if it's missing (Docker's `get.docker.com` script, needs sudo), starts it at boot and adds the user to the `docker` group | Folders on the server, `CE_CONN_DIR` and `CE_EXPORT_DIR` (default `/srv/cactusdb/conn` and `/srv/cactusdb/export`), created and handed to the run user automatically |
| `k3s` | Builds the image for the nodes' architecture and imports it into k3s (on this machine, over SSH with `K3S_SSH`, or your own `K3S_IMPORT` command), then applies a Namespace, Deployment, Service and volumes | PersistentVolumeClaims (k3s's `local-path`), or folders on the node with `K8S_STORAGE=hostpath` |
| `k8s` | The same, pushing the image to `K8S_REGISTRY` (Docker Desktop's Kubernetes uses the local image directly) | PersistentVolumeClaims (default StorageClass), or `hostpath` |

On k3s and Kubernetes:

- **Reaching it:** `K8S_SERVICE_TYPE=NodePort` (default; `K8S_NODE_PORT` fixes the port),
  `LoadBalancer` (k3s has a built-in one), or `ClusterIP` for use behind your own Ingress.
  The script lists the addresses, tries each one, and when none answers from your machine
  (e.g. Docker Desktop's Kubernetes or a firewall) prints the `kubectl port-forward`
  command to use instead.
- **Architecture:** the image is built for the nodes' CPU (amd64 or arm64), also from an
  arm64 Mac for an amd64 server: the image has no build steps that need emulation. A
  cluster with both kinds of node needs `K8S_REGISTRY` (multi-architecture image via
  `docker buildx`) or `K8S_NODE` to pin cactusdb to one node.
- **Safe defaults:** one replica (one writer for `cactus.conn.db`), `Recreate` updates,
  runs as an unprivileged user (`K8S_RUN_AS_UID`, default 1000) with a read-only root
  filesystem and no capabilities; a short init step hands the volumes to that user.
  Readiness and liveness checks use `/api/info`. `CACTUSDB_PASSPHRASE` is stored as a
  Secret.
- **Uninstall** removes the Deployment, Service and Secret, keeps the data volumes unless
  you add `--purge`, never touches folders on the nodes, and removes the namespace only if
  `deploy.sh` created it and it's empty.
- `kubectl` must be able to reach the cluster (`K8S_CONTEXT` picks a context; `KUBECTL`
  can be e.g. `sudo k3s kubectl` on a k3s server). Building needs Docker or Podman.

For `ce` over SSH you need SSH key access to the server and the `docker` command on your
own machine (Docker Desktop, or just the Docker CLI). The data folders are created by a
short-lived container, so no `sudo` is needed after Docker CE is installed.

Tested on Docker Desktop (Docker and its Kubernetes), on k3s v1.31, and with `ce` against a
fresh Ubuntu 24.04 server over SSH (Docker CE 29 installed by `CE_INSTALL_DOCKER=yes`): install, reinstall
with the data kept, NodePort, LoadBalancer, PVC and hostPath storage, the passphrase
Secret, and uninstall with and without `--purge`.

### Running in a container (Docker only) — `deploy-container.sh`

The older script, with options on the command line instead of an env file. Everything it
does, `deploy.sh --docker dd` (or `ce`) does too. For Docker CE on a Linux server, add
`--remote user@server` (and `--install-docker` to install Docker CE there first); the data
folders are then on the server, `/srv/cactusdb` by default:

```sh
./deploy-container.sh up --remote admin@server --install-docker --port 8087
./deploy-container.sh status --remote admin@server       # also logs, restart, down
```

`deploy-container.sh` builds a small Docker (or Podman) image from the Linux binary in
`dist/` and runs cactusdb in it, listening on every address. The port is published on the
host, and the export folder and the saved-connections folder are stored on the host, so
they survive upgrades and re-creating the container.

```sh
./deploy-container.sh                                   # port 8080, data in ~/cactusdb
./deploy-container.sh up --port 8087 --export-dir /srv/cactusdb/export --conn-dir /srv/cactusdb/conn
./deploy-container.sh up --port 8087 --sqlite-dir /srv/sqlite   # also browse SQLite files
./deploy-container.sh status | logs | restart | down
```
```
cactusdb 1.12.0 is running in container cactusdb
  open http://localhost:8087
  open http://192.168.1.20:8087
  saved connections: /srv/cactusdb/conn/cactus.conn.db (key: /srv/cactusdb/conn/cactus.key)
  exports:           /srv/cactusdb/export/{json,csv,xlsx,xml,sql,backup}
```

| Option | Default | What it does |
|--------|---------|--------------|
| `--port` | `8080` | Port to publish on the host (cactusdb listens on the same port in the container) |
| `--bind` | `0.0.0.0` | Host address to publish on; `127.0.0.1` keeps it to the host itself |
| `--data-dir` | `~/cactusdb` | Base folder on the host for `export/` and `conn/` |
| `--export-dir` | `<data-dir>/export` | Host folder for exports (`/opt/cactusdb/export` in the container) |
| `--conn-dir` | `<data-dir>/conn` | Host folder for `cactus.conn.db` and `cactus.key` (`/opt/cactusdb/conn`); readable only by you |
| `--sqlite-dir` | | Host folder of SQLite databases, mounted read-only at `/sqlite`: connect to `/sqlite/<file>` |
| `--tz` | `$TZ` | Time zone for log and file-name times, e.g. `Europe/Amsterdam` |
| `--name`, `--image` | `cactusdb` | Container and image name |
| `--runtime` | docker, else podman | Container engine to use |
| `--from-source` | | Compile the binary with Go first instead of using `dist/` |
| `--remote` | | `user@server`: deploy to Docker CE on that Linux server over SSH (via `deploy.sh --docker ce`) |
| `--install-docker` | | With `--remote`: install Docker CE on the server first if it's missing |

- `up` replaces a running container with the new image; your data folders are untouched.
  `down` removes the container and keeps the data.
- The container runs as your user, so exports and connection files on the host belong to
  you. It restarts automatically (`--restart unless-stopped`) and has a health check.
- The log goes to `./deploy-container.sh logs` (`docker logs`). The ⏻ Shutdown button isn't
  shown; stop it with `./deploy-container.sh down`.
- Set `CACTUSDB_PASSPHRASE` before running the script to use a passphrase instead of
  `cactus.key` (see the table above).
- **Export folder in a container:** a folder chosen in the web UI is a path *inside* the
  container. Only `/opt/cactusdb/export` (and folders under it, such as
  `/opt/cactusdb/export/team`) is stored on the host; anything else is lost when the
  container is re-created.
- The firewall and [Security](#7-security) notes apply as for the program itself.

## 6. Files cactusdb creates

Next to the program (unless you chose other locations with the options above):

```
cactusdb/
├── cactusdb(.exe)       the program
├── cactus.conn.db       your saved connections and the chosen export folder (an SQLite database)
├── cactus.key           the key that encrypts saved usernames and passwords
├── cactusdb.log         the action log (with --runas background or --logfile)
├── cactusdb.pid         the process id of a background cactusdb, while it runs
└── export/              your exported files, one subfolder per type
    ├── backup/          database backups (.zip)
    ├── csv/
    ├── json/
    ├── sql/             DDL scripts
    ├── xlsx/            Excel
    └── xml/
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
- By default cactusdb listens on **every network interface**, so anyone who can reach
  the port can use your saved connections, browse those databases and (in background
  mode) shut cactusdb down. There is no login screen, so run it only on a trusted
  network, or start it with `--host 127.0.0.1` to allow only your own computer. A
  firewall rule that allows the port only from your workstations' addresses is a good
  extra safeguard.
- **Command-line passwords:** a password given with `--dbpassword` can be seen by other
  users of the computer (in the process list) and is kept in your shell history. Leave it
  out to be prompted (nothing is shown as you type), or set `CACTUSDB_DBPASSWORD` in scripts.
- cactusdb never writes to your databases (see [Read-only](#read-only)). The only files it
  writes (and, on the *Exported files* page when you confirm, deletes) are `cactus.conn.db`,
  `cactus.key` and the files in the export folder.

## 8. Troubleshooting

| Problem | Fix |
|---------|-----|
| `cannot listen on 0.0.0.0:8080: …` | Another program uses that port. Pick another: `--port 8090`. |
| Other computers can't open `http://<ip address>:<port>` | Check the address is one printed at startup and that cactusdb wasn't started with `--host 127.0.0.1`. Then allow the port in the firewall: on Windows accept the *Windows Defender Firewall* prompt on first start (or `netsh advfirewall firewall add rule name=cactusdb dir=in action=allow protocol=TCP localport=8080`); on macOS allow cactusdb under *System Settings › Network › Firewall*; on Linux e.g. `sudo ufw allow 8080/tcp` or `sudo firewall-cmd --add-port=8080/tcp`. |
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
| `backup.go` | Database backup: DDL plus every row as `INSERT`s, in a maximum-compression `.zip` |
| `background.go` | `--runas background` |
| `sys_windows.go`, `sys_other.go` | Platform specifics: detaching from the terminal, console colours |
| `db.go` | Connecting, listing columns, running queries |
| `objects.go` | Listing objects (tables, views, procedures, …) and building their definitions |
| `readonly.go` | The read-only query check |
| `export.go` | Excel / JSON / XML / CSV export |
| `store.go` | Saved connections and settings (`cactus.conn.db`) |
| `settings.go` | Changing the export folder from the web UI |
| `files.go` | The *Exported files* page: listing, previews, deleting, and compressed downloads of a selection |
| `Dockerfile` | Container image (no build steps, so it builds for any CPU without emulation) |
| `deploy.sh`, `.env.example` | Deployment to Docker, k3s or Kubernetes, and its settings |
| `deploy-container.sh` | Docker-only deployment (older) |
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
