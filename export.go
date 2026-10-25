package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"
)

// exportFormats maps each export format to its file extension.
var exportFormats = map[string]string{"csv": "csv", "json": "json", "xml": "xml", "xlsx": "xlsx"}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportToDir runs q and writes the full result into a new, timestamped file
// in dir. The file only appears under its final name once it is complete.
func exportToDir(ctx context.Context, dir, engine string, db *sql.DB, q, format, name string) (path string, rows int, err error) {
	ext, ok := exportFormats[format]
	if !ok {
		return "", 0, fmt.Errorf("unknown export format %q", format)
	}
	if name == "" {
		name = "query"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	base := unsafeFileChars.ReplaceAllString(name, "_") + "_" + time.Now().Format("20060102-150405")
	path = filepath.Join(dir, base+"."+ext)
	for i := 2; fileExists(path); i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s_%d.%s", base, i, ext))
	}

	tmp, err := os.CreateTemp(dir, ".cactusdb-export-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	tmp.Chmod(0o644)            // CreateTemp uses 0600; exports are ordinary files
	rows, err = export(ctx, tmp, engine, db, q, format, name)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", rows, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", rows, err
	}
	return path, rows, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// export runs q and writes every row to w in the requested format.
func export(ctx context.Context, w io.Writer, engine string, db *sql.DB, q, format, name string) (int, error) {
	n := 0
	switch format {
	case "csv":
		bw := bufio.NewWriter(w)
		bw.WriteString("\xEF\xBB\xBF") // BOM so Excel detects UTF-8
		cw := csv.NewWriter(bw)
		err := streamQuery(ctx, engine, db, q, func(cols []string) error {
			return cw.Write(cols)
		}, func(row []any) (bool, error) {
			n++
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = cellString(v)
			}
			return true, cw.Write(rec)
		})
		cw.Flush()
		if err == nil {
			err = cw.Error()
		}
		if ferr := bw.Flush(); err == nil {
			err = ferr
		}
		return n, err

	case "json":
		bw := bufio.NewWriter(w)
		var keys [][]byte
		bw.WriteString("[")
		err := streamQuery(ctx, engine, db, q, func(cols []string) error {
			for _, c := range cols {
				k, err := marshalJSON(c)
				if err != nil {
					return err
				}
				keys = append(keys, k)
			}
			return nil
		}, func(row []any) (bool, error) {
			if n > 0 {
				bw.WriteString(",")
			}
			n++
			bw.WriteString("\n  {")
			for i, v := range row {
				if i > 0 {
					bw.WriteString(", ")
				}
				bw.Write(keys[i])
				bw.WriteString(": ")
				b, err := marshalJSON(v)
				if err != nil {
					return false, err
				}
				bw.Write(b)
			}
			_, err := bw.WriteString("}")
			return true, err
		})
		bw.WriteString("\n]\n")
		if ferr := bw.Flush(); err == nil {
			err = ferr
		}
		return n, err

	case "xml":
		bw := bufio.NewWriter(w)
		var cols []string
		bw.WriteString(xml.Header)
		bw.WriteString("<table name=\"")
		xml.EscapeText(bw, []byte(name))
		bw.WriteString("\">\n")
		err := streamQuery(ctx, engine, db, q, func(c []string) error {
			cols = c
			return nil
		}, func(row []any) (bool, error) {
			n++
			bw.WriteString("  <row>\n")
			for i, v := range row {
				bw.WriteString("    <column name=\"")
				xml.EscapeText(bw, []byte(cols[i]))
				if v == nil {
					bw.WriteString("\" null=\"true\"/>\n")
					continue
				}
				bw.WriteString("\">")
				xml.EscapeText(bw, []byte(cellString(v)))
				bw.WriteString("</column>\n")
			}
			_, err := bw.WriteString("  </row>\n")
			return true, err
		})
		bw.WriteString("</table>\n")
		if ferr := bw.Flush(); err == nil {
			err = ferr
		}
		return n, err

	case "xlsx":
		return exportXLSX(ctx, w, engine, db, q, name)
	}
	return 0, fmt.Errorf("unknown export format %q", format)
}

func exportXLSX(ctx context.Context, w io.Writer, engine string, db *sql.DB, q, name string) (int, error) {
	xf := excelize.NewFile()
	defer xf.Close()
	sheet := sheetName(name)
	xf.SetSheetName("Sheet1", sheet)
	sw, err := xf.NewStreamWriter(sheet)
	if err != nil {
		return 0, err
	}
	bold, _ := xf.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	n := 0
	err = streamQuery(ctx, engine, db, q, func(cols []string) error {
		hdr := make([]any, len(cols))
		for i, c := range cols {
			hdr[i] = excelize.Cell{Value: c, StyleID: bold}
		}
		return sw.SetRow("A1", hdr)
	}, func(row []any) (bool, error) {
		if n+2 > excelize.TotalRows {
			return false, fmt.Errorf("result has more rows than Excel allows (%d)", excelize.TotalRows-1)
		}
		vals := make([]any, len(row))
		for i, v := range row {
			switch t := v.(type) {
			case json.Number: // exact decimal; Excel stores numbers as doubles anyway
				if f, err := t.Float64(); err == nil {
					v = f
				} else {
					v = t.String()
				}
			case string:
				if len(t) > excelize.TotalCellChars {
					v = t[:excelize.TotalCellChars]
				}
			}
			vals[i] = v
		}
		n++
		cell, _ := excelize.CoordinatesToCellName(1, n+1)
		return true, sw.SetRow(cell, vals)
	})
	if err != nil {
		return n, err
	}
	if err := sw.Flush(); err != nil {
		return n, err
	}
	_, err = xf.WriteTo(w)
	return n, err
}

// sheetName makes a valid Excel worksheet name (max 31 chars, no []:*?/\).
func sheetName(name string) string {
	s := regexp.MustCompile(`[\[\]:*?/\\]`).ReplaceAllString(name, "_")
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	if s == "" {
		s = "Data"
	}
	return s
}

// marshalJSON is json.Marshal without HTML escaping of <, > and &.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}
