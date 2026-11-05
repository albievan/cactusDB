package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Row counts and sizes of every table, for the "rows and MB" toggle of the
// object list. The size and a row estimate come from the database's
// catalog in one query; the rows are then counted exactly, a few tables at a
// time, within a time budget: a table not counted in time keeps the estimate.

const (
	tableStatsThreads = 4
	tableStatsBudget  = 30 * time.Second
)

type tableStat struct {
	Name  string `json:"name"`
	Rows  int64  `json:"rows"`  // -1 when unknown
	Exact bool   `json:"exact"` // Rows was counted, not estimated
	Bytes int64  `json:"bytes"` // data and indexes; -1 when the engine doesn't say
}

// tableSizeQuery returns name, estimated rows and bytes per table.
func tableSizeQuery(engine string) string {
	switch engine {
	case EngineMySQL, EngineMariaDB:
		return `SELECT table_name, COALESCE(table_rows, -1), COALESCE(data_length, 0) + COALESCE(index_length, 0)
			FROM information_schema.tables WHERE table_schema = DATABASE()`
	case EnginePostgres:
		return `SELECT n.nspname || '.' || c.relname, GREATEST(c.reltuples, -1)::bigint, pg_total_relation_size(c.oid)
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p')`
	case EngineMSSQL:
		return `SELECT s.name + '.' + t.name,
			COALESCE((SELECT SUM(p.rows) FROM sys.partitions p WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)), -1),
			COALESCE((SELECT SUM(a.total_pages) FROM sys.partitions p JOIN sys.allocation_units a ON a.container_id = p.partition_id
				WHERE p.object_id = t.object_id), 0) * CAST(8192 AS bigint)
			FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id`
	case EngineSQLite:
		// dbstat lists the pages of every table and index.
		return `SELECT m.tbl_name, -1, SUM(d.pgsize) FROM dbstat d JOIN sqlite_master m ON m.name = d.name GROUP BY m.tbl_name`
	}
	return "" // Firebird keeps no size per table
}

// tableStats returns the rows and size of each of tables.
func tableStats(ctx context.Context, engine string, db *sql.DB, tables []string) []tableStat {
	out := make([]tableStat, len(tables))
	idx := make(map[string]int, len(tables))
	for i, t := range tables {
		out[i] = tableStat{Name: t, Rows: -1, Bytes: -1}
		idx[t] = i
	}
	if q := tableSizeQuery(engine); q != "" {
		if rows, err := db.QueryContext(ctx, q); err == nil {
			for rows.Next() {
				var name string
				var est, size sql.NullInt64
				if rows.Scan(&name, &est, &size) != nil {
					continue
				}
				if i, ok := idx[name]; ok {
					if est.Valid && est.Int64 >= 0 {
						out[i].Rows = est.Int64
					}
					if size.Valid {
						out[i].Bytes = size.Int64
					}
				}
			}
			rows.Close()
		}
	}

	ctx, cancel := context.WithTimeout(ctx, tableStatsBudget)
	defer cancel()
	queue := make(chan int)
	var wg sync.WaitGroup
	for range min(tableStatsThreads, max(1, len(tables))) {
		wg.Go(func() {
			for i := range queue {
				if n, err := countRows(ctx, engine, db, out[i].Name); err == nil {
					out[i].Rows, out[i].Exact = n, true
				}
			}
		})
	}
	for i := range out {
		if ctx.Err() != nil {
			break
		}
		queue <- i
	}
	close(queue)
	wg.Wait()
	return out
}

func (s *server) handleTableStats(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	groups, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var tables []string
	for _, g := range groups {
		if g.Type == ObjTable {
			tables = g.Items
		}
	}
	if len(tables) == 0 {
		writeErr(w, 400, errors.New("the database has no tables"))
		return
	}
	start := time.Now()
	stats := tableStats(r.Context(), ss.engine, ss.db, tables)
	var rows, bytes int64
	exact, sized := true, false
	for _, t := range stats {
		if t.Rows > 0 {
			rows += t.Rows
		}
		if t.Bytes >= 0 {
			bytes, sized = bytes+t.Bytes, true
		}
		exact = exact && t.Exact
	}
	if !sized {
		bytes = -1
	}
	logAction(r, "[%s] table sizes of %d table(s) in %s", ss.label, len(stats), time.Since(start).Round(time.Millisecond))
	writeJSON(w, 200, map[string]any{"tables": stats, "rows": rows, "exact": exact, "bytes": bytes})
}
