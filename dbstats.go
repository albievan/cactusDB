package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The dashboard: what the connected database tells about itself (version,
// uptime, size, connections, activity, caches, its largest tables…), read
// from its statistics views. Every query only reads; one that fails (often
// for want of a privilege) is reported as not available and the rest still
// show.

// statSection is one part of the dashboard: cards (the first row, a card per
// column) or a table. A column named "… %" is drawn as a bar, "… (bytes)" as
// a size and "… (seconds)" as a duration.
type statSection struct {
	Title   string   `json:"title"`
	Kind    string   `json:"kind"` // cards or table
	Note    string   `json:"note,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Rows    [][]any  `json:"rows,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// statQuery is a section read with one query, or built by fn.
type statQuery struct {
	title, kind, note, sql string
	fn                     func(ctx context.Context, db *sql.DB) (statSection, error)
}

func cards(title, q string) statQuery      { return statQuery{title: title, kind: "cards", sql: q} }
func table(title, q string) statQuery      { return statQuery{title: title, kind: "table", sql: q} }
func (q statQuery) why(n string) statQuery { q.note = n; return q }

// dashboards are the sections of each dialect.
var dashboards = map[string][]statQuery{
	"postgres": {
		cards("Server", `SELECT current_setting('server_version') AS "Version",
			extract(epoch FROM now() - pg_postmaster_start_time())::bigint AS "Up for (seconds)",
			pg_database_size(current_database()) AS "Database size (bytes)",
			(SELECT count(*) FROM pg_stat_activity) AS "Connections",
			current_setting('max_connections')::int AS "Max connections",
			round(100.0 * (SELECT count(*) FROM pg_stat_activity) / current_setting('max_connections')::int, 1) AS "Connections used %"`),
		cards("This database", `SELECT numbackends AS "Sessions", xact_commit AS "Commits", xact_rollback AS "Rollbacks",
			round(100.0 * blks_hit / nullif(blks_hit + blks_read, 0), 2) AS "Cache hit %",
			tup_returned AS "Rows returned", tup_fetched AS "Rows fetched", tup_inserted AS "Rows inserted",
			tup_updated AS "Rows updated", tup_deleted AS "Rows deleted", deadlocks AS "Deadlocks",
			temp_bytes AS "Temporary files (bytes)", conflicts AS "Conflicts"
			FROM pg_stat_database WHERE datname = current_database()`),
		table("Connections by state", `SELECT coalesce(state, '(background)') AS "State", count(*) AS "Connections"
			FROM pg_stat_activity GROUP BY 1 ORDER BY 2 DESC`),
		table("Running queries", `SELECT pid AS "PID", usename AS "User", application_name AS "Application", client_addr::text AS "Client",
			state AS "State", extract(epoch FROM now() - query_start)::bigint AS "Running (seconds)", wait_event AS "Waiting for",
			left(query, 300) AS "Query"
			FROM pg_stat_activity WHERE state IS NOT NULL AND state <> 'idle' AND pid <> pg_backend_pid() ORDER BY query_start LIMIT 25`),
		table("Largest tables", `SELECT schemaname || '.' || relname AS "Table", n_live_tup AS "Rows (estimate)",
			pg_total_relation_size(relid) AS "Size (bytes)", seq_scan AS "Full scans", idx_scan AS "Index scans",
			n_dead_tup AS "Dead rows", last_autovacuum AS "Last autovacuum", last_autoanalyze AS "Last autoanalyze"
			FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 15`),
		table("Least used indexes", `SELECT schemaname || '.' || indexrelname AS "Index", relname AS "Table", idx_scan AS "Scans",
			pg_relation_size(indexrelid) AS "Size (bytes)"
			FROM pg_stat_user_indexes ORDER BY idx_scan, pg_relation_size(indexrelid) DESC LIMIT 15`).why("indexes rarely or never used cost writes and space"),
		table("Locks", `SELECT mode AS "Mode", count(*) AS "Locks", count(*) FILTER (WHERE NOT granted) AS "Waiting" FROM pg_locks GROUP BY mode ORDER BY 2 DESC`),
		table("Settings", `SELECT name AS "Setting", setting || coalesce(' ' || unit, '') AS "Value" FROM pg_settings
			WHERE name IN ('shared_buffers', 'work_mem', 'maintenance_work_mem', 'effective_cache_size', 'max_connections', 'wal_level',
			'max_wal_size', 'autovacuum', 'random_page_cost', 'default_transaction_read_only', 'timezone', 'server_encoding')
			ORDER BY name`),
	},
	"mysql": {
		{title: "Server", kind: "cards", fn: mysqlStatus},
		cards("This database", `SELECT DATABASE() AS "Database", COUNT(*) AS "Tables", COALESCE(SUM(table_rows), 0) AS "Rows (estimate)",
			COALESCE(SUM(data_length), 0) AS "Data (bytes)", COALESCE(SUM(index_length), 0) AS "Indexes (bytes)"
			FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'`),
		table("Running statements", `SELECT id AS "ID", user AS "User", host AS "Client", db AS "Database", command AS "Command",
			time AS "Running (seconds)", state AS "State", LEFT(info, 300) AS "Statement"
			FROM information_schema.processlist WHERE command NOT IN ('Sleep', 'Daemon') AND id <> CONNECTION_ID() ORDER BY time DESC LIMIT 25`),
		table("Connections by user", `SELECT user AS "User", COUNT(*) AS "Connections", SUM(command = 'Sleep') AS "Idle"
			FROM information_schema.processlist GROUP BY user ORDER BY 2 DESC`),
		table("Largest tables", `SELECT table_name AS "Table", engine AS "Engine", table_rows AS "Rows (estimate)",
			data_length AS "Data (bytes)", index_length AS "Indexes (bytes)", data_free AS "Free (bytes)", update_time AS "Changed"
			FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
			ORDER BY data_length + index_length DESC LIMIT 15`),
		{title: "Settings", kind: "table", fn: mysqlVariables},
	},
	"mssql": {
		cards("Server", `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(50)) AS [Version],
			CAST(SERVERPROPERTY('Edition') AS nvarchar(100)) AS [Edition], CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(20)) AS [Level],
			DB_NAME() AS [Database], CAST(DATABASEPROPERTYEX(DB_NAME(), 'Recovery') AS nvarchar(20)) AS [Recovery model],
			(SELECT CAST(SUM(CAST(size AS bigint)) * 8192 AS bigint) FROM sys.database_files) AS [Database size (bytes)]`),
		cards("Activity", `SELECT DATEDIFF(second, i.sqlserver_start_time, SYSDATETIME()) AS [Up for (seconds)], i.cpu_count AS [CPUs],
			CAST(i.physical_memory_kb AS bigint) * 1024 AS [Server memory (bytes)],
			(SELECT COUNT(*) FROM sys.dm_exec_sessions WHERE is_user_process = 1) AS [User sessions],
			(SELECT COUNT(*) FROM sys.dm_exec_requests r JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
				WHERE s.is_user_process = 1 AND r.session_id <> @@SPID) AS [Running requests],
			(SELECT COUNT(*) FROM sys.dm_exec_requests WHERE blocking_session_id <> 0) AS [Blocked requests]
			FROM sys.dm_os_sys_info i`).why("needs VIEW SERVER STATE"),
		table("Running requests", `SELECT r.session_id AS [Session], s.login_name AS [Login], s.host_name AS [Client], r.status AS [Status],
			r.command AS [Command], r.total_elapsed_time / 1000 AS [Running (seconds)], r.wait_type AS [Waiting for],
			NULLIF(r.blocking_session_id, 0) AS [Blocked by], LEFT(t.text, 300) AS [Statement]
			FROM sys.dm_exec_requests r JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
			OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
			WHERE s.is_user_process = 1 AND r.session_id <> @@SPID ORDER BY r.total_elapsed_time DESC`).why("needs VIEW SERVER STATE"),
		table("Sessions by login", `SELECT login_name AS [Login], COUNT(*) AS [Sessions], SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) AS [Running]
			FROM sys.dm_exec_sessions WHERE is_user_process = 1 GROUP BY login_name ORDER BY 2 DESC`),
		table("Largest tables", `SELECT TOP 15 s.name + '.' + t.name AS [Table],
			SUM(CASE WHEN p.index_id IN (0, 1) THEN p.rows ELSE 0 END) AS [Rows],
			CAST(SUM(a.total_pages) AS bigint) * 8192 AS [Size (bytes)], CAST(SUM(a.used_pages) AS bigint) * 8192 AS [Used (bytes)]
			FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id
			JOIN sys.partitions p ON p.object_id = t.object_id JOIN sys.allocation_units a ON a.container_id = p.partition_id
			GROUP BY s.name, t.name ORDER BY SUM(a.total_pages) DESC`),
		table("Top waits", `SELECT TOP 12 wait_type AS [Wait], waiting_tasks_count AS [Waits], wait_time_ms / 1000 AS [Waited (seconds)]
			FROM sys.dm_os_wait_stats WHERE waiting_tasks_count > 0 AND wait_type NOT LIKE 'SLEEP%' AND wait_type NOT LIKE '%QUEUE%'
			AND wait_type NOT LIKE 'BROKER%' AND wait_type NOT LIKE 'XE%' AND wait_type NOT LIKE '%IDLE%' AND wait_type NOT LIKE 'HADR%'
			AND wait_type NOT IN ('CHECKPOINT_QUEUE', 'WAITFOR', 'DIRTY_PAGE_POLL', 'SQLTRACE_INCREMENTAL_FLUSH_SLEEP', 'SOS_WORK_DISPATCHER', 'QDS_ASYNC_QUEUE')
			ORDER BY wait_time_ms DESC`).why("what the server waited for most since it started; needs VIEW SERVER STATE"),
		table("Files", `SELECT name AS [File], type_desc AS [Type], CAST(size AS bigint) * 8192 AS [Size (bytes)],
			CASE max_size WHEN -1 THEN 'unlimited' WHEN 0 THEN 'fixed' ELSE CAST(CAST(max_size AS bigint) * 8192 / 1048576 AS varchar(20)) + ' MB' END AS [Grows to],
			physical_name AS [Path] FROM sys.database_files`),
	},
	"sqlite": {
		cards("Database", `SELECT sqlite_version() AS "SQLite version",
			(SELECT page_count FROM pragma_page_count()) * (SELECT page_size FROM pragma_page_size()) AS "Size (bytes)",
			(SELECT freelist_count FROM pragma_freelist_count()) * (SELECT page_size FROM pragma_page_size()) AS "Free (bytes)",
			(SELECT page_size FROM pragma_page_size()) AS "Page size",
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%') AS "Tables",
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'index') AS "Indexes",
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'view') AS "Views",
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger') AS "Triggers"`),
		table("Largest tables and indexes", `SELECT name AS "Name", SUM(pgsize) AS "Size (bytes)", COUNT(*) AS "Pages"
			FROM dbstat GROUP BY name ORDER BY 2 DESC LIMIT 15`).why("from SQLite's dbstat table"),
		table("Settings", `SELECT 'encoding' AS "Setting", (SELECT encoding FROM pragma_encoding()) AS "Value"
			UNION ALL SELECT 'journal mode', (SELECT journal_mode FROM pragma_journal_mode())
			UNION ALL SELECT 'auto vacuum', (SELECT auto_vacuum FROM pragma_auto_vacuum())
			UNION ALL SELECT 'user version', (SELECT user_version FROM pragma_user_version())
			UNION ALL SELECT 'schema version', (SELECT schema_version FROM pragma_schema_version())`),
	},
	"firebird": {
		cards("Server", `SELECT rdb$get_context('SYSTEM', 'ENGINE_VERSION') AS "Version", MON$SQL_DIALECT AS "SQL dialect",
			MON$ODS_MAJOR || '.' || MON$ODS_MINOR AS "ODS", CAST(MON$PAGES AS BIGINT) * MON$PAGE_SIZE AS "Database size (bytes)",
			MON$PAGE_SIZE AS "Page size", MON$CREATION_DATE AS "Created",
			MON$NEXT_TRANSACTION - MON$OLDEST_TRANSACTION AS "Transactions since oldest",
			CASE MON$FORCED_WRITES WHEN 1 THEN 'on' ELSE 'off' END AS "Forced writes"
			FROM MON$DATABASE`),
		cards("I/O and memory", `SELECT i.MON$PAGE_READS AS "Page reads", i.MON$PAGE_WRITES AS "Page writes", i.MON$PAGE_FETCHES AS "Page fetches",
			CAST(100.0 * (i.MON$PAGE_FETCHES - i.MON$PAGE_READS) / NULLIF(i.MON$PAGE_FETCHES, 0) AS NUMERIC(5, 2)) AS "Cache hit %",
			m.MON$MEMORY_USED AS "Memory (bytes)", m.MON$MAX_MEMORY_USED AS "Most memory (bytes)"
			FROM MON$IO_STATS i JOIN MON$MEMORY_USAGE m ON m.MON$STAT_GROUP = 0 WHERE i.MON$STAT_GROUP = 0`),
		table("Attachments", `SELECT MON$ATTACHMENT_ID AS "ID", TRIM(MON$USER) AS "User", MON$REMOTE_ADDRESS AS "Client",
			MON$REMOTE_PROCESS AS "Program", CASE MON$STATE WHEN 1 THEN 'active' ELSE 'idle' END AS "State", MON$TIMESTAMP AS "Since"
			FROM MON$ATTACHMENTS WHERE MON$SYSTEM_FLAG = 0 ORDER BY MON$TIMESTAMP`),
		table("Running statements", `SELECT s.MON$STATEMENT_ID AS "Statement", s.MON$ATTACHMENT_ID AS "Attachment", s.MON$TIMESTAMP AS "Started",
			SUBSTRING(s.MON$SQL_TEXT FROM 1 FOR 300) AS "SQL"
			FROM MON$STATEMENTS s WHERE s.MON$STATE = 1 AND s.MON$ATTACHMENT_ID <> CURRENT_CONNECTION`),
		table("Transactions", `SELECT CASE MON$STATE WHEN 1 THEN 'active' ELSE 'idle' END AS "State", COUNT(*) AS "Transactions"
			FROM MON$TRANSACTIONS GROUP BY 1`),
	},
	"oracle": {
		cards("Instance", `SELECT instance_name AS "Instance", host_name AS "Host", version AS "Version", status AS "Status",
			ROUND((SYSDATE - startup_time) * 86400) AS "Up for (seconds)" FROM v$instance`).why("needs SELECT on v$instance"),
		table("Sessions", `SELECT status AS "Status", type AS "Type", COUNT(*) AS "Sessions" FROM v$session GROUP BY status, type ORDER BY 3 DESC`),
		cards("This schema", `SELECT USER AS "Schema", (SELECT COUNT(*) FROM user_tables) AS "Tables", (SELECT COUNT(*) FROM user_indexes) AS "Indexes",
			(SELECT COUNT(*) FROM user_views) AS "Views", (SELECT NVL(SUM(bytes), 0) FROM user_segments) AS "Size (bytes)" FROM dual`),
		table("Largest segments", `SELECT segment_name AS "Segment", segment_type AS "Type", bytes AS "Size (bytes)"
			FROM user_segments ORDER BY bytes DESC FETCH FIRST 15 ROWS ONLY`),
		table("Tablespaces", `SELECT tablespace_name AS "Tablespace", ROUND(used_percent, 1) AS "Used %" FROM dba_tablespace_usage_metrics ORDER BY 2 DESC`).why("needs SELECT on dba_tablespace_usage_metrics"),
	},
}

// MariaDB has the same statistics as MySQL.
func init() { dashboards[EngineMariaDB] = dashboards[EngineMySQL] }

// mysqlStatus builds the server cards from SHOW GLOBAL STATUS.
func mysqlStatus(ctx context.Context, db *sql.DB) (statSection, error) {
	st, err := showPairs(ctx, db, "SHOW GLOBAL STATUS")
	if err != nil {
		return statSection{}, err
	}
	vars, _ := showPairs(ctx, db, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('version', 'version_comment', 'max_connections')")
	n := func(k string) float64 { f, _ := strconv.ParseFloat(st[k], 64); return f }
	sec := statSection{Columns: []string{}, Rows: [][]any{{}}}
	add := func(label string, v any) {
		sec.Columns = append(sec.Columns, label)
		sec.Rows[0] = append(sec.Rows[0], v)
	}
	add("Version", strings.TrimSpace(vars["version"]+" "+vars["version_comment"]))
	add("Up for (seconds)", int64(n("Uptime")))
	add("Connections", int64(n("Threads_connected")))
	if mc, _ := strconv.ParseFloat(vars["max_connections"], 64); mc > 0 {
		add("Max connections", int64(mc))
		add("Connections used %", round2(100*n("Threads_connected")/mc))
	}
	add("Running", int64(n("Threads_running")))
	add("Most connections at once", int64(n("Max_used_connections")))
	if up := n("Uptime"); up > 0 {
		add("Statements per second", round2(n("Questions")/up))
	}
	add("Slow queries", int64(n("Slow_queries")))
	if req := n("Innodb_buffer_pool_read_requests"); req > 0 {
		add("Buffer pool hit %", round2(100*(1-n("Innodb_buffer_pool_reads")/req)))
	}
	add("SELECTs", int64(n("Com_select")))
	add("INSERTs", int64(n("Com_insert")))
	add("UPDATEs", int64(n("Com_update")))
	add("DELETEs", int64(n("Com_delete")))
	add("Received (bytes)", int64(n("Bytes_received")))
	add("Sent (bytes)", int64(n("Bytes_sent")))
	add("Failed connections", int64(n("Aborted_connects")))
	add("Open tables", int64(n("Open_tables")))
	return sec, nil
}

// mysqlVariables shows the settings that matter most for performance.
func mysqlVariables(ctx context.Context, db *sql.DB) (statSection, error) {
	vars, err := showPairs(ctx, db, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('innodb_buffer_pool_size', 'max_connections', "+
		"'long_query_time', 'slow_query_log', 'table_open_cache', 'tmp_table_size', 'max_heap_table_size', 'wait_timeout', "+
		"'innodb_log_file_size', 'innodb_flush_log_at_trx_commit', 'character_set_server', 'collation_server', 'sql_mode', 'time_zone')")
	if err != nil {
		return statSection{}, err
	}
	sec := statSection{Columns: []string{"Setting", "Value"}}
	for _, k := range sortedKeys(vars) {
		sec.Rows = append(sec.Rows, []any{k, vars[k]})
	}
	return sec, nil
}

// showPairs reads a SHOW … statement's name and value columns.
func showPairs(ctx context.Context, db *sql.DB, q string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k.String] = v.String
	}
	return out, rows.Err()
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// runStatQuery reads one section.
func runStatQuery(ctx context.Context, db *sql.DB, q statQuery) statSection {
	sec := statSection{Title: q.title, Kind: q.kind, Note: q.note}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if q.fn != nil {
		s, err := q.fn(ctx, db)
		if err != nil {
			sec.Error = err.Error()
			return sec
		}
		sec.Columns, sec.Rows = s.Columns, s.Rows
		return sec
	}
	rows, err := db.QueryContext(ctx, q.sql)
	if err != nil {
		sec.Error = err.Error()
		return sec
	}
	defer rows.Close()
	sec.Columns, _ = rows.Columns()
	types, _ := rows.ColumnTypes()
	for rows.Next() && len(sec.Rows) < 100 {
		vals := make([]any, len(sec.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			sec.Error = err.Error()
			return sec
		}
		for i, v := range vals {
			t := ""
			if i < len(types) {
				t = strings.ToUpper(types[i].DatabaseTypeName())
			}
			vals[i] = normalize(v, t)
		}
		sec.Rows = append(sec.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		sec.Error = err.Error()
	}
	return sec
}

// hasDashboard is whether the dashboard has anything to show for a dialect.
func hasDashboard(dialect string) bool { return len(dashboards[dialect]) > 0 }

func (s *server) handleDBStats(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	qs := dashboards[ss.engine]
	if len(qs) == 0 {
		writeErr(w, 400, fmt.Errorf("there is no dashboard for %s databases", engineName(ss.kind())))
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	out := make([]statSection, 0, len(qs))
	for _, q := range qs {
		out = append(out, runStatQuery(ctx, ss.db, q))
	}
	writeJSON(w, 200, map[string]any{"engine": ss.kind(), "label": ss.label, "sections": out,
		"checked": time.Now().Format(time.RFC3339), "ms": time.Since(start).Milliseconds()})
}
