<p align="center"><img src="web/cactusdb.svg" alt="cactusDB icon" width="128" height="128"></p>

<h1 align="center">cactusdb</h1>

**One small program. No installation. Browse your databases in a web browser, or export them straight from the command line.**

cactusdb is a standalone app for **Linux, macOS and Windows** that lets you look at
relational, cloud, NoSQL and vector databases (24 types, see [Supported databases](#9-supported-databases)):
- **relational:** SQLite, MySQL, MariaDB, PostgreSQL, Firebird, Microsoft SQL Server, Oracle,
  Informix, Microsoft Access, CockroachDB
- **cloud:** Azure SQL Database (Hyperscale), Azure Database for PostgreSQL, Azure HorizonDB,
  Amazon Redshift, Amazon Athena, Google BigQuery
- **NoSQL:** MongoDB, Azure Cosmos DB, Couchbase, Apache Cassandra, Redis, Neo4j
- **vector:** PostgreSQL with pgvector, Weaviate

It shows their data and schemas: tables or collections, views, stored procedures,
functions, triggers and generators/sequences. It
also exports their data to
**Excel**, **JSON**, **XML** or **CSV**. You start it from a terminal and use it in your browser,
or export tables with a single command without opening a browser at all.

- No installer, no runtime, no database drivers or client libraries to install:
  everything is inside the one file.
- **Read-only:** it can never change, insert or delete data in your databases. The one
  database it writes to is a new one it creates itself, when it migrates a database to
  another type (☰ → Migrate database).
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

The quickest way is a setup script. It picks the right build for your computer (Intel/AMD
or ARM), downloads only that one, saves it as `cactusdb` (`cactusdb.exe` on Windows) and
checks that it runs. Run it in the folder cactusdb should live in, because cactusdb keeps
its files next to itself.

**Linux and macOS** ([`setup.sh`](setup.sh)):
```sh
mkdir -p ~/cactusdb && cd ~/cactusdb
curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh
./cactusdb --port 8080
```

**Windows**, in PowerShell ([`setup.ps1`](setup.ps1)):
```powershell
mkdir C:\cactusdb -Force; cd C:\cactusdb
irm https://raw.githubusercontent.com/albievan/cactusDB/main/setup.ps1 | iex
.\cactusdb.exe --port 8080
```

- **Another folder:** pass it to the script (`… | sh -s -- /opt/cactusdb`, or
  `.\setup.ps1 C:\cactusdb`) or set `CACTUSDB_DIR`.
- **Another branch or tag:** set `CACTUSDB_REF` (the default is `main`).
- **Updating:** run the script again. It replaces `cactusdb` in place. A cactusdb that is
  running keeps running, and the new version is used from its next start.
- **macOS:** the script also clears the download quarantine, so you can skip the `xattr`
  step below.

Or pick the file for your computer from the [`dist/`](dist) folder yourself:

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
cactusdb 1.48.1
  driver SQLite          modernc.org/sqlite v1.61.0 (bundled)
  driver MySQL, MariaDB  github.com/go-sql-driver/mysql v1.10.1 (bundled)
  driver Firebird        github.com/nakagami/firebirdsql v0.9.23 (bundled)
  driver SQL Server      github.com/microsoft/go-mssqldb v1.11.2 (bundled)
  driver PostgreSQL, …   github.com/jackc/pgx/v5 v5.11.0 (bundled)
  driver Oracle          github.com/sijms/go-ora/v2 v2.9.0 (bundled)
  …                      (one line per driver: see Drivers)
  saved connections: /home/me/cactusdb/cactus.conn.db (credentials encrypted with key file /home/me/cactusdb/cactus.key)
  export folder:     /home/me/cactusdb/export
  open http://localhost:8080
  open http://192.168.1.20:8080
  (listening on every network interface; --host 127.0.0.1 limits it to this computer)
  press Ctrl+C to stop (closing this terminal doesn't stop it; --runas background runs it without one)
```
Open one of the URLs shown in your browser. cactusdb listens on every network interface,
so other computers on your network can use it too, at `http://<this computer's IP address>:<port>`
(the addresses are listed at startup). If they can't connect, allow the port in this
computer's firewall (see [Troubleshooting](#8-troubleshooting)). Leave the terminal window open while you use
cactusdb, and press **Ctrl+C** in it to stop the app.

## 3. Using cactusdb

The left sidebar shows the **objects** of the database you're connected to. While you're
not connected it shows your **saved connections** instead, each with a **▶** button to
connect, a **New connection** button and the export folder. Disconnecting clears
everything of the database from the screen (its tables, the query, the data, the
definition), and the main area shows *Not connected to any database* with a small desert
scene: the cactusdb cactus sends signals up its two circuit-trace arms, the rings at their
ends call out, and the signal fizzles before it reaches a database that is only a mirage.
Around it smaller cactuses sway, some with a blossom on top, desert flowers nod in the
breeze and tumbleweeds of different sizes roll past. The sky follows the light or dark mode
of your system: in light mode the sun glows with slowly turning rays, in dark mode it
becomes a crescent moon that rocks gently among twinkling stars, with now and then a
shooting star. With *reduce motion* switched on in the system settings
the scene stays still.

Once connected, until you pick a table, a smaller scene shows what to do: an arrow nudges
towards the tables on the left while a table beside the cactus fills in, row by row, with
a highlight moving down it (still too with *reduce motion*).

The **Connections** menu at the bottom of the sidebar has these items:

| Menu item | What it does |
|---|---|
| **Connections** | Your saved connections, grouped by the type of database: **▶** connects, **Edit** changes one, the bin icon deletes it |
| **New connection** | Opens the connection dialog to add a connection |
| **Folder setup** | Where exports and backups are saved, and where the connection database is kept; change either |
| **Drivers** | The databases cactusdb supports, and the version of each built-in driver |
| **Settings** | How long a connection may go unused before cactusdb disconnects it (see below), Google Analytics, and deleting old events of the usage log |
| **Logs** | Opens the **Logs** tab (see below) |
| **DB container deployment** | When the server has Docker or Kubernetes: deploy SQL Server, PostgreSQL, MySQL, Oracle, MongoDB… as a container, choosing the data folder, port and password, then add it as a connection. Each deployed container has a page with its settings and its CPU, memory and storage use, and start, stop, restart, delete and purge (see [Deploying a database as a container](#db-containers)) |
| **Compare databases** | Compares the structure (DDL) of databases with a master and makes the script that removes the differences (see below) |
| **Migrate database** | Copies a database, structure and data, with its views, procedures and triggers, to a new database of another type, e.g. Firebird to MariaDB (see [Migrating a database](#migrate)) |
| **Check for update** | Whether a newer cactusdb is out; **Install** puts it in place and restarts cactusdb with the same options, or copy the command to download it for any system (see below) |

Click outside the menu or press Esc to close it. The connection details are only shown in
the connection dialog, opened by **New connection** or **Edit**, never in the sidebar.

1. **Click New connection** and choose the database type. The list is grouped into
   *Relational*, *Cloud*, *NoSQL* and *Vector*, and the form changes with the type: its
   labels and hints, and only the fields that type uses.
2. **Fill in the connection details:**

   | Engine | What to enter |
   |--------|---------------|
   | SQLite | The path to the database file, e.g. `C:\data\shop.db` or `/home/me/shop.sqlite` |
   | MySQL / MariaDB | Host, port (default 3306), username, password, database name |
   | Firebird | Host, port (default 3050), username (often `SYSDBA`), password, and the database **path on the server** or an alias, e.g. `/var/lib/firebird/data/employee.fdb`, `C:\Data\EMPLOYEE.FDB` or `employee` |
   | PostgreSQL | Host, port (default 5432), username (often `postgres`), password, database name (leave blank to use the database named after the user) |
   | SQL Server | Host, port (default 1433), SQL Server login (e.g. `sa`) and password, database name (leave blank for the login's default database). For a named instance, enter the host as `server\INSTANCE` and clear the port. |
   | Oracle | Host, port (default 1521), username, password, and the **service name** (e.g. `FREEPDB1`, `ORCL`). Options: `schema=HR` to browse another schema than the user's own, `sid=ORCL` for a SID, `ssl=true`. |
   | Informix | The host and port of the Informix **wire listener** (default 27017), username, password and database (e.g. `stores_demo`). The listener must allow SQL passthrough (`security.sql.passthrough=true`). |
   | Microsoft Access | The path to the `.mdb` or `.accdb` file. |
   | CockroachDB | Host, port (default 26257), username (often `root`), password, database (e.g. `defaultdb`). For CockroachDB Cloud add the options it gives, e.g. `sslmode=verify-full`. |
   | Azure SQL Database (Hyperscale) | `myserver.database.windows.net`, port 1433, SQL login and password, database. Encrypted; connects with `ApplicationIntent=ReadOnly`, so a read replica answers when there is one. |
   | Azure Database for PostgreSQL, Azure HorizonDB | The server's or cluster's host name, port 5432, username, password, database. TLS is required (`sslmode=require`). |
   | Amazon Redshift | The cluster endpoint, port 5439, username, password, database (e.g. `dev`). TLS is required. |
   | Amazon Athena | The AWS **region** (e.g. `eu-west-1`), access key ID and secret access key (for temporary credentials: the secret, a space and the session token), the database (blank: every database), and in Options the `workgroup` (default `primary`), an `output` S3 location if the workgroup has none, and the `catalog` (default `AwsDataCatalog`). |
   | Google BigQuery | The **project ID**, the **service account key** (paste the key file's JSON, or give the path of the file on the server), the dataset (blank: every dataset). Options: `location=EU`, `maxbytes=…` (the most a query may bill). A gcloud `application_default_credentials.json` works too. |
   | MongoDB | Host and port (default 27017) or a whole `mongodb://` / `mongodb+srv://` URI in Host, username, password, database (blank: every database). Options: `authSource=admin` (the default), `tls=true`, `replicaSet=…`. |
   | Azure Cosmos DB | The account **endpoint** (`https://myaccount.documents.azure.com:443/`), an account **key** (a read-only key is best), the database (blank: every database). The NoSQL API; for its MongoDB, Cassandra or PostgreSQL API use those types. |
   | Couchbase | The host of a query node and the query port (8093, or 18093 with `tls=true`), username, password, the bucket (blank: every bucket). |
   | Apache Cassandra | Host (several: `host1,host2`), port (default 9042), username, password, keyspace (blank: every keyspace). Options: `consistency=LOCAL_QUORUM`, `dc=dc1`, `tls=true`. Also ScyllaDB. |
   | Redis | Host, port (default 6379), username (ACL, optional), password, the database number (0, 1, …). Options: `tls=true`. Also Valkey. |
   | Neo4j | Host and port (default 7687) or a `neo4j+s://` URI, username (`neo4j`), password, database (blank: the default). |
   | PostgreSQL + pgvector | As PostgreSQL. **Test connection** says which pgvector version the database has. |
   | Weaviate | Host and port (default 8080) or `https://cluster.weaviate.cloud`, and the API key if it needs one. |

   **Options** (shown for the types that have them) takes extra settings as
   `key=value&key=value`. They are saved with the connection but not encrypted, so secrets
   go in the password field.

   The **database** can be picked instead of typed: **List databases** next to the field
   connects and lists the server's databases (MySQL, MariaDB, PostgreSQL and its family,
   SQL Server, Azure SQL, MongoDB, Cosmos DB, Couchbase buckets, Cassandra keyspaces, Neo4j,
   Redis, Athena, BigQuery datasets). **Test connection** and **Save** do it too when no
   database is filled in; press **Save** again to save without one. Types that can't
   connect without their database never save or connect without it: SQLite and Access (the
   file), Firebird (the path or alias), Oracle (the service name) and Informix.
3. *(Optional)* Click **Test connection** to check the details first.
4. Click **Connect** to connect without saving, or give it a name and a **connection
   password** (at least 6 characters, typed twice) and click **Save** to remember it,
   protected by that password (see [Connection passwords](#connection-passwords)). A saved
   connection is then one click away: **▶** next to its name.
5. Once connected, the **Objects** list has the sidebar to itself.
   Objects are split into tabs (Tables, Views, Procedures, Functions, Triggers,
   Generators/Sequences…), see [Browsing objects](#browsing-objects).
6. **Click a table or view.** cactusdb writes the query that selects every column
   (`SELECT "id", "name", … FROM "customers"`) and shows the data in the grid.
   - Click a column heading to sort; click again to reverse.
   - Use **Show** to view 100, 1,000, 10,000 or 100,000 rows on screen.
   - Empty values are shown as *NULL*.
7. *(Optional)* Edit the SQL, e.g. add `WHERE` or `ORDER BY`, and press **Run** or
   **Ctrl+Enter**. Only read queries are accepted (see [Read-only](#read-only)).
8. **Export** with the **Excel**, **JSON**, **XML** or **CSV** buttons. The export
   always contains **all rows** of the query, not just the rows on screen. The file is
   saved in the connection's folder, in a subfolder for its type:
   `<export folder>/<connection name>/json|csv|xlsx|xml/` (see
   [Where exports go](#where-exports-go)). A bar shows the full path with a **Download** link.
   **Clear** removes that bar again.

**Saved connections** appear under **Connections** in the menu, and in the sidebar while
you're not connected, grouped by the type of database (Firebird, MariaDB, SQL Server…, each
with how many there are) and sorted by name, each with the date and time it was saved. Each type opens and closes from its heading,
and several can be open at once; when you open cactusdb in the browser, they all start
closed. Hovering over a connection shows its whole name, which the list may cut off. Click **▶** (or the name) to connect; hovering shows the rest of its compact button group:
the pencil edits it and the bin deletes it. 🔒 marks a connection protected by its own password,
🔓 one saved by an older cactusdb or by the command line that has none yet (**Edit** it to
set one).

<a id="connection-passwords"></a>
**Connection passwords.** Every connection you save gets its own **connection password**,
which you choose when you save it. You need it to:
- connect to the connection
- open it with **Edit** (the details are shown only then)
- **delete** it
- preview, download or delete its [exported files](#exported-files)

cactusdb asks for it the first time you need it, and then remembers it in that browser tab
until you reload the page. The prompt says what it's for: **Connect to “…”** with a
**Connect** button, **Edit “…”** with **Unlock to edit**, or **Delete “…”?** with
**Delete**. A remembered password that no longer works (because it was changed meanwhile)
is forgotten quietly; *Wrong connection password* only appears after you typed one.

**Disconnecting when idle.** When nobody clicks, types or scrolls for **15 minutes**,
cactusdb disconnects from the database, and the sidebar says so. It never disconnects while
something is running (a query, an export, *Export all*, a backup); the time starts again
when that finishes. Change the time in **☰ → Settings** (0 = never). The setting is saved
in `cactus.conn.db` on the cactusdb server and applies to everyone using it. The server
also closes connections of pages that were closed, a little after the same time.

**Checking for an update.** When the page opens, cactusdb checks whether a newer version is
out. When one is, a dot appears on the **Connections** button, and the cactusdb log says so
at startup. **☰ → Check for update** shows the version you have and the latest one, and the
command that downloads cactusdb for each system:

- the setup scripts for **Linux and macOS** (`setup.sh`) and **Windows** (`setup.ps1`), which
  pick the right file themselves
- one command per file: Linux amd64 and arm64, macOS Apple Silicon (arm64) and Intel (amd64),
  and Windows amd64 and arm64

The system the cactusdb you're using runs on is marked **this server**. Click the small copy
button next to a command, paste it in a terminal on that computer, in cactusdb's folder, and
restart cactusdb. The check reads one small file from GitHub: [`VERSION`](VERSION). If the
cactusdb server has no internet access, your browser reads it instead.

<a id="install-update"></a>
**Installing an update from the browser.** When a newer version is out, the dialog also
has **Install *x.y.z* and restart** (click it twice: the second click confirms). It shows
what will happen first: which file is replaced and the command cactusdb restarts with.

1. The cactusdb server downloads the binary for its own system from `dist/` on GitHub (the
   bar shows the progress).
2. It checks the file against [`dist/SHA256SUMS`](dist/SHA256SUMS), and runs it with
   `--version`: it must start on this computer and be a newer cactusdb.
3. It puts it in place of the running program. The version you had is kept next to it as
   `cactusdb.old` (`cactusdb.old.exe` on Windows); to go back, stop cactusdb and rename it.
4. It stops like **Shutdown** does: running exports stop, and everyone using cactusdb is
   disconnected.
5. It starts the new version **with the options it was started with** (see below). The page
   waits for it and reloads itself.

A page that is open while cactusdb is upgraded some other way (from another browser, or by
replacing the program on the server) notices within about 20 seconds and reloads itself,
or, while a dialog is open, offers **Reload**.

The options come from the connection database. Every time the web UI starts, cactusdb saves
how it was started in the `startup_options` table of `cactus.conn.db`, one row per port:

| Column | |
|---|---|
| `port` | the web UI's port |
| `args` | the options as typed, as a JSON array, e.g. `["--port","8080","--runas","background"]` |
| `command` | the same as one command line, as shown in the dialog |
| `executable`, `working_dir` | the program and the folder it was started in |
| `run_as` | `foreground` or `background` |
| `env` | the names of the `CACTUSDB_…` environment variables that were set, never their values |
| `version`, `pid`, `started_at`, `started_by`, `hostname` | which cactusdb, process, time (UTC), user and computer |

Started with `--runas background`, the new version runs in the background too, logging to
the same file. On Linux and macOS the new version takes over the running process: the same
process id, terminal, environment (so `CACTUSDB_PASSPHRASE` still applies) and service
manager (systemd, launchd). On Windows it starts as a new process; in a console window it
keeps that window. If the new version can't be started, cactusdb puts the previous one back
and starts that. The usage log records *install update* and *upgrade installed* (with the
versions and the command).

When **the cactusdb server can't reach GitHub** but your browser can, your browser
downloads the file and sends it to the server. As the server then runs a file it didn't
download itself, this asks for the [superuser password](#superuser) (wrong guesses are slowed
down like everywhere else); without a superuser, use the commands instead.

Install isn't offered when cactusdb can't replace itself: in a **container** (update the
image with `deploy.sh`, or the next start would bring the old version back), or when the
folder of the program isn't writable for the user cactusdb runs as (the dialog says so; use
the command, as a user that may).

It isn't only a prompt: the connection's database username and password, and its
[saved queries](#saved-queries), are encrypted with a key derived from the connection
password (Argon2id, then AES-256-GCM). Without it they can't be read, not even with
`cactus.key`. After five wrong guesses for a connection, each further attempt has to wait,
from 2 seconds up to 5 minutes.

To change it, open the connection with **Edit** and fill in *New connection password*. Its
saved queries are re-encrypted with the new password. A forgotten connection password can't
be recovered. The [superuser](#superuser) can still open the connection, if one was set up
before; otherwise delete the connection and save it again.

<a id="superuser"></a>
**Superuser.** The superuser password opens every saved connection, its saved queries and
its exported files, without each connection's own password. Click **🛡** next to
*Saved connections* and sign in. The list then shows every connection's details, and you
can connect to, edit or delete any of them. **Sign out** ends it, and so does reloading the
page.
- **Setting it:** set it on the computer that runs cactusdb with
  `cactusdb --superuser set`, which asks twice and, when changing it, first asks for the
  current one. `cactusdb --superuser status` shows whether one is set. In a container, set
  `CACTUSDB_SUPERUSER_PASSWORD` once (see `.env.example`). It only sets a missing password
  and never changes an existing one.
- **Never stored:** the password itself is never stored, and it isn't part of the program.
  It encrypts the private half of a key pair, and every connection password is also
  sealed to the public half (X25519). So saving a connection never needs the superuser
  password, but opening a connection with it does.
- **Older connections:** connections saved *before* the superuser password was set can be
  opened by the superuser once they've been opened with their own password.

<a id="saved-queries"></a>
**Saved queries.** For a saved connection, write a query on the *Data* tab and click
**💾 Save query**, then give it a **name** and a **description** (the dialog fills the window,
and follows it as you resize it, so a long query has room). Only read-only queries can
be saved: cactusdb checks it with the same rules as running a query, so `INSERT`,
`UPDATE`, `DELETE`, schema changes and several statements at once are refused.
**Check syntax**, in the same dialog, asks the database whether the query is valid (its
syntax, and that its tables and columns exist) without running it: SQLite, PostgreSQL and
its relatives, MySQL, MariaDB and Firebird prepare it, and SQL Server describes it. Other
databases (Oracle among them) start it, read-only, and it is stopped as soon as its columns
are known, before any row is read, or after 15 seconds. The database's own error is shown
when it isn't valid. **Save** does the same check first: a query that isn't valid isn't
saved (one the database couldn't check in time is saved, and the notice says so).

**Closing with changes.** **Cancel** or Esc after changing anything in the dialog asks
whether to **Save** the changes, **Discard** them or **Keep editing**.

**Versions.** Every save of a query is kept as a version (v1, v2, …, the newest 100), shown
next to its name in the list. The **Version** list at the top of *Edit saved query* (or the
🕘 **Versions** button of a query) shows them with when each was saved; pick one to load it
into the form, and **Save** to make it the current version again: as a new version, so no
version is ever lost. Versions are encrypted like the query, and deleted with it. A query
saved before 1.46.0 starts with its contents then as v1.

The **Saved queries** tab, shown while you're connected, lists the connection's queries.
Filter them by name or description, then **Run** a query (it opens in the editor and
runs), **Edit** it, see its **Versions**, or **Delete** it. Saved queries are kept in `cactus.conn.db`, encrypted
with the connection's password, and are deleted with the connection.

**Exporting saved queries.** Each saved query has a **Export…** menu: Excel, CSV, JSON,
XML or **All four formats**. It exports the query's data (every row, not the SQL) to its
own folder, one per day: `<export folder>/<connection>/queries/<dd.mm.yyyy>/<query
name>-<dd.mm.yyyy.hh.mm>.<csv|xlsx|json|xml>`, e.g.
`queries/07.11.2026/cutter_subdiv_code-07.11.2026.14.05.csv` (characters that can't be in a
file name become `_`; a second export in the same minute gets `_2`). The *Exported files*
page lists them as type **Saved queries**. **Export all queries…** above the list does the same for every query shown
(a filter narrows them down), one file per query and format. The bar above the list shows
the progress, then a download link for each file and any query that failed.

**Logs.** The **Logs** tab (also **☰ → Logs**) shows cactusdb's log: connections,
tables opened, queries, exports, and every request with its result, its duration and the
session it belongs to (`session=1a2b3c4d`).
- **Log** or **Crash log**, and **Day**: a new log file starts every day, and the days kept
  (30) can be picked. The crash log has a file per day too, and only says anything when
  cactusdb crashed.
- **Filter** the lines, **Problems only** (failures, errors, warnings), the last 500 to
  20,000 lines, **Newest first**.
- **Live** shows new lines as they come in, at the top, highlighted (today's log, every
  2 seconds, only what's new).
- **Copy** copies the lines shown; **Download all (.zip)** downloads every log and crash
  log file kept, compressed as much as zip can.

Without a log file (in the foreground without `--logfile`) the tab shows the lines since
cactusdb started, kept in memory.

<a id="compare-databases"></a>
**Compare databases.** **☰ → Compare databases** (or the **Compare** tab) compares the
*structure* of databases, never their data: tables (column by column, with their keys,
indexes and foreign keys), views, procedures, functions, triggers, sequences/generators,
domains, events, packages…
1. Choose the **Master**: a saved connection, or the one you're connected to.
2. Tick the databases to **Compare with**, any number of them. A protected connection asks
   for its connection password (or the superuser password).
3. Click **Compare**. cactusdb reads every object of every database at the same time; the bar
   shows how far it is.

The result has one column per database and one row per object, coloured: **missing** (red:
only the master has it), **extra** (blue: the master doesn't have it), **≠ differs** (amber)
and **✓ same**. *Differences only* (the default) hides what is the same everywhere; filter by
type or name. Click an object (or one of its cells) to see, on the right, what differs (e.g.
`column email missing`, `column name differs: master varchar(100) / here varchar(50)`) and
the master's definition beside that database's, line by line, the differing lines
highlighted.

**Script** shows, per database, the SQL that makes its structure match the master's;
**Copy** or **.sql** to take it. It drops what the master doesn't have (untick *Drop what
the master doesn't have* to keep those), alters tables (adds, drops and changes columns,
keys and indexes; MySQL/MariaDB put a new column where the master has it), creates what is
missing, and replaces views, procedures, functions and triggers that differ (`CREATE OR
ALTER` on Firebird and SQL Server, `CREATE OR REPLACE` on PostgreSQL, drop and create
elsewhere). Dropping a table or column is marked `WARNING`, as its data goes with it. What
can't be done safely is left as a comment to do by hand: a changed sequence (it would lose
its current value), a changed domain, a column change SQLite can't make, a default that SQL
Server keeps as a constraint.

cactusdb never runs the script, it stays read-only: read it, take a backup, then run it with
your own tool (mysql, psql, sqlcmd, isql…). Whitespace and things that aren't structure are
not differences: MySQL's next `AUTO_INCREMENT` value, the `DEFINER` user, the database's own
name in a view, a sequence's current value, and the order of columns. Only databases of the
same kind are compared object by object (MySQL and MariaDB count as one); with another kind
only the names are compared, and no script is made.

<a id="migrate"></a>
**Migrating a database.** **☰ → Migrate database** (or the **Migrate** tab) copies a
database to a **new** database of another type, e.g. Firebird to MariaDB, SQL Server to
PostgreSQL or MySQL to SQLite:
1. **From:** the source, a saved connection or the one you're connected to (any SQL
   database: SQLite, MySQL, MariaDB, PostgreSQL and its relatives, SQL Server, Azure SQL,
   Firebird, Oracle, Access, Informix).
2. **To:** the type of the new database (MariaDB, MySQL, PostgreSQL, SQL Server, SQLite or
   Firebird), the server (a saved connection to it, e.g. one you deployed with ☰ → DB
   container deployment) and the **new database's name**: for SQLite a full path on the
   cactusdb server, for Firebird a path on the Firebird server.
3. **What to migrate:** the tables always, and ticked: their **rows**, the **sequences**,
   the **views**, the **procedures & functions** and the **triggers**. **Names in the new
   database:** as they are, lower case or UPPER CASE (Firebird's upper-case names become
   lower case elsewhere, by default).
4. **How:** *Create the new database and copy into it* (needs the superuser password), or
   *Only write the script (.sql)*, which changes no database: run it yourself with mysql,
   psql, sqlcmd, sqlite3 or isql. The 👁 button in the password field shows what you typed.
   The superuser password also opens the source and target connections. A connection saved
   before the superuser password was set can't be opened by it yet: cactusdb asks for that
   connection's own password once, and from then on the superuser password opens it too.

cactusdb creates the database (refusing one that already exists), then:
- **Sequences** (generators), going on from their current values (MySQL and SQLite have
  none: they are listed).
- **Tables:** each column's type in the new database's terms (e.g. Firebird's `BLOB SUB_TYPE
  TEXT` becomes `LONGTEXT` on MariaDB, `TEXT` on PostgreSQL, `NVARCHAR(MAX)` on SQL Server),
  nullability, defaults, auto-numbered (identity) columns and the primary key.
- **Rows**, in batches of multi-row INSERTs committed every few thousand rows; afterwards
  each table's rows are counted again. Dates and times are copied as they are, without a
  time zone moving them.
- **Unique constraints, indexes, foreign keys and checks**, after the rows; then each
  auto-numbered column goes on from the largest value copied.
- **Views**, their query rewritten: quoting, names, and what differs (Firebird's `FIRST`/`SKIP`
  and SQL Server's `TOP` become `LIMIT`, or the reverse; `IIF`, `LIST`, `GEN_ID`, `NEXT VALUE
  FOR`, `CONCAT`, `ISNULL`, `SUBSTRING(… FROM … FOR …)`, PostgreSQL's `::` casts and `= ANY
  (ARRAY[…])`, `WHERE flag` on SQL Server). A view that reads another is made after it.
- **Procedures, functions and triggers:** Firebird's PSQL is translated to MariaDB/MySQL
  and to PostgreSQL's PL/pgSQL: variables, `IF`, `WHILE`, `FOR SELECT … INTO … DO` (a cursor
  loop on MariaDB), `SUSPEND` (a selectable procedure is a function `RETURNS TABLE` on
  PostgreSQL; on MariaDB `CALL` returns its rows), `EXIT`, `EXCEPTION` (`SIGNAL` / `RAISE`),
  `EXECUTE PROCEDURE … RETURNING_VALUES`, `WHEN ANY DO` and the `INSERTING`/`UPDATING`/
  `DELETING` of a trigger (on MariaDB a trigger per event). Between databases of the same
  kind (e.g. MySQL to MariaDB, PostgreSQL to Azure Database for PostgreSQL) they are copied
  as they are. Other code (T-SQL, PL/pgSQL, MySQL's) is listed with its source, to rewrite
  by hand. On MySQL and SQLite, which have no sequences, Firebird's usual trigger that
  numbers a column from a generator becomes an auto-numbered column.

The page shows the progress, then every object with its result: **✓ done**, **check** (made,
but read its notes), **by hand** (not made: its source is shown), **✘ failed** (with the
database's error) or **skipped**. Click one to see its source beside what was made of it.
**Script** downloads the SQL of the migration (what was run, and at the end, as comments,
what is left to do by hand); **Add as connection** opens the connection dialog filled in
for the new database. If it stops (or you click **Stop**), the new database is left as it
is: delete it before migrating again.

**Adding and editing connections.** Click **New connection** (in the menu or the sidebar)
to open the connection dialog: a name, the database type and the connection details. Click
**Test connection** to check them before saving. cactusdb connects, reports the server
version and the number of tables (or the exact error), and disconnects again. Nothing is
saved until you click **Save**. The eye button in the password field, and in the
connection password and its confirmation, shows what you typed there; each is masked
again when the form closes. When you edit a saved connection the eye only ever shows what
you type: the saved password never reaches the browser (leave the field blank to keep
it). **Edit** on a saved connection opens the same form, where
you can test it, change it or rename it. Saved passwords are never sent to the browser:
the password box shows *saved – leave blank to keep*. Leave it empty to keep the stored
password (testing uses it too), or type a new one to replace it.

**Export all tables.** Under the *Objects* heading, the four file icons (**XLS**, **CSV**,
**JSON**, **XML**) export every table of the database, one file per table, four tables at a
time. **ALL**, the fifth, exports every table in all four formats, one format after the
other. Each table gets a progress bar with its row count, and a download link when it's
done. The list fills the whole data area (in several columns on wide screens), so you see
as many tables as possible. Opening a table brings the grid back, and **✕** closes the
list while the export carries on. When the export is finished, **Clear** removes the
export status. Disconnecting stops an export that is still running.

<a id="where-exports-go"></a>
**Where exports go.** Every connection gets its own folder in the export folder, named after
the connection. Inside it, each kind of file has a subfolder. For a connection called
`optimo-prod`:
```
export/optimo-prod/json/     export/optimo-prod/csv/     export/optimo-prod/xlsx/
export/optimo-prod/xml/      export/optimo-prod/sql/     (DDL)
export/optimo-prod/backup/   (backups)
```
- **Saved or named connections** use that name. Characters a folder name can't hold become
  `_`, so `Optima (EU)` becomes `Optima _EU`.
- **Unnamed connections** use `<host>-<database>`, or the database file's name for SQLite.
- **The command line** uses the name the connection is saved under, `cli-<host>`.
- **Older exports** made before version 1.13 stay where they are, directly in
  `export/<type>/`.

<a id="exported-files"></a>
**Exported files.** The **Exported files** tab lists every file in the export folder: data
exports, DDL scripts and backups, with the connection each belongs to, when it was created
and its size (uncompressed).
- **Sort:** click a column heading to sort by connection, file name, type, date or size.
  Click it again to reverse the order.
- **Filter by connection:** the **Connection** list shows every connection with its number
  of files. Older exports appear as *(older exports)*. The type counts follow the chosen
  connection, and a download of one connection's files is named after it.
- **Filter by date:** the **Exported** list shows every day files were exported on, newest
  first, with the number of files of that day (for the chosen connection). Pick a day to show
  only the files exported on it; **All dates** shows them all again. Days are in your
  browser's time zone.
- **Filter by type:** the **Type** buttons show how many files of each type there are
  (Excel, CSV, JSON, XML, DDL, Backup). Click one to show only that type, or click several
  to combine them, for example CSV and JSON. **All** shows every type again.
- **Search by name:** the **Name** box shows files whose name contains what you type,
  ignoring case. With several words, for example `order 2026`, a name must contain all of
  them, in any order. The matching text is highlighted. **Esc** or **✕** clears the box.
- Connection, date, type and name filters combine. The footer shows how many files are shown and
  their size. If nothing matches, **Clear the filters** resets them. The filters and the
  sort order are remembered in your browser.
- **Select:** tick the files you want, or click their rows. The heading checkbox selects every
  file shown.
- **Preview** (the 👁 button after a file's checkbox) opens the file in a window:
  - CSV and Excel files (the first sheet) as a table
  - JSON and XML files in their own format, as they are written in the file, coloured: keys,
    strings, numbers, tags and attributes. A JSON file written on one line is laid out on
    lines of its own, indented, with every value kept exactly as written
  - DDL scripts and other text as text
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

**Export folder.** **Folder setup** in the menu (or **Change** in the sidebar while you're
not connected) shows where exports are saved, and lets you type another folder: a full path on the computer cactusdb runs on (a
leading `~` means your home folder). cactusdb creates it if needed, checks it can write
there, and saves the choice in `cactus.conn.db`, so it's used again after a restart, by the
web UI and by command-line exports. **Use default** goes back to the `export` folder next to
the program. Files exported before the change can still be downloaded until cactusdb stops.
`--exportto` on the command line always wins over the saved folder.

**Connection database.** The same dialog shows where `cactus.conn.db` is (saved
connections, saved queries, settings and the usage log) and can **move** it. **Move it to**
starts with its current place: change it to the new file, or a folder to put
`cactus.conn.db` in, and click **Move database**, then **Yes, move it**.
- A new path without an extension (e.g. `/srv/cactusdb-data`) or ending in `/` is a folder;
  one with an extension (e.g. `/srv/data/my.db`) is the file itself.
- The folder is created if it doesn't exist. An existing file is never overwritten.
- cactusdb copies the database while holding its only connection, so nothing is written
  in between, checks the copy, uses it straight away and deletes the old file.
- The key file `cactus.key` goes along when it was next to the database (not when the key
  comes from `CACTUSDB_PASSPHRASE` or a `--key-file` elsewhere).
- People using cactusdb keep working. A request that arrives at the very moment of the
  move may fail; repeat it.
- The new place is remembered in `cactus.conn.db.location` next to the program and used at
  the next start, by the web UI and by command-line exports. **Default place** moves it
  back next to the program and removes that file.
- `--conn-db` always wins: if cactusdb was started with it, the dialog says so, and you need
  to change the option to the new place for the next start (for a container: the
  volume the file is on, see `deploy.sh`).

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

The version of the cactusdb you're using is shown under its name in the top bar (for
example *v1.31.0*), as reported by the running program. Next to the *read-only* label, a
second label shows the type of the database you're connected to (for example *PostgreSQL*
or *Firebird*); it goes away when you disconnect.

<a id="who-is-using"></a>
**Who is using cactusdb.** The people button in the top bar (for example 👥 *3*) shows how
many pages have cactusdb open right now: every browser tab counts, from this computer or
another. It turns green when someone else is using it too. Hover over it for the client
IPs, or click it for the list: each page's **client IP**, its browser, when it was opened and
when it was last heard from, with *this page* marked. A page checks in every 5 seconds; one
that is closed or reloaded leaves the list straight away, and one that goes quiet (the
computer asleep, the network gone) after about two and a half minutes. With
[`--remotelogin`](#remote-login) the list shows each page's user. cactusdb's log notes each page
opening and closing, with its IP and how many are using it.

**Notices of other people's changes.** When someone, on another page, **adds, changes,
renames or deletes a saved connection**, **adds, changes or deletes a saved query**,
**deploys a database container** (when it starts, and when it is ready or fails) or
**updates cactusdb to the latest release**, every other page shows a notice within a few
seconds: who did it (their client IP), when, and what. The page they did it on doesn't get
one. What changed is loaded again: the saved connections, the saved queries (on a page
connected to that same connection) and the list of containers (when it is open). A saved
query's name isn't in the notice, as it is kept encrypted with the connection password.
After an update every page reloads by itself once the new version is running. The last
notices are listed under *Changes by others* in the people button's list.

<a id="messages"></a>
**Direct messages.** To write to someone using cactusdb, open the people button's list and
click the 💬 button on their row, or click 💬 **Messages** in the top bar (shown when someone
else has cactusdb open) and pick them under **To**. Type the message and press **Enter**
(**Shift+Enter** starts a new line). It pops up on their screen within a few seconds,
whatever they are doing: they don't need to be connected to a database, or to the same one as
you, only to have cactusdb open. The message shows on top of an open window (such as the Save
query window) too, and stays until it is read: **Reply** opens the conversation so they can
answer, **✕** closes it. The 💬 button shows how many are unread, and while the cactusdb tab is
in the background its title says so, for example *(1) New message · cactusdb*; the message is
there when they come back to the tab. Messages are **private**: only the page you picked, and yours, are given
a message. Other pages, from the same computer or another, never see it. Pages are addressed by
a handle that isn't the secret id a page checks in with, so no page can read another's
messages. Messages are kept in memory only, are never logged or written to disk, and are
forgotten once both pages are closed. Reloading a page starts a new conversation. Under
**Your name** you can give a name the others see next to your IP (kept in this browser).

<a id="remote-login"></a>
### Signing in first: `--remotelogin`

Start cactusdb with **`--remotelogin`** and nothing of it can be seen or used before signing
in: opening it shows the sign-in page, and every request without a sign-in is refused. The
sign-in page looks like cactusInv's: on the left, in green, what cactusdb does and its
source on GitHub; on the right the form (with *Show what I type*), and the version.

```sh
cactusdb --port 8087 --remotelogin --runas background
```

- **The first user is `admin`, password `admin`.** Signing in with it asks for a new
  password straight away (at least 8 characters, not `admin`). Forgot it? On the server,
  `cactusdb --resetadmin` sets it back to `admin`, to be changed at the next sign-in.
- **Every other user is an e-mail address.** After the right password, cactusdb e-mails an
  **8-digit security code**; the next screen asks for it (typing or pasting it, it signs in
  as soon as the 8 digits are there). A code works for 10 minutes; **Send a new code**
  e-mails another one (at most every 30 seconds) and the earlier one stops working. After 5
  wrong codes the sign-in starts again. `admin` has no address, so it gets no code.
- **Adding users:** any signed-in user can add one under ☰ → **Users**. The user name must
  be a valid e-mail address. cactusdb e-mails them a link to set their password, which
  **works for one hour** and once; **Send link** e-mails a new one (for a user who already
  has a password, a password reset link).
- **Forgot your password?** on the sign-in page e-mails a link to set a new password (one
  hour). The answer is the same whether or not the address has an account. Setting the new
  password signs that user out everywhere.
- **Request access:** on the sign-in page anyone can request access with their e-mail
  address, name, a password and why they need it. They can't sign in until a signed-in user
  approves the request under ☰ → **Users** (the open pages are told, and ☰ → Users shows
  how many are waiting). Approving e-mails them that they can sign in now; **Reject** removes
  the request.
- ☰ → **Users** also lists everyone with their status (*Active*, *Invited: no password yet*,
  *Requests access*), who added them and their last sign-in, deletes users (asks first; not
  yourself, nor the last user who can sign in), and changes your own password. ☰ →
  **Sign out** signs this browser out. A sign-in ends after 12 hours unused.
- The e-mails go through the mail server of ☰ → [**E-mail server**](#email-server), which
  must be set up first (sign in as `admin` to do it).

Wrong passwords are slowed down: after 5 for a user name or from one computer, each further
try waits 2 seconds, doubling up to 5 minutes. Passwords are stored as Argon2id hashes;
codes, links and sign-ins as SHA-256 hashes of random tokens, so the connection file never
holds one that works. The browser holds its sign-in in an HttpOnly cookie. The log notes
every sign-in, failed sign-in and user change with the client IP, never a password or code.

<a id="email-server"></a>
### E-mail server

☰ → **E-mail server** sets up the mail server cactusdb sends e-mail through: the sign-in
codes, invitations, password reset links and approvals of `--remotelogin`. It works like
cactusInv's *Settings → E-mail server*:

- **Gmail**, **Outlook / Microsoft 365** or **Custom** fills in the host, port and security,
  with a hint for that provider. **Setup guide** explains step by step what to do at the
  provider (for Gmail: an App password), and what to try if the test fails.
- **SMTP host**, **Port**, **Security** (STARTTLS, SSL/TLS or None), **User name** and
  **Password**. The password is stored encrypted with cactusdb's key and never shown again;
  leave it empty to keep it. *None* sends nothing encrypted, so it is only allowed without a
  user name (a test mail server on your own network).
- **Send as**, e.g. `IT <it@example.com>` (the user name when empty). cactusdb warns when
  Gmail or Microsoft 365 is known not to send as that address.
- **Use this server for all e-mail**: nothing is sent until it is ticked.
- **Address in e-mailed links**: how the people cactusdb e-mails reach it, e.g.
  `http://10.66.1.136:8087`; the links start with it. It is filled in with the address you
  opened cactusdb at. cactusdb never takes it from a request, which anyone can fake to make a
  reset link point elsewhere.
- **Send test e-mail** (after saving) sends a test message through the saved settings and
  shows the mail server's answer if it is refused.

<a id="dashboard"></a>
### The database's dashboard

When the connected database has statistics cactusdb can read, a **Dashboard** button
appears in the top bar. It opens the **Dashboard** tab in the main area (not a dialog): what
the database says about itself, measured again every 10 seconds while the tab is shown (untick
**Live** to stop; **Refresh** measures at once). Only reads: the statistics views of the
database.

| Database | What it shows |
|---|---|
| PostgreSQL (and pgvector, Azure, CockroachDB, Redshift: what they support) | Version, uptime, size, connections of the maximum, cache hit rate, commits and rollbacks, rows read and changed, deadlocks, temporary files; connections by state, running queries, the largest tables (dead rows, scans, last autovacuum), the least used indexes, locks, the main settings |
| MySQL, MariaDB | Version, uptime, connections of the maximum, running threads, statements per second, slow queries, InnoDB buffer pool hit rate, SELECT/INSERT/UPDATE/DELETE counts, traffic; the database's tables, rows, data and index size; running statements, connections by user, the largest tables, the main settings |
| SQL Server, Azure SQL | Version, edition, recovery model, size, uptime, CPUs, memory, sessions, running and blocked requests; running requests, sessions by login, the largest tables, top waits, the files |
| Firebird | Version, dialect, ODS, size, page size, forced writes, transactions since the oldest; page reads, writes and fetches, cache hit rate, memory; attachments, running statements, transactions |
| SQLite | Version, size, free space, page size, the number of tables, indexes, views and triggers; the largest tables and indexes; encoding, journal mode, auto vacuum |
| Oracle | Instance, host, version, uptime; sessions; the schema's tables, indexes, views and size; the largest segments; tablespace use |

A part the connection may not read (SQL Server's activity needs *VIEW SERVER STATE*, Oracle's
`v$` views need a grant…) is listed at the bottom as not available, with the reason; the rest
still shows. Sizes, durations and percentages are shown as such, with a bar for percentages.

**Potential issues are shown in red**, with a red glow like a focused field, and listed at
the top (click one to go to it): connections or space more than 85 % used, a cache or
buffer pool hit rate under 90 %, deadlocks, conflicts, slow queries, blocked requests or
waiting locks, more than 100 failed connections, a statement running for more than a
minute, a table with many dead rows (VACUUM it), a large index that is never used, forced
writes off or a long-open transaction (Firebird), and a SQLite file that is mostly free
space. Without any, the top says *No potential issues found*.

<a id="browsing-objects"></a>
### Browsing objects: views, stored procedures, generators…

The **Objects** section has one tab per object type that exists in the database, each with
a count, e.g. `Tables 42` `Views 7` `Procedures 12` `Generators 9`. Click a tab to list
those objects.

- The **filter** box searches every tab at once. Each tab shows its number of matches,
  and the list jumps to a tab that has matches.
- **Rows and size of every table** are shown as soon as you connect: a **Rows** and an
  **MB** column (data and indexes) next to each table, and the totals under the tabs, e.g.
  `179 tables · ≈12,345,678 rows · 4,321.5 MB`. On connecting they come from the
  database's own statistics, which is instant but approximate, marked `≈` (MySQL and
  MariaDB's InnoDB estimate can be off by a few percent or more). Tables the database has
  no estimate for (SQLite, Firebird) are counted, for up to 10 seconds.
- **⟳ # MB**, next to the Tables tab, counts the rows of every table exactly and measures
  the sizes again: four tables at a time, for up to 30 seconds; a table that takes longer
  keeps the estimate, marked `≈`. Sizes come from the database's catalog.
- **Estimated sizes:** when the database doesn't report a table's size (Firebird keeps
  none per table), it is estimated as **rows × row size**, marked `≈`. The row size is the
  sum of the declared sizes of the columns, e.g. `name varchar(20)`, `surname varchar(20)`,
  `age int`, `dateofbirth datetime`, `id` (auto-increment int) and `photo blob` make
  20 + 20 + 4 + 8 + 4 + 1,024 = 1,080 bytes a row:

  | Column type | Bytes |
  |---|---|
  | `char(n)`, `varchar(n)`, `binary(n)` | n (`nchar`/`nvarchar`: 2 × n) |
  | `tinyint`, `boolean`, `bit` | 1 |
  | `smallint` | 2 |
  | `int`, `integer`, `real`, `date` | 4 |
  | `bigint`, `double`, `datetime`, `timestamp`, `time` | 8 |
  | `decimal(p,s)`, `numeric(p,s)` | p / 2 + 1 |
  | `uuid`, `uniqueidentifier` | 16 |
  | `text`, `clob`, `json`, `varchar(max)`, `blob sub_type text` | 256 (nominal) |
  | `blob`, `bytea`, `image`, `varbinary(max)` | 1,024 (nominal) |
  | anything else (e.g. a Firebird domain) | 8 |

  Computed columns aren't stored and count 0. Text and blob columns have no declared
  size, so the estimate can be well off for tables that hold large documents or images.
  Hover over a table to see whether its size is measured or estimated.
- Long names wrap at `_`, so the whole name is always visible. The open table is
  highlighted in the list, and its name is shown next to the tabs.
- **Sorting:** click a column heading above the list (**Name**, **Rows**, **MB**) to sort by
  it; click it again to reverse the order. ▲ is ascending, ▼ descending. Rows and MB start
  with the biggest. The order is remembered in this browser.
- **⟳**, and **Refresh** next to **Disconnect**, refresh the connection and everything
  of the database: cactusdb closes its unused connections (a firewall or the database server
  may have cut them), checks a fresh one, and lists every table, view, procedure, function,
  trigger, sequence… again, with their rows and sizes. The open table or definition is
  reloaded; if it no longer exists, the selection is cleared.
- The Objects and Saved connections sections fold open and closed from their heading, and
  cactusdb remembers your choice.
- The sidebar is as wide as you like: drag its right edge (220 pixels up to 60% of the
  window). The browser remembers the width; double-click the edge for the usual width again.
  With the edge focused, the arrow keys make it narrower or wider and Home resets it.

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

<a id="other-languages"></a>
### Databases that don't speak SQL

Each database is browsed in its own query language. Clicking an object writes the query
that shows its data, as for SQL databases, and the grid, **Show**, exports, **Export all
tables**, **DDL**, saved queries and table sizes work the same:

| Database | Objects | Query |
|----------|---------|-------|
| MongoDB | Collections and views (`database.collection` when no database is set) | `db.orders.find({ status: "open" }, { _id: 0 }).sort({ total: -1 }).limit(10)`, `.aggregate([…])`, `.countDocuments({…})`, `.distinct("field")`, `.getIndexes()`. Values as in the MongoDB shell: `ObjectId("…")`, `ISODate("…")`, `/regex/i`… |
| Azure Cosmos DB | Containers, their stored procedures, functions and triggers | Cosmos DB SQL. The container is named in a first line: `-- container: shop.orders`, then `SELECT * FROM c WHERE c.status = 'open'` |
| Couchbase | Collections (`bucket.scope.collection`) and indexes | SQL++ (N1QL): `SELECT META(c).id AS _id, c.* FROM `travel`.`inventory`.`airline` AS c` |
| Apache Cassandra | Tables, materialized views, user types, functions, indexes (`keyspace.name`) | CQL `SELECT` |
| Redis | Key groups by prefix (`user:*`, and `*` for every key) | `SCAN MATCH user:*` lists every matching key with its type, time to live and value; other read commands as typed: `GET`, `HGETALL`, `LRANGE`, `ZRANGE`, `XRANGE`, `INFO`… |
| Neo4j | Node labels `(:Person)`, relationship types `[:KNOWS]`, indexes, constraints | Cypher: `MATCH (n:Person) RETURN n LIMIT 100`. Nodes and relationships are shown with one column per property. |
| Weaviate | Collections | GraphQL `{ Get { Article { title _additional { id } } } }`; a `Get` without arguments pages through every object, so exports have them all. |
| Microsoft Access | Tables, saved queries (their SQL), linked tables | `SELECT [TOP n] * \| columns FROM table [WHERE column = value AND …] [ORDER BY column [DESC]]`, with `=`, `<>`, `<`, `>`, `LIKE 'A*'`, `IS [NOT] NULL` and `#2024-01-31#` dates |
| Google BigQuery, Amazon Athena, Informix, Oracle… | Tables, views and more | Their SQL |

Documents are shown with one column per top-level field, taken from the first 1,000
documents in the order the fields appear; nested documents and arrays are shown as JSON.
The **Definition** of a collection, key group, label or container lists its fields and
their types from a sample, with its indexes and options. **Backup** and **Compare** are
for SQL databases with their own DDL; for the others, export the data and the DDL.

<a id="db-containers"></a>
### Deploying a database as a container

**☰ → DB container deployment** starts a new database server as a container on the
computer cactusdb runs on, when it has **Docker** (Docker CE or Docker Desktop) or
**Kubernetes** (k8s, k3s, with `kubectl`). The dialog shows what it found, the database
containers cactusdb deployed, and a form.

**How to install Docker or Kubernetes on this server** (in the dialog; open when neither
was found) shows the steps for the system cactusdb runs on: its OS, its processor (Intel/AMD
or ARM 64-bit) and, on Linux, its distribution. One tab each for **Docker CE**, **Docker
Desktop**, **k3s** and **Kubernetes (k8s)**, with the commands to copy:

- Linux: Docker CE with Docker's install script (and the `docker` group for cactusdb's
  user), Docker Desktop's `.deb`/`.rpm` (Intel/AMD only), k3s with its installer and a
  kubeconfig for cactusdb's user, and kubectl plus kubeadm for a full cluster.
- macOS: Docker Desktop (Homebrew or the download for Apple silicon or Intel), Docker CE in
  Colima, k3s with k3d or Colima, and Docker Desktop's Kubernetes.
- Windows: Docker Desktop with WSL 2 (`winget` or the download), Docker CE and k3s inside
  WSL 2, and Docker Desktop's Kubernetes with `kubectl` from `winget`.

Then **Refresh** looks for `docker`, `kubectl` and `k3s` again. The form:

| Field | |
|-------|---|
| Database | SQL Server 2022 (Developer), PostgreSQL 17, PostgreSQL 17 + pgvector, MySQL 8.4, MariaDB 11.4, Oracle Database 23ai Free, Firebird 5, MongoDB 8.0, Redis 7.4, Neo4j 5, Apache Cassandra 5.0, CockroachDB 24.3, Weaviate 1.28 |
| Deploy to | Docker, or the Kubernetes cluster of `kubectl`'s current context (shown) |
| Container name, port | The port on this computer (Docker), or of the Kubernetes service: a *LoadBalancer*, or a *NodePort* (30000–32767) |
| Image | Filled in; change the tag for another version |
| Folder for the database files | Where the database keeps its files: a folder on the cactusdb server (Docker) or on the node (Kubernetes, `hostPath`), created if missing. A folder that already holds database files is used as it is, so a removed container can be deployed again with its data. Empty: a Docker volume `<name>-data`, or a Kubernetes volume claim of the size you give |
| Username, database | Where the image lets you choose them (PostgreSQL, MongoDB, MySQL/MariaDB database, Firebird file…); otherwise shown (`sa`, `system`/`FREEPDB1`, `SYSDBA`, `neo4j`…) |
| Password | The administrator password, with the database's rules (SQL Server: 8+ characters, three of upper, lower, digits, symbols). Cassandra and CockroachDB (insecure mode) have none. For Weaviate it is the API key |
| Reachable from other computers | Docker only: otherwise the port is published on 127.0.0.1 only |
| Restart | Docker only: when Docker starts the container again by itself. **Unless stopped** (the default): after a crash and when Docker starts, unless you stopped it; **Always**: even if you stopped it; **On failure**: only when it stops with an error; **No**: never. Kubernetes always restarts a deployment's pods |
| Superuser password | Needed to deploy (see [Superuser](#superuser)) |

**Deploy** downloads the image (unless it's already there), starts the container and
connects until the database answers, showing each step. Then **Add as connection…** opens
the New connection dialog filled in (type, host, port, user, password, database): give it
a connection password and save it. The deployed containers are in a table (name, database,
where it runs, port, state, status): click a column heading to sort by it (again for the
other way), and type in **Search by container name** to show only the containers whose
name contains it. Each row has its compact group of buttons, always shown (hover one for
what it does): **View**, **Add connection**, **Logs** (the last 300 lines, newest first)
and **Remove**. Remove asks first (click it again) and removes the
container (Docker) or the deployment, service and secret (Kubernetes); the data folder,
volume or volume claim is kept.

<a id="container-page"></a>
**A container's page.** **View** (or its name) opens the page of a deployed container in
the main area, under its own **Container · name** tab beside Data, Logs and Compare, not as
a dialog. Other tabs can be used meanwhile (it stops measuring while hidden); **All
containers** opens the list again and **Close** closes the page and its tab. The page uses
the whole width of the main area; on a wide screen the settings and the environment are side
by side, and the log is as tall as the window allows. It shows:

- **Usage**, measured again every 5 seconds: **CPU** (percent of one core, as `docker
  stats` counts it, with the cores it can use), **memory** (used, of its limit or of all
  Docker's memory) and **storage**: the database files (measured inside the container, at
  most once a minute), what the container wrote besides them, the image, the volume
  claim's size (Kubernetes) and the disk and network I/O. On Kubernetes CPU and memory come
  from `kubectl top`, which needs the cluster's metrics-server.
- **Settings**: the image and its ID, state and health, when it was created and started,
  the restart policy and restarts, the ports, the data folder or volume, the command, the
  limits and labels; on Kubernetes the namespace, replicas, service, volume claim and pods
  (node, IP, restarts).
- **Environment**: its variables; passwords, keys and tokens are hidden. The eye button
  next to a hidden value shows it after you enter the superuser password (asked every
  time); click it again to hide the value. On Kubernetes it shows the value from the
  deployment's Secret.
- One compact row of buttons: **Start**, **Stop**, **Restart**, **Delete** and
  **Purge**. On Kubernetes, stop scales the deployment to 0 and start back to 1. Each
  asks first and needs the superuser password. **Delete** is Remove: the data stays.
  **Purge** removes everything about the container and can't be undone: the container, its
  data folder and every file in it (or its volume or volume claim) and its image, unless
  another container uses that image. It lists what it will remove and wants the container's
  name typed. It refuses a data folder that is a disk's root or a folder just below it, the
  home folder, or a folder holding cactusdb. On Kubernetes a short-lived pod on the node
  deletes a `hostPath` folder (the pod must be running, so cactusdb knows the node); the
  nodes remove the unused image themselves.
- **Logs**, **Add connection** and **⟳ Refresh**. **Logs** (after the superuser password)
  shows the container's last 1,000 log lines **newest first**: a message over several lines
  keeps its lines in order. It is **live**: new lines appear at the top every 3 seconds and
  the view stays at the newest lines (if you scroll down to read, what you're reading stays
  in place). Untick **Live** to stop it. Lines about an error, a failure or a warning
  (`Error: 18456`, `Login failed`, `ERROR:`, `FATAL:`, `[ERROR]`, `WARNING`, `level=error`…)
  are **red**; **All lines** / **Errors only** switches between the whole log and just those
  lines, with how many there are. **Export** saves the whole log to the export folder
  (`<export folder>/<container>/log/<container>_<date>-<time>.log`, listed on the Exported
  files page as type *Log*), and **Download** downloads the whole log as a `.log` file; both
  keep the log's own order, oldest first.

- Passwords never appear on a command line: docker gets them from its environment, and on
  Kubernetes they are in a Secret (`<name>-credentials`) sent to `kubectl apply` on stdin.
- cactusdb finds `docker` and `kubectl` (or `k3s kubectl`) on the PATH and in the usual
  install folders. On Linux the user cactusdb runs as must be allowed to use Docker (the
  `docker` group) or have a kubeconfig.
- SQL Server and Oracle run as their own user: a new data folder is given to that user
  (or made writable), and on Kubernetes an init container sets its owner.
- The images are the vendors' (Microsoft's SQL Server Developer edition, which is for
  development and testing; Oracle Free from `gvenzl/oracle-free`). SQL Server, Oracle and
  Cassandra need about 2 GB of memory.
- cactusdb itself stays read-only: it creates the server, never writes into its databases.

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
connection's `sql` folder (`<export folder>/<connection>/sql/`).

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
up the whole connected database to `<database>_backup_<date>-<time>.zip` in the
connection's `backup` folder (`<export folder>/<connection>/backup/`). The web UI shows a **Download** link when it's done.

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
   - Oracle queries run in a `SET TRANSACTION READ ONLY` transaction that is rolled back.
   - CockroachDB, Azure Database for PostgreSQL, HorizonDB and pgvector sessions are
     read-only like PostgreSQL's; Redshift queries run in a read-only transaction.
   - BigQuery runs every query as a dry run first and refuses anything but a `SELECT`.
   - Couchbase queries are sent with `readonly=true`; Neo4j queries run in READ
     transactions; the Cosmos DB, Weaviate and Access readers have no way to write at all.
   - MongoDB takes only `find`, `aggregate` (without `$out` or `$merge`), `countDocuments`,
     `distinct` and `getIndexes`; Redis only the commands that read; Cassandra and Athena
     only `SELECT`.

The one exception is [migrating a database](#migrate): cactusdb then writes to a **new**
database it creates for it (and refuses one that already exists), and only for the
superuser. The source is read like any other database, in read-only transactions.

## 4. Exporting from the command line (no browser)

Give cactusdb the database details and the table, and it exports straight to a file and
exits. No web server is started, and nothing is saved to `cactus.conn.db`.

```sh
cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima \
         --dbtable orders --exportas excel --exportto ./export
```
```
Password for root@10.66.1.136:3306:
Exporting 1 table(s) as xlsx to /home/me/cactusdb/export/cli-10.66.1.136/xlsx
  orders                             15234 rows  /home/me/cactusdb/export/cli-10.66.1.136/xlsx/orders_20261026-141502.xlsx
```

The password is **asked for** and not shown while you type. For scripts, pass it with
`--dbpassword xyz` or, better, the `CACTUSDB_DBPASSWORD` environment variable (see
[Security](#7-security)). SQLite needs no password.

| Option | What it does |
|--------|--------------|
| `--dbtype` | `sqlite`, `mysql`, `mariadb`, `firebird`, `mssql` (or `sqlserver`), `postgres` (or `postgresql`), `oracle`, `informix`, `access`, `cockroachdb`, `azuresql`, `azurepg`, `horizondb`, `redshift`, `athena`, `bigquery`, `mongodb`, `cosmosdb`, `couchbase`, `cassandra`, `redis`, `neo4j`, `pgvector` or `weaviate`. Required. |
| `--host` | The database server. Default `localhost`. For a SQL Server named instance use `server\INSTANCE`. |
| `--dbport` | The database port. Default: the standard port (3306, 3050, 1433 or 5432). |
| `--user` | The database user. Required for the SQL databases, except SQLite and Access. |
| `--dbpassword` | The password. Leave it out to be prompted. |
| `--dbname` | The database. For SQLite and Access the database file; for Firebird the database path or alias on the server; for Oracle the service name; for BigQuery the dataset, for Cassandra the keyspace… Required for the SQL databases. |
| `--dboptions` | Extra connection settings, as in the web UI's **Options**: `key=value&key=value`, e.g. `workgroup=primary&output=s3://bucket/results/` for Athena. |
| `--dbtable` | The table or view to export; several separated by commas (`--dbtable orders,customers`); or `all` for every table. |
| `--showtable` | `all` lists the tables and views; a table name prints its definition (`CREATE TABLE …`). |
| `--exportas` | `csv` (default), `json`, `xml` or `excel`. |
| `--exportto` | The base folder for exports. Default: the `export` folder next to cactusdb. Files go to `<exportto>/cli-<host>/<type>/`: one folder for the connection (named like the saved connection), with a subfolder for each type: `json`, `csv`, `xlsx` (Excel), `xml`, `sql` for DDL and `backup` for backups. |
| `--threads` | How many tables to export at the same time. Default `4`. |
| `--exportddl` | `all` writes the DDL of every object to one `.sql` file; table names (comma-separated) write just those tables. See [DDL export](#ddl-export). |
| `--backup` | `yes` backs up the whole database (DDL and every row) to a compressed `.zip` in the connection's `backup` folder. See [Database backup](#backup). |
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
| `--exportto` | folder `export` next to the cactusdb program | The base folder for exported files: each connection gets a folder in it, with a subfolder for each type (`json`, `csv`, `xlsx`, `xml`, `sql` for DDL and `backup` for backups). See [Where exports go](#where-exports-go). Created automatically if missing. A relative path such as `./export` is relative to the folder you start cactusdb from. |
| `--conn-db` | `cactus.conn.db` next to the cactusdb program, or where it was moved to (☰ → Folder setup) | The file saved connections, settings and the usage log are stored in |
| `--key-file` | `cactus.key` next to the connection file | The key that encrypts saved usernames and passwords. Created automatically. Not used when `CACTUSDB_PASSPHRASE` is set. |
| `--host` | `0.0.0.0` (every network interface) | The address to listen on. By default other computers on your network can use cactusdb at `http://<this computer's IP address>:<port>`; the addresses are printed at startup. Use `127.0.0.1` to allow only this computer, `::` to listen on IPv6 too, or one of this computer's IP addresses to listen on that network only. See [Security](#7-security). |
| `--runas` | `foreground` | `background` starts the web UI in the background: it keeps running after you close the terminal and logs to `--logfile`. See below. |
| `--logfile` | `cactusdb.log` next to the program, with `--runas background` | Every action is logged here: connections (never passwords), tables opened, queries, exports, saved connections, and every request with its result and session (`session=` and the first 8 characters of its id, so you can follow one browser's requests). In the foreground, the log goes to the terminal and, if you give `--logfile`, to that file too. A new file starts every day: earlier days are renamed to `cactusdb-YYYY-MM-DD.log` and kept for 30 days. |
| `--remotelogin` | off | Everyone signs in before anything can be seen: the first user is `admin` / `admin`, other users are e-mail addresses and get an e-mailed code. See [Signing in first](#remote-login). |
| `--resetadmin` | | Set the `--remotelogin` user `admin`'s password back to `admin` (to be changed at the next sign-in), then exit. Use the same `--conn-db` as the running cactusdb. |
| `--version` | | Print the version and exit |
| `--help` | | List the options |

Examples:
```sh
cactusdb --port 9000                                  # different port
cactusdb --port 8087 --exportto D:\Reports\exports    # exports to another drive (Windows)
cactusdb --port 8087 --exportto ~/Desktop/exports     # exports to your Desktop (macOS/Linux)
cactusdb --port 8087 --web yes                        # open from any machine: http://<ip address>:8087
cactusdb --port 8087 --host 127.0.0.1                 # this computer only
cactusdb --port 8087 --remotelogin                    # everyone signs in first (admin / admin)
CACTUSDB_PASSPHRASE='my long passphrase' cactusdb --port 8080   # encrypt with a passphrase (macOS/Linux)
```

### Running in the background

```sh
cactusdb --port 8087 --runas background
```
```
cactusdb 1.31.0 is running in the background (process id 48211)
  open http://localhost:8087
  open http://192.168.1.20:8087
  log file: /home/me/cactusdb/cactusdb.log
  stop it with: kill 48211
```
cactusdb starts a copy of itself without a terminal, waits until it answers, and tells
you where to open it. If it can't start (the port is in use, the connection file can't be
opened…), you get the error straight away. The process id is also written to
`cactusdb.pid` next to the log file, so you can stop it later:

- **From the browser:** a background cactusdb has **⏻ Shutdown** at the bottom of the
  ☰ menu (bottom left, under *Check for update*). Click it and confirm. cactusdb cancels running exports,
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
2026/10/26 17:26:35 127.0.0.1 [cli-10.66.1.136] export xlsx: 15234 row(s) to /home/me/cactusdb/export/cli-10.66.1.136/xlsx/orders_20261026-172635.xlsx
```

| Environment variable | What it does |
|----------------------|--------------|
| `CACTUSDB_DBPASSWORD` | The database password for [command-line exports](#4-exporting-from-the-command-line-no-browser), so you aren't prompted for it. |
| `CACTUSDB_GA_ID` | The [Google Analytics](GOOGLE_ANALYTICS.md) Measurement ID of this server (`G-…`); wins over ☰ → Settings. |
| `CACTUSDB_GA_API_SECRET` | The Measurement Protocol API secret, to send command-line runs to Google Analytics; wins over ☰ → Settings. |
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
| `dd` | Checks and creates the data folders (and `DD_SQLITE_DIR`, if set), then builds the image and runs a container (restart `unless-stopped`, health check) as your user, publishing `CACTUSDB_PORT` on `DD_BIND` (every address by default) | Host folders `DD_CONN_DIR` and `DD_EXPORT_DIR` (default `~/cactusdb/conn` and `~/cactusdb/export`) |
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

**Reaching your databases from the container (`dd`, `ce`):** inside the container,
`localhost` is the container itself. Use the host `host.docker.internal` for a database on
the computer running Docker, also one in another container that publishes its port there,
for example a Firebird container on 3050. Use a server's name or IP address otherwise. cactusdb
explains this if a connection to `localhost` fails. On k3s and Kubernetes, use the
database's Service name or a server address.

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
cactusdb 1.31.0 is running in container cactusdb
  open http://localhost:8087
  open http://192.168.1.20:8087
  saved connections: /srv/cactusdb/conn/cactus.conn.db (key: /srv/cactusdb/conn/cactus.key)
  exports:           /srv/cactusdb/export/<connection>/{json,csv,xlsx,xml,sql,backup}
```

| Option | Default | What it does |
|--------|---------|--------------|
| `--port` | `8080` | Port to publish on the host (cactusdb listens on the same port in the container) |
| `--bind` | `0.0.0.0` | Host address to publish on; `127.0.0.1` keeps it to the host itself |
| `--data-dir` | `~/cactusdb` | Base folder on the host for `export/` and `conn/` |
| `--export-dir` | `<data-dir>/export` | Host folder for exports (`/opt/cactusdb/export` in the container) |
| `--conn-dir` | `<data-dir>/conn` | Host folder for `cactus.conn.db` and `cactus.key` (`/opt/cactusdb/conn`); readable only by you |
| `--sqlite-dir` | | Host folder of SQLite databases, mounted read-only at `/sqlite`: connect to `/sqlite/<file>`. Created if it doesn't exist yet. |
| `--tz` | `$TZ` | Time zone for log and file-name times, e.g. `Europe/Amsterdam` |
| `--name`, `--image` | `cactusdb` | Container and image name |
| `--runtime` | docker, else podman | Container engine to use |
| `--from-source` | | Compile the binary with Go first instead of using `dist/` |
| `--remote` | | `user@server`: deploy to Docker CE on that Linux server over SSH (via `deploy.sh --docker ce`) |
| `--install-docker` | | With `--remote`: install Docker CE on the server first if it's missing |

- `up` replaces a running container with the new image; your data folders are untouched.
  `down` removes the container and keeps the data.
- **Connecting to databases:** inside the container, `localhost` is the container itself.
  Use the host `host.docker.internal` for databases on this computer, including database
  containers that publish their port here, such as Firebird on 3050. Use a server's name or
  IP address otherwise. The scripts add this name to the container, on Docker Desktop and on
  Linux.
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
├── cactusdb.old(.exe)   the previous version, after an update installed from the web UI
├── cactus.conn.db       your saved connections, the chosen export folder, the usage log and
│                        the startup options (for installing an update from the web UI)
│                        (an SQLite database; can be moved: ☰ → Folder setup)
├── cactus.conn.db.location  where it was moved to, if it was
├── cactus.key           the key that encrypts saved usernames and passwords
├── cactusdb.log         today's action log (with --runas background or --logfile)
├── cactusdb-2026-11-06.log  the log of an earlier day (kept 30 days)
├── cactusdb.pid         the process id of a background cactusdb, while it runs
├── cactusdb-8080.running  marks a running web UI (on port 8080); left behind, it tells the
│                        next start that cactusdb was killed (see Troubleshooting)
├── cactusdb-crash.log   what Go printed when cactusdb crashed, if it ever did (also a
│                        file per day: cactusdb-crash-YYYY-MM-DD.log)
└── export/              your exported files, one folder per connection
    └── optimo-prod/     named after the connection
        ├── backup/      database backups (.zip)
        ├── csv/
        ├── json/
        ├── queries/     exports of saved queries, a folder per day:
        │   └── 07.11.2026/cutter_subdiv_code-07.11.2026.14.05.csv
        ├── sql/         DDL scripts
        ├── xlsx/        Excel
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

### The usage log: what is used in cactusdb

Every function used in cactusdb is recorded in the **`usage_events`** table of
`cactus.conn.db`, with the client's IP address and the session. cactusdb has no page to
show it, by design: to explore it, open `cactus.conn.db` with any SQLite tool (DB Browser
for SQLite, `sqlite3`, DBeaver, …) or add it as a **SQLite connection** in cactusdb itself.

Two kinds of events are recorded:

- **`server`**: every request the page makes: connecting, opening a table, running a
  query, an export, a backup, comparing databases, saving a connection… With its HTTP
  result, how long it took and what it did (the same text as the log: SQL shortened to one
  line; passwords are never recorded).
- **`cli`**: every [command-line](#4-exporting-from-the-command-line-no-browser) export, DDL
  export, backup and `--showtable`, with what it did, whether it worked (`status` 200 or
  500, and the error in `detail`), how long it took, and in `user_agent` the cactusdb
  version, system, user and computer (`client_ip` is `local`). Recorded when there is a
  connection database (`cactus.conn.db`, or where it was moved to).
- **`ui`**: what only happens in the page: tabs, menu items, buttons, sorting, check boxes,
  drop-downs, the filter. Of what you type only *which field* is recorded, never the
  text (no SQL, passwords or connection details), except the filter boxes and day pickers.
  The page sends them in batches every few seconds and when it is closed.

| Column | What it holds |
|---|---|
| `id` | Increases with every event |
| `at` | When, in UTC: `2026-11-07 08:15:02.123` |
| `source` | `server`, `ui` or `cli` |
| `function` | What was used: `connect`, `open table`, `run query`, `export`, `compare databases`, `tab: logs`, `menu: compare`, `sort objects: rows`, `set: cmpDrops`, `button: disconnectBtn`, … |
| `detail` | What it did, e.g. `[shop] query: select * from orders`, the button's label, `on`/`off`, the chosen value |
| `client_ip` | The browser's IP address (behind a proxy or ingress: the proxy's) |
| `session_id` | The database session, the first 8 characters as in the log (the whole id works like a password while it lasts); empty before connecting |
| `page_id` | One browser tab, from opening the page until it is closed or reloaded: groups everything one person did, also before connecting |
| `connection` | The connection's name; `engine`: its database type |
| `method`, `path`, `status`, `duration_ms` | The request of a `server` event and its result |
| `user_agent` | The browser |

Progress polls of exports and comparisons and the Logs tab's live updates aren't
recorded: they aren't something you did. Events are written in the background, so they
never slow cactusdb down.

**Housekeeping:** ☰ → Settings → *Usage log* shows how many events there are and from
when to when, and deletes the events of a date range: pick the first and last day (days in
your computer's time zone; leave one empty for "from the first" or "up to the last"),
click **Delete events**, then **Yes, delete them**. cactusdb never deletes events itself.

Some questions it answers:

```sql
-- the most used functions in the last 30 days
SELECT function, count(*) AS times FROM usage_events
WHERE at >= datetime('now', '-30 days') GROUP BY function ORDER BY times DESC;

-- who uses cactusdb: per computer, per day
SELECT date(at) AS day, client_ip, count(DISTINCT page_id) AS visits, count(*) AS events
FROM usage_events GROUP BY day, client_ip ORDER BY day DESC, events DESC;

-- everything one session did, in order
SELECT at, source, function, detail FROM usage_events WHERE session_id = 'a98bd9d1' ORDER BY at;

-- slow or failed requests
SELECT at, function, detail, status, duration_ms FROM usage_events
WHERE source = 'server' AND (status >= 400 OR duration_ms > 5000) ORDER BY at DESC;
```

### Google Analytics (opt-in)

Each cactusdb server can also send usage statistics to its own Google Analytics property,
but only for people who say yes. [GOOGLE_ANALYTICS.md](GOOGLE_ANALYTICS.md) explains what
is sent and how to set it up. In short:

- Set the server's **Measurement ID** in ☰ → Settings (or `CACTUSDB_GA_ID`). Without one,
  nothing is loaded or sent.
- Everyone is then asked once, in a bar at the bottom of the page: *May cactusdb send
  anonymous usage statistics?* Nothing is sent until they click **Yes, send**; they can
  change it any time in ☰ → Settings (*this browser*).
- Only which functions are used is sent, never data, SQL, names, paths or addresses.
- When a proxy or firewall blocks Google, cactusdb works exactly the same: Google's script
  just doesn't arrive, and nothing more is attempted.
- Command-line runs are sent by the server with an **API secret**, when *Send command-line
  runs* is switched on; they wait at most 2 seconds for Google.

## 7. Security

- **Who can open cactusdb:** anyone who can reach its port, unless it is started with
  [`--remotelogin`](#remote-login): then only signed-in users, with an e-mailed code for
  each sign-in.

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
- **Connection passwords** add a second layer for each saved connection. Its username,
  password and saved queries are encrypted again with a key derived from the
  connection's own password (Argon2id: 3 passes, 64 MiB, random salt per connection). So
  even someone with `cactus.conn.db` *and* `cactus.key` can't read them without that
  password. Wrong guesses are slowed down. The connection's exported files are only served
  to someone who gives its password. See [Connection passwords](#connection-passwords).
- **Who is using cactusdb** (the people button) shows every open page's client IP and
  browser to everyone who can open cactusdb, and the notices name who changed what by client
  IP. They never contain a password, a query or a query's name.
- **Direct messages** are given only to the page they are sent to and the page that sent
  them, are held in memory only (never logged or stored), and are dropped once both pages
  are closed. The log notes only that one IP sent a message to another.
- **Migrating** writes only to a database cactusdb creates for it, refusing one that already
  exists, and only with the superuser password. *Only write the script* writes to no
  database. The script of a migration can be downloaded only by whoever started it.
- **The superuser password** is never stored. It protects an X25519 private key, and each
  connection password is sealed to the matching public key (with HKDF-SHA256 and
  AES-256-GCM). Choose a long one, keep it somewhere safe, and don't put it in shared
  files. It opens everything. See [Superuser](#superuser).
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
  `cactus.key` and the files in the export folder, and, when an update is installed from
  the web UI, the program itself (the previous one is kept as `cactusdb.old`).
- **Installing an update from the web UI** only ever downloads the file for this system
  from `dist/` of this repository on GitHub (over HTTPS), checks it against
  `dist/SHA256SUMS` and runs it with `--version` before using it; it must be newer than the
  running version. Like Shutdown it only accepts a JSON request, so other websites can't
  start it. A file sent from the browser (for a server without internet access) needs the
  superuser password. `startup_options` holds the names of `CACTUSDB_…` variables, never
  their values.
- **Deploying database containers** (☰ → DB container deployment) runs `docker` or
  `kubectl` on the cactusdb server, so deploying, starting, stopping, restarting, deleting,
  purging and reading container logs need the superuser password (wrong ones are slowed
  down like the others). Looking at a container's settings and usage doesn't; its
  passwords are hidden there. Without a superuser password nobody can deploy. cactusdb only
  lists, manages and reads the logs of containers it deployed itself (labelled `com.cactusdb.managed`, or `app.kubernetes.io/managed-by=cactusdb`).
  Database passwords reach Docker through the environment and Kubernetes through a Secret,
  never a command line, and are not kept by cactusdb.
- **The usage log** in `cactus.conn.db` (see *The usage log*) holds the SQL that was run
  (shortened), table and connection names and IP addresses, but never passwords or a whole
  session id. It isn't encrypted, so give `cactus.conn.db` to someone else only if they
  may see that too.

## 8. Troubleshooting

| Problem | Fix |
|---------|-----|
| In a container (Docker, `deploy.sh`): `dial tcp [::1]:3050: connect: connection refused` (or `127.0.0.1`) | Inside a container, `localhost` is the container itself, not your computer. Use the host **`host.docker.internal`**: it reaches the computer running Docker, and database containers that publish their port there (for example Firebird on 3050). For another server, use its name or IP address. cactusdb says this in the error message too. Containers started by `deploy.sh` or `deploy-container.sh` before 1.13.2 lack this name: run `--install` (or `up`) again. |
| On a server, tables (or connecting, or saving a query) sometimes don't load for about five minutes, then work again, while a cactusdb on your own computer always works with the same database | A firewall or intrusion-prevention device between your computer and the server blocks you from cactusdb's port for a few minutes when it sees an HTTP **403** reply (it takes it for someone guessing passwords) or an HTTP **DELETE** request. Before 1.21.0 cactusdb answered 403 every time the page opened a protected connection, and to a wrong connection password. Update to 1.22.0 or later: the web UI then receives every error as 200 with the real status in the `X-Cactusdb-Status` header (1.21.0), and deletes with POST instead of DELETE (1.22.0). Such a device can also block you when the page receives a database's syntax error: MySQL's and MariaDB's *You have an error in your SQL syntax* is a classic sign of SQL injection. Since 1.46.1 the page receives error messages escaped (`\u0059\u006f…`), which it shows as usual but such a device doesn't recognise; update to it if the block came right after **Check syntax**, saving a query or a query with a mistake. To see whether you're blocked, try another port on the same server (SSH, a web server): if that works but cactusdb's port doesn't, it's the block, and it passes by itself; the server's log (Logs tab) then has no line at all from your computer for those minutes. |
| On a server, connecting is quick but a table stays at *Loading …* and the request times out, then later cactusdb doesn't answer at all, with nothing in `cactusdb-crash.log` | Before 1.20.0, cactusdb wrote every request to the terminal it was started in, and waited for the terminal to take it. When that terminal stopped reading, an SSH session whose VPN or network dropped, or a terminal paused or scrolled back, the output filled up after about a hundred requests and every request then waited: the page still loaded, but tables didn't. When SSH finally closed the dead session, the hangup signal (SIGHUP) stopped cactusdb on the spot, without a crash. Since 1.20.0 the terminal can't hold up a request (log lines it doesn't take are dropped and counted), and cactusdb keeps running when its terminal closes. For a server, start it with **`--runas background`** anyway, or as a service: it then logs to `cactusdb.log` and needs no terminal at all. If a later start says the port is in use, cactusdb names the copy that's still running. |
| Connecting to a cactusdb on a server works sometimes, and sometimes not, while a cactusdb on your own computer always works | Before 1.17.0 cactusdb kept every session's database connections open until it stopped: a closed or reloaded browser tab left its connections behind, and idle connections were cut by the database server (`wait_timeout`), a firewall or the VPN, so the next request on one failed. A server that runs for days collected them until the database's connection limit, shared with its other applications, was reached. cactusdb now closes database connections idle for a minute, disconnects after the idle time of Settings (default 15 minutes), ends the session when the page is closed, and retries a connect that got no answer. The log shows how long each connect took (`connect … in 120ms`), and a failed connect shows the database's exact error. |
| *cactusdb was restarted: the previous cactusdb … stopped without shutting down* | The previous cactusdb was killed rather than stopped with Ctrl+C or Shutdown, most often by the system for lack of memory. Since 1.15.0 Excel exports use far less memory (exporting four large tables at once used to need hundreds of MB). Give the server or container more memory, or export fewer tables at once. |
| *No answer from cactusdb at … (the browser reports “Failed to fetch”)* | The browser got no answer at all, and the message says what cactusdb was doing, after how long, and what it found when it checked:<br>• **"cactusdb is running … cut on the way"**: a firewall, proxy, VPN or Wi-Fi drop between your computer and the server closed the connection. Since 1.14.1 cactusdb closes idle connections itself after 20 seconds, before such devices drop them silently, and the page retries a read-only request once. If long requests still fail after the same number of seconds, that device has a time limit: have it raised, or narrow the query.<br>• **"doesn't answer now either"**: cactusdb stopped or crashed (look at its log or start it again), or the network to the server is down. When you start it again it says why the previous run ended, in its terminal and on the page: *crashed: …* (the details are in `cactusdb-crash.log` next to the connection file), or *stopped without shutting down*, which means it was killed: most often by the system because the server ran out of memory (on Linux, `dmesg | grep -i -E 'killed process|out of memory'` shows it; in a container, raise its memory limit), or the server or container restarted.<br>• **"was restarted meanwhile"**: reload the page and connect again.<br>An internal error inside cactusdb no longer cuts the connection: it's shown as *internal error in cactusdb…*, with the details in the log. |
| A saved connection asks for a password I don't have | Since 1.14 every saved connection has its own connection password. If you forgot it, the superuser can open it (🛡), provided a superuser password was set before the connection's password and the connection has been opened since. Otherwise delete it (✕) and save it again. |
| *wrong connection password* / *try again in N seconds* | After five wrong passwords for a connection, each further try has to wait (2 seconds, doubling up to 5 minutes). Wait, then type the password carefully. |
| *No superuser password has been set* | Set one on the computer running cactusdb: `cactusdb --superuser set` (in a container: `docker exec -it cactusdb cactusdb --conn-db /opt/cactusdb/conn/cactus.conn.db --superuser set`). |
| `--remotelogin`: *cactusdb couldn't e-mail your sign-in code* or *no e-mail server is set up* | The e-mail users need e-mail to sign in. Sign in as `admin` (it gets no code), open ☰ → **E-mail server**, set it up, tick *Use this server for all e-mail*, save and **Send test e-mail**. If the test fails, the message is the mail server's answer; the **Setup guide** lists the usual causes. |
| `--remotelogin`: forgot `admin`'s password | On the server, with the same `--conn-db`: `cactusdb --resetadmin`. Then sign in with `admin` / `admin` and choose a new password. |
| `--remotelogin`: the link in the e-mail doesn't open cactusdb | Set **Address in e-mailed links** under ☰ → E-mail server to the address the others use for cactusdb (e.g. `http://10.66.1.136:8087`), then **Send link** again under ☰ → Users. A link works for one hour, and once. |
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
| Microsoft SQL Server | 2012 and newer (tested with SQL Server 2022). Uses SQL Server authentication; TLS encryption is used when the server offers it, and the server certificate is trusted like SSMS's *Trust server certificate* option. |
| Oracle | 11g and newer, including 19c, 21c, 23ai and Autonomous Database, through Oracle's own network protocol: no Instant Client needed. DDL from `DBMS_METADATA`, or built from the data dictionary without that privilege. |
| Informix | 12.10 and newer, through the wire listener with SQL passthrough. |
| Microsoft Access | `.mdb` files of Access 97 (Jet 3) and 2000–2003 (Jet 4), `.accdb` files of Access 2007 and newer, read directly: no Access, ACE or ODBC driver needed. Password-protected (encrypted) `.accdb` files can't be read. Tested with the sample files of the mdbtools project. |
| CockroachDB | 23.1 and newer, self-hosted or Cloud (PostgreSQL protocol). DDL from `SHOW CREATE`. |
| Azure SQL Database | Every service tier, Hyperscale included, with SQL authentication. |
| Azure Database for PostgreSQL, Azure HorizonDB | Flexible Server and HorizonDB (PostgreSQL protocol, TLS). |
| Amazon Redshift | Provisioned clusters and Serverless (PostgreSQL protocol, TLS). DDL from `SHOW TABLE` / `SHOW VIEW`. |
| Amazon Athena | Engine versions 2 and 3, through the Athena API signed with AWS Signature Version 4. |
| Google BigQuery | Through the BigQuery REST API, with a service account key or gcloud application-default credentials. |
| MongoDB | 4.0 and newer, Atlas included, and servers that speak its protocol (Amazon DocumentDB, Azure Cosmos DB for MongoDB, FerretDB). |
| Azure Cosmos DB | The NoSQL API, through its REST API with an account key; the emulator too (`http://localhost:8081`). |
| Couchbase | 6.5 and newer (collections from 7.0), through the query service. |
| Apache Cassandra | 3.x, 4.x and 5.x, ScyllaDB, DataStax Enterprise, Azure Cosmos DB for Apache Cassandra. |
| Redis | 5 and newer, Valkey, Azure Cache for Redis, Amazon ElastiCache; RedisJSON values are shown as JSON. |
| Neo4j | 4.4 and 5.x, Aura included (Bolt protocol). |
| PostgreSQL + pgvector | Any PostgreSQL with the `vector` extension; vectors are shown as text, e.g. `[0.1,0.2,0.3]`. |
| Weaviate | 1.20 and newer, Weaviate Cloud included. |

Not every type was tried against a live server of that kind. They were all checked with
the protocol's own test cases or a stand-in server, and real servers were used for Redis
and for Access files. Please report anything that doesn't work.

All drivers are built into cactusdb and listed under **Drivers** in the menu. Unlike tools such
as DBeaver, there is nothing to download, and no client library (libmysqlclient,
fbclient.dll, libpq, Oracle Instant Client, ODBC drivers, cloud SDKs, …) needs to be installed.

---

## For developers

cactusdb is written in Go and uses pure-Go drivers, so no CGO or C compiler is needed.

```sh
go build -o cactusdb .   # build for this computer
./build.sh               # build all six binaries into dist/, as the version in VERSION
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

`engines_test.go` checks the other database types against stand-in servers (Cosmos DB,
Couchbase, BigQuery, Athena, Weaviate) and AWS's signature test case. `access_probe_test.go`
reads every table of the Access files in a folder:

```sh
CACTUSDB_ACCESS_DIR=~/mdbtestdata/data go test -run TestAccessFiles ./...
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
| `sys_windows.go`, `sys_other.go` | Platform specifics: detaching from the terminal, console colours, restarting after an update |
| `db.go` | The database types (`engines`) and their connection forms, connecting, listing columns, running queries |
| `backend.go` | The databases reached through their own client: the `backend` interface, wrapped in `database/sql`, and documents shown as rows |
| `oracle.go`, `pgflavors.go` | Oracle; the differences of CockroachDB, Redshift and pgvector from PostgreSQL |
| `eng_mongo.go`, `jsparse.go` | MongoDB, and the MongoDB shell syntax of its queries |
| `eng_informix.go` | Informix through the wire listener (SQL passthrough) |
| `eng_cassandra.go`, `eng_redis.go`, `eng_neo4j.go` | Cassandra, Redis, Neo4j |
| `eng_http.go`, `eng_couchbase.go`, `eng_cosmos.go`, `eng_weaviate.go`, `eng_bigquery.go`, `eng_athena.go` | The databases reached through an HTTPS API (BigQuery's Google sign-in, Athena's AWS Signature Version 4) |
| `jet.go`, `eng_access.go` | Reading Microsoft Access files, and the SQL subset that queries them |
| `containers.go` | ☰ → DB container deployment: finding Docker and Kubernetes, the catalogue of database images, deploying with `docker run` or `kubectl apply`, and the list, logs and removal of what it deployed |
| `containers_manage.go` | A deployed container's page: its settings and CPU, memory and storage use, and start, stop, restart, delete and purge |
| `databases.go` | The connection form's list of the server's databases, and the types that need a database |
| `objects.go` | Listing objects (tables, views, procedures, …) and building their definitions |
| `tablestats.go`, `rowsize.go` | Rows and size of every table; the size estimated as rows × the columns' declared sizes when the database doesn't report it |
| `readonly.go` | The read-only query check |
| `export.go` | Excel / JSON / XML / CSV export |
| `store.go` | Saved connections and settings (`cactus.conn.db`) |
| `settings.go` | Changing the export folder from the web UI |
| `lock.go` | Connection passwords (Argon2id keys), saved queries in the store, the wrong-password throttle |
| `auth.go` | `--remotelogin`: users, signing in with the e-mailed code, invitations, password resets, requests for access, the sign-in gate in front of everything (`/api/auth/…`, `/api/users`); the sign-in page is `web/login.html` |
| `mail.go` | ☰ → E-mail server: the SMTP settings (password sealed with the key), sending e-mail (STARTTLS, SSL/TLS or none), the test e-mail (`/api/email`) |
| `superuser.go` | The superuser password: X25519 key pair, sealed connection passwords, `--superuser` |
| `queries.go` | The saved-queries API (read-only check) and Check syntax |
| `files.go` | The *Exported files* page: listing, previews, deleting, and compressed downloads of a selection |
| `Dockerfile` | Container image (no build steps, so it builds for any CPU without emulation) |
| `deploy.sh`, `.env.example` | Deployment to Docker, k3s or Kubernetes, and its settings |
| `deploy-container.sh` | Docker-only deployment (older) |
| `crypt.go` | Encryption of saved usernames and passwords |
| `compare.go` | ☰ → Compare databases: the structure of databases against a master, and the script that removes the differences |
| `dbstats.go` | The Dashboard: the statistics queries of each kind of database and `/api/dbstats` |
| `presence.go` | Who is using cactusdb (the open pages and their client IPs, `/api/presence`), the notices of changes sent to the other pages, and direct messages between pages (`/api/messages`) |
| `migrate.go` | ☰ → Migrate database: the job, creating the new database, copying the rows, keys, indexes and the report and script |
| `migrate_schema.go` | Tables described without their engine: reading each engine's catalog, and writing the target's column types, defaults and DDL |
| `migrate_code.go` | Views, procedures and triggers for the target: an SQL tokenizer and rewrites, and Firebird's PSQL parsed and written as MariaDB/MySQL or PL/pgSQL |
| `dbmove.go` | Moving `cactus.conn.db` (☰ → Folder setup) and finding it again at the next start |
| `ga.go` | Google Analytics settings, and command-line runs in the usage log and Google Analytics |
| `usage.go` | The usage log: every function used, in the `usage_events` table of `cactus.conn.db` |
| `logs.go`, `logrotate.go` | The Logs tab (live, any day, crash log, .zip of every log); a new log file every day |
| `termlog.go` | Logging to the terminal without ever holding up a request |
| `update.go`, `VERSION` | Checking GitHub for a newer version; `VERSION` names the version of the binaries in `dist/` |
| `upgrade.go`, `dist/SHA256SUMS` | Installing a newer version from the web UI and restarting with the saved startup options (`startup_options` table); the checksums the download is checked against (written by `build.sh`) |
| `setup.sh`, `setup.ps1` | Download the right binary for this computer as `cactusdb` / `cactusdb.exe` |
| `web/index.html`, `web/cactusdb.svg` | The browser interface and icon, embedded into the binary |
| `web/vendor/` | [Bootstrap 5.3.8](https://getbootstrap.com/) (its CSS and JavaScript bundle) and [Bootstrap Icons 1.13.2](https://icons.getbootstrap.com/), embedded too: the page loads nothing from the internet |

## Changes

See [CHANGELOG.md](CHANGELOG.md) for what's new in each version.

## License

Public domain, under the same terms as SQLite: see [LICENSE](LICENSE).

> May you do good and not evil.
> May you find forgiveness for yourself and forgive others.
> May you share freely, never taking more than you give.

The third-party drivers built into the binary keep their own licenses (BSD, MPL-2.0,
MIT and Apache-2.0), listed in [LICENSE](LICENSE). Bootstrap and Bootstrap Icons are MIT
licensed: their licenses are next to them in `web/vendor/`.
