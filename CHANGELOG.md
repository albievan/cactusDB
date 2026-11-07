# Changelog

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
