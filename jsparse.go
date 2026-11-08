package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// A small parser for the MongoDB shell's query syntax:
//
//	db.orders.find({ status: "open", total: { $gt: 100 } }, { _id: 0 }).sort({ total: -1 }).limit(10)
//	db.getSiblingDB("shop").getCollection("orders").aggregate([{ $match: {} }])
//
// Arguments are JavaScript literals: objects with quoted or bare keys,
// arrays, 'single' or "double" quoted strings, numbers, true/false/null,
// /regex/flags and ObjectId(), ISODate(), new Date(), NumberLong(),
// NumberInt(), NumberDecimal(), UUID() and Timestamp().

// shellCall is one call in the chain: name and arguments.
type shellCall struct {
	name string
	args []any
}

// shellQuery is a parsed db.… expression.
type shellQuery struct {
	db         string // getSiblingDB(…) ("" = the connection's)
	collection string // "" for a db method, e.g. getCollectionNames()
	calls      []shellCall
}

type jsParser struct {
	s   string
	pos int
}

func parseShellQuery(q string) (*shellQuery, error) {
	p := &jsParser{s: stripLineComments(q)}
	p.skip()
	if !p.word("db") {
		return nil, errors.New(`a query starts with db. — e.g. db.orders.find({ status: "open" })`)
	}
	sq := &shellQuery{}
	for p.skip(); p.peek() == '.'; p.skip() {
		p.pos++
		p.skip()
		name := p.ident()
		if name == "" {
			return nil, p.errorf("a name after the dot")
		}
		p.skip()
		if p.peek() != '(' {
			if sq.collection != "" || len(sq.calls) > 0 {
				return nil, p.errorf("( after " + name)
			}
			sq.collection = name // db.orders
			continue
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		switch {
		case name == "getSiblingDB" && sq.collection == "" && len(sq.calls) == 0 && sq.db == "":
			s, ok := firstString(args)
			if !ok {
				return nil, errors.New("getSiblingDB needs a database name")
			}
			sq.db = s
		case name == "getCollection" && sq.collection == "" && len(sq.calls) == 0:
			s, ok := firstString(args)
			if !ok {
				return nil, errors.New("getCollection needs a collection name")
			}
			sq.collection = s
		default:
			sq.calls = append(sq.calls, shellCall{name: name, args: args})
		}
	}
	p.skip()
	if p.peek() == ';' {
		p.pos++
		p.skip()
	}
	if p.pos < len(p.s) {
		return nil, p.errorf("the end of the query (run one query at a time)")
	}
	if len(sq.calls) == 0 {
		return nil, errors.New("the query has no method: add e.g. .find({})")
	}
	return sq, nil
}

func firstString(args []any) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok && s != ""
}

func (p *jsParser) errorf(want string) error {
	near := p.s[p.pos:]
	if len(near) > 30 {
		near = near[:30] + "…"
	}
	if near == "" {
		return fmt.Errorf("expected %s at the end of the query", want)
	}
	return fmt.Errorf("expected %s at %q", want, near)
}

func (p *jsParser) peek() byte {
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *jsParser) skip() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.pos++
			continue
		}
		if strings.HasPrefix(p.s[p.pos:], "//") {
			for p.pos < len(p.s) && p.s[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		if strings.HasPrefix(p.s[p.pos:], "/*") {
			if end := strings.Index(p.s[p.pos+2:], "*/"); end >= 0 {
				p.pos += end + 4
				continue
			}
		}
		return
	}
}

func isIdentByte(c byte, first bool) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || !first && c >= '0' && c <= '9'
}

func (p *jsParser) ident() string {
	start := p.pos
	for p.pos < len(p.s) && isIdentByte(p.s[p.pos], p.pos == start) {
		p.pos++
	}
	return p.s[start:p.pos]
}

// word reads w if it is the next identifier.
func (p *jsParser) word(w string) bool {
	save := p.pos
	if p.ident() == w {
		return true
	}
	p.pos = save
	return false
}

// args reads ( value, value… ).
func (p *jsParser) args() ([]any, error) {
	p.pos++ // (
	var out []any
	for {
		p.skip()
		if p.peek() == ')' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
		default:
			return nil, p.errorf(", or )")
		}
	}
}

// value reads one literal as a BSON value.
func (p *jsParser) value() (any, error) {
	p.skip()
	c := p.peek()
	switch {
	case c == '{':
		return p.object()
	case c == '[':
		p.pos++
		arr := bson.A{}
		for {
			p.skip()
			if p.peek() == ']' {
				p.pos++
				return arr, nil
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			p.skip()
			switch p.peek() {
			case ',':
				p.pos++
			case ']':
			default:
				return nil, p.errorf(", or ]")
			}
		}
	case c == '"' || c == '\'':
		return p.str()
	case c == '/':
		return p.regex()
	case c == '-' || c == '+' || c == '.' || c >= '0' && c <= '9':
		return p.number()
	case isIdentByte(c, true):
		return p.named()
	}
	return nil, p.errorf("a value")
}

func (p *jsParser) object() (bson.D, error) {
	p.pos++ // {
	d := bson.D{}
	for {
		p.skip()
		if p.peek() == '}' {
			p.pos++
			return d, nil
		}
		var key string
		if c := p.peek(); c == '"' || c == '\'' {
			s, err := p.str()
			if err != nil {
				return nil, err
			}
			key = s
		} else {
			// Bare keys may contain dots: { "a.b": 1 } is often written a.b.
			start := p.pos
			for p.pos < len(p.s) && (isIdentByte(p.s[p.pos], false) || p.s[p.pos] == '.') {
				p.pos++
			}
			key = p.s[start:p.pos]
		}
		if key == "" {
			return nil, p.errorf("a field name")
		}
		p.skip()
		if p.peek() != ':' {
			return nil, p.errorf(": after " + key)
		}
		p.pos++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		d = append(d, bson.E{Key: key, Value: v})
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
		default:
			return nil, p.errorf(", or }")
		}
	}
}

func (p *jsParser) str() (string, error) {
	quote := p.s[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == quote:
			p.pos++
			return b.String(), nil
		case c == '\\' && p.pos+1 < len(p.s):
			p.pos++
			e := p.s[p.pos]
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'u':
				if p.pos+4 < len(p.s) {
					if r, err := strconv.ParseUint(p.s[p.pos+1:p.pos+5], 16, 32); err == nil {
						b.WriteRune(rune(r))
						p.pos += 4
						break
					}
				}
				b.WriteByte(e)
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
		p.pos++
	}
	return "", errors.New("a string isn't closed")
}

func (p *jsParser) regex() (any, error) {
	p.pos++ // /
	var b strings.Builder
	for p.pos < len(p.s) && p.s[p.pos] != '/' {
		if p.s[p.pos] == '\\' && p.pos+1 < len(p.s) {
			b.WriteByte('\\')
			p.pos++
		}
		b.WriteByte(p.s[p.pos])
		p.pos++
	}
	if p.pos >= len(p.s) {
		return nil, errors.New("a /regular expression/ isn't closed")
	}
	p.pos++
	flags := p.ident()
	return bson.Regex{Pattern: b.String(), Options: flags}, nil
}

func (p *jsParser) number() (any, error) {
	start := p.pos
	if c := p.peek(); c == '-' || c == '+' {
		p.pos++
	}
	for p.pos < len(p.s) && (unicode.IsDigit(rune(p.s[p.pos])) || strings.ContainsRune(".eE+-", rune(p.s[p.pos]))) {
		if (p.s[p.pos] == '+' || p.s[p.pos] == '-') && !strings.ContainsRune("eE", rune(p.s[p.pos-1])) {
			break
		}
		p.pos++
	}
	text := p.s[start:p.pos]
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		if n >= -1<<31 && n < 1<<31 {
			return int32(n), nil
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, fmt.Errorf("bad number %q", text)
	}
	return f, nil
}

// named reads true, false, null, undefined, or a constructor call.
func (p *jsParser) named() (any, error) {
	name := p.ident()
	switch name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "undefined":
		return nil, nil
	case "new":
		p.skip()
		name = p.ident()
	}
	p.skip()
	if p.peek() != '(' {
		return nil, fmt.Errorf("unknown name %s (put text in quotes)", name)
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	arg := func() string {
		if len(args) > 0 {
			return fmt.Sprint(args[0])
		}
		return ""
	}
	switch name {
	case "ObjectId", "ObjectID":
		if len(args) == 0 {
			return bson.NewObjectID(), nil
		}
		return bson.ObjectIDFromHex(arg())
	case "ISODate", "Date":
		if len(args) == 0 {
			return time.Now().UTC(), nil
		}
		if n, ok := args[0].(int64); ok {
			return time.UnixMilli(n).UTC(), nil
		}
		return parseShellDate(arg())
	case "NumberLong", "Long":
		return strconv.ParseInt(arg(), 10, 64)
	case "NumberInt", "Int32":
		n, err := strconv.ParseInt(arg(), 10, 32)
		return int32(n), err
	case "NumberDecimal", "Decimal128":
		return bson.ParseDecimal128(arg())
	case "UUID":
		hexs := strings.ReplaceAll(arg(), "-", "")
		b := make([]byte, 0, 16)
		for i := 0; i+1 < len(hexs); i += 2 {
			v, err := strconv.ParseUint(hexs[i:i+2], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad UUID %q", arg())
			}
			b = append(b, byte(v))
		}
		if len(b) != 16 {
			return nil, fmt.Errorf("bad UUID %q", arg())
		}
		return bson.Binary{Subtype: 4, Data: b}, nil
	case "Timestamp":
		if len(args) == 2 {
			t, _ := strconv.ParseUint(fmt.Sprint(args[0]), 10, 32)
			i, _ := strconv.ParseUint(fmt.Sprint(args[1]), 10, 32)
			return bson.Timestamp{T: uint32(t), I: uint32(i)}, nil
		}
	case "RegExp":
		opts := ""
		if len(args) > 1 {
			opts = fmt.Sprint(args[1])
		}
		return bson.Regex{Pattern: arg(), Options: opts}, nil
	}
	return nil, fmt.Errorf("unknown function %s()", name)
}

func parseShellDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date %q (use e.g. 2024-05-01 or 2024-05-01T10:00:00Z)", s)
}
