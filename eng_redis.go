package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis: the sidebar groups keys by prefix (user:*, order:*…); opening a
// group runs SCAN MATCH <pattern>, which lists every matching key with its
// type, time to live and value. Other read commands are run as typed.

func init() { backendOpeners[EngineRedis] = openRedis }

type redisBackend struct {
	c *redis.Client
}

// redisScanLimit is how many keys the object list and counts look at.
const redisScanLimit = 200000

func openRedis(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	o := &redis.Options{
		Addr:        hostPort(p.Host, p.Port, 6379),
		Username:    p.Username,
		Password:    p.Password,
		DialTimeout: 10 * time.Second,
		ReadTimeout: 60 * time.Second,
	}
	if p.Database != "" {
		n, err := strconv.Atoi(strings.TrimSpace(p.Database))
		if err != nil {
			return nil, fmt.Errorf("the database of Redis is a number (0, 1, …), not %q", p.Database)
		}
		o.DB = n
	}
	if optBool(opts, "tls", false) || optBool(opts, "ssl", false) {
		o.TLSConfig = &tls.Config{ServerName: strings.Split(p.Host, ":")[0], InsecureSkipVerify: optBool(opts, "insecure", false)}
	}
	return &redisBackend{c: redis.NewClient(o)}, nil
}

func (r *redisBackend) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }
func (r *redisBackend) Close() error                   { return r.c.Close() }

func (r *redisBackend) Version(ctx context.Context) string {
	info, err := r.c.Info(ctx, "server").Result()
	if err != nil {
		return "Redis"
	}
	name := "Redis"
	ver := ""
	for _, line := range strings.Split(info, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), ":")
		switch k {
		case "redis_version":
			ver = v
		case "valkey_version":
			name, ver = "Valkey", v
		case "server_name":
			if v == "valkey" {
				name = "Valkey"
			}
		}
	}
	return strings.TrimSpace(name + " " + ver)
}

// scanKeys calls fn for each key matching pattern (at most limit keys).
func (r *redisBackend) scanKeys(ctx context.Context, pattern, typ string, limit int, fn func(keys []string) error) error {
	var cursor uint64
	seen := 0
	for {
		var keys []string
		var err error
		if typ != "" {
			keys, cursor, err = r.c.ScanType(ctx, cursor, pattern, 1000, typ).Result()
		} else {
			keys, cursor, err = r.c.Scan(ctx, cursor, pattern, 1000).Result()
		}
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if limit > 0 && seen+len(keys) > limit {
				keys = keys[:limit-seen]
			}
			seen += len(keys)
			if err := fn(keys); err != nil {
				return err
			}
		}
		if cursor == 0 || limit > 0 && seen >= limit {
			return nil
		}
	}
}

// redisGlobEscape escapes the characters MATCH patterns give a meaning.
var redisGlobEscape = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)

// keyGroup is the sidebar group of a key: "user:*" for "user:42".
func keyGroup(key string) string {
	for _, sep := range []string{":", "."} {
		if i := strings.Index(key, sep); i > 0 {
			return redisGlobEscape.Replace(key[:i+1]) + "*" // a[1]:x is the group a\[1\]:*
		}
	}
	return ""
}

func (r *redisBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	groups := map[string]bool{}
	err := r.scanKeys(ctx, "*", "", redisScanLimit, func(keys []string) error {
		for _, k := range keys {
			if g := keyGroup(k); g != "" {
				groups[g] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	items := []string{"*"}
	var names []string
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	items = append(items, names...)
	return []ObjectGroup{{Type: ObjTable, Label: "Key groups", Queryable: true, Items: items}}, nil
}

func (r *redisBackend) SelectFor(_ context.Context, name string) (string, error) {
	if name == "*" {
		return "SCAN", nil
	}
	return "SCAN MATCH " + redisQuote(name), nil
}

func (r *redisBackend) Count(ctx context.Context, name string) (int64, error) {
	if name == "*" {
		return r.c.DBSize(ctx).Result()
	}
	var n int64
	err := r.scanKeys(ctx, name, "", 0, func(keys []string) error {
		n += int64(len(keys))
		return nil
	})
	return n, err
}

func (r *redisBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	types := map[string]int{}
	fields := map[string]int{}
	var sample []string
	total := 0
	err := r.scanKeys(ctx, name, "", 20000, func(keys []string) error {
		pipe := r.c.Pipeline()
		cmds := make([]*redis.StatusCmd, len(keys))
		for i, k := range keys {
			cmds[i] = pipe.Type(ctx, k)
		}
		pipe.Exec(ctx)
		for i, c := range cmds {
			t := c.Val()
			types[t]++
			total++
			if len(sample) < 20 {
				sample = append(sample, keys[i])
			}
			if t == "hash" && types[t] <= 200 {
				if fs, err := r.c.HKeys(ctx, keys[i]).Result(); err == nil {
					for _, f := range fs {
						fields[f]++
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# key group %s\n# %d key(s)", name, total)
	if total >= 20000 {
		b.WriteString(" or more (the first 20,000 are described)")
	}
	b.WriteString("\n\n# types\n")
	var ts []string
	for t := range types {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	for _, t := range ts {
		fmt.Fprintf(&b, "%-10s %d\n", t, types[t])
	}
	if len(fields) > 0 {
		b.WriteString("\n# hash fields (of up to 200 hashes): in how many hashes\n")
		var fs []string
		for f := range fields {
			fs = append(fs, f)
		}
		sort.Slice(fs, func(i, j int) bool {
			return fields[fs[i]] > fields[fs[j]] || fields[fs[i]] == fields[fs[j]] && fs[i] < fs[j]
		})
		for _, f := range fs {
			fmt.Fprintf(&b, "%-30s %d\n", f, fields[f])
		}
	}
	if len(sample) > 0 {
		b.WriteString("\n# sample keys: type, time to live\n")
		for _, k := range sample {
			t := r.c.Type(ctx, k).Val()
			ttl := r.c.PTTL(ctx, k).Val()
			fmt.Fprintf(&b, "%s  %s  %s\n", k, t, redisTTL(ttl))
		}
	}
	return b.String(), nil
}

func redisTTL(d time.Duration) string {
	if d < 0 {
		return "no expiry"
	}
	return d.Round(time.Millisecond).String()
}

// redisReadCommands may be run: they only read. SCAN is cactusdb's listing.
var redisReadCommands = map[string]bool{
	"GET": true, "MGET": true, "STRLEN": true, "GETRANGE": true, "SUBSTR": true, "LCS": true,
	"EXISTS": true, "TYPE": true, "TTL": true, "PTTL": true, "EXPIRETIME": true, "PEXPIRETIME": true,
	"SCAN": true, "DBSIZE": true, "INFO": true, "TIME": true, "PING": true, "ECHO": true, "RANDOMKEY": true,
	"HGET": true, "HMGET": true, "HGETALL": true, "HKEYS": true, "HVALS": true, "HLEN": true, "HEXISTS": true,
	"HSCAN": true, "HSTRLEN": true, "HRANDFIELD": true, "HTTL": true,
	"LRANGE": true, "LLEN": true, "LINDEX": true, "LPOS": true,
	"SMEMBERS": true, "SCARD": true, "SISMEMBER": true, "SMISMEMBER": true, "SRANDMEMBER": true, "SSCAN": true,
	"SINTER": true, "SINTERCARD": true, "SUNION": true, "SDIFF": true,
	"ZRANGE": true, "ZRANGEBYSCORE": true, "ZREVRANGE": true, "ZREVRANGEBYSCORE": true, "ZRANGEBYLEX": true,
	"ZREVRANGEBYLEX": true, "ZCARD": true, "ZSCORE": true, "ZMSCORE": true, "ZRANK": true, "ZREVRANK": true,
	"ZCOUNT": true, "ZLEXCOUNT": true, "ZSCAN": true, "ZRANDMEMBER": true, "ZINTER": true, "ZUNION": true, "ZDIFF": true,
	"XRANGE": true, "XREVRANGE": true, "XLEN": true, "XINFO": true, "XPENDING": true,
	"BITCOUNT": true, "GETBIT": true, "BITPOS": true, "BITFIELD_RO": true, "PFCOUNT": true, "SORT_RO": true,
	"GEOPOS": true, "GEODIST": true, "GEOSEARCH": true, "GEOHASH": true, "GEORADIUS_RO": true, "GEORADIUSBYMEMBER_RO": true,
	"MEMORY": true, "OBJECT": true,
	"JSON.GET": true, "JSON.MGET": true, "JSON.TYPE": true, "JSON.OBJKEYS": true, "JSON.OBJLEN": true,
	"JSON.ARRLEN": true, "JSON.STRLEN": true, "JSON.RESP": true,
	"FT.SEARCH": true, "FT.AGGREGATE": true, "FT.INFO": true, "FT._LIST": true, "FT.EXPLAIN": true,
	"TS.GET": true, "TS.MGET": true, "TS.RANGE": true, "TS.REVRANGE": true, "TS.MRANGE": true, "TS.INFO": true,
}

// Subcommands that only read, for the commands that have writing ones too.
var redisReadSubcommands = map[string]map[string]bool{
	"MEMORY": {"USAGE": true, "STATS": true},
	"OBJECT": {"ENCODING": true, "FREQ": true, "IDLETIME": true, "REFCOUNT": true},
	"XINFO":  {"STREAM": true, "GROUPS": true, "CONSUMERS": true},
}

var errRedisReadOnly = errors.New("cactusdb is read-only: only Redis commands that read are allowed (GET, HGETALL, SCAN, LRANGE…)")

func (r *redisBackend) CheckReadOnly(q string) error {
	args, err := redisArgs(q)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("the command is empty")
	}
	cmd := strings.ToUpper(args[0])
	if cmd == "KEYS" { // reads, but stops a busy server until every key is listed
		return errors.New("KEYS blocks the Redis server while it walks every key: use SCAN MATCH pattern instead")
	}
	if !redisReadCommands[cmd] {
		return fmt.Errorf("%w (%s writes or isn't supported)", errRedisReadOnly, cmd)
	}
	if subs := redisReadSubcommands[cmd]; subs != nil && (len(args) < 2 || !subs[strings.ToUpper(args[1])]) {
		return fmt.Errorf("%w (%s %s)", errRedisReadOnly, cmd, strings.ToUpper(strings.Join(args[1:min(2, len(args))], "")))
	}
	return nil
}

// redisArgs splits a command line: one command, words separated by spaces,
// "double" or 'single' quotes for words with spaces.
func redisArgs(q string) ([]string, error) {
	q = stripLineComments(q)
	var args []string
	var cur strings.Builder
	in := false
	var quote rune
	started := false
	for i := 0; i < len(q); i++ {
		c := rune(q[i])
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(q) {
				i++
				switch q[i] {
				case 'n':
					cur.WriteByte('\n')
				case 't':
					cur.WriteByte('\t')
				case 'r':
					cur.WriteByte('\r')
				case 'x': // \xHH, as redis-cli and strconv.Quote write bytes
					if b, err := strconv.ParseUint(q[min(i+1, len(q)):min(i+3, len(q))], 16, 8); err == nil && i+2 < len(q) {
						cur.WriteByte(byte(b))
						i += 2
					} else {
						cur.WriteByte('x')
					}
				default:
					cur.WriteByte(q[i])
				}
			} else if c == quote {
				quote = 0
			} else {
				cur.WriteByte(q[i]) // bytes as they are: a key in UTF-8 stays UTF-8
			}
		case c == '"' || c == '\'':
			quote, in, started = c, true, true
		case c == ' ' || c == '\t' || c == '\r':
			if in {
				args = append(args, cur.String())
				cur.Reset()
				in = false
			}
		case c == '\n':
			if in {
				args = append(args, cur.String())
				cur.Reset()
				in = false
			}
			if started && strings.TrimSpace(q[i:]) != "" {
				return nil, errors.New("run one Redis command at a time")
			}
		default:
			cur.WriteByte(q[i])
			in, started = true, true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote isn't closed")
	}
	if in {
		args = append(args, cur.String())
	}
	return args, nil
}

// redisQuote quotes a word for a command line when it needs it.
func redisQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\") {
		return s
	}
	return strconv.Quote(s)
}

func (r *redisBackend) Query(ctx context.Context, q string) (resultSet, error) {
	args, err := redisArgs(q)
	if err != nil {
		return nil, err
	}
	cmd := strings.ToUpper(args[0])
	if cmd == "SCAN" {
		if s, ok := parseFullScan(args[1:]); ok {
			return r.scanRows(ctx, s)
		}
	}
	if cmd == "INFO" {
		info, err := r.c.Do(ctx, toAny(args)...).Text()
		if err != nil {
			return nil, err
		}
		return infoRows(info), nil
	}
	v, err := r.c.Do(ctx, toAny(args)...).Result()
	if errors.Is(err, redis.Nil) {
		return newSliceRows([]string{"value"}, [][]any{{nil}}), nil
	}
	if err != nil {
		return nil, err
	}
	return redisReply(v), nil
}

func toAny(args []string) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

// redisReply shows a reply as rows: a map as field/value rows, an array as
// one row per element, anything else as one row.
func redisReply(v any) resultSet {
	switch t := v.(type) {
	case map[any]any:
		keys := make([]string, 0, len(t))
		vals := map[string]any{}
		for k, val := range t {
			ks := fmt.Sprint(k)
			keys = append(keys, ks)
			vals[ks] = val
		}
		sort.Strings(keys)
		rows := make([][]any, len(keys))
		for i, k := range keys {
			rows[i] = []any{k, redisValue(vals[k])}
		}
		return newSliceRows([]string{"field", "value"}, rows)
	case []any:
		rows := make([][]any, len(t))
		for i, e := range t {
			rows[i] = []any{redisValue(e)}
		}
		return newSliceRows([]string{"value"}, rows)
	}
	return newSliceRows([]string{"value"}, [][]any{{redisValue(v)}})
}

// redisValue turns a part of a reply into a cell: nested replies as JSON.
func redisValue(v any) any {
	switch t := v.(type) {
	case map[any]any:
		d := doc{}
		for k, val := range t {
			d = append(d, field{fmt.Sprint(k), redisValue(val)})
		}
		sort.Slice(d, func(i, j int) bool { return d[i].Key < d[j].Key })
		return d
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redisValue(e)
		}
		return out
	case error:
		return "(error) " + t.Error()
	}
	return v
}

// infoRows shows INFO as section, name, value rows.
func infoRows(info string) resultSet {
	var rows [][]any
	section := ""
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			section = strings.TrimPrefix(line, "# ")
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			rows = append(rows, []any{section, k, v})
		}
	}
	return newSliceRows([]string{"section", "name", "value"}, rows)
}

type redisScan struct {
	pattern, typ string
}

// parseFullScan reads SCAN [0] [MATCH pattern] [TYPE type] [COUNT n]:
// without a cursor other than 0 it lists every matching key.
func parseFullScan(args []string) (redisScan, bool) {
	s := redisScan{pattern: "*"}
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "0":
			if i != 0 {
				return s, false
			}
		case "MATCH":
			if i+1 >= len(args) {
				return s, false
			}
			i++
			s.pattern = args[i]
		case "TYPE":
			if i+1 >= len(args) {
				return s, false
			}
			i++
			s.typ = strings.ToLower(args[i])
		case "COUNT":
			i++
		default:
			return s, false // a cursor: Redis's own one page
		}
	}
	return s, true
}

// redisValueLimit is how many elements of a list, set, sorted set or stream
// a SCAN listing shows.
const (
	redisValueLimit  = 1000
	redisStringLimit = 64 << 10
)

// scanRows lists the keys matching s with their type, time to live and
// value, reading them in batches.
func (r *redisBackend) scanRows(ctx context.Context, s redisScan) (resultSet, error) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan []any, 500)
	errc := make(chan error, 1)
	go func() {
		defer close(ch)
		err := r.scanKeys(ctx, s.pattern, s.typ, 0, func(keys []string) error {
			rows, err := r.keyRows(ctx, keys)
			if err != nil {
				return err
			}
			for _, row := range rows {
				select {
				case ch <- row:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			errc <- err
		}
	}()
	return &chanRows{cols: []string{"key", "type", "ttl", "value"}, ch: ch, errc: errc, cancel: cancel}, nil
}

// keyRows reads the type, time to live and value of keys, pipelined.
func (r *redisBackend) keyRows(ctx context.Context, keys []string) ([][]any, error) {
	pipe := r.c.Pipeline()
	types := make([]*redis.StatusCmd, len(keys))
	ttls := make([]*redis.DurationCmd, len(keys))
	for i, k := range keys {
		types[i] = pipe.Type(ctx, k)
		ttls[i] = pipe.PTTL(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	pipe = r.c.Pipeline()
	vals := make([]redis.Cmder, len(keys))
	for i, k := range keys {
		switch types[i].Val() {
		case "string": // the start of a long one: the listing shows values, it doesn't copy them
			vals[i] = pipe.GetRange(ctx, k, 0, redisStringLimit-1)
		case "hash": // about redisValueLimit fields, not millions
			vals[i] = pipe.HScan(ctx, k, 0, "", redisValueLimit)
		case "list":
			vals[i] = pipe.LRange(ctx, k, 0, redisValueLimit-1)
		case "set":
			vals[i] = pipe.SRandMemberN(ctx, k, redisValueLimit)
		case "zset":
			vals[i] = pipe.ZRangeWithScores(ctx, k, 0, redisValueLimit-1)
		case "stream":
			vals[i] = pipe.XRangeN(ctx, k, "-", "+", redisValueLimit)
		case "ReJSON-RL":
			vals[i] = pipe.Do(ctx, "JSON.GET", k)
		}
	}
	pipe.Exec(ctx) // errors are per command
	rows := make([][]any, len(keys))
	for i, k := range keys {
		ttl := any(nil)
		if d := ttls[i].Val(); d >= 0 {
			ttl = d.Seconds()
		}
		rows[i] = []any{k, types[i].Val(), ttl, redisCmdValue(vals[i])}
	}
	return rows, nil
}

// redisCmdValue shows the value read by a pipelined command.
func redisCmdValue(c redis.Cmder) any {
	if c == nil || c.Err() != nil {
		return nil
	}
	switch t := c.(type) {
	case *redis.StringCmd:
		return t.Val()
	case *redis.ScanCmd: // HSCAN: field, value, field, value…
		kv, _ := t.Val()
		d := make(doc, 0, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			d = append(d, field{kv[i], kv[i+1]})
		}
		sort.Slice(d, func(a, b int) bool { return d[a].Key < d[b].Key })
		return d
	case *redis.MapStringStringCmd:
		m := t.Val()
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := make(doc, len(keys))
		for i, k := range keys {
			d[i] = field{k, m[k]}
		}
		return d
	case *redis.StringSliceCmd:
		out := make([]any, len(t.Val()))
		for i, s := range t.Val() {
			out[i] = s
		}
		return out
	case *redis.ZSliceCmd:
		out := make([]any, len(t.Val()))
		for i, z := range t.Val() {
			out[i] = doc{{"member", z.Member}, {"score", z.Score}}
		}
		return out
	case *redis.XMessageSliceCmd:
		out := make([]any, len(t.Val()))
		for i, m := range t.Val() {
			out[i] = doc{{"id", m.ID}, {"values", m.Values}}
		}
		return out
	case *redis.Cmd:
		if s, ok := t.Val().(string); ok {
			if v, err := parseOrdered([]byte(s)); err == nil {
				return v
			}
			return s
		}
		return redisValue(t.Val())
	}
	return nil
}

// chanRows is a result read by a goroutine.
type chanRows struct {
	cols   []string
	ch     chan []any
	errc   chan error
	cancel context.CancelFunc
}

func (c *chanRows) Columns() []string { return c.cols }
func (c *chanRows) Types() []string   { return make([]string, len(c.cols)) }
func (c *chanRows) Close() error      { c.cancel(); return nil }
func (c *chanRows) Next() ([]any, error) {
	row, ok := <-c.ch
	if !ok {
		select {
		case err := <-c.errc:
			return nil, err
		default:
			return nil, io.EOF
		}
	}
	for i, v := range row {
		row[i] = docCell(v)
	}
	return row, nil
}
