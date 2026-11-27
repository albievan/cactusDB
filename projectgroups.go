package main

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

// A project query can group its rows for the reader: the query builder's
// "Group" without a total writes a last line "-- group rows by: ["Location"]"
// (a comment, so SQLite runs the query as it is). The grid then shows a
// group per value, to expand and collapse, and the exports keep the groups:
// Excel as an outline, JSON and XML nested, CSV sorted with those columns first.

var groupRowsLine = regexp.MustCompile(`(?m)^\s*--\s*group rows by:\s*(\[.*\])\s*$`)

// projectGroupRows returns the output columns a query groups its rows by.
func projectGroupRows(q string) []string {
	m := groupRowsLine.FindAllStringSubmatch(q, -1)
	if len(m) == 0 {
		return nil
	}
	var names []string
	if json.Unmarshal([]byte(m[len(m)-1][1]), &names) != nil {
		return nil
	}
	return names
}

// groupNode is a group of rows: the value of its column, and either the
// groups under it or, at the last level, its rows.
type groupNode struct {
	val  any
	rows [][]any
	sub  []*groupNode
	n    int
}

// groupTree groups rows by the columns idx, level by level, in the order of
// the groups' values (rows keep their order within a group).
func groupTree(rows [][]any, idx []int) []*groupNode {
	if len(idx) == 0 {
		return nil
	}
	var order []string
	byKey := map[string]*groupNode{}
	for _, r := range rows {
		k := fmt.Sprintf("%T:%v", r[idx[0]], r[idx[0]])
		g := byKey[k]
		if g == nil {
			g = &groupNode{val: r[idx[0]]}
			byKey[k] = g
			order = append(order, k)
		}
		g.rows = append(g.rows, r)
		g.n++
	}
	out := make([]*groupNode, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		if len(idx) > 1 {
			g.sub = groupTree(g.rows, idx[1:])
			g.rows = nil
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return lessValue(out[i].val, out[j].val) })
	return out
}

// lessValue orders group values: NULL first, numbers by value, then text
// (capitals alike).
func lessValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b != nil
	}
	fa, oka := numberOf(a)
	fb, okb := numberOf(b)
	switch {
	case oka && okb:
		return fa < fb
	case oka != okb:
		return oka
	}
	return strings.ToLower(cellString(a)) < strings.ToLower(cellString(b))
}

func numberOf(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

// exportGroupedToDir exports the rows of q grouped by the output columns
// groups (a column that isn't in the result is left out).
func exportGroupedToDir(ctx context.Context, dir string, db *sql.DB, q, format, name string, groups []string) (path string, rows int, err error) {
	ext, ok := exportFormats[format]
	if !ok {
		return "", 0, fmt.Errorf("unknown export format %q", format)
	}
	var cols []string
	var all [][]any
	err = streamQuery(ctx, EngineSQLite, db, q, func(c []string) error { cols = c; return nil }, func(r []any) (bool, error) {
		all = append(all, r)
		return true, nil
	})
	if err != nil {
		return "", 0, err
	}
	var idx []int
	for _, g := range groups {
		for i, c := range cols {
			if strings.EqualFold(c, g) && !slicesContains(idx, i) {
				idx = append(idx, i)
				break
			}
		}
	}
	tree := groupTree(all, idx)
	return writeExportFile(dir, name, ext, func(w io.Writer) (int, error) {
		switch format {
		case "csv":
			return len(all), groupedCSV(w, exportColNames(ctx, "csv", cols), idx, tree)
		case "json":
			return len(all), groupedJSON(w, exportColNames(ctx, "json", cols), idx, tree)
		case "xml":
			return len(all), groupedXML(w, exportColNames(ctx, "xml", cols), idx, tree, name)
		case "xlsx":
			return len(all), groupedXLSX(w, exportColNames(ctx, "xlsx", cols), idx, tree, name)
		}
		return 0, fmt.Errorf("unknown export format %q", format)
	})
}

func slicesContains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// leaves calls fn with every row of the tree, group by group.
func leaves(tree []*groupNode, fn func(r []any) error) error {
	for _, g := range tree {
		if err := leaves(g.sub, fn); err != nil {
			return err
		}
		for _, r := range g.rows {
			if err := fn(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// groupedCSV: every row, grouped, with the group columns first.
func groupedCSV(w io.Writer, cols []string, idx []int, tree []*groupNode) error {
	order := append([]int{}, idx...)
	for i := range cols {
		if !slicesContains(idx, i) {
			order = append(order, i)
		}
	}
	bw := bufio.NewWriter(w)
	bw.WriteString("\xEF\xBB\xBF")
	cw := csv.NewWriter(bw)
	rec := make([]string, len(order))
	for k, i := range order {
		rec[k] = csvText(cols[i])
	}
	cw.Write(rec)
	err := leaves(tree, func(r []any) error {
		for k, i := range order {
			if s, ok := r[i].(string); ok {
				rec[k] = csvText(s)
			} else {
				rec[k] = cellString(r[i])
			}
		}
		return cw.Write(rec)
	})
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	return err
}

// groupedJSON: [{"Location": "BIN", "rows": 12, "items": [{…}, …]}, …], an
// "items" of groups under each group for a second grouping column. The
// items hold the other columns.
func groupedJSON(w io.Writer, cols []string, idx []int, tree []*groupNode) error {
	var node func(level int, gs []*groupNode) []map[string]any
	node = func(level int, gs []*groupNode) []map[string]any {
		out := []map[string]any{}
		for _, g := range gs {
			m := map[string]any{cols[idx[level]]: g.val, "rows": g.n}
			if g.sub != nil {
				m["items"] = node(level+1, g.sub)
			} else {
				items := []map[string]any{}
				for _, r := range g.rows {
					it := map[string]any{}
					for i, c := range cols {
						if !slicesContains(idx, i) {
							it[c] = r[i]
						}
					}
					items = append(items, it)
				}
				m["items"] = items
			}
			out = append(out, m)
		}
		return out
	}
	b, err := json.MarshalIndent(node(0, tree), "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// groupedXML: <group column="Location" value="BIN" rows="12"> around the
// rows (or the groups) of each group.
func groupedXML(w io.Writer, cols []string, idx []int, tree []*groupNode, name string) error {
	bw := bufio.NewWriter(w)
	esc := func(s string) string { var b strings.Builder; xml.EscapeText(&b, []byte(s)); return b.String() }
	bw.WriteString(xml.Header)
	fmt.Fprintf(bw, "<table name=\"%s\">\n", esc(name))
	var write func(level int, gs []*groupNode)
	write = func(level int, gs []*groupNode) {
		ind := strings.Repeat("  ", level+1)
		for _, g := range gs {
			fmt.Fprintf(bw, "%s<group column=\"%s\"", ind, esc(cols[idx[level]]))
			if g.val == nil {
				bw.WriteString(` null="true"`)
			} else {
				fmt.Fprintf(bw, ` value="%s"`, esc(cellString(g.val)))
			}
			fmt.Fprintf(bw, " rows=\"%d\">\n", g.n)
			if g.sub != nil {
				write(level+1, g.sub)
			}
			for _, r := range g.rows {
				fmt.Fprintf(bw, "%s  <row>\n", ind)
				for i, c := range cols {
					if slicesContains(idx, i) {
						continue
					}
					if r[i] == nil {
						fmt.Fprintf(bw, "%s    <column name=\"%s\" null=\"true\"/>\n", ind, esc(c))
						continue
					}
					fmt.Fprintf(bw, "%s    <column name=\"%s\">%s</column>\n", ind, esc(c), esc(cellString(r[i])))
				}
				fmt.Fprintf(bw, "%s  </row>\n", ind)
			}
			fmt.Fprintf(bw, "%s</group>\n", ind)
		}
	}
	write(0, tree)
	bw.WriteString("</table>\n")
	return bw.Flush()
}

// groupedXLSX: a bold row per group ("Location: BIN (12 rows)") above its
// rows, which are an outline level deeper, so Excel's [-]/[+] collapse and
// expand them.
func groupedXLSX(w io.Writer, cols []string, idx []int, tree []*groupNode, name string) error {
	xf := excelize.NewFile()
	defer xf.Close()
	xf.SetZipWriter(func(io.Writer) excelize.ZipWriter { return zip.NewWriter(w) })
	sheet := sheetName(name)
	xf.SetSheetName("Sheet1", sheet)
	above := false // the group's row is above its rows
	if err := xf.SetSheetProps(sheet, &excelize.SheetPropsOptions{OutlineSummaryBelow: &above}); err != nil {
		return err
	}
	sw, err := xf.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	bold, _ := xf.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	grp, _ := xf.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E8F1EC"}}})
	hdr := make([]any, len(cols))
	for i, c := range cols {
		hdr[i] = excelize.Cell{Value: c, StyleID: bold}
	}
	if err := sw.SetRow("A1", hdr); err != nil {
		return err
	}
	row := 1
	put := func(vals []any, opts ...excelize.RowOpts) error {
		row++
		if row > excelize.TotalRows {
			return fmt.Errorf("result has more rows than Excel allows (%d)", excelize.TotalRows-1)
		}
		cell, _ := excelize.CoordinatesToCellName(1, row)
		return sw.SetRow(cell, vals, opts...)
	}
	var write func(level int, gs []*groupNode) error
	write = func(level int, gs []*groupNode) error {
		for _, g := range gs {
			v := "(empty)"
			if g.val != nil {
				v = cellString(g.val)
			}
			label := fmt.Sprintf("%s: %s (%d row%s)", cols[idx[level]], v, g.n, map[bool]string{true: "", false: "s"}[g.n == 1])
			if err := put([]any{excelize.Cell{Value: label, StyleID: grp}}, excelize.RowOpts{OutlineLevel: level}); err != nil {
				return err
			}
			if err := write(level+1, g.sub); err != nil {
				return err
			}
			for _, r := range g.rows {
				vals := make([]any, len(r))
				for i, x := range r {
					if n, ok := x.(json.Number); ok {
						if f, err := n.Float64(); err == nil {
							x = f
						}
					}
					vals[i] = x
				}
				if err := put(vals, excelize.RowOpts{OutlineLevel: level + 1}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := write(0, tree); err != nil {
		return err
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	_, err = xf.WriteTo(w)
	return err
}
