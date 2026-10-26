package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"
)

// ddlOrder is the order object types are written in a DDL script, so that
// what an object depends on (domains, sequences, tables) comes first.
var ddlOrder = []string{ObjDomain, ObjSequence, ObjException, ObjTable, ObjIndex, ObjView, ObjMatView,
	ObjFunction, ObjProcedure, ObjPackage, ObjTrigger, ObjEvent, ObjSynonym}

// ddlTarget is one object to write.
type ddlTarget = tableRef

// ddlTargets returns every object in groups, in ddlOrder.
func ddlTargets(groups []ObjectGroup) []ddlTarget {
	var out []ddlTarget
	for _, typ := range ddlOrder {
		for _, g := range groups {
			if g.Type == typ {
				for _, it := range g.Items {
					out = append(out, ddlTarget{typ: g.Type, name: it})
				}
			}
		}
	}
	return out
}

// routineType reports whether an object's source contains statements of its
// own, so a script must change the statement terminator around it.
func routineType(typ string) bool {
	switch typ {
	case ObjProcedure, ObjFunction, ObjPackage, ObjTrigger, ObjEvent:
		return true
	}
	return false
}

// writeDDL writes the definitions of targets to w as an SQL script and
// returns how many objects it wrote. An object whose definition can't be
// read is noted in a comment, so one bad object doesn't lose the rest.
func writeDDL(ctx context.Context, w io.Writer, engine string, db *sql.DB, label string, targets []ddlTarget, progress func(done int)) (int, error) {
	if progress == nil {
		progress = func(int) {}
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "-- DDL of %s (%s)\n-- exported by cactusdb %s on %s, %d object(s)\n",
		label, engine, version, time.Now().Format("2006-01-02 15:04:05"), len(targets))
	switch engine {
	case EngineFirebird:
		bw.WriteString("-- Routines and triggers use SET TERM ^ ; as isql requires.\n")
	case EngineMySQL, EngineMariaDB:
		bw.WriteString("-- Routines, triggers and events use DELIMITER $$ as the mysql client requires.\n")
	case EngineMSSQL:
		bw.WriteString("-- Each object is a separate batch (GO), as sqlcmd and SSMS require.\n")
	}

	if engine == EngineMySQL || engine == EngineMariaDB {
		// Tables are in name order, so a foreign key may name a table that
		// comes later; mysqldump does the same.
		bw.WriteString("\nSET FOREIGN_KEY_CHECKS = 0;\n")
	}

	// Foreign keys are added after the last table, when every table they
	// can reference exists.
	var fks []string
	flushFKs := func() {
		if len(fks) == 0 {
			return
		}
		bw.WriteString("\n-- foreign keys\n")
		for _, fk := range fks {
			if engine == EngineMSSQL {
				fk += "\nGO"
			}
			bw.WriteString(fk + "\n")
		}
		fks = nil
	}

	n := 0
	for i, t := range targets {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		if t.typ != ObjTable && t.typ != ObjDomain && t.typ != ObjSequence && t.typ != ObjException {
			flushFKs()
		}
		var src string
		var err error
		if t.typ == ObjTable {
			later := deferredFKs(engine)
			src, err = tableDefinition(ctx, engine, db, t.name, !later)
			if err == nil && later {
				var more []string
				more, err = foreignKeyDDL(ctx, engine, db, t.name)
				fks = append(fks, more...)
			}
		} else {
			src, err = objectDefinition(ctx, engine, db, t.typ, t.name)
		}
		src = strings.TrimSpace(src)
		fmt.Fprintf(bw, "\n-- %s %s\n", t.typ, t.name)
		if err != nil || src == "" {
			if err == nil {
				err = fmt.Errorf("no definition")
			}
			fmt.Fprintf(bw, "-- (definition not available: %s)\n", strings.ReplaceAll(err.Error(), "\n", " "))
			progress(i + 1)
			continue
		}
		bw.WriteString(terminate(engine, t.typ, src))
		n++
		progress(i + 1)
	}
	flushFKs()
	if engine == EngineMySQL || engine == EngineMariaDB {
		bw.WriteString("\nSET FOREIGN_KEY_CHECKS = 1;\n")
	}
	return n, bw.Flush()
}

// terminate adds the statement terminator a script needs after src.
func terminate(engine, typ, src string) string {
	// Some definitions end with a comment line (e.g. a sequence's current
	// value); the terminator goes before it.
	body, trailer := src, ""
	if i := strings.LastIndex(src, "\n-- "); i >= 0 && !strings.Contains(src[i+1:], "\n") {
		body, trailer = strings.TrimSpace(src[:i]), src[i:]
	}
	switch {
	case engine == EngineMSSQL:
		return strings.TrimSuffix(body, ";") + trailer + "\nGO\n"
	case engine == EngineFirebird && routineType(typ):
		return "SET TERM ^ ;\n" + strings.TrimSuffix(body, ";") + "^\nSET TERM ; ^" + trailer + "\n"
	case (engine == EngineMySQL || engine == EngineMariaDB) && routineType(typ):
		return "DELIMITER $$\n" + strings.TrimSuffix(body, ";") + "$$\nDELIMITER ;" + trailer + "\n"
	case strings.HasSuffix(body, ";"):
		return body + trailer + "\n"
	}
	return body + ";" + trailer + "\n"
}

// exportDDLToDir writes the DDL of targets to <name>_ddl_<date>-<time>.sql in dir.
func exportDDLToDir(ctx context.Context, dir, engine string, db *sql.DB, label, name string, targets []ddlTarget, progress func(done int)) (string, int, error) {
	return writeExportFile(dir, name+"_ddl", "sql", func(w io.Writer) (int, error) {
		return writeDDL(ctx, w, engine, db, label, targets, progress)
	})
}
