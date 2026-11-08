package main

import (
	"crypto/rc4"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

// A reader of Microsoft Access database files (.mdb: Jet 3 and 4; .accdb:
// ACE), read-only by nature: the file is opened for reading only. It reads
// the table definitions and rows, after the format as documented by the
// mdbtools and Jackcess projects.

const (
	jetPageData  = 0x01
	jetPageTDef  = 0x02
	jetMSysTDef  = 2 // MSysObjects' table definition page
	jetDelFlag   = 0x8000
	jetOverflow  = 0x4000
	jetOffsetMsk = 0x1FFF
)

// Column types.
const (
	jetBool     = 0x01
	jetByte     = 0x02
	jetInt      = 0x03
	jetLong     = 0x04
	jetMoney    = 0x05
	jetFloat    = 0x06
	jetDouble   = 0x07
	jetDateTime = 0x08
	jetBinary   = 0x09
	jetText     = 0x0A
	jetOLE      = 0x0B
	jetMemo     = 0x0C
	jetGUID     = 0x0F
	jetNumeric  = 0x10
	jetComplex  = 0x12
	jetBigInt   = 0x13
	jetExtDate  = 0x14
)

type jetFile struct {
	f        *os.File
	jet3     bool
	version  byte
	pageSize int
	pages    int64
	dbKey    uint32 // non-zero: pages are RC4-encoded

	mu      sync.Mutex
	byTable map[uint32][]uint32 // table definition page → its data pages
}

type jetColumn struct {
	name      string
	typ       byte
	num       int // the column's number in a row (its bit in the null mask)
	varIndex  int
	fixedOff  int
	size      int
	fixed     bool
	autoNum   bool
	prec      int
	scale     int
	nameIndex int // the order of the definition
}

type jetTable struct {
	name    string
	tdef    uint32
	usage   [4]byte // the usage map's row (1 byte) and page (3 bytes)
	rows    int64
	cols    []jetColumn // in the order of the definition
	numCols int
}

func openJet(path string) (*jetFile, error) {
	f, err := os.Open(path) // read-only
	if err != nil {
		return nil, err
	}
	head := make([]byte, 0x80+0x18)
	if _, err := f.ReadAt(head, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("not an Access database: %w", err)
	}
	if !strings.HasPrefix(string(head[4:]), "Standard Jet DB") && !strings.HasPrefix(string(head[4:]), "Standard ACE DB") {
		f.Close()
		return nil, errors.New("not an Access database (.mdb or .accdb)")
	}
	j := &jetFile{f: f, version: head[0x14]}
	j.jet3 = j.version == 0
	j.pageSize = 4096
	if j.jet3 {
		j.pageSize = 2048
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	j.pages = st.Size() / int64(j.pageSize)
	// The header from 0x18 is RC4-masked with a fixed key; it holds the key
	// of an encoded database (Jet 3 and 4's "encrypt database").
	n := 128
	if j.jet3 {
		n = 126
	}
	hdr := make([]byte, n)
	copy(hdr, head[0x18:0x18+n])
	c, _ := rc4.NewCipher([]byte{0xC7, 0xDA, 0x39, 0x6B})
	c.XORKeyStream(hdr, hdr)
	if j.version <= 1 { // ACE files are encrypted differently, and can't be read
		j.dbKey = binary.LittleEndian.Uint32(hdr[0x3E-0x18:])
	}
	// The table definition of MSysObjects is the check that it can be read.
	p, err := j.page(jetMSysTDef)
	if err != nil || p[0] != jetPageTDef {
		j.dbKey = 0
		if p, err = j.page(jetMSysTDef); err != nil || p[0] != jetPageTDef {
			f.Close()
			return nil, errors.New("this Access database can't be read: it is encrypted (password-protected) or damaged")
		}
	}
	return j, nil
}

func (j *jetFile) Close() error { return j.f.Close() }

// page reads page n, decoded.
func (j *jetFile) page(n uint32) ([]byte, error) {
	if int64(n) >= j.pages {
		return nil, fmt.Errorf("page %d is past the end of the file", n)
	}
	b := make([]byte, j.pageSize)
	if _, err := j.f.ReadAt(b, int64(n)*int64(j.pageSize)); err != nil {
		return nil, err
	}
	if j.dbKey != 0 && n > 0 {
		var k [4]byte
		binary.LittleEndian.PutUint32(k[:], j.dbKey^n)
		c, _ := rc4.NewCipher(k[:])
		c.XORKeyStream(b, b)
	}
	return b, nil
}

func u16(b []byte, i int) int    { return int(binary.LittleEndian.Uint16(b[i:])) }
func u32(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[i:]) }
func u24(b []byte, i int) uint32 { return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 }

// tdef reads a table definition, which may continue on other pages.
func (j *jetFile) tdef(pg uint32) ([]byte, error) {
	p, err := j.page(pg)
	if err != nil {
		return nil, err
	}
	if p[0] != jetPageTDef {
		return nil, fmt.Errorf("page %d is not a table definition", pg)
	}
	buf := append([]byte{}, p...)
	for next, seen := u32(p, 4), 0; next != 0 && seen < 1000; seen++ {
		q, err := j.page(next)
		if err != nil {
			return nil, err
		}
		buf = append(buf, q[8:]...)
		next = u32(q, 4)
	}
	return buf, nil
}

// table reads the definition of the table at page pg.
func (j *jetFile) table(name string, pg uint32) (*jetTable, error) {
	b, err := j.tdef(pg)
	if err != nil {
		return nil, err
	}
	// Offsets of the two formats.
	numRows, numCols, numRIdx, colsStart, ridxSize, colSize := 16, 45, 51, 63, 12, 25
	cNum, cVar, cFixed, cFlags, cSize := 5, 7, 21, 15, 23
	if j.jet3 {
		numRows, numCols, numRIdx, colsStart, ridxSize, colSize = 12, 25, 31, 43, 8, 18
		cNum, cVar, cFixed, cFlags, cSize = 1, 3, 14, 13, 16
	}
	t := &jetTable{name: name, tdef: pg, rows: int64(u32(b, numRows)), numCols: u16(b, numCols)}
	usageOff := 55
	if j.jet3 {
		usageOff = 35
	}
	copy(t.usage[:], b[usageOff:usageOff+4])
	off := colsStart + int(u32(b, numRIdx))*ridxSize
	if off+t.numCols*colSize > len(b) {
		return nil, fmt.Errorf("table %s: damaged definition", name)
	}
	for i := 0; i < t.numCols; i++ {
		e := b[off+i*colSize:]
		c := jetColumn{typ: e[0], num: u16(e, cNum), varIndex: u16(e, cVar), fixedOff: u16(e, cFixed),
			size: u16(e, cSize), fixed: e[cFlags]&0x01 != 0, autoNum: e[cFlags]&0x04 != 0, nameIndex: i}
		if !j.jet3 {
			c.prec, c.scale = int(e[11]), int(e[12])
		}
		t.cols = append(t.cols, c)
	}
	off += t.numCols * colSize
	for i := range t.cols {
		if j.jet3 {
			n := int(b[off])
			t.cols[i].name = decodeJet3(b[off+1 : off+1+n])
			off += 1 + n
		} else {
			n := u16(b, off)
			t.cols[i].name = decodeUCS2(b[off+2 : off+2+n])
			off += 2 + n
		}
	}
	return t, nil
}

// dataPages returns the data pages of the table defined at page tdef,
// found by reading every page header once.
func (j *jetFile) dataPages(tdef uint32) ([]uint32, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.byTable == nil {
		j.byTable = map[uint32][]uint32{}
		const batch = 256
		buf := make([]byte, batch*j.pageSize)
		for start := int64(0); start < j.pages; start += batch {
			n, err := j.f.ReadAt(buf, start*int64(j.pageSize))
			if n == 0 && err != nil {
				return nil, err
			}
			for i := 0; i < n/j.pageSize; i++ {
				pg := uint32(start) + uint32(i)
				p := buf[i*j.pageSize : i*j.pageSize+8]
				if j.dbKey != 0 && pg > 0 {
					var k [4]byte
					binary.LittleEndian.PutUint32(k[:], j.dbKey^pg)
					c, _ := rc4.NewCipher(k[:])
					hdr := make([]byte, 8)
					c.XORKeyStream(hdr, p)
					p = hdr
				}
				if p[0] == jetPageData && p[1] == 0x01 {
					t := u32(p, 4)
					j.byTable[t] = append(j.byTable[t], pg)
				}
			}
		}
	}
	return j.byTable[tdef], nil
}

// rowAt returns row r of data page pg (following an overflow pointer).
func (j *jetFile) rowAt(p []byte, r int, depth int) ([]byte, bool, error) {
	countOff := 12
	if j.jet3 {
		countOff = 8
	}
	n := u16(p, countOff)
	if r >= n {
		return nil, false, nil
	}
	start := u16(p, countOff+2+r*2)
	end := j.pageSize
	if r > 0 {
		end = u16(p, countOff+2+(r-1)*2) & jetOffsetMsk
	}
	if depth == 0 && start&jetDelFlag != 0 && start&jetOverflow == 0 {
		return nil, false, nil // deleted, or the moved data of an overflow row
	}
	s := start & jetOffsetMsk
	if s >= end || end > len(p) {
		return nil, false, nil
	}
	row := p[s:end]
	if start&jetOverflow != 0 && depth == 0 {
		if len(row) < 4 {
			return nil, false, nil
		}
		q, err := j.page(u24(row, 1))
		if err != nil {
			return nil, false, err
		}
		return j.rowAt(q, int(row[0]), depth+1)
	}
	return row, true, nil
}

// ownedPages reads a table's usage map: the pages it owns. Freed pages
// may still look like its data pages, with rows deleted long ago.
func (j *jetFile) ownedPages(t *jetTable) ([]uint32, error) {
	p, err := j.page(u24(t.usage[:], 1))
	if err != nil {
		return nil, err
	}
	m, ok, err := j.lvalRow(p, int(t.usage[0]))
	if err != nil || !ok || len(m) < 5 {
		return nil, fmt.Errorf("no usage map")
	}
	var out []uint32
	bits := func(bitmap []byte, base uint32) {
		for i, by := range bitmap {
			for k := 0; k < 8; k++ {
				if by&(1<<k) != 0 {
					out = append(out, base+uint32(i*8+k))
				}
			}
		}
	}
	switch m[0] {
	case 0:
		bits(m[5:], u32(m, 1))
	case 1:
		per := uint32((j.pageSize - 4) * 8)
		for k := 0; 1+k*4+4 <= len(m); k++ {
			mp := u32(m, 1+k*4)
			if mp == 0 {
				continue
			}
			bp, err := j.page(mp)
			if err != nil {
				return nil, err
			}
			bits(bp[4:], uint32(k)*per)
		}
	default:
		return nil, fmt.Errorf("unknown usage map type %d", m[0])
	}
	return out, nil
}

// rows calls fn with the values of every row of t, in column order.
func (j *jetFile) rows(t *jetTable, fn func(vals []any) (bool, error)) error {
	pages, err := j.dataPages(t.tdef)
	if err != nil {
		return err
	}
	if owned, err := j.ownedPages(t); err == nil {
		own := make(map[uint32]bool, len(owned))
		for _, pg := range owned {
			own[pg] = true
		}
		kept := pages[:0:0]
		for _, pg := range pages {
			if own[pg] {
				kept = append(kept, pg)
			}
		}
		pages = kept
	}
	for _, pg := range pages {
		p, err := j.page(pg)
		if err != nil {
			return err
		}
		countOff := 12
		if j.jet3 {
			countOff = 8
		}
		for r := 0; r < u16(p, countOff); r++ {
			start := u16(p, countOff+2+r*2)
			if start&jetOverflow != 0 && start&jetDelFlag != 0 {
				continue // a deleted overflow row
			}
			row, ok, err := j.rowAt(p, r, 0)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			vals, err := j.crack(t, row)
			if err != nil {
				return fmt.Errorf("table %s, page %d, row %d: %w", t.name, pg, r, err)
			}
			more, err := fn(vals)
			if err != nil || !more {
				return err
			}
		}
	}
	return nil
}

// crack reads the values of one row.
func (j *jetFile) crack(t *jetTable, row []byte) (vals []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("damaged row: %v", r)
		}
	}()
	end := len(row) - 1
	var rowCols, varCols int
	if j.jet3 {
		rowCols = int(row[0])
	} else {
		rowCols = u16(row, 0)
	}
	maskSize := (rowCols + 7) / 8
	mask := row[len(row)-maskSize:]
	var varOffs []int
	hasVar := false
	for _, c := range t.cols {
		hasVar = hasVar || !c.fixed && c.typ != jetBool
	}
	switch {
	case !hasVar:
		// No table of variable columns at all.
	case j.jet3:
		varCols = int(row[end-maskSize])
		varOffs = make([]int, varCols+1)
		numJumps := (len(row) - 1) / 256
		colPtr := end - maskSize - numJumps - 1
		if (colPtr-varCols)/256 < numJumps {
			numJumps--
		}
		used := 0
		for i := 0; i <= varCols; i++ {
			for used < numJumps && i == int(row[end-maskSize-used-1]) {
				used++
			}
			varOffs[i] = int(row[colPtr-i]) + used*256
		}
	default:
		varCols = u16(row, end-maskSize-1)
		varOffs = make([]int, varCols+1)
		for i := 0; i <= varCols; i++ {
			varOffs[i] = u16(row, end-maskSize-3-i*2)
		}
	}
	fixedBase := 2
	if j.jet3 {
		fixedBase = 1
	}
	vals = make([]any, len(t.cols))
	for i, c := range t.cols {
		notNull := c.num < rowCols && mask[c.num/8]&(1<<(c.num%8)) != 0
		if c.typ == jetBool {
			vals[i] = notNull
			continue
		}
		if !notNull {
			continue
		}
		var data []byte
		if c.fixed {
			if c.num >= rowCols {
				continue
			}
			s := fixedBase + c.fixedOff
			if s+c.size > len(row) {
				continue
			}
			data = row[s : s+c.size]
		} else {
			if c.varIndex >= varCols {
				continue
			}
			s, e := varOffs[c.varIndex], varOffs[c.varIndex+1]
			if s > e || e > len(row) {
				continue
			}
			data = row[s:e]
		}
		vals[i], err = j.value(c, data)
		if err != nil {
			return nil, err
		}
	}
	return vals, nil
}

// epochAccess is day 0 of Access dates.
var epochAccess = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

func (j *jetFile) value(c jetColumn, d []byte) (any, error) {
	switch c.typ {
	case jetByte:
		return int64(d[0]), nil
	case jetInt:
		return int64(int16(binary.LittleEndian.Uint16(d))), nil
	case jetLong, jetComplex:
		return int64(int32(binary.LittleEndian.Uint32(d))), nil
	case jetBigInt:
		return int64(binary.LittleEndian.Uint64(d)), nil
	case jetMoney:
		v := int64(binary.LittleEndian.Uint64(d))
		r := new(big.Rat).SetFrac64(v, 10000)
		return json.Number(r.FloatString(4)), nil
	case jetFloat:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(d))), nil
	case jetDouble:
		return math.Float64frombits(binary.LittleEndian.Uint64(d)), nil
	case jetDateTime:
		v := math.Float64frombits(binary.LittleEndian.Uint64(d))
		days := math.Trunc(v)
		frac := math.Abs(v - days)
		ms := math.Round(frac * 86400000)
		return epochAccess.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond), nil
	case jetGUID:
		if len(d) == 16 {
			return "{" + formatGUID(d) + "}", nil
		}
	case jetNumeric:
		if len(d) < 17 {
			return nil, nil
		}
		be := make([]byte, 16)
		for g := 0; g < 4; g++ { // four little-endian ints, most significant first
			for k := 0; k < 4; k++ {
				be[g*4+k] = d[1+g*4+3-k]
			}
		}
		n := new(big.Int).SetBytes(be)
		if d[0]&0x80 != 0 {
			n.Neg(n)
		}
		r := new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.scale)), nil))
		return json.Number(r.FloatString(c.scale)), nil
	case jetText:
		return j.text(d), nil
	case jetMemo, jetOLE:
		b, err := j.longValue(d)
		if err != nil {
			return nil, err
		}
		if c.typ == jetMemo {
			return j.text(b), nil
		}
		return b, nil
	case jetExtDate:
		return strings.TrimSpace(string(d)), nil
	}
	return d, nil // binary
}

// text decodes a text value: Jet 4's UCS-2, possibly compressed.
func (j *jetFile) text(b []byte) string {
	if j.jet3 {
		return decodeJet3(b)
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		var u []byte
		compressed := true
		for i := 2; i < len(b); {
			switch {
			case b[i] == 0:
				compressed = !compressed
				i++
			case compressed:
				u = append(u, b[i], 0)
				i++
			case i+1 < len(b):
				u = append(u, b[i], b[i+1])
				i += 2
			default:
				i++
			}
		}
		b = u
	}
	return decodeUCS2(b)
}

func decodeUCS2(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func decodeJet3(b []byte) string {
	s, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

// longValue reads a memo or OLE value: inline, on one other page, or in a
// chain of pages.
func (j *jetFile) longValue(d []byte) ([]byte, error) {
	if len(d) < 12 {
		return nil, nil
	}
	head := u32(d, 0)
	length := int(head & 0x3FFFFFFF)
	switch {
	case head&0x80000000 != 0:
		if 12+length > len(d) {
			length = len(d) - 12
		}
		return d[12 : 12+length], nil
	case head&0x40000000 != 0:
		p, err := j.page(u24(d, 5))
		if err != nil {
			return nil, err
		}
		row, ok, err := j.lvalRow(p, int(d[4]))
		if err != nil || !ok {
			return nil, err
		}
		return row[:min(length, len(row))], nil
	}
	var out []byte
	rowNum, pg := int(d[4]), u24(d, 5)
	for steps := 0; pg != 0 && len(out) < length && steps < 100000; steps++ {
		p, err := j.page(pg)
		if err != nil {
			return nil, err
		}
		row, ok, err := j.lvalRow(p, rowNum)
		if err != nil || !ok || len(row) < 4 {
			break
		}
		rowNum, pg = int(row[0]), u24(row, 1)
		out = append(out, row[4:]...)
	}
	if len(out) > length {
		out = out[:length]
	}
	return out, nil
}

// lvalRow is rowAt on a long value page, whose rows aren't flagged.
func (j *jetFile) lvalRow(p []byte, r int) ([]byte, bool, error) {
	countOff := 12
	if j.jet3 {
		countOff = 8
	}
	if r >= u16(p, countOff) {
		return nil, false, nil
	}
	s := u16(p, countOff+2+r*2) & jetOffsetMsk
	end := j.pageSize
	if r > 0 {
		end = u16(p, countOff+2+(r-1)*2) & jetOffsetMsk
	}
	if s >= end {
		return nil, false, nil
	}
	return p[s:end], true, nil
}

// jetObject is a row of MSysObjects.
type jetObject struct {
	id    uint32
	name  string
	typ   int64
	flags int64
}

// objects reads MSysObjects.
func (j *jetFile) objects() ([]jetObject, error) {
	t, err := j.table("MSysObjects", jetMSysTDef)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, c := range t.cols {
		idx[c.name] = i
	}
	var out []jetObject
	err = j.rows(t, func(v []any) (bool, error) {
		o := jetObject{}
		if id, ok := v[idx["Id"]].(int64); ok {
			o.id = uint32(id) & 0x00FFFFFF
		}
		o.name, _ = v[idx["Name"]].(string)
		o.typ, _ = v[idx["Type"]].(int64)
		o.flags, _ = v[idx["Flags"]].(int64)
		out = append(out, o)
		return true, nil
	})
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a].name) < strings.ToLower(out[b].name) })
	return out, err
}

// userTable: a local table that isn't a system or hidden one.
func (o jetObject) userTable() bool {
	return o.typ == 1 && o.flags&0x80000002 == 0 && !strings.HasPrefix(o.name, "MSys") && !strings.HasPrefix(o.name, "~")
}
