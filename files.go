package main

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The Exported files page: a list of everything in the export folder's
// type subfolders, and a download of the selected files as one .zip,
// compressed at the highest Deflate level before the download starts.

// zipRetention is how long a prepared .zip stays downloadable.
const zipRetention = 15 * time.Minute

type exportedFile struct {
	Kind     string    `json:"kind"` // the subfolder: csv, json, xlsx, xml, sql or backup
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Folder   string    `json:"folder"` // the export folder it is in
	path     string
}

// preparedZip is a .zip built for download.
type preparedZip struct {
	path, name string
	timer      *time.Timer
}

// listExportedFiles lists the files in the type subfolders of every export
// folder of this run, newest first. A file name found in more than one
// folder is listed from the current folder.
func (s *server) listExportedFiles() []exportedFile {
	var out []exportedFile
	seen := map[string]bool{}
	kinds := make([]string, 0, len(exportSubdirs))
	for k := range exportSubdirs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, dir := range s.exportFolders() {
		for _, kind := range kinds {
			entries, err := os.ReadDir(exportSubdir(dir, kind))
			if err != nil {
				continue
			}
			for _, e := range entries {
				// Unfinished exports are hidden temporary files.
				if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") || seen[kind+"/"+e.Name()] {
					continue
				}
				fi, err := e.Info()
				if err != nil {
					continue
				}
				seen[kind+"/"+e.Name()] = true
				out = append(out, exportedFile{Kind: kind, Name: e.Name(), Size: fi.Size(), Modified: fi.ModTime(),
					Folder: dir, path: filepath.Join(exportSubdir(dir, kind), e.Name())})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func (s *server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files := s.listExportedFiles()
	if files == nil {
		files = []exportedFile{}
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
		Files []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"files"`
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
		byKey[f.Kind+"/"+f.Name] = f
	}
	var chosen []exportedFile
	var total int64
	for _, rf := range req.Files {
		f, ok := byKey[rf.Kind+"/"+rf.Name]
		if !ok {
			writeErr(w, 404, fmt.Errorf("%s/%s is not an exported file (any more)", rf.Kind, rf.Name))
			return
		}
		if !slices.ContainsFunc(chosen, func(c exportedFile) bool { return c.path == f.path }) {
			chosen = append(chosen, f)
			total += f.Size
		}
	}

	name := "cactusdb-exports_" + time.Now().Format("20060102-150405") + ".zip"
	if len(chosen) == 1 {
		name = chosen[0].Name + ".zip"
	}
	start := time.Now()
	tmp, err := os.CreateTemp("", "cactusdb-download-*.zip")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	err = writeZip(r, tmp, chosen)
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
	logAction(r, "compressed %d file(s), %s to %s, for download as %s", len(chosen), humanSize(total), humanSize(fi.Size()), name)
	writeJSON(w, 200, map[string]any{
		"name":     name,
		"files":    len(chosen),
		"size":     total,
		"zipSize":  fi.Size(),
		"seconds":  time.Since(start).Seconds(),
		"download": "/api/files/zip/" + id,
	})
}

// writeZip writes files to w as a .zip at the highest compression level,
// each under <type>/<name>, stopping if the request is cancelled.
func writeZip(r *http.Request, w io.Writer, files []exportedFile) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	for _, f := range files {
		if err := r.Context().Err(); err != nil {
			return err
		}
		in, err := os.Open(f.path)
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: f.Kind + "/" + f.Name, Method: zip.Deflate, Modified: f.Modified}
		h.SetMode(0o644)
		out, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(out, ctxReader{r, in})
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
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
