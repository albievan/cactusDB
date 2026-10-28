# Changelog

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
