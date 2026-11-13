package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestClassifyType(t *testing.T) {
	for _, c := range []struct {
		engine, typ, kind string
		l, p, s           int64
	}{
		{EngineFirebird, "NUMERIC(15,2)", kDecimal, 0, 15, 2},
		{EngineFirebird, "VARCHAR(80)", kVarchar, 80, 0, 0},
		{EngineFirebird, "CHAR(8) CHARACTER SET OCTETS", kBinary, 8, 0, 0},
		{EngineFirebird, "BLOB SUB_TYPE TEXT", kText, 0, 0, 0},
		{EngineFirebird, "BLOB SUB_TYPE BINARY", kBlob, 0, 0, 0},
		{EngineFirebird, "FLOAT", kFloat32, 0, 0, 0},
		{EngineFirebird, "TIMESTAMP WITH TIME ZONE", kTimestampTZ, 0, 0, 0},
		{EngineMySQL, "tinyint(1)", kInt16, 0, 0, 0}, // not BOOLEAN: it holds -128..127
		{EngineMySQL, "int(10) unsigned", kInt64, 0, 0, 0},
		{EngineMySQL, "bigint(20) unsigned", kDecimal, 0, 20, 0},
		{EngineMySQL, "enum('a','bb')", kVarchar, 0, 0, 0},
		{EnginePostgres, "character varying(120)", kVarchar, 120, 0, 0},
		{EnginePostgres, "timestamp(6) without time zone", kTimestamp, 0, 0, 0},
		{EnginePostgres, "integer[]", kText, 0, 0, 0},
		{EnginePostgres, "bytea", kBlob, 0, 0, 0},
		{EngineMSSQL, "nvarchar(max)", kText, 0, 0, 0},
		{EngineMSSQL, "float(53)", kFloat64, 0, 0, 0},
		{EngineMSSQL, "bit", kBool, 0, 0, 0},
		{EngineMSSQL, "timestamp", kBinary, 8, 0, 0},
		{EngineMSSQL, "uniqueidentifier", kUUID, 0, 0, 0},
		{EngineSQLite, "INTEGER", kInt64, 0, 0, 0},
		{EngineSQLite, "", kBlob, 0, 0, 0},
		{EngineSQLite, "VARCHAR(20)", kVarchar, 20, 0, 0},
	} {
		k, l, p, s := classifyType(c.engine, c.typ)
		if k != c.kind || l != c.l || p != c.p || s != c.s {
			t.Errorf("%s %q = %s %d %d %d, want %s %d %d %d", c.engine, c.typ, k, l, p, s, c.kind, c.l, c.p, c.s)
		}
	}
	c := &mCol{Kind: kDecimal, Prec: 40, Scale: 4}
	if got := colType(EngineFirebird, c, 3); got != "NUMERIC(18,4)" {
		t.Errorf("Firebird 3 decimal = %s", got)
	}
	if got := colType(EngineMSSQL, &mCol{Kind: kVarchar, Len: 9000}, 0); got != "NVARCHAR(MAX)" {
		t.Errorf("SQL Server long varchar = %s", got)
	}
}

func TestNeutralDefault(t *testing.T) {
	for _, c := range []struct{ engine, def, kind, want string }{
		{EngineFirebird, "DEFAULT 'Cape Town'", kVarchar, "'Cape Town'"},
		{EngineFirebird, "DEFAULT CURRENT_TIMESTAMP", kTimestamp, "CURRENT_TIMESTAMP"},
		{EngineFirebird, "DEFAULT 'NOW'", kTimestamp, "CURRENT_TIMESTAMP"},
		{EngineMSSQL, "((0))", kInt32, "0"},
		{EngineMSSQL, "(getdate())", kTimestamp, "CURRENT_TIMESTAMP"},
		{EngineMSSQL, "((1))", kBool, "TRUE"},
		{EnginePostgres, "'N'::bpchar", kChar, "'N'"},
		{EnginePostgres, "now()", kTimestamp, "CURRENT_TIMESTAMP"},
		{EnginePostgres, "gen_random_uuid()", kUUID, ""},
		{EngineMySQL, "current_timestamp(6)", kTimestamp, "CURRENT_TIMESTAMP"},
	} {
		if got := neutralDefault(c.engine, c.def, c.kind); got != c.want {
			t.Errorf("%s %q = %q, want %q", c.engine, c.def, got, c.want)
		}
	}
	if got := renderDefault(EngineMSSQL, &mCol{Kind: kBool, Default: "TRUE"}); got != "1" {
		t.Errorf("SQL Server TRUE = %s", got)
	}
	if got := renderDefault(EngineMariaDB, &mCol{Kind: kText, Default: "'x'"}); got != "('x')" {
		t.Errorf("MariaDB text default = %s", got)
	}
}

func testXlate(src, dst string) *xlate {
	return &xlate{src: src, dst: dst, nameCase: "lower", seqOK: true, dropQual: map[string]bool{"PUBLIC": true, "DBO": true},
		names:   map[string]string{"CUSTOMER": "customer", "NAME": "name", "ACTIVE": "active", "TOTAL": "total", "GEN_X": "gen_x"},
		columns: map[string]bool{"NAME": true, "ACTIVE": true, "TOTAL": true}, bools: map[string]bool{"ACTIVE": true}}
}

func TestTranslateSQL(t *testing.T) {
	for _, c := range []struct{ src, dst, in, want string }{
		{EngineFirebird, EngineMariaDB, `SELECT FIRST 3 SKIP 1 NAME FROM CUSTOMER ORDER BY TOTAL`, "SELECT `name` FROM `customer` ORDER BY `total` LIMIT 3 OFFSET 1"},
		{EngineFirebird, EnginePostgres, `SELECT IIF(TOTAL > 1, 'a', 'b') FROM RDB$DATABASE`, `SELECT CASE WHEN "total" > 1 THEN 'a' ELSE 'b' END`},
		{EngineFirebird, EngineMariaDB, `SELECT GEN_ID(GEN_X, 1), LIST(NAME, ';') FROM CUSTOMER`, "SELECT NEXTVAL(`gen_x`), GROUP_CONCAT(`name` SEPARATOR ';') FROM `customer`"},
		{EngineFirebird, EnginePostgres, `SELECT NEXT VALUE FOR GEN_X FROM RDB$DATABASE`, `SELECT nextval('"gen_x"')`},
		{EngineFirebird, EngineMSSQL, `SELECT SUBSTRING(NAME FROM 2 FOR 3) FROM CUSTOMER WHERE ACTIVE`, `SELECT SUBSTRING([name], 2, 3) FROM [customer] WHERE [active] = 1`},
		{EngineMSSQL, EnginePostgres, `SELECT TOP 5 ISNULL([Name], N'x') FROM dbo.Customer`, `SELECT COALESCE("name", 'x') FROM "customer" LIMIT 5`},
		{EnginePostgres, EngineMariaDB, `SELECT (name)::text, COALESCE(total, 0::numeric) FROM public.customer WHERE status = ANY (ARRAY['a'::bpchar, 'b'::bpchar])`,
			"SELECT CAST((`name`) AS CHAR), COALESCE(`total`, 0) FROM `customer` WHERE status IN ('a', 'b')"},
		{EngineMariaDB, EngineFirebird, "select concat(`name`,'!') from `customer` order by `total` limit 2, 3",
			`select ("name" || '!') from "customer" order by "total" OFFSET 2 ROWS FETCH NEXT 3 ROWS ONLY`},
		{EngineMariaDB, EngineMSSQL, "select `name` from `customer` where `active` is true limit 3",
			`select [name] from [customer] where [active] = 1 ORDER BY (SELECT NULL) OFFSET 0 ROWS FETCH NEXT 3 ROWS ONLY`},
	} {
		x := testXlate(c.src, c.dst)
		if got := strings.Join(strings.Fields(x.render(lexSQL(c.in, c.src), true)), " "); got != c.want {
			t.Errorf("%s → %s\n%s\n got: %s\nwant: %s", c.src, c.dst, c.in, got, c.want)
		}
	}
}

const fbSelectable = `DECLARE VARIABLE SOFAR NUMERIC(15,2) = 0;
BEGIN
  FOR SELECT NAME, TOTAL FROM CUSTOMER WHERE ACTIVE AND TOTAL > :P_MIN INTO :NAME, :TOTAL DO
  BEGIN
    IF (TOTAL IS NULL) THEN CONTINUE;
    SOFAR = SOFAR + TOTAL;
    SUSPEND;
  END
  IF (SOFAR < 0) THEN EXCEPTION E_NEG;
END`

func TestTranslateProcedure(t *testing.T) {
	r := &psqlRoutine{kind: "procedure", name: "RUNNING", source: fbSelectable,
		ins:         []mCol{{Name: "P_MIN", Kind: kDecimal, Prec: 15, Scale: 2}},
		outs:        []mCol{{Name: "NAME", Kind: kVarchar, Len: 80}, {Name: "TOTAL", Kind: kDecimal, Prec: 15, Scale: 2}},
		excMessages: map[string]string{"E_NEG": "negative"}}
	var err error
	if r.decls, r.body, err = parseBody(r.source); err != nil {
		t.Fatal(err)
	}
	r.selectable = hasKind(r.body, "suspend")
	if !r.selectable || len(r.decls) != 1 {
		t.Fatalf("selectable %v, decls %v", r.selectable, r.decls)
	}
	my := strings.Join(writeRoutine(testXlate(EngineFirebird, EngineMariaDB), r), "\n")
	for _, want := range []string{"CREATE PROCEDURE `running`(IN p_min DECIMAL(15,2))", "DECLARE v_name VARCHAR(80);",
		"DECLARE sofar DECIMAL(15,2) DEFAULT 0;", "CREATE TEMPORARY TABLE `tmp_running` (`name` VARCHAR(80), `total` DECIMAL(15,2));",
		"DECLARE cur_2 CURSOR FOR SELECT `name`, `total` FROM `customer` WHERE `active` AND `total` > p_min;", "FETCH cur_2 INTO v_name, v_total;",
		"ITERATE loop_1;", "SET sofar = sofar + v_total;", "INSERT INTO `tmp_running` VALUES (v_name, v_total);",
		"SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = @cactusdb_message;", "SELECT * FROM `tmp_running`;"} {
		if !strings.Contains(my, want) {
			t.Errorf("MariaDB: missing %q in\n%s", want, my)
		}
	}
	pg := strings.Join(writeRoutine(testXlate(EngineFirebird, EnginePostgres), r), "\n")
	for _, want := range []string{`RETURNS TABLE (name VARCHAR(80), total NUMERIC(15,2))`, "#variable_conflict use_column",
		`FOR name, total IN SELECT "name", "total" FROM "customer" WHERE "active" AND "total" > p_min`, "CONTINUE loop_1;",
		"sofar := sofar + total;", "RETURN NEXT;", "RAISE EXCEPTION '%', 'negative';"} {
		if !strings.Contains(pg, want) {
			t.Errorf("PostgreSQL: missing %q in\n%s", want, pg)
		}
	}
}

func TestTranslateTrigger(t *testing.T) {
	src := `AS
BEGIN
  IF (DELETING) THEN NEW.TOTAL = 0; ELSE NEW.TOTAL = OLD.TOTAL + 1;
  IF (NEW.NAME IS NULL) THEN NEW.NAME = 'x';
END`
	r := &psqlRoutine{kind: "trigger", name: "T_BIU", table: "CUSTOMER", when: "BEFORE", events: []string{"INSERT", "UPDATE"}, source: src}
	var err error
	if r.decls, r.body, err = parseBody(src); err != nil {
		t.Fatal(err)
	}
	my := writeRoutine(testXlate(EngineFirebird, EngineMariaDB), r)
	if len(my) != 2 || !strings.Contains(my[0], "BEFORE INSERT ON `customer`") || !strings.Contains(my[1], "`t_biu_upd` BEFORE UPDATE") {
		t.Fatalf("MariaDB triggers:\n%s", strings.Join(my, "\n\n"))
	}
	if strings.Contains(my[0], "SET NEW.`total` = 0") || !strings.Contains(my[0], "SET NEW.`total` = OLD.`total` + 1;") {
		t.Errorf("DELETING wasn't folded away:\n%s", my[0])
	}
	pg := writeRoutine(testXlate(EngineFirebird, EnginePostgres), r)
	if len(pg) != 2 || !strings.Contains(pg[0], "IF (TG_OP = 'DELETE') THEN") || !strings.Contains(pg[0], "RETURN NEW;") ||
		!strings.Contains(pg[1], `CREATE TRIGGER "t_biu" BEFORE INSERT OR UPDATE ON "customer" FOR EACH ROW EXECUTE FUNCTION "trg_t_biu"()`) {
		t.Errorf("PostgreSQL trigger:\n%s", strings.Join(pg, "\n\n"))
	}

	auto := &psqlRoutine{kind: "trigger", when: "BEFORE", events: []string{"INSERT"}}
	for _, body := range []string{
		"AS BEGIN IF (NEW.ID IS NULL) THEN NEW.ID = GEN_ID(GEN_X, 1); END",
		"AS BEGIN NEW.ID = NEXT VALUE FOR GEN_X; END",
	} {
		auto.decls, auto.body, _ = parseBody(body)
		if col, gen, ok := autoIncrementTrigger(auto); !ok || col != "ID" || gen != "GEN_X" {
			t.Errorf("%s: %q %q %v", body, col, gen, ok)
		}
	}
	auto.decls, auto.body, _ = parseBody("AS BEGIN NEW.ID = GEN_ID(GEN_X, 1); NEW.AT = 'NOW'; END")
	if _, _, ok := autoIncrementTrigger(auto); ok {
		t.Error("a trigger that does more was taken for an auto-increment one")
	}
}

func TestParseBodyErrors(t *testing.T) {
	for _, src := range []string{
		"BEGIN FOR EXECUTE STATEMENT 'x' INTO :A DO SUSPEND; END",
		"DECLARE C CURSOR FOR (SELECT 1 FROM RDB$DATABASE); BEGIN END",
		"BEGIN IF (1 = 1) X = 1; END",
	} {
		if _, _, err := parseBody(src); err == nil {
			t.Errorf("no error for %q", src)
		}
	}
}

func TestConvertValue(t *testing.T) {
	if v := convertValue(nil, &mCol{Kind: kBlob}, EngineMSSQL); v == nil {
		t.Error("SQL Server: a NULL BLOB must be a typed NULL")
	}
	if v := convertValue(int64(1), &mCol{Kind: kBool}, EnginePostgres); v != true {
		t.Errorf("tinyint 1 → %v", v)
	}
	if v := convertValue([]byte("12.50"), &mCol{Kind: kDecimal}, EnginePostgres); v != "12.50" {
		t.Errorf("decimal bytes → %v", v)
	}
	g := []byte{0x78, 0x56, 0x34, 0x12, 0x34, 0x12, 0x34, 0x12, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}
	if v := convertValue(g, &mCol{Kind: kUUID, SrcType: "uniqueidentifier"}, EnginePostgres); v != "12345678-1234-1234-1234-56789ABCDEF0" && v != "12345678-1234-1234-1234-56789abcdef0" {
		t.Errorf("GUID → %v", v)
	}
}

// Code cut short ends a parse with an error or a TODO, never a crash.
func TestParserEndsDontPanic(t *testing.T) {
	for _, src := range []string{
		"AS BEGIN EXECUTE PROCEDURE",
		"AS BEGIN EXECUTE PROCEDURE p",
		"AS BEGIN EXECUTE PROCEDURE p RETURNING_VALUES",
		"BEGIN EXECUTE",
		"AS BEGIN IF (",
		"AS DECLARE VARIABLE",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parseBody(%q) panics: %v\n%s", src, r, debug.Stack())
				}
			}()
			parseBody(src)
		}()
	}
	for _, src := range []string{"SELECT 1 WHERE a = ANY (ARRAY", "SELECT 1 WHERE a = ANY (ARRAY "} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("anyArrayPass(%q) panics: %v", src, r)
				}
			}()
			anyArrayPass(lexSQL(src, EnginePostgres))
		}()
	}
}

// Values whose plain copy lost data: money as text, bit strings as bytes.
func TestMigrateValueEdges(t *testing.T) {
	for in, want := range map[string]string{"$1,234.56": "1234.56", "-$5.00": "-5.00", "($5.00)": "-5.00", "1.234,56 €": "1234.56", "$1,234": "1234", "$0.07": "0.07"} {
		if got := moneyNumber(in); got != want {
			t.Errorf("money %q: %q, want %q", in, got, want)
		}
	}
	if b, ok := bitString("101000001", "BIT(9)"); !ok || len(b) != 2 || b[0] != 0xA0 || b[1] != 0x80 {
		t.Errorf("bits: %x %v", b, ok)
	}
	if k, _, _, _ := classifyType(EngineMySQL, "tinyint(1)"); k != kInt16 {
		t.Errorf("tinyint(1) = %s", k)
	}
	if k, _, _, _ := classifyType(EngineMySQL, "geometry"); k != kBlob {
		t.Errorf("geometry = %s", k)
	}
}
