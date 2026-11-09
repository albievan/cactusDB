# Changelog

## 1.40.0 — 2026-11-09

- **DB container deployment**: a **Restart** choice below *Reachable from other computers*
  (Docker): **Unless stopped** (the default, as before), **Always**, **On failure** or **No**.
  The container page's settings show the policy it has. Hidden for Kubernetes, which always
  restarts a deployment's pods.

## 1.39.0 — 2026-11-09

- **Dashboard**: when the connected database has statistics cactusdb can read (PostgreSQL
  and its family, MySQL, MariaDB, SQL Server, Azure SQL, Firebird, SQLite, Oracle), a
  **Dashboard** button in the top bar opens a **Dashboard** tab in the main area: version,
  uptime, size, connections, activity, cache hit rates, running queries, the largest tables,
  waits, locks, settings… as cards (with bars for percentages) and tables, measured again
  every 10 seconds while shown (**Live**), from the database's statistics views only. What
  the connection may not read is listed as not available, with the reason.

## 1.38.0 — 2026-11-09

- **DB container deployment**: the deployed containers are in a **table** (container,
  database, runs on, port, state, status) **sorted by any column** (click its heading, again
  for the other way) with a **search by container name**; each row's buttons (view, add
  connection, logs, remove) are always shown, not only on hover.
- **How to install Docker or Kubernetes on this server**: steps for the server's OS,
  processor (amd64 or arm64) and Linux distribution, one tab each for Docker CE, Docker
  Desktop, k3s and Kubernetes (k8s), with the commands to copy. Open when neither Docker nor
  Kubernetes was found.
- Each **saved connection shows when it was saved** (date and time) next to its name, and in
  its hint.
- The **sidebar can be resized**: drag its right edge; the width is remembered in the
  browser, and a double-click on the edge restores the usual width.

## 1.37.0 — 2026-11-09

- **A container's log** on its page is shown **newest first** (a message over several lines
  keeps its lines in order) and **live**: new lines come in every 3 seconds, at the top,
  while the view stays there; untick **Live** to stop. **Export** saves the whole log to
  `<export folder>/<container>/log/` (a new *Log* type on the Exported files page) and
  **Download** downloads it as a `.log` file. The password asked for is the superuser's,
  kept while the page is open. The log in the containers list is newest first too.
- A **hidden value** of a container's environment (a password, key or token) has an eye
  button: after the superuser password, asked every time, it shows the value (on
  Kubernetes from the deployment's Secret); clicking again hides it. Each reveal is logged.

## 1.36.3 — 2026-11-08

- The type groups of the saved connections **open and close** from their heading (a chevron
  shows which are open), in the menu and the sidebar alike; several can be open at once, and
  all start closed when cactusdb is opened in the browser.
- Opening or closing something in the menu no longer closes the menu.

## 1.36.2 — 2026-11-08

- **Saved connections are grouped by the type of database** (Firebird, MariaDB, SQL Server…,
  each heading with how many there are) and sorted by name, in the menu and in the sidebar.
- Hovering over a saved connection shows a hint with its **whole name** (which the list may
  cut off), its type and, for the superuser, where it connects to.
- README: the menu items are named as they are now, without the old emoji.

## 1.36.1 — 2026-11-08

- The top bar shows the type of the connected database (e.g. *PostgreSQL*, *Firebird*) in a
  label with a database icon next to *read-only*; it goes away on disconnect.

## 1.36.0 — 2026-11-08

- **The interface is built with Bootstrap 5.3.8** (the latest stable release) and **Bootstrap
  Icons 1.13.2**, embedded in the binary (`web/vendor/`): the page still loads nothing from
  the internet. Light or dark follows the system, in cactusdb's colours.
  - Buttons, button groups (container actions, script and connection), toggle buttons (the
    object types, file types, Migrate and Compare options), switches (Live, Newest first,
    Problems only, the analytics settings), form controls, input groups (show password, list
    databases), tabs, tables, cards, alerts, progress bars (striped and moving while the size
    is unknown), spinners and toasts are Bootstrap components; dialogs keep the browser's own
    `<dialog>` in Bootstrap's modal look, with its close button.
  - Every compact or icon button uses a Bootstrap icon instead of an emoji, a symbol or a
    drawn icon: the menu, refresh, run, save, download, start, stop, restart, delete, purge,
    view, logs, add connection, copy, preview, the export formats and the object types.

## 1.35.2 — 2026-11-08

- **Folder setup → Connection database**: *Move it to* starts with the database's current
  place instead of being empty; moving to that same place says so before asking.
- A new path without an extension (like the dialog's own example,
  `/Users/you/cactusdb-data`) is now a folder for `cactus.conn.db`; before, it became a
  file of that name.

## 1.35.1 — 2026-11-08

- **Migrate** no longer stops with "this connection was saved before the superuser password
  was set…": when the source or target connection was saved before the superuser password,
  cactusdb asks for that connection's own password once (a wrong one is asked again) and
  goes on; from then on the superuser password opens it too.
- A 👁 **Show password** button in the superuser password fields (Migrate, the container page
  and DB container deployment) and in the connection-password prompt; a cleared field is
  masked again.
- The *Use the superuser password instead* checkbox no longer shows in prompts that don't
  offer it.

## 1.35.0 — 2026-11-08

- **☰ → ⇢ Migrate database** (or the **Migrate** tab): copy a database to a **new** database
  of another type, e.g. Firebird to MariaDB, with its structure, data, views, procedures and
  triggers.
  - From any SQL database (SQLite, MySQL, MariaDB, the PostgreSQL family, SQL Server, Azure
    SQL, Firebird, Oracle, Access, Informix) to MariaDB, MySQL, PostgreSQL, SQL Server,
    SQLite or Firebird. cactusdb creates the new database on the chosen server (a saved
    connection), refuses one that already exists, and needs the superuser password; or it
    only writes the script (.sql) and changes no database.
  - Sequences with their current values; tables with each column's type for the new
    database, defaults, identity columns and primary keys; the rows (batched, committed every
    few thousand, then counted); unique constraints, indexes, foreign keys and checks; the
    identity counters set to go on.
  - Views rewritten for the new database (quoting, names, FIRST/SKIP/TOP/LIMIT, IIF, LIST,
    GEN_ID, CONCAT, ISNULL, PostgreSQL casts and ANY(ARRAY), …).
  - Firebird's procedures, functions and triggers translated to MariaDB/MySQL and to
    PostgreSQL (PL/pgSQL): FOR SELECT loops, SUSPEND, EXCEPTION, EXECUTE PROCEDURE,
    INSERTING/UPDATING/DELETING, WHEN ANY. Between databases of the same kind they are
    copied; other code is listed with its source to rewrite by hand. On MySQL and SQLite the
    generator trigger of a column becomes an auto-numbered column.
  - A report of every object (done, check, by hand, failed, skipped) with its source beside
    what was made of it, the script to download, and *Add as connection* for the new
    database.

## 1.34.1 — 2026-11-08

- **A container's page is a page, not a dialog**: **View** opens it in the main area under
  its own **Container · name** tab, beside Data, Logs and Compare. It measures every 5
  seconds only while shown; **☰ All containers** opens the list and **✕ Close** closes the
  tab (and forgets the superuser password typed there).
- **The buttons beside each deployed container are one compact group with icons**: 👁 View,
  🔌 Add connection, 📜 Logs and 🗑 Remove, each with a hint. The page's own Add connection,
  Logs and Refresh are grouped the same way.

## 1.34.0 — 2026-11-08

- **A deployed container's page** (☰ → 🐳 DB container deployment → **View**, or click its
  name):
  - **Usage**, refreshed every 5 seconds: CPU (percent of one core, with the cores it can
    use), memory (used, of its limit or Docker's memory) and storage (the database files,
    what the container wrote besides them, the image, the volume claim's size, disk and
    network I/O). Kubernetes: from `kubectl top` when the cluster has metrics-server.
  - **Settings**: image and image ID, state and health, created and started, restart policy
    and restarts, ports, data folder or volume, command, limits and labels; on Kubernetes the
    namespace, replicas, service, volume claim and pods. **Environment** variables, with
    passwords, keys and tokens hidden.
  - One compact button row: **Start**, **Stop**, **Restart**, **Delete** (the data stays) and
    **Purge**, each asking first and needing the superuser password. Purge removes
    everything: the container, its data folder and files or its volume (claim) and, when no
    other container uses it, its image; it lists what goes, wants the name typed, and
    refuses a data folder at a disk's root, one level below it, the home folder or one
    holding cactusdb. On Kubernetes, stop and start scale the deployment, and a short pod on
    the node deletes a `hostPath` data folder.
  - **Logs** and **Add connection** there too.
- **New connection: the server's databases.** **☰ List databases** next to the Database
  field lists them (MySQL, MariaDB, the PostgreSQL family, SQL Server, Azure SQL, MongoDB,
  Cosmos DB, Couchbase, Cassandra, Neo4j, Redis, Athena, BigQuery) to pick one. Test
  connection and Save do it too when the field is empty; Save again saves without one.
- **A database is required where the type can't do without one**: SQLite and Access (the
  file), Firebird (path or alias), Oracle (service name) and Informix. The form marks it
  *(required)*, and neither the form nor the server saves or connects without it.
- **The page reloads after an upgrade.** Installing an update could leave the page on
  "Installing…" when the new version started before the page saw it restarting; it now
  reloads. A page open while cactusdb is upgraded some other way (another browser, the
  program replaced on the server) notices within 20 seconds and reloads, or offers
  **Reload** while a dialog is open. The page is served with `Cache-Control: no-cache`, so
  the browser always loads the new one.

## 1.33.0 — 2026-11-08

- **DB container deployment** (☰ → 🐳 DB container deployment): when the cactusdb server has
  Docker (Docker CE or Docker Desktop) or Kubernetes (k8s, k3s), deploy a database server as
  a container: SQL Server 2022, PostgreSQL 17 (also with pgvector), MySQL 8.4, MariaDB 11.4,
  Oracle Database 23ai Free, Firebird 5, MongoDB 8.0, Redis 7.4, Neo4j 5, Cassandra 5.0,
  CockroachDB 24.3 or Weaviate 1.28.
  - Choose where the database files go (a folder on the server or node, or a volume), the
    port, the image tag, the administrator password and, where the image allows, the user
    and database; on Kubernetes the namespace, a LoadBalancer or NodePort service and the
    volume size.
  - Deploying shows each step and waits until the database accepts connections; then
    **Add as connection…** fills in the New connection dialog.
  - The containers cactusdb deployed are listed with Add connection, Logs and Remove (the
    data is kept).
  - Deploying, removing and reading logs need the superuser password. Passwords reach
    docker through its environment and Kubernetes through a Secret, never a command line.

## 1.32.0 — 2026-11-08

- **18 more database types**, grouped in the New connection dialog as *Relational*,
  *Cloud*, *NoSQL* and *Vector*, each with its own form (labels, hints, only the fields it
  uses) and a new **Options** field (`key=value&…`, also `--dboptions` on the command line):
  - Oracle (no Instant Client), Informix (wire listener, SQL passthrough), Microsoft Access
    (`.mdb`/`.accdb` read directly, with a small SQL subset and the SQL of saved queries),
    CockroachDB;
  - Azure SQL Database (Hyperscale, read replica when there is one), Azure Database for
    PostgreSQL, Azure HorizonDB, Amazon Redshift, Amazon Athena, Google BigQuery;
  - MongoDB (MongoDB shell syntax), Azure Cosmos DB (NoSQL API), Couchbase (SQL++),
    Apache Cassandra (CQL), Redis (key groups, SCAN listing), Neo4j (Cypher);
  - PostgreSQL with pgvector, and Weaviate (GraphQL).
- Each is read-only: the server's own read-only mode where it has one (BigQuery dry run,
  Couchbase `readonly`, Neo4j READ transactions, Oracle read-only transactions), and a check
  of every query that lets only reads through.
- Documents are shown with a column per top-level field; definitions describe collections,
  key groups, labels and containers from a sample, with their indexes and options.
- Backup and Compare stay for the SQL databases with their own DDL; they're switched off
  for the others.

## 1.31.0 — 2026-11-07

- **Not connected: day and night**: the sky of the desert scene follows the system's light
  or dark mode. In light mode the sun is animated (a glowing core and slowly turning,
  shimmering rays); in dark mode it becomes a crescent moon that rocks gently, with a soft
  glow, twinkling stars and now and then a shooting star. With *reduce motion* it stays
  still.

## 1.30.0 — 2026-11-07

- **Install an update from the browser**: ☰ → ⬆ Check for update → **Install and restart**.
  The server downloads the binary for its own system from GitHub, checks it against the new
  `dist/SHA256SUMS` and by running it with `--version`, puts it in place (the previous
  version is kept as `cactusdb.old`), stops, and starts the new version with the options it
  was started with. The page follows the download and reloads when the new version answers.
  On Linux and macOS the new version keeps the process id, terminal, log file and service
  manager; started with `--runas background` it stays in the background. If it can't start,
  the previous version is put back.
- **Startup options saved**: every start of the web UI records how it was started (the
  options as typed, the program, folder, run mode, version, process, user and computer, and
  the names of the `CACTUSDB_…` variables) in the new `startup_options` table of
  `cactus.conn.db`, one row per port. The restart after an update reads them from there, and
  the Shutdown dialog shows the real command to start cactusdb again.
- A server without internet access can be sent the binary by the browser, with the
  superuser password.
- Not offered in containers (update the image) or when the program's folder isn't writable.

## 1.29.0 — 2026-11-07

- **Google Analytics, opt-in**: each cactusdb server can have its own Measurement ID
  (☰ → ⚙ Settings, or `CACTUSDB_GA_ID`). People are asked once; nothing is loaded or sent
  until they say yes, and they can change it in Settings. Only names of functions and fixed
  values are sent (engine, format, tab), never data, SQL, names or addresses. When a proxy
  or firewall blocks Google, cactusdb works the same and stops trying. See
  GOOGLE_ANALYTICS.md.
- **Command-line runs are events**: every export, DDL export, backup and `--showtable` from
  the command line is recorded in the usage log (`source` = `cli`), and sent to Google
  Analytics with the Measurement Protocol when an API secret is set and *Send command-line
  runs* is on (waiting at most 2 seconds for Google).
- **Usage log housekeeping**: ☰ → ⚙ Settings shows how many events there are and deletes
  the events of a date range.
- **Move the connection database**: ☰ → 📁 Folder setup can move `cactus.conn.db` (and
  `cactus.key` next to it) to another folder or file, while cactusdb runs; the folder is
  created, the copy checked and the old files deleted. The new place is remembered in
  `cactus.conn.db.location` next to the program for the next start (`--conn-db` still wins).
- **↻ Refresh** next to Disconnect (and ⟳ above the object list): checks the connection with
  a fresh one, lists every table, view, procedure, trigger… again, and reloads what is open.

## 1.28.0 — 2026-11-07

- **Usage log**: every function used in cactusdb is recorded in the new `usage_events`
  table of `cactus.conn.db`, with the client's IP address, the session (first 8 characters,
  as in the log), a page id per browser tab, the connection and its engine.
  - `server` events: every API call (connect, open table, run query, export, backup,
    compare, save connection, …) with its result, duration and what it did.
  - `ui` events: tabs, menu items, buttons, sorting, check boxes, drop-downs and filters
    in the page, sent in batches. What is typed is never recorded, only which field.
  - cactusdb has no page to view it: open `cactus.conn.db` with any SQLite tool. The
    README has the columns and example queries.
- **GOOGLE_ANALYTICS.md**: the plan for Google Analytics (not built yet) and what to set
  up in Google Analytics first.

## 1.27.0 — 2026-11-07

- **Not connected**: the desert scene is busier. Four smaller cactuses (the logo again, some
  with a blossom on top) sway at their own pace, a dozen desert flowers in pink, orange,
  purple and yellow nod in the breeze, and three tumbleweeds of different sizes and speeds
  roll past. The signalling cactus and the mirage database are unchanged.

## 1.26.0 — 2026-11-07

- **Estimated table sizes**: when the database doesn't report a table's size (Firebird),
  the **MB** column now shows rows × row size, marked `≈`, instead of `?`. The row size is
  the sum of the columns' declared sizes: `varchar(20)` 20 bytes, `int` 4, `datetime` 8,
  `decimal(p,s)` p/2+1, …; text and blob columns, which have no declared size, count a
  nominal 256 and 1,024 bytes. The totals under the tabs add them up, marked `≈` too, and
  the tooltip says which sizes are estimated.

## 1.25.0 — 2026-11-07

- **Not connected**: the unplugged cable is replaced by the cactusdb logo in a small desert
  scene. Signals run up the cactus's circuit traces, the rings at their ends send out
  ripples towards a database that is only a shimmering mirage (with a **?**), and a
  tumbleweed rolls by. Works in light and dark mode; still with *reduce motion*.

## 1.24.0 — 2026-11-07

- **☰ → ⇄ Compare databases** (and a **Compare** tab): compares the structure (DDL), never
  the data, of a **master** database with any number of others: tables column by column
  (keys, indexes, foreign keys), views, procedures, functions, triggers, sequences, domains,
  events, packages.
  - Every object is shown per database as **missing**, **extra**, **≠ differs** or **✓ same**,
    coloured; *Differences only*, by type, by name.
  - Click one to see what differs (`column email missing`, `column name differs: …`) and
    the two definitions side by side, the differing lines highlighted.
  - **Script**: per database, the SQL that makes it match the master: drops (optional),
    `ALTER TABLE` for columns, keys and indexes, creates, and replaces changed views and
    routines. **Copy** or **⤓ .sql**. cactusdb never runs it.
  - Tested in the browser on MariaDB: after running the script on the other database,
    comparing again found every object the same.
- **Disconnecting clears the screen**: the tables, the query, the data and the definition go,
  and the main area shows an animated unplugged cable, *Not connected to any database* (also
  when the page opens).

## 1.23.0 — 2026-11-07

- **Logs is a tab** of the main page (and ☰ → 📜 Logs opens it), instead of a dialog:
  - **Live**: new lines appear at the top as they come in, highlighted.
  - **Crash log**, and a **Day** to view the log of any day kept.
  - **⤓ Download all (.zip)**: every log and crash log file, at the highest compression.
- **A new log file every day**: `cactusdb.log` is today's; earlier days become
  `cactusdb-YYYY-MM-DD.log` (and `cactusdb-crash-YYYY-MM-DD.log`), kept for 30 days.
- **The session in the log**: every request line and action says `session=` and the first
  8 characters of the session id (not all of it: it works like a password while it lasts).
- **Exports of saved queries get their own folder**:
  `<connection>/queries/<dd.mm.yyyy>/<query name>-<dd.mm.yyyy.hh.mm>.<ext>`, listed on the
  *Exported files* page as **Saved queries**.
- Fixed: **Download selected** failed for every file when one of them had been deleted or
  replaced meanwhile ("compress 179 file(s) FAILED: … no such file or directory"). The
  missing ones are now left out, and named.
- Checked on the server in the browser with 1.22.0: saving and deleting a query no longer
  blocks the connection.

## 1.22.0 — 2026-11-07

- Fixed: **deleting a saved query (or connection) on a server blocked cactusdb for about five
  minutes**, like the 403s fixed in 1.21.0: the same firewall or intrusion-prevention device
  blocks a computer from the port after an HTTP DELETE request. Tested on the server in the
  browser: connecting, opening and recounting tables, the Definition tab, saved queries
  (save, edit, run), exports, Export all, previews and downloads leave it alone; only the
  DELETE set it off. The web UI now deletes with POST (`…/delete`); the DELETE routes remain
  for scripts.
- **Export saved queries**: **⤓ Export…** on each saved query, and **⤓ Export all queries…**
  above the list, export the queries' data (all rows) as Excel, CSV, JSON, XML or all four.
- **ALL** next to the four *Export all tables* icons exports every table in all four formats.
- **☰ → 📜 Logs** shows cactusdb's log (the log file, or the lines since it started), with a
  filter, *Problems only*, and the crash log.
- Fixed: an Excel export failed when the query or table name started or ended with `'`, e.g. a
  saved query called `code <> ''` (Excel forbids it in a sheet name). `History` is renamed too.

## 1.21.0 — 2026-11-07

- Fixed: **on a server, tables, connecting and saving queries stopped working for about five
  minutes at a time**, while the same database always worked from a cactusdb on your own
  computer. Something between the browser and the server (a firewall or intrusion-prevention
  device) blocks a computer from cactusdb's port for about five minutes as soon as cactusdb
  answers **403**. It answers 403 to "this connection is protected: enter its password",
  which the page receives every time it opens a protected connection, and to a wrong
  connection password. During the block nothing on that port answers, not even open
  connections. Now the web UI receives every error as 200 with the real status in the
  `X-Cactusdb-Status` header, so those devices see nothing to block. Scripts and other
  clients still get the usual status codes.
- **Full table names**: long names wrap at `_` instead of being cut off.
- **The open table is highlighted** in the list and next to the tabs, like the active tab.
- **What's new** in *Check for update* is a button, readable in dark mode. The commands no
  longer show scroll bars: the copy button copies the whole command.

## 1.20.0 — 2026-11-06

- Fixed: **on a server, tables sometimes didn't load and cactusdb later stopped answering**,
  with an empty crash log. cactusdb wrote every request to the terminal it ran in and waited
  for it. When that terminal stopped reading (an SSH session over a dropped VPN, a paused
  terminal), every request hung after about a hundred lines, while the page itself still
  loaded. When SSH closed the dead session, its hangup signal stopped cactusdb without a
  trace. Now the terminal can never hold up a request (lines it doesn't take are dropped and
  counted), cactusdb keeps running when its terminal closes (SIGHUP, SIGPIPE), and a start on
  a port that's still in use names the cactusdb running there.
  The *No answer from cactusdb* message now mentions this case and `--runas background`.
- **Rows and MB of every table as soon as you connect**, from the database's statistics
  (instant, marked `≈`), in a Rows and an MB column. **⟳ # MB** counts every table exactly.
- **Sort the table list** by clicking **Name**, **Rows** or **MB** above it: a second click
  reverses the order (▲ ascending, ▼ descending).

## 1.19.0 — 2026-11-06

- **New logo**: a cactus drawn as two circuit traces.
- **Check for update**: a new **☰ → ⬆ Check for update** shows whether a newer cactusdb
  is out, and the command that downloads it for every system: the setup scripts for
  Linux/macOS and Windows, and one command per file (Linux, macOS and Windows, each for
  Intel/AMD and ARM). Each has a small copy button. The system cactusdb runs on is marked
  **this server**. The page checks when it opens and puts a dot on the menu button when an
  update is available. The log says so at startup too. The check reads the new
  [`VERSION`](VERSION) file on GitHub, from the browser when the server has no internet.
- Fixed: copying (a definition, a command) failed on a cactusdb opened as
  `http://<server>`, because the browser only allows the clipboard API on https and localhost.
- Messages now show above an open dialog instead of behind it.

## 1.18.0 — 2026-11-06

- **The password prompt says what you're about to do**:
  - **Connect to “…”** with a **Connect** button
  - **Edit “…”** with **Unlock to edit**
  - **Delete “…”?** with **Delete**

  A remembered password that no longer works is forgotten quietly, instead of opening
  the prompt with *wrong connection password* before you typed anything.
- **Disconnect when idle**: after 15 minutes without a click, key or scroll, cactusdb
  disconnects and says why. It never disconnects while a query, export, *Export all* or
  backup is running, and the time starts again when that finishes. The time is set in the
  new **☰ → ⚙ Settings** (0 = never) and saved on the server. The server closes sessions
  of closed pages after the same time (it used to wait 30 minutes).
- **Setup scripts**: `setup.sh` (Linux, macOS) and `setup.ps1` (Windows) download only
  the build for this computer, save it as `cactusdb` / `cactusdb.exe`, check that it runs,
  and update it when run again:
  `curl -fsSL https://raw.githubusercontent.com/albievan/cactusDB/main/setup.sh | sh` or
  `irm https://raw.githubusercontent.com/albievan/cactusDB/main/setup.ps1 | iex`.

## 1.17.0 — 2026-11-06

- Fixed: **connecting to a cactusdb on a server worked only sometimes.** cactusdb kept every
  session's database connections open until it stopped. Each closed or reloaded tab left
  its connections behind, and idle connections were cut by the database, a firewall or the
  VPN, so the next request on one failed. Now:
  - database connections idle for a minute are closed
  - sessions unused for 30 minutes are closed (not while a query or export runs)
  - closing the page ends its session
  - a connect that got no answer is retried once
  - the log records how long each connect took
- **New menu** at the bottom of the sidebar: **Connections**, **New connection**, **Folder
  setup** and **Drivers**. Folder setup and Drivers open in their own dialog.
- The connection form is no longer always shown: **New connection** or **Edit** opens it,
  and its new **Connect** button connects without saving.
- Each saved connection has a compact **▶** button to connect.
- While you're not connected, the sidebar lists your saved connections with **▶**, a **New
  connection** button and the export folder.

## 1.16.0 — 2026-11-05

- **Rows and size of every table**: the new **# MB** button next to the Tables tab shows
  each table's row count and size in MB (data and indexes), with totals for the
  database. Rows are counted exactly, four tables at a time, for up to 30 seconds;
  slower tables show the database's estimate, marked ≈. Works on MySQL, MariaDB,
  PostgreSQL, SQL Server and SQLite; Firebird shows row counts only.

## 1.15.0 — 2026-10-30

- Fixed: **cactusdb on a server could stop in the middle of "Export all"**, leaving the page
  with *No answer from cactusdb … doesn't answer now either*. Excel exports built each
  finished workbook in memory, and four tables export at once, so large tables could
  make the server run out of memory and the system killed cactusdb. Workbooks are now
  written straight to the file: four 400,000-row tables need 210 MB instead of 560 MB.
- An internal error while exporting a table now fails only that table, with a message,
  instead of stopping cactusdb.
- **Why did cactusdb stop?** When cactusdb starts after a run that didn't shut down, it
  says why, in its terminal and once on the page: *crashed* with the error (kept in
  `cactusdb-crash.log` next to the connection file), or *stopped without shutting down*,
  that is killed, most often for lack of memory, with how to check that.

## 1.14.1 — 2026-10-29

- **"Failed to fetch" explained.** When the browser gets no answer, the page now says what
  it was doing (e.g. running the query), the request, and after how long. It then checks
  cactusdb and tells you which case applies:
  - cactusdb is running, so the connection was cut on the way (firewall, proxy, VPN)
  - cactusdb doesn't answer (stopped, crashed or the network is down)
  - cactusdb was restarted meanwhile

  Requests that only read (running a query, loading a table) are retried once first.
- Fixed: using a cactusdb on another computer (e.g. a server) could fail with "Failed to
  fetch" while the same database worked from a local cactusdb. A firewall or VPN in
  between silently dropped idle connections, and the browser sent the next request into
  the dead one. cactusdb now closes idle connections after 20 seconds, before such
  devices do.
- An internal error inside cactusdb is now returned as an error message (details in the
  log) instead of cutting the connection.

## 1.14.0 — 2026-10-29

- **A password for every saved connection.** Saving a connection now asks for its own
  connection password. It's needed to connect, edit or delete the connection and to
  use its exported files. The connection's username, password and saved queries are
  encrypted with a key derived from it (Argon2id, AES-256-GCM), so `cactus.key` alone
  no longer reveals them. Wrong guesses are slowed down. Connections saved before stay
  usable and can be given a password with **Edit**. The command line never replaces a
  protected connection.
- **Saved queries per connection**: **💾 Save query** next to Run, with a name and a
  description; a **Saved queries** tab to run, edit or delete them. Only read-only
  queries can be saved. Stored in `cactus.conn.db`, encrypted with the connection's
  password.
- **Superuser password**: opens every connection, its saved queries and its exported
  files (🛡 in *Saved connections*). It is never stored or built into cactusdb: set it
  with `cactusdb --superuser set`, or once with `CACTUSDB_SUPERUSER_PASSWORD`
  (`.env.example`, deploy scripts).
- **Exported files of a protected connection** can be previewed, downloaded or deleted
  only with its connection password or the superuser password (🔒 on the *Exported
  files* page). Previews are requested by POST, so passwords never appear in URLs.
- **Sidebar:** only the database's objects stay in the sidebar. The connection form,
  saved connections, export folder and drivers are in the **☰ Connections** pop-up menu
  at the bottom.
- Fixed: two password prompts in a row from piped input (`--superuser set`) read only
  the first line.

## 1.13.2 — 2026-10-28

- Fixed: cactusdb in a container (`deploy.sh`, `deploy-container.sh`) couldn't reach
  databases on the computer running Docker. `localhost` there is the container itself,
  and `host.docker.internal` didn't resolve on every Docker setup. The scripts now
  start the container with `--add-host host.docker.internal:host-gateway`, so
  `host.docker.internal` always reaches the computer running Docker and the database
  containers that publish ports on it.
- A failed connection to `localhost` from inside a container now explains this and
  suggests `host.docker.internal` (Connect, Test connection and command line).
- Tested connections to all six databases (SQLite, MySQL, MariaDB, PostgreSQL, Firebird,
  SQL Server) both natively and from a container.

## 1.13.1 — 2026-10-28

- Fixed: in 1.13.0 the **DDL** button under *Objects* showed a long piece of garbled
  text instead of "DDL" (a slip while changing its tooltip). It shows "DDL" again.
- The version (e.g. `v1.13.0`) is shown under the cactusdb name in the top bar.

## 1.13.0 — 2026-10-28

- **Exports are sorted by connection**: every export, DDL script and backup goes to
  `<export folder>/<connection name>/<type>/`, e.g. `export/optimo-prod/json/`. Saved or
  named connections use their name (made safe for a folder name), unnamed ones
  `<host>-<database>` or the SQLite file name, the command line `cli-<host>`. Exports made
  before stay in `export/<type>/` and are still listed and downloadable.
- *Exported files* page: a **Connection** column (sortable) and a connection filter;
  type counts follow the chosen connection; a download of one connection's files is named
  after it.
- Fixed: `deploy.sh --docker dd` stopped with "DD_SQLITE_DIR … is not a folder" when
  that folder didn't exist yet, and only after building the image. The SQLite folder is
  now created like the export and connections folders, and every folder is checked
  before the build (a path that is a file is still refused). The same for
  `deploy-container.sh --sqlite-dir`.

## 1.12.3 — 2026-10-28

- Logo: the cactus is now solid white, without the curved database-disc lines across
  the trunk.

## 1.12.2 — 2026-10-28

- Logo: removed the coral flower (red dot); the icon is now just the white cactus and
  database cylinder on the green tile.

## 1.12.1 — 2026-10-28

- **New logo**: a modern, flat app-icon tile (emerald-to-teal gradient) with a white
  cactus whose trunk doubles as a database cylinder, plus a coral flower. It reads
  well from favicon size up and in light and dark themes. Used in the header, the browser
  tab, the page placeholders and the README.

## 1.12.0 — 2026-10-28

- *Exported files* page, better filters:
  - The **Type** buttons can be combined (for example CSV and JSON); **All** resets.
  - The **Name** search finds names containing the text in any case; with several
    words, a name must contain all of them. Matches are highlighted.
  - **Esc** clears the search, and a **Clear the filters** button appears when nothing
    matches.
  - The footer shows the size of the files shown.
  - Filters and the sort order are remembered in the browser.

## 1.11.0 — 2026-10-28

- **Overall progress bar** below the Run button while exporting. For *Export all
  tables* it shows the percentage, tables done, rows exported, elapsed time and an
  estimate of the time left. Single exports, DDL exports and backups show a moving bar
  with the elapsed time. Each ends with a summary that **Clear** removes.

## 1.10.0 — 2026-10-28

- **Preview** on the *Exported files* page (👁 after each checkbox): CSV, Excel (first
  sheet) and JSON exports as a table, XML, SQL and other text as text, and backups as
  their list of files plus the start of the SQL script. At most 200 rows or 256 KB.
  The preview can also download or delete the file.
- **Delete** (🗑 after each checkbox) and **Delete selected**: always asks for
  confirmation first, listing the files and their total size. Only files on the list can
  be deleted, and only by requests from the page itself.

## 1.9.0 — 2026-10-28

- **Exported files** page (a new tab): every file in the export folder (data exports,
  DDL scripts, backups) with its creation date and uncompressed size. Sort by file name,
  type, date or size; filter by type or name; select several files. **Download selected**
  first compresses them into one `.zip` at the highest compression level, then starts the
  download and shows how much smaller it got. Prepared downloads are removed after
  15 minutes or when cactusdb stops.

## 1.8.0 — 2026-10-27

- **Docker CE** in the deployment scripts: `deploy.sh --docker ce` deploys to Docker CE
  (Docker Engine) on a Linux server, either this machine or another one over SSH
  (`CE_HOST=user@server`). The image is built on the server for its CPU, the data
  folders are created there and owned by the SSH user, and `CE_INSTALL_DOCKER=yes`
  installs Docker CE first if it's missing (get.docker.com, systemd, docker group).
  `deploy-container.sh` gets the same with `--remote user@server` and `--install-docker`.
- `deploy.sh`: in the env file a later line now wins over an earlier one (the
  environment still wins over both); `--env none` ignores any `.env`.

## 1.7.0 — 2026-10-27

- **`deploy.sh`**: deploy cactusdb as a container on Docker (`--docker dd`), k3s
  (`--docker k3s`) or Kubernetes (`--docker k8s`) with `--install`, check it with
  `--status`, and remove it with `--uninstall` (data kept; `--purge` deletes the data
  volumes after asking). All settings are in an env file (`--env .env`).
  - k3s: the image is imported into k3s's containerd, locally or over SSH.
  - Kubernetes: the image is pushed to a registry, multi-architecture with `docker buildx`.
  - PVC or hostPath storage; NodePort, LoadBalancer or ClusterIP service; non-root,
    read-only root filesystem; passphrase as a Secret.
- **`.env.example`**: every deployment setting with its default and an explanation.
- The container image has no build steps any more, so an amd64 image builds on an arm64
  Mac (and the other way round) without emulation. Time-zone data is now built into
  cactusdb, so `TZ` works in minimal containers and on Windows.

## 1.6.0 — 2026-10-27

- Exports are sorted into a subfolder of the export folder per type: `json`, `csv`,
  `xlsx` (Excel), `xml`, `sql` for DDL scripts and `backup` for backups, e.g. `--exportto ./export --exportas json`
  writes to `./export/json/`. Applies to the command line and the web UI (single exports,
  *Export all tables* and DDL). Download links include the subfolder.
- Eye button to show the password while entering a **new** connection (the *+ New*
  form and the connection panel). It's not offered when editing or opening a saved
  connection, and the field is masked again whenever the form is closed or reused.
- **Export folder** section in the web UI: change where exports are saved. The folder is
  checked (full path, created if missing, writable) and saved in `cactus.conn.db`, so the
  web UI and command-line exports use it after a restart. `--exportto` still wins.
  Files exported to the previous folder stay downloadable.
- **`deploy-container.sh`** and a `Dockerfile`: run cactusdb in a Docker/Podman container
  on every address, with the port published and the export and saved-connections
  folders stored on the host (`up`, `down`, `restart`, `logs`, `status`).
- **Database backup**: the **Backup** button (and `--backup yes` on the command line)
  writes the whole database, every object and every row, as a restorable SQL script in a
  `.zip` compressed at the maximum level, with a Download link. SQLite backups also hold
  an exact copy of the database file. Sequences and identity columns continue where they
  were after a restore. Saved in the `backup` subfolder of the export folder.
- Fixed: the Firebird DDL export left out `GENERATED … AS IDENTITY` on identity columns.

## 1.5.0 — 2026-10-27

- The web UI now listens on **every network interface** (`0.0.0.0`) by default, so it
  can be opened from other workstations at `http://<ip address>:<port>`. The startup
  banner lists this computer's addresses. Use `--host 127.0.0.1` for the old
  this-computer-only behaviour.
- README: firewall steps for remote access and an updated security note.

## 1.4.2 — 2026-10-26

- A **Clear** button removes the export status once an export is finished (single
  export, *Export all tables* and DDL export).
- Fixed: a long connection name in the top bar (e.g. a long SQLite path) made the page
  wider than the window, pushing the buttons on the right off-screen.

## 1.4.1 — 2026-10-26

- **⏻ Shutdown** button in the web UI when cactusdb runs with `--runas background`: after
  confirming, cactusdb cancels running exports, closes all database connections and
  exits cleanly. Only JSON requests are accepted, so other websites can't trigger it.
- The *Export all tables* progress list uses the full height of the window, in several
  columns on wide screens, with a ✕ to close it.

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
