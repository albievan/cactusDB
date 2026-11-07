package main

import (
	"context"
	"testing"
)

func TestColumnBytes(t *testing.T) {
	for typ, want := range map[string]int64{
		"varchar(20)": 20, "VARCHAR(20) CHARACTER SET UTF8": 20, "character varying(50)": 50,
		"int": 4, "int(11) unsigned": 4, "INTEGER": 4, "INT AUTO_INCREMENT": 4, "serial": 4,
		"datetime": 8, "timestamp with time zone": 8, "DATE": 4, "bigint": 8, "DOUBLE PRECISION": 8,
		"smallint": 2, "tinyint(1)": 1, "boolean": 1, "decimal(10,2)": 6, "NUMERIC(18, 4)": 10,
		"char(3)": 3, "nvarchar(10)": 20, "nvarchar(max)": nominalTextBytes, "varbinary(max)": nominalBlobBytes,
		"blob": nominalBlobBytes, "BLOB SUB_TYPE BINARY SEGMENT SIZE 80": nominalBlobBytes,
		"BLOB SUB_TYPE TEXT": nominalTextBytes, "longtext": nominalTextBytes, "bytea": nominalBlobBytes,
		"uuid": 16, "uniqueidentifier": 16, "float": 8, "float(10)": 4, "D_MONEY": unknownTypeBytes,
	} {
		if got := columnBytes(typ); got != want {
			t.Errorf("columnBytes(%q) = %d, want %d", typ, got, want)
		}
	}
}

// The example from the request: name varchar(20), surname varchar(20), age
// int, dateofbirth datetime, an auto-increment id and a photo blob.
func TestRowBytesOf(t *testing.T) {
	def := "CREATE TABLE `person` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `name` varchar(20) DEFAULT NULL,\n" +
		"  `surname` varchar(20) NOT NULL,\n  `age` int,\n  `dateofbirth` datetime,\n  `photo` blob,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB"
	if got, want := rowBytesOf(def), int64(4+20+20+4+8+nominalBlobBytes); got != want {
		t.Errorf("rowBytesOf = %d, want %d", got, want)
	}
	fb := "CREATE TABLE PERSON (\n  ID INTEGER NOT NULL,\n  NAME VARCHAR(20),\n  FULLNAME COMPUTED BY (NAME || 'x')\n);"
	if got := rowBytesOf(fb); got != 24 {
		t.Errorf("Firebird with a computed column = %d, want 24", got)
	}
}

func TestEstimateSizes(t *testing.T) {
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := []tableStat{{Name: "customer", Rows: 3, Bytes: -1}, {Name: "orders", Rows: 2, Bytes: -1}, {Name: "x", Rows: -1, Bytes: -1}}
	estimateSizes(context.Background(), EngineSQLite, db, out)
	// customer: INTEGER 4 + TEXT 256; orders: INTEGER, INT, REAL 4 each
	if out[0].Bytes != 3*260 || !out[0].BytesEstimated || out[1].Bytes != 2*12 || !out[1].BytesEstimated {
		t.Errorf("got %+v", out[:2])
	}
	if out[2].Bytes != -1 || out[2].BytesEstimated { // rows unknown: no estimate
		t.Errorf("unknown rows: %+v", out[2])
	}
}
