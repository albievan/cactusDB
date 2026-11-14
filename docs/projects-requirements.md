# Feature: Projects — link several databases and query across them

Repository: github.com/albievan/cactusDB (Go, single binary, frontend in `web/index.html`).

## 1. Goal

Add **projects** to cactusdb. A project links several saved connections of different
database types (e.g. Firebird + MariaDB + SQL Server), holds a chosen set of their tables,
and lets me write and run **one SELECT that joins tables from different databases**. The
query diagram becomes a two-way visual query designer: editing the diagram rewrites the
SQL, and editing the SQL redraws the diagram.

Today a session is one connection to one database, saved queries belong to one saved
connection, and the query diagram is display-only (`qdDraw` / `qdSoon` in
`web/index.html`). This feature extends all three.

## 2. Terms

| Term | Meaning |
|------|---------|
| Project | A named, saved set of linked databases, selected tables, queries and diagram layouts. |
| Linked database | One saved cactusdb connection added to a project, with a **database alias**. |
| Database alias | Short name, unique in the project (e.g. `fb`, `maria`, `ms`), used in SQL to say which database a table is in. |
| Project table | A table or view of a linked database that I selected for the project. |
| Cross-database query | A SELECT that uses project tables from one or more linked databases. |

## 3. Decisions already made (do not reopen)

1. **Execution: temporary SQLite.** cactusdb reads the rows it needs from each source
   database, loads them into a throwaway SQLite database, and runs the query there.
   No hand-written join engine.
2. **Aliases are defined in the project**, not in the query text. There is no `JOINDB`
   statement. The query is plain SQL that names tables as `<db alias>.<table>`.
3. **Linkable database types in this version: every SQL-speaking type cactusdb supports**
   (SQLite, MySQL, MariaDB, PostgreSQL and its flavours, Firebird, SQL Server, Azure SQL,
   Oracle, Informix, Access, CockroachDB, Redshift, Athena, BigQuery). NoSQL and vector
   types (MongoDB, Cosmos DB, Couchbase, Cassandra, Redis, Neo4j, Weaviate) cannot be
   added to a project yet; say so in the UI when I try.
4. **The diagram is a full visual query builder**: tables, joins, output columns, filters,
   sorting, grouping and aggregates.

## 4. Requirements

### 4.1 Projects

- P1. Create, open, rename, duplicate, close and delete a project. Deleting asks first.
- P2. Projects are stored in cactusdb's own database (`cactus.conn.db`), in new tables
  created/migrated at startup the same way `queries`, `query_versions` and `rel_views` are
  (`lock.go`). Existing `cactus.conn.db` files must upgrade without losing data.
- P3. A project stores: its name and description; its linked databases (saved connection
  id + alias); its project tables; its saved queries (with versions, as saved queries
  have today); and for each query the diagram layout (table positions, sizes, collapsed
  state).
- P4. A project never stores credentials. It refers to saved connections by id.
- P5. Opening a project connects to every linked database. A connection protected by a
  connection password asks for it (or is unlocked by the superuser password), following
  the existing rules. If one connection fails or is cancelled, the project still opens;
  that database is shown as *not connected* with a **Retry** button, and queries that
  need it say which database is missing.
- P6. A saved connection that a project uses cannot be deleted silently: warn and list
  the projects that use it. A project whose connection was deleted opens with that link
  marked broken and lets me re-point it at another saved connection.
- P7. Projects have their own menu item and work in the main area: see 4.1a.
- P8. With `--remotelogin`, projects follow the same access rules as the saved
  connections and saved queries they contain. A user who may not use a connection may not
  use it through a project either.

### 4.1a Where the feature lives in the UI

- U1. **Its own menu item.** Add a **Projects** button to the pop-up menu (`#navMenu`,
  a new `data-menu="projects"` entry with an icon and a `title`, like *Compare* and
  *Migrate*). It is always available, connected or not, and is not folded into
  *Connections* or *Saved queries*.
- U2. **Everything happens in the main area.** The menu item opens a **Projects** tab
  and pane inside `<main>` (the main div), the same way *Compare* and *Migrate* have
  their tab and pane there. The project list, linking databases, selecting tables, the
  query editor, the visual query designer (diagram + filter/sort/group grid) and the
  result grid are all built in this pane. Queries are built and written here, not in a
  modal dialog or pop-up window. Small confirmations (delete, unsaved changes, the join
  dialog of D5) may still be dialogs.
- U3. The pane has its own query editor, diagram and grid. Reuse the code of the Data
  tab's editor (suggestions, `qdDraw`, grid, export buttons) rather than copying it,
  but the Data tab itself keeps working on the single connected database as it does
  today.
- U4. Suggested layout of the pane, to be confirmed in your plan: project bar at the top
  (project name, linked databases with their connection state, Save, Run); below it the
  SQL editor and the diagram side by side; the filter/sort/group grid under the
  diagram; results at the bottom. Panels are resizable, and the layout uses the full
  width and height of the main area.
- U5. While a project is open, the left sidebar lists the project's linked databases
  and their project tables (grouped by database alias), for dragging onto the diagram
  (D3).
- U5a. **Disconnect from every database first.** Opening a project (or choosing
  *Projects* → *New project*) first disconnects every database connection that is open
  in this browser session, exactly as **Disconnect** does (objects, query, data and
  definition cleared from the screen). If there are unsaved changes (an edited saved
  query, an unsaved project), ask *Save / Discard / Cancel* before disconnecting. Only
  then does the project connect to its linked databases (P5). The reverse holds too:
  connecting to a single database while a project is open closes the project first
  (with the same unsaved-changes question) and disconnects all its linked databases. A
  single-database connection and a project are never open at the same time; closing a
  project disconnects all its linked databases and removes its staging database.
- U6. The tab title shows the open project's name, and an unsaved-changes marker when
  the project or the query has changes.

### 4.2 Linking databases

- L1. Add a saved connection to the project and give it an alias. Default alias: a short
  form of the connection name. Aliases are unique per project, case-insensitive, and
  valid unquoted SQL identifiers (letters, digits, underscore; not starting with a
  digit; not an SQLite keyword).
- L2. The same saved connection may be linked twice under different aliases only if that
  is simple to support; otherwise refuse with a clear message.
- L3. Remove a linked database. If saved queries use it, warn and list them first.
- L4. Renaming an alias offers to update the project's saved queries.
- L5. Minimum 1 linked database (a project with one database is valid). Propose and
  document a sensible maximum.

### 4.3 Selecting tables

- T1. For each linked database, show its tables and views (reuse the existing object
  list and catalog code) with search, select all / none, and row counts where cheap.
- T2. Only project tables are offered in suggestions, the diagram's *add table* list and
  the sidebar. A query naming a table that is not a project table gets a clear error
  with a one-click *add it to the project*.
- T3. Source tables that live in a schema (`dbo.customers`, `public.orders`) keep that
  schema in the project's record of the table. Each project table has a **project
  name**, unique within its database alias, defaulting to the table name (and to
  `schema_table` when two schemas hold the same table name).
- T4. Column names, types and keys are read from the catalog only and cached per open
  project, with a **Refresh** action.

### 4.4 Query syntax

- Q1. Tables are written `<db alias>.<project table name> [<table alias>]`; columns as
  `<table alias>.<column>` or `<db alias>.<table>.<column>`.

  ```sql
  SELECT c.name, o.order_date, i.amount
  FROM   fb.customer c
  JOIN   maria.orders o   ON o.customer_id = c.id
  LEFT JOIN ms.invoices i ON i.order_id = o.id
  WHERE  o.order_date >= '2026-01-01'
  ORDER BY c.name
  ```
- Q2. The SQL dialect of a cross-database query is **SQLite's**. Say this in the editor
  (a short note with a link to the README section), because functions and date handling
  differ from the source databases.
- Q3. Only read queries are accepted: run the existing read-only check (`checkReadOnly`,
  `readonly.go`) with SQLite rules on the query, so a single `SELECT` or `WITH` only.
- Q4. Suggestions work as they do today, extended with: database aliases after `FROM` /
  `JOIN`; project tables after `<db alias>.`; columns after a table alias.
- Q5. Errors name the database alias and table they concern, and distinguish *the query
  is wrong* from *a source database failed*.

### 4.5 Running a cross-database query

- R1. Work out which project tables and which of their columns the query uses. Load only
  those columns.
- R2. Push simple filters down to the source where it is provably safe (a WHERE
  condition on one table comparing a column with a constant, not under an OR with other
  tables, not on the nullable side of an outer join). When unsure, don't push down:
  correctness before speed.
- R3. Source databases are read through the existing read-only paths (read-only
  sessions / rolled-back transactions). Nothing is ever written to a source database.
- R4. Staging database: a temporary SQLite database, private to the run (or to the open
  project), deleted afterwards and on shutdown; never inside the export folder. Suggested
  design, to be confirmed in your plan: one in-memory or temp-file database attached per
  database alias, so `fb.customer` resolves natively in SQLite. Check SQLite's
  attached-database limit in the bundled driver (`modernc.org/sqlite`) before relying
  on it; if it is too low, fall back to one database with name-mangled tables and
  rewrite the query.
- R5. Limits, each a setting with a sensible default: maximum rows loaded per table,
  maximum total staging size, and a timeout. Hitting a limit **stops the query with a
  message** naming the table and the limit. Never return a result built from silently
  truncated tables.
- R6. Show progress while loading (per table: rows loaded) and allow **Cancel**, which
  stops the source reads and removes the staging database.
- R7. Type mapping from each source type to SQLite: preserve integers, decimals (no
  silent loss of precision: keep as text or exact numeric where SQLite REAL would
  round), dates/timestamps (ISO 8601 text), booleans, text, NULLs and BLOBs. Reuse the
  mapping the migration code already has where it fits (`migrate*.go`).
- R8. Document the behaviour differences that staging causes, and handle the common
  ones: text comparison is case-sensitive in SQLite but not in MariaDB / SQL Server
  defaults; Firebird `CHAR` columns are space-padded (trim on load); Firebird and Oracle
  fold unquoted names to upper case (match project table and column names
  case-insensitively).
- R9. Results appear in the existing grid with sorting and *Show n rows*.
- R9a. **Export after a query has executed.** Once a project query has run, offer export
  buttons for **Excel**, **CSV**, **XML**, **JSON** and **All formats**. *All formats*
  writes the four files in one action. Buttons are disabled until a query has executed
  successfully, and again while the query text differs from what was last executed
  (until it is run again). An export always contains **all rows** of the query, not just
  the rows on screen, and is produced from the same loaded data as the result shown (no
  second read of the source databases that could give different rows). Files go to
  `<export folder>/<project name>/xlsx|csv|xml|json/`, named after the saved query (or
  a timestamp for an unsaved one), with the existing behaviour kept: the bar showing the
  full path with a **Download** link (one line per file for *All formats*), the CSV
  formula-injection guard, private file permissions, progress and cancel for large
  exports, and listing under *Exported files*.
- R10. Reuse loaded tables across runs while the project is open only if there is an
  obvious **Reload data** control and the result shows when each table was loaded.
  Otherwise load fresh on every run. Choose one and state it in the plan.
- R11. A project query that uses tables of only one linked database still runs through
  the same path (SQLite dialect), for consistency.

### 4.6 Query diagram as a visual designer

The diagram and the SQL are two views of the same query and stay in step.

- D1. **SQL → diagram.** Typing in the editor redraws the diagram (debounced, as now).
  Each table box shows its database alias, and boxes are coloured or grouped per linked
  database with a legend.
- D2. **Diagram → SQL.** Every change on the diagram rewrites the SQL text immediately.
  Generated SQL is consistently formatted (one clause per line, as in Q1).
- D3. **Add a table**: drag it from the sidebar onto the diagram, or use *Add table*
  (database alias → table). It gets a unique table alias. **Remove a table** removes its
  joins and its columns from the query.
- D4. **Join by dragging**: drag from a column of one table to a column of another to
  create a join on those two columns, across databases or within one. Dragging a second
  column pair between the same two tables adds an `AND` to that join.
- D5. **Join dialog** (click a join line, or *Link tables* with two tables selected):
  choose the tables, the column pairs, the operator (`=` by default; `<>`, `<`, `<=`,
  `>`, `>=`), and the join type (INNER, LEFT, RIGHT, FULL, CROSS). Delete a join here or
  with the Delete key.
- D6. **Suggest joins**: when two tables are added, offer joins from foreign keys (same
  database) and from matching column names and types (across databases, e.g.
  `customer_id` ↔ `id` of `customer`), reusing the *Linked columns* logic of DB
  Relationships (`relations.go`). Suggestions are offered, never applied without a click.
- D7. **Output columns**: a checkbox per column adds or removes it from the SELECT list;
  drag to reorder; set a column alias; `*` per table.
- D8. **Filters, sorting, grouping** in a grid under the diagram (like the Access query
  designer): per column a filter (operator + value, AND/OR groups), sort direction and
  order, group by, and an aggregate (COUNT, SUM, AVG, MIN, MAX), plus DISTINCT and
  LIMIT.
- D9. **SQL the diagram cannot represent** (subqueries, CTEs, UNION, window functions,
  expressions it doesn't understand): the diagram shows what it can, switches to
  **read-only** with a notice saying why, and never rewrites or drops that SQL. Parts it
  does not model but can keep (e.g. an expression in the SELECT list) are preserved
  verbatim when it regenerates the query.
- D10. A warning when a join compares columns of clearly incompatible types (text with
  number, date with number). The existing red outline for a table joined to nothing, and
  for an unknown table, stays.
- D11. Undo / redo (Ctrl+Z / Ctrl+Shift+Z) covers diagram edits and text edits as one
  history.
- D12. Layout: drag tables, auto-arrange, zoom, fit to window. Positions are saved with
  the query.
- D13. Keyboard access for every diagram action (an alternative to dragging), and the
  existing *Query diagram* switch still hides it.

### 4.7 Saved queries in a project

- S1. Save, rename, describe, version and delete queries in a project, with the same
  behaviour as saved queries of a connection (versions, *Closing with changes*,
  Execute showing the first 100 rows).
- S2. Existing saved queries of single connections keep working unchanged.

## 5. Constraints (existing rules of this codebase)

- **Read-only is absolute (hard requirement).** This feature must never modify, insert,
  delete or otherwise change anything in a linked database: no INSERT, UPDATE, DELETE,
  MERGE, REPLACE, TRUNCATE, no CREATE / ALTER / DROP, no stored-procedure calls, no
  temporary tables on the source, no locks, no functions with side effects. Enforce it
  in three layers, all of them required:
  1. **Query check.** The project query must pass the existing read-only check
     (`checkReadOnly`) as SQLite: one `SELECT` or `WITH … SELECT` statement only.
     Refuse multi-statement text, `ATTACH` / `DETACH`, `PRAGMA`, `VACUUM`,
     `load_extension` and any other SQLite function or statement that writes, reads
     files or changes settings. The visual designer can only generate `SELECT`.
  2. **Source reads.** The only SQL cactusdb sends to a linked database is `SELECT`
     statements it generates itself from the project's table and column names (quoted
     identifiers, bound values). Text typed by the user is never sent to a source
     database. Every source read uses the existing database-level protections
     (read-only sessions and files, read-only or always-rolled-back transactions, per
     engine, as described under *Read-only* in the README).
  3. **Staging.** The temporary SQLite database is the only thing this feature writes
     to. It holds copies, is private to cactusdb, is never one of the user's databases,
     and is deleted when the query or project closes. The user's query runs against it
     on a read-only (`query_only`) connection, so even a query that slipped past the
     check could change nothing.
  There is no setting, flag or superuser option that relaxes this. Add unit tests for
  the refused statements and integration tests (`CACTUSDB_IT=1`) that run write attempts
  through a project against real servers and prove the data is unchanged. State the
  guarantee in the README section and show a *Read-only* badge in the Projects pane.
- **One self-contained binary**: pure Go, no CGO, no new runtime dependency. The
  frontend stays plain HTML/JS in `web/index.html` with Bootstrap from `web/vendor`; no
  framework, no build step, no CDN.
- **Security**: project data is protected to the same standard as saved queries (sealed
  storage, `crypt.go` / `lock.go`). A project spans connections that each have their own
  connection password, so propose how a project's data is sealed and unlocked, and get
  it approved in the plan. Identifiers sent to source databases are quoted with each
  engine's own quoting; values are bound as parameters, never concatenated.
- Follow the existing conventions: logging of actions (never passwords), the usage log,
  `go test ./...` passing, README section for the feature, CHANGELOG entry, VERSION bump.
- Single-connection behaviour (connect, browse, query, export, migrate, compare) must
  not change.

## 6. Out of scope for this version

- NoSQL and vector databases in projects.
- Writing data, or saving a cross-database result back into any database.
- Command-line use of projects (`--project …`).
- Sharing or exporting/importing a project as a file. (List these as follow-ups.)

## 7. How to work

1. **Read first**: `main.go` (sessions, `handleQuery`), `store.go`, `lock.go`,
   `queries.go`, `readonly.go`, `relations.go`, `migrate_schema.go`, and the query
   editor and `qdDraw` code in `web/index.html`.
2. **Write a plan and stop for my approval** before coding. It must cover: the new
   tables in `cactus.conn.db`; the new API routes; how a project session relates to the
   existing `session`; the staging design (R4) and caching choice (R10); how the SQL is
   parsed for both pushdown and the diagram; the sealing design; the default limits;
   and anything in this document that is ambiguous or conflicts with the code.
3. **Build in phases**, each working, tested and committed on a feature branch:
   1. Projects: storage, API, UI to create/open, link databases, select tables.
   2. Cross-database execution: staging, type mapping, limits, progress, cancel, grid,
      export.
   3. Diagram shows cross-database queries (D1) and joins can be edited (D2–D6).
   4. Full builder (D7–D13) and saved project queries (S1).
   5. README, CHANGELOG, VERSION.
4. **Ask me** when a requirement is unclear or two requirements conflict. Do not guess.

## 8. Acceptance tests

- A project linking a Firebird, a MariaDB and a SQL Server connection can be created,
  closed, reopened after a restart, and still has its aliases, tables, queries and
  diagram layout.
- The query in Q1 returns the same rows as the same data joined by hand, including NULL
  keys and a LEFT JOIN with no match.
- Dragging `maria.orders.customer_id` onto `fb.customer.id` adds
  `JOIN … ON o.customer_id = c.id` to the SQL; deleting that `ON` in the text removes
  the line from the diagram.
- Changing a join to LEFT in the dialog changes the SQL; ticking a column adds it to the
  SELECT list; a filter in the grid appears in the WHERE.
- A query with a subquery makes the diagram read-only, and its text is unchanged after
  the diagram is clicked.
- A table over the row limit stops the query with a message naming the table.
- Opening a project while connected to a database disconnects that database first;
  connecting to a single database while a project is open closes the project first. Both
  ask about unsaved changes.
- After a project query has executed, Excel, CSV, XML, JSON and *All formats* each
  produce files holding every row of the result; *All formats* produces all four. The
  buttons are disabled before the first run and after the query text is edited.
- `INSERT`, `UPDATE`, `DELETE`, `ATTACH`, `PRAGMA` writes and multi-statement text are
  refused in a project query.
- With one of the three databases stopped, the project opens, shows which one is down,
  and queries on the other two run.
- Unit tests cover: alias validation, table/column resolution, pushdown decisions, type
  mapping per engine, SQL ↔ diagram model round-trips, store migration from an existing
  `cactus.conn.db`. Integration tests (behind `CACTUSDB_IT=1`, as `integration_test.go`)
  cover a real cross-database join and the read-only guarantee.
