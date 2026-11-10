package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// The Exported files page: a list of everything in the export folder's
// type subfolders, and a download of the selected files as one .zip,
// compressed at the highest Deflate level before the download starts.

// fileRef names a listed file in a request from the page.
type fileRef struct {
	Conn string `json:"conn"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (r fileRef) key() string { return r.Conn + "/" + r.Kind + "/" + r.Name }

// zipRetention is how long a prepared .zip stays downloadable.
const zipRetention = 15 * time.Minute

type exportedFile struct {
	Conn     string    `json:"conn"` // the connection's folder; "" for exports made before files were sorted by connection
	Kind     string    `json:"kind"` // the subfolder: csv, json, xlsx, xml, sql, backup or queries
	Name     string    `json:"name"` // for queries: <day>/<file>
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Folder   string    `json:"folder"` // the export folder it is in
	// Protected: the files of a connection with its own password need that
	// password (or the superuser password) to be previewed, downloaded or deleted.
	Protected bool `json:"protected,omitempty"`
	path      string
}

// protectedFolders maps the export folder of every connection that has its
// own password to that connection's id.
func (s *server) protectedFolders() map[string]int64 {
	out := map[string]int64{}
	list, err := s.store.List()
	if err != nil {
		return out
	}
	for _, c := range list {
		if c.Lock != "" {
			out[connFolderName(c.Name)] = c.ID
		}
	}
	return out
}

// fileUnlock is how a request from the page opens protected files: the
// connection password per connection folder, or the superuser password.
type fileUnlock struct {
	Unlock        map[string]string `json:"unlock"`
	SuperPassword string            `json:"superPassword"`
}

// unlockFolders checks the passwords for every protected connection folder
// among folders. On failure it has written a 403 naming the folder (so the
// page can ask for that connection's password) and returns false.
func (s *server) unlockFolders(w http.ResponseWriter, r *http.Request, folders []string, u fileUnlock) bool {
	prot := s.protectedFolders()
	done := map[string]bool{}
	for _, f := range folders {
		id, ok := prot[f]
		if !ok || done[f] {
			continue
		}
		done[f] = true
		rec := &statusCapture{header: http.Header{}}
		if _, _, ok := s.unlockConnAs(rec, r, id, u.Unlock[f], u.SuperPassword); !ok {
			msg := "this connection's files are protected: enter its connection password"
			if rec.code == 429 || (u.Unlock[f] != "" || u.SuperPassword != "") {
				msg = strings.TrimSpace(rec.errText())
			}
			writeJSON(w, map[bool]int{true: 429, false: 403}[rec.code == 429], map[string]any{"error": msg, "locked": true, "conn": f})
			return false
		}
	}
	return true
}

// statusCapture records a handler's error response instead of sending it.
type statusCapture struct {
	header http.Header
	code   int
	body   []byte
}

func (c *statusCapture) Header() http.Header  { return c.header }
func (c *statusCapture) WriteHeader(code int) { c.code = code }
func (c *statusCapture) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return len(b), nil
}
func (c *statusCapture) errText() string {
	var e struct{ Error string }
	json.Unmarshal(c.body, &e)
	return e.Error
}

// preparedZip is a .zip built for download.
type preparedZip struct {
	path, name string
	timer      *time.Timer
}

// key identifies a listed file: connection/type/name.
func (f exportedFile) key() string { return f.Conn + "/" + f.Kind + "/" + f.Name }

// format is the file's type (csv, xlsx, …): its folder, or for an export of
// a saved query, which are sorted by day, its extension.
func (f exportedFile) format() string {
	if f.Kind == queriesSubdir {
		return strings.TrimPrefix(filepath.Ext(f.Name), ".")
	}
	return f.Kind
}

// listExportedFiles lists the files in every export folder of this run,
// <export>/<connection>/<type>/ (and <export>/<type>/ from before files
// were sorted by connection), newest first. A file found in more than one
// export folder is listed from the current one.
func (s *server) listExportedFiles() []exportedFile {
	var out []exportedFile
	seen := map[string]bool{}
	kinds := make([]string, 0, len(exportSubdirs))
	for k := range exportSubdirs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, dir := range s.exportFolders() {
		conns := []string{""}
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !exportSubdirs[e.Name()] && validFileName(e.Name()) {
					conns = append(conns, e.Name())
				}
			}
		}
		for _, conn := range conns {
			for _, kind := range kinds {
				kindDir := exportSubdir(filepath.Join(dir, conn), kind)
				entries, err := os.ReadDir(kindDir)
				if err != nil {
					continue
				}
				for _, e := range entries {
					// Unfinished exports are hidden temporary files.
					if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
						continue
					}
					f := exportedFile{Conn: conn, Kind: kind, Name: e.Name(), Folder: dir, path: filepath.Join(kindDir, e.Name())}
					if seen[f.key()] {
						continue
					}
					fi, err := e.Info()
					if err != nil {
						continue
					}
					seen[f.key()] = true
					f.Size, f.Modified = fi.Size(), fi.ModTime()
					out = append(out, f)
				}
			}
			if conn != "" {
				out = append(out, listQueryExports(dir, conn, seen)...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// listQueryExports lists the exports of saved queries of a connection:
// <dir>/<conn>/queries/<day>/<file>, as kind queries and name <day>/<file>.
func listQueryExports(dir, conn string, seen map[string]bool) []exportedFile {
	var out []exportedFile
	qdir := filepath.Join(dir, conn, queriesSubdir)
	days, _ := os.ReadDir(qdir)
	for _, d := range days {
		if !d.IsDir() || !validFileName(d.Name()) {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(qdir, d.Name()))
		for _, e := range entries {
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			f := exportedFile{Conn: conn, Kind: queriesSubdir, Name: d.Name() + "/" + e.Name(), Folder: dir, path: filepath.Join(qdir, d.Name(), e.Name())}
			fi, err := e.Info()
			if seen[f.key()] || err != nil {
				continue
			}
			seen[f.key()] = true
			f.Size, f.Modified = fi.Size(), fi.ModTime()
			out = append(out, f)
		}
	}
	return out
}

func (s *server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files := s.listExportedFiles()
	if files == nil {
		files = []exportedFile{}
	}
	prot := s.protectedFolders()
	for i := range files {
		_, files[i].Protected = prot[files[i].Conn]
	}
	writeJSON(w, 200, map[string]any{"folder": s.exportFolder(), "files": files})
}

// handlePrepareZip compresses the chosen files into one .zip and returns
// where to download it. Like /api/settings it only accepts JSON, so other
// websites can't make it do the work.
func (s *server) handlePrepareZip(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		Files []fileRef `json:"files"`
		fileUnlock
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(req.Files) == 0 {
		writeErr(w, 400, errors.New("no files selected"))
		return
	}
	// Only files in the list can be chosen: no paths, no other folders.
	byKey := map[string]exportedFile{}
	for _, f := range s.listExportedFiles() {
		byKey[f.key()] = f
	}
	folders := make([]string, len(req.Files))
	for i, rf := range req.Files {
		folders[i] = rf.Conn
	}
	if !s.unlockFolders(w, r, folders, req.fileUnlock) {
		return
	}
	var chosen []exportedFile
	var total int64
	for _, rf := range req.Files {
		f, ok := byKey[rf.key()]
		if !ok {
			writeErr(w, 404, fmt.Errorf("%s is not an exported file (any more)", strings.TrimPrefix(rf.key(), "/")))
			return
		}
		if !slices.ContainsFunc(chosen, func(c exportedFile) bool { return c.path == f.path }) {
			chosen = append(chosen, f)
			total += f.Size
		}
	}

	// Named after the connection when every file comes from the same one.
	prefix := "cactusdb"
	if c := chosen[0].Conn; c != "" && !slices.ContainsFunc(chosen, func(f exportedFile) bool { return f.Conn != c }) {
		prefix = c
	}
	name := prefix + "-exports_" + time.Now().Format("20060102-150405") + ".zip"
	if len(chosen) == 1 {
		name = chosen[0].Name + ".zip"
	}
	start := time.Now()
	tmp, err := os.CreateTemp("", "cactusdb-download-*.zip")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	missing, err := writeZip(r, tmp, chosen)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		logAction(r, "compress %d file(s) FAILED: %v", len(chosen), err)
		writeErr(w, 500, fmt.Errorf("compressing failed: %w", err))
		return
	}
	fi, err := os.Stat(tmp.Name())
	if err != nil {
		os.Remove(tmp.Name())
		writeErr(w, 500, err)
		return
	}

	id := newID()
	pz := &preparedZip{path: tmp.Name(), name: name}
	pz.timer = time.AfterFunc(zipRetention, func() { s.dropZip(id) })
	s.mu.Lock()
	if s.zips == nil {
		s.zips = map[string]*preparedZip{}
	}
	s.zips[id] = pz
	s.mu.Unlock()
	if len(missing) > 0 {
		logAction(r, "compress: %d file(s) skipped, no longer there: %s", len(missing), strings.Join(missing, ", "))
	}
	logAction(r, "compressed %d file(s), %s to %s, for download as %s", len(chosen)-len(missing), humanSize(total), humanSize(fi.Size()), name)
	writeJSON(w, 200, map[string]any{
		"name":     name,
		"files":    len(chosen) - len(missing),
		"missing":  missing,
		"size":     total,
		"zipSize":  fi.Size(),
		"seconds":  time.Since(start).Seconds(),
		"download": "/api/files/zip/" + id,
	})
}

// writeZip writes files to w as a .zip at the highest compression level,
// each under <type>/<name>, stopping if the request is cancelled.
// writeZip writes files to w as a .zip. A file that is gone by the time it is
// its turn (deleted, or replaced by a newer export) is skipped and returned
// in missing, instead of failing the whole download.
func writeZip(r *http.Request, w io.Writer, files []exportedFile) (missing []string, err error) {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	for _, f := range files {
		if err := r.Context().Err(); err != nil {
			return missing, err
		}
		in, err := os.Open(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, strings.TrimPrefix(f.key(), "/"))
			continue
		}
		if err != nil {
			return missing, err
		}
		// In the .zip as <connection>/<type>/<name>.
		h := &zip.FileHeader{Name: strings.TrimPrefix(f.key(), "/"), Method: zip.Deflate, Modified: f.Modified}
		h.SetMode(0o644)
		out, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(out, ctxReader{r, in})
		}
		in.Close()
		if err != nil {
			return missing, err
		}
	}
	if len(missing) == len(files) {
		return missing, errors.New("none of the files exists any more: reload the list")
	}
	return missing, zw.Close()
}

// ctxReader stops reading when the request is cancelled, so a large file
// isn't compressed for a browser that has gone.
type ctxReader struct {
	r  *http.Request
	in io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.r.Context().Err(); err != nil {
		return 0, err
	}
	return c.in.Read(p)
}

// handleDownloadZip serves a prepared .zip. It stays available until it
// expires, so an interrupted download can be retried.
func (s *server) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	pz := s.zips[r.PathValue("id")]
	s.mu.Unlock()
	if pz == nil {
		writeErr(w, 404, errors.New("this download has expired: select the files and download them again"))
		return
	}
	logAction(r, "download %s", pz.name)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(pz.name, `"`, "_")+`"`)
	http.ServeFile(w, r, pz.path)
}

// dropZip deletes a prepared .zip.
func (s *server) dropZip(id string) {
	s.mu.Lock()
	pz := s.zips[id]
	delete(s.zips, id)
	s.mu.Unlock()
	if pz != nil {
		pz.timer.Stop()
		os.Remove(pz.path)
	}
}

// dropZips deletes every prepared .zip (when cactusdb stops).
func (s *server) dropZips() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.zips))
	for id := range s.zips {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.dropZip(id)
	}
}

// Previews show at most this many rows, or this much text.
const (
	previewRows  = 200
	previewBytes = 256 << 10
)

// findExportedFile returns the listed file ref; anything else (other
// folders, paths, temporary files) is not found.
func (s *server) findExportedFile(ref fileRef) (exportedFile, bool) {
	for _, f := range s.listExportedFiles() {
		if f.key() == ref.key() {
			return f, true
		}
	}
	return exportedFile{}, false
}

// handlePreviewFile returns the start of an exported file for the preview:
// rows for CSV and Excel, the list of files for a backup .zip (with the
// start of its SQL script), and text for everything else: JSON and XML are
// shown as written, in their own format.
func (s *server) handlePreviewFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		fileRef
		fileUnlock
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !s.unlockFolders(w, r, []string{req.Conn}, req.fileUnlock) {
		return
	}
	f, ok := s.findExportedFile(req.fileRef)
	if !ok {
		writeErr(w, 404, errors.New("file not found (deleted or moved?)"))
		return
	}
	out := map[string]any{"conn": f.Conn, "kind": f.Kind, "name": f.Name, "size": f.Size, "modified": f.Modified, "limit": previewRows}
	var err error
	switch f.format() {
	case "csv":
		err = previewCSV(f.path, out)
	case "xlsx":
		err = previewXLSX(f.path, out)
	case "zip", "backup":
		if strings.HasSuffix(f.Name, ".zip") {
			err = previewZip(f.path, out)
		} else {
			err = previewText(f.path, out)
		}
	default:
		err = previewText(f.path, out)
	}
	if err != nil {
		writeErr(w, 400, fmt.Errorf("can't preview %s: %w", f.Name, err))
		return
	}
	writeJSON(w, 200, out)
}

func previewCSV(path string, out map[string]any) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	cr := csv.NewReader(fh)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	var rows [][]string
	for len(rows) <= previewRows {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rows = append(rows, rec)
	}
	if len(rows) > 0 && len(rows[0]) > 0 { // the byte order mark cactusdb writes for Excel
		rows[0][0] = strings.TrimPrefix(rows[0][0], "\ufeff")
	}
	return tableOut(rows, out, func() bool { _, err := cr.Read(); return err == nil })
}

func previewXLSX(path string, out map[string]any) error {
	xf, err := excelize.OpenFile(path)
	if err != nil {
		return err
	}
	defer xf.Close()
	sheets := xf.GetSheetList()
	if len(sheets) == 0 {
		return errors.New("no sheets")
	}
	it, err := xf.Rows(sheets[0])
	if err != nil {
		return err
	}
	defer it.Close()
	var rows [][]string
	for len(rows) <= previewRows && it.Next() {
		row, err := it.Columns()
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	out["sheet"] = sheets[0]
	out["sheets"] = len(sheets)
	return tableOut(rows, out, it.Next)
}

// tableOut puts a header row and up to previewRows rows in out; more says
// whether the file has rows beyond them.
func tableOut(rows [][]string, out map[string]any, more func() bool) error {
	out["mode"] = "table"
	if len(rows) == 0 {
		out["columns"], out["rows"] = []string{}, [][]string{}
		return nil
	}
	body := rows[1:]
	truncated := len(body) > previewRows
	if truncated {
		body = body[:previewRows]
	} else {
		truncated = more()
	}
	out["columns"], out["rows"], out["truncated"] = rows[0], body, truncated
	return nil
}

// previewText returns the start of a text file, cut at a line end.
func previewText(path string, out map[string]any) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return textOut(fh, out)
}

func textOut(rd io.Reader, out map[string]any) error {
	buf := make([]byte, previewBytes+1)
	n, err := io.ReadFull(rd, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return err
	}
	truncated := n > previewBytes
	b := buf[:min(n, previewBytes)]
	if truncated {
		if i := bytes.LastIndexByte(b, '\n'); i > 0 {
			b = b[:i+1]
		}
	}
	if !utf8.Valid(b) {
		b = []byte(strings.ToValidUTF8(string(b), "�"))
		if bytes.Count(b, []byte("�")) > len(b)/20 {
			out["mode"] = "binary"
			return nil
		}
	}
	out["mode"], out["text"], out["truncated"] = "text", string(b), truncated
	return nil
}

// previewZip lists a .zip (a backup) and shows the start of its .sql script.
func previewZip(path string, out map[string]any) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	var entries []map[string]any
	var script *zip.File
	for _, zf := range zr.File {
		entries = append(entries, map[string]any{"name": zf.Name, "size": zf.UncompressedSize64, "compressed": zf.CompressedSize64})
		if script == nil && strings.HasSuffix(zf.Name, ".sql") {
			script = zf
		}
	}
	out["entries"] = entries
	if script != nil {
		rc, err := script.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if err := textOut(rc, out); err != nil {
			return err
		}
		out["textOf"] = script.Name
	}
	out["mode"] = "zip"
	return nil
}

// handleDeleteFiles deletes the chosen exported files. Like /api/settings it
// only accepts JSON, so other websites can't delete files.
func (s *server) handleDeleteFiles(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		Files []fileRef `json:"files"`
		fileUnlock
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(req.Files) == 0 {
		writeErr(w, 400, errors.New("no files selected"))
		return
	}
	folders := make([]string, len(req.Files))
	for i, rf := range req.Files {
		folders[i] = rf.Conn
	}
	if !s.unlockFolders(w, r, folders, req.fileUnlock) {
		return
	}
	byKey := map[string]exportedFile{}
	for _, f := range s.listExportedFiles() {
		byKey[f.key()] = f
	}
	deleted := 0
	var freed int64
	failed := []string{}
	for _, rf := range req.Files {
		f, ok := byKey[rf.key()]
		if !ok {
			continue // already gone
		}
		delete(byKey, rf.key())
		if err := os.Remove(f.path); err != nil {
			failed = append(failed, f.Name+": "+err.Error())
			logAction(r, "delete %s FAILED: %v", f.path, err)
			continue
		}
		logAction(r, "deleted %s", f.path)
		deleted++
		freed += f.Size
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted, "freed": freed, "failed": failed})
}
