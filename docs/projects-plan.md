# Plan: Projects — link several databases and query across them

Plan for `cactusdb-new-feature-requirements.md`. Nothing is built until this is approved.
Section numbers in brackets refer to the requirements (P1, R4, D9…).

## 1. Storage: new tables in `cactus.conn.db` [P2, P3, P4, P6]

Created at startup in `migrateLocks()` the way `queries` / `rel_views` are
(`CREATE TABLE IF NOT EXISTS`, no data migration needed: existing files only gain
empty tables).

```sql
CREATE TABLE IF NOT EXISTS projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,            -- sealed with the store key (shown in the list before unlocking)
  data TEXT NOT NULL,            -- sealed with the project key: description, links, tables, settings
  pkeys TEXT NOT NULL,           -- the project key, wrapped per locked linked connection (see 6)
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS project_links (      -- plain ids only: which project uses which connection (P6)
  project_id INTEGER NOT NULL, conn_id INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS project_links_conn ON project_links(conn_id);
CREATE TABLE IF NOT EXISTS project_queries (
  id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL,
  data TEXT NOT NULL,            -- sealed: name, description, SQL, diagram layout
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS project_query_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT, query_id INTEGER NOT NULL, project_id INTEGER NOT NULL,
  version INTEGER NOT NULL, data TEXT NOT NULL, saved_at TEXT NOT NULL);
```

`data` of a project: `{description, links:[{connId, alias}], tables:[{alias, schema, source,
name}], limits:{…}}`. Credentials are never stored; links refer to `connections.id` (P4).
Versions work like saved queries (`maxQueryVersions` = 100, an unchanged save adds none).

Deleting a saved connection (P6): `handleDeleteConn` first looks in `project_links`; if
projects use it, it answers 409 with their names and the page asks *Delete anyway?*.
`Store.Delete` then leaves the projects alone; their link is shown **broken** with
*Re-point to another connection*.

## 2. API routes

| Route | Does |
|---|---|
| `GET /api/projects` | List (id, name, link count, broken links) |
| `POST /api/projects` | Create / rename / describe / duplicate (`{op}`) |
| `POST /api/projects/{id}/delete` | Delete (page asks first) |
| `POST /api/projects/{id}/open` | Unlock, connect every link, return a **project session** |
| `POST /api/projects/{id}/save` | Save links, aliases, tables, limits |
| `POST /api/project/close` | Disconnect all links, remove the staging folder |
| `POST /api/project/retry` | Reconnect one link (P5) |
| `GET /api/project/catalog?alias=` | Objects / columns of one link (reuses `listObjects`, `relColumnsQuery`) |
| `POST /api/project/parse` | SQL → tables, columns, pushdown, errors (Go parser) |
| `POST /api/project/run` | Load (staging) and run; streams progress; returns the grid |
| `POST /api/project/cancel` | Cancel loading/running |
| `POST /api/project/export` | xlsx/csv/xml/json/all from the staged result (R9a) |
| `GET/POST /api/project/queries…` | Saved project queries and versions (S1) |
| `POST /api/project/joins` | Join suggestions for two tables (D6) |

All go through `requireLogin` and the CSRF/same-origin middleware like every route.

## 3. Project session [P5, U5a]

A new `projectSession` in `server.projects map[string]*projectSession`:
`{id, projectID, box (project key), links map[alias]*session, broken map[alias]error,
staging *staging, cancel, lastUsed}`. Each link is an ordinary `*session` (same open,
read-only and idle rules), but it isn't put in `server.sessions`, so the Data tab and the
other single-database routes can't reach it. Idle cleanup closes a project session like
a session. Closing removes the links and the staging folder.

**One at a time in the page** (U5a): opening a project first runs the page's
`disconnect()` (after a new *Save / Discard / Cancel* question when a saved query or the
project has changes), and `connect()` closes an open project first, with the same
question. The server doesn't enforce this per browser (it has no notion of "this
browser's sessions"); the page does.

## 4. Staging [R4, R5, R6, R10, R11]

- **Design (R4):** per run, a private folder (`os.MkdirTemp`, 0700, in the system temp
  folder, never the export folder) with **one SQLite file per database alias**. cactusdb
  writes each file on its own connection, then opens the query connection: an in-memory
  main database with every file `ATTACH`ed **read-only** (`mode=ro`) under its alias, then
  `PRAGMA query_only=1`, and only then runs the user's query. So `fb.customer` resolves
  natively. The bundled SQLite allows **10 attached databases** (checked: `ATTACH` fails at
  the 11th), so a project has **at most 10 linked databases** (L5).
- **Caching (R10): load fresh on every run.** The staging of the *last* run is kept until
  the next run or until the project closes, so the grid, *Show n rows* and the exports use
  exactly the rows shown (R9a). No *Reload data* control needed.
- **Limits (R5),** settings with defaults: **1,000,000 rows per table**, **2 GB staging
  in all**, **10 minutes** per run. Hitting one stops the run: *"maria.orders has more
  than 1,000,000 rows (the limit in Settings → Projects)"*. Never a truncated result.
- **Progress and cancel (R6):** per table *rows loaded*; **Cancel** cancels the context
  (source reads stop via `streamRows`), then removes the staging folder.
- R11: a one-database query takes the same path.

## 5. SQL parsing [R1, R2, Q1–Q5, D1, D2, D9]

There is no SQL parser in the code yet (only a tokenizer in `relations.go` and the JS
`qaParse`), so two are needed:

- **Go (execution, authoritative):** a SELECT parser for the SQLite dialect into a small
  model: FROM / JOIN items (`alias.table [AS] t`, join type, ON), the select list,
  WHERE as an expression tree, GROUP BY, HAVING, ORDER BY, LIMIT, and *unknown* parts kept
  as text. Used for: which project tables and columns are used (R1); `*` and unqualified
  columns resolved with the cached catalog; **anything it can't resolve → load all
  columns of that table** (correct, just slower); pushdown (R2); errors that name the
  alias and table (Q5); *add it to the project* (T2).
- **JS (diagram):** `qaParse` grows into a model parser plus a generator (model → SQL,
  one clause per line) for D1/D2. SQL it can't model (subqueries, CTEs, UNION, window
  functions) makes the diagram read-only (D9); expressions it doesn't understand are
  kept verbatim.
- One shared corpus of queries (`testdata/project_queries.json`) is checked by Go unit
  tests and by a browser round-trip test in CI.

**Pushdown (R2), conservative:** only top-level `AND` conditions of WHERE, of the form
`t.column <op> constant`, on a table that isn't on the nullable side of an outer join.
The condition always stays in the SQLite query too, so the source only has to return a
**superset**. That makes these safe: `=` on any column (a byte-equal match is equal under
any collation); `<, <=, >, >=, <>, BETWEEN, IS [NOT] NULL` on **numeric** columns; the
same on **date/timestamp** columns with an ISO-8601 literal, bound as a typed value.
Not pushed: text ranges (collations order differently), LIKE, functions, OR, HAVING.
Values are bound parameters; Access, Athena, BigQuery and Informix take no parameters,
so **nothing is pushed to them**.

## 6. Sealing [Security]

A project gets its own random **project key** (AES-256-GCM, like a `secretBox`).
- `name` is sealed with the store key (so the list shows names without passwords).
- `data` and its queries are sealed with the project key, then the store key.
- The project key is stored **wrapped once per locked linked connection** (with that
  connection's key). With no locked links it is wrapped with the store key only.
- **Opening needs every locked linked connection's password, or the superuser
  password** (which unlocks each through escrow, as today). Rationale: a project holds
  queries and table names of all its databases, so someone with one connection's
  password must not read the others'. It is also what P8 needs (see 7).
- Changing a connection's password re-wraps the project keys of its projects (the
  existing `resealQueries` path). Linking/unlinking a locked connection adds/removes its
  wrap.
- Cost: one Argon2 derivation per locked link when opening (≈0.5 s each, at most 2 at
  once, as now).

## 7. Points in the requirements that conflict with the code or are open

1. **P5 vs sealing:** *"if a connection is cancelled, the project still opens"* — with
   the sealing above, cancelling a **locked** connection's password means the project
   can't be decrypted. Proposal: a *cancelled* password doesn't open the project; a
   database that **can't be reached** (down, wrong host) still lets the project open with
   that link *not connected* + **Retry**, because the password is checked locally first.
2. **P8:** there is no per-user access in `--remotelogin` (every signed-in user is equal;
   access is the connection password). So P8 = "every locked link's password, or the
   superuser" (6).
3. **R9a folder:** `<export>/<project name>/xlsx/…` can collide with a connection's
   export folder of the same name. Proposal: `<export>/projects/<project name>/xlsx|csv|xml|json/`,
   listed under *Exported files*, and protected like a locked connection's folder when
   the project has a locked link.
4. **Q3:** `checkReadOnly` (SQLite) also lets `VALUES`, `SHOW`, `DESCRIBE`, `EXPLAIN`
   through. Projects add a stricter check: one statement, starting `SELECT` or `WITH`,
   and refused words/functions `ATTACH, DETACH, PRAGMA, VACUUM, load_extension,
   readfile, writefile, edit, fts3_tokenizer, sqlite_compileoption_*`. Third layer:
   `query_only` + read-only attached files.
5. **R7 decimals:** SQLite's NUMERIC affinity can turn decimal text into REAL (rounding).
   Proposal: integers → INTEGER; decimals with ≤ 15 significant digits → REAL (exact for
   display and comparisons at that precision); wider decimals → TEXT with no affinity
   (exact; compared as text) and the result says which columns. Dates/timestamps → ISO
   text; booleans → 0/1; BLOB; NULL kept. Reuses `classifyType` / `convertValue`.
6. **R8:** text columns from MySQL/MariaDB/SQL Server/Access are staged with
   `COLLATE NOCASE` (their default is case-insensitive); others binary. Firebird `CHAR`
   trimmed on load. Names matched case-insensitively (Firebird/Oracle upper-case).
7. **T4 for Athena/BigQuery:** their columns come from `INFORMATION_SCHEMA` (a small
   billed query on BigQuery); every run reads data and is billed. The UI says so.
8. **Engines (decision 3):** the list omits **pgvector, Azure Database for PostgreSQL and
   HorizonDB**, which cactusdb treats as PostgreSQL. Proposal: allow them (they speak
   PostgreSQL). Couchbase and Cosmos DB stay out (NoSQL).
9. **L2:** the same connection linked twice: refused (*"already linked as `fb`"*).
10. **U3:** the Data tab's editor/grid/export use global ids; they get parameterised the
    way `QD_MAIN`/`QD_DLG` already are, so the Projects pane reuses the code while the
    Data tab keeps working unchanged.
11. **R9a export progress:** a single export has no progress or cancel today; project
    exports get both (they read the local staging database, so they're fast anyway).

## 8. UI [U1–U6]

Menu item **Projects** (`data-menu="projects"`, `bi-diagram-3-fill`) → tab and pane in
`<main>`. Pane: project bar (name, linked databases with state dots, *Read-only* badge,
Save, Run, Cancel) on top; SQL editor and diagram side by side (resizable); the
filter/sort/group grid under the diagram; results and export buttons at the bottom. The
sidebar lists the linked databases and their project tables while a project is open
(drag onto the diagram). The tab title shows the project name and `●` for unsaved
changes.

## 9. Phases (feature branch `feature/projects`, each tested and committed)

1. Storage, sealing, API, UI to create/open/close, link databases, select tables.
2. Staging, type mapping, limits, progress, cancel, grid, exports, read-only layers.
3. Diagram of cross-database queries (D1) and join editing (D2–D6).
4. Full builder (D7–D13) and saved project queries (S1).
5. README, CHANGELOG, VERSION; merge to `main`, build, release.

Tests: unit tests for alias validation, resolution, pushdown, type mapping per engine,
SQL ↔ model round trips, store migration, refused statements; integration tests
(`CACTUSDB_IT=1`) for a real cross-database join and the read-only guarantee; browser
tests for the acceptance list in section 8 of the requirements.

## 10. Size

This is a large feature (roughly the size of Compare plus Migrate together). Each phase
ends working and committed, so it can be reviewed or stopped between phases.
