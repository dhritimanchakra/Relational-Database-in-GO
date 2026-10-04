//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sql.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.kv.NoSync = true
	return db, path
}

func mustExec(t *testing.T, db *DB, sql string) *Result {
	t.Helper()
	res, err := db.Exec(sql)
	if err != nil {
		t.Fatalf("%s\n  -> %v", sql, err)
	}
	return res
}

func execErr(t *testing.T, db *DB, sql, wantSubstr string) {
	t.Helper()
	_, err := db.Exec(sql)
	if err == nil {
		t.Fatalf("%s\n  -> expected an error containing %q", sql, wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("%s\n  -> error %q does not contain %q", sql, err, wantSubstr)
	}
}

func col(res *Result, i int) []string {
	out := make([]string, len(res.Rows))
	for j, r := range res.Rows {
		out[j] = r[i]
	}
	return out
}

func TestSQLBasicCRUD(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE users (id INT, name TEXT, age INT)")
	mustExec(t, db, "INSERT INTO users VALUES (1, 'alice', 30)")
	mustExec(t, db, "INSERT INTO users VALUES (2, 'bob', 25)")
	mustExec(t, db, "INSERT INTO users VALUES (3, 'carol', 41)")

	res := mustExec(t, db, "SELECT * FROM users")
	if !reflect.DeepEqual(res.Cols, []string{"id", "name", "age"}) || len(res.Rows) != 3 {
		t.Fatalf("bad select *: %+v", res)
	}
	res = mustExec(t, db, "SELECT name FROM users WHERE age > 26")
	if !reflect.DeepEqual(col(res, 0), []string{"alice", "carol"}) {
		t.Fatalf("got %v", col(res, 0))
	}
	if r := mustExec(t, db, "UPDATE users SET age = 31, name = 'alicia' WHERE id = 1"); r.Affected != 1 {
		t.Fatalf("update affected %d", r.Affected)
	}
	res = mustExec(t, db, "SELECT name, age FROM users WHERE id = 1")
	if !reflect.DeepEqual(res.Rows, [][]string{{"alicia", "31"}}) {
		t.Fatalf("got %v", res.Rows)
	}
	if r := mustExec(t, db, "DELETE FROM users WHERE age < 30"); r.Affected != 1 {
		t.Fatalf("delete affected %d", r.Affected)
	}
	res = mustExec(t, db, "SELECT id FROM users")
	if !reflect.DeepEqual(col(res, 0), []string{"1", "3"}) {
		t.Fatalf("got %v", col(res, 0))
	}
}

func TestSQLDeleteMultiColumn(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE t (id INT, a TEXT, b TEXT, c INT)")
	for i := 0; i < 50; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO t VALUES (%d, 'a%d', 'b%d', %d)", i, i, i, i*10))
	}
	for round := 0; round < 20; round++ { // map order is random: repeat
		mustExec(t, db, fmt.Sprintf("DELETE FROM t WHERE id = %d", round))
	}
	if n := len(mustExec(t, db, "SELECT * FROM t").Rows); n != 30 {
		t.Fatalf("expected 30 rows left, got %d", n)
	}
}

func TestSQLScanMultiLevelTree(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE big (id INT, payload TEXT)")
	mustExec(t, db, "CREATE TABLE other (id INT)")
	mustExec(t, db, "BEGIN")
	pad := strings.Repeat("x", 60)
	for i := 0; i < 3000; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO big VALUES (%d, '%s')", i, pad))
	}
	mustExec(t, db, "INSERT INTO other VALUES (1)")
	mustExec(t, db, "COMMIT")

	if n := len(mustExec(t, db, "SELECT * FROM big").Rows); n != 3000 {
		t.Fatalf("full scan returned %d rows, want 3000", n)
	}
	res := mustExec(t, db, "SELECT id FROM big WHERE id >= 1000 AND id < 1005")
	if !reflect.DeepEqual(col(res, 0), []string{"1000", "1001", "1002", "1003", "1004"}) {
		t.Fatalf("range scan wrong: %v", col(res, 0))
	}
	res = mustExec(t, db, "SELECT id FROM big WHERE id <= 2")
	if !reflect.DeepEqual(col(res, 0), []string{"0", "1", "2"}) {
		t.Fatalf("upper-bound scan wrong: %v", col(res, 0))
	}
	if n := len(mustExec(t, db, "SELECT * FROM other").Rows); n != 1 {
		t.Fatalf("neighbour table has %d rows", n)
	}
}

func TestSQLIntKeysSortNumerically(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE n (id INT, v TEXT)")
	for _, id := range []int{10, 2, -5, 100, 1, 0} {
		mustExec(t, db, fmt.Sprintf("INSERT INTO n VALUES (%d, 'x')", id))
	}
	got := col(mustExec(t, db, "SELECT id FROM n"), 0)
	if !reflect.DeepEqual(got, []string{"-5", "0", "1", "2", "10", "100"}) {
		t.Fatalf("not numerically ordered: %v", got)
	}
}

func TestSQLTextPrimaryKey(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE kv (k TEXT, v INT)")
	mustExec(t, db, "INSERT INTO kv VALUES ('banana', 2)")
	mustExec(t, db, "INSERT INTO kv VALUES ('apple', 1)")
	mustExec(t, db, "INSERT INTO kv VALUES ('it''s', 3)")
	got := col(mustExec(t, db, "SELECT k FROM kv"), 0)
	if !reflect.DeepEqual(got, []string{"apple", "banana", "it's"}) {
		t.Fatalf("got %v", got)
	}
	if n := len(mustExec(t, db, "SELECT * FROM kv WHERE k = 'apple'").Rows); n != 1 {
		t.Fatal("point lookup on text key failed")
	}
}

func TestSQLUpdatePrimaryKey(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE t (id INT, v TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES (1, 'a')")
	mustExec(t, db, "INSERT INTO t VALUES (2, 'b')")
	mustExec(t, db, "UPDATE t SET id = 7 WHERE id = 1")
	if got := col(mustExec(t, db, "SELECT id FROM t"), 0); !reflect.DeepEqual(got, []string{"2", "7"}) {
		t.Fatalf("old key left behind or new key missing: %v", got)
	}
	execErr(t, db, "UPDATE t SET id = 2 WHERE id = 7", "duplicate primary key")

	if got := col(mustExec(t, db, "SELECT id FROM t"), 0); !reflect.DeepEqual(got, []string{"2", "7"}) {
		t.Fatalf("failed UPDATE was not rolled back: %v", got)
	}
}

func TestSQLErrors(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "CREATE TABLE t (id INT, name TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES (1, 'a')")
	execErr(t, db, "CREATE TABLE t (id INT)", "already exists")
	execErr(t, db, "INSERT INTO t VALUES (1, 'dup')", "duplicate primary key")
	execErr(t, db, "INSERT INTO t VALUES (2)", "2 columns")
	execErr(t, db, "INSERT INTO t VALUES ('x', 'y')", "is INT")
	execErr(t, db, "INSERT INTO t VALUES (3, 4)", "is TEXT")
	execErr(t, db, "SELECT * FROM missing", "does not exist")
	execErr(t, db, "SELECT nope FROM t", "no such column")
	execErr(t, db, "SELECT * FROM t WHERE nope = 1", "no such column")
	execErr(t, db, "SELEKT * FROM t", "syntax error")
	execErr(t, db, "SELECT * FROM", "syntax error")
	execErr(t, db, "SELECT * FROM t WHERE id", "syntax error")
	execErr(t, db, "INSERT INTO t VALUES (1, 'unterminated)", "syntax error")
	execErr(t, db, "SELECT * FROM t; SELECT 1", "syntax error")
	execErr(t, db, "CREATE TABLE z (a BLOB)", "unknown column type")
	execErr(t, db, "INSERT INTO t VALUES (99999999999999999999, 'x')", "64-bit")
	execErr(t, db, "COMMIT", "no active transaction")

	if n := len(mustExec(t, db, "SELECT * FROM t").Rows); n != 1 {
		t.Fatalf("db damaged by failed statements: %d rows", n)
	}
}

func TestSQLTransactions(t *testing.T) {
	db, path := newTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INT, v TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES (1, 'keep')")

	mustExec(t, db, "BEGIN")
	mustExec(t, db, "INSERT INTO t VALUES (2, 'gone')")
	mustExec(t, db, "UPDATE t SET v = 'changed' WHERE id = 1")
	mustExec(t, db, "CREATE TABLE temp (id INT)")
	execErr(t, db, "INSERT INTO t VALUES (2, 'dup')", "duplicate")
	if n := len(mustExec(t, db, "SELECT * FROM t").Rows); n != 2 {
		t.Fatalf("txn should see its own writes, got %d rows", n)
	}
	execErr(t, db, "BEGIN", "already active")
	mustExec(t, db, "ROLLBACK")

	res := mustExec(t, db, "SELECT v FROM t")
	if !reflect.DeepEqual(col(res, 0), []string{"keep"}) {
		t.Fatalf("rollback failed: %v", col(res, 0))
	}
	execErr(t, db, "SELECT * FROM temp", "does not exist")

	mustExec(t, db, "BEGIN")
	mustExec(t, db, "INSERT INTO t VALUES (3, 'committed')")
	mustExec(t, db, "COMMIT")
	db.Close()

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := col(mustExec(t, db, "SELECT id FROM t"), 0); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Fatalf("after reopen: %v", got)
	}
}

func TestSQLPersistsAcrossReopen(t *testing.T) {
	db, path := newTestDB(t)
	mustExec(t, db, "CREATE TABLE a (id INT, name TEXT)")
	mustExec(t, db, "CREATE TABLE b (k TEXT, n INT)")
	mustExec(t, db, "INSERT INTO a VALUES (1, 'x')")
	mustExec(t, db, "INSERT INTO a VALUES (2, 'y')")
	mustExec(t, db, "INSERT INTO b VALUES ('q', 9)")
	mustExec(t, db, "UPDATE a SET name = 'updated' WHERE id = 2")
	mustExec(t, db, "DELETE FROM a WHERE id = 1")
	db.Close()

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.TableNames(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("tables: %v", got)
	}
	if got := mustExec(t, db, "SELECT * FROM a").Rows; !reflect.DeepEqual(got, [][]string{{"2", "updated"}}) {
		t.Fatalf("table a: %v", got)
	}
	if got := mustExec(t, db, "SELECT * FROM b").Rows; !reflect.DeepEqual(got, [][]string{{"q", "9"}}) {
		t.Fatalf("table b: %v", got)
	}
}

func TestSQLCaseInsensitiveKeywordsAndIdents(t *testing.T) {
	db, _ := newTestDB(t)
	defer db.Close()
	mustExec(t, db, "create table Users (ID int, Name text)")
	mustExec(t, db, "InSeRt InTo users VaLuEs (1, 'MixedCase')")
	res := mustExec(t, db, "select NAME from USERS where id = 1;")
	if !reflect.DeepEqual(res.Rows, [][]string{{"MixedCase"}}) {
		t.Fatalf("got %v", res.Rows)
	}
}

func TestParseAndSplit(t *testing.T) {
	stmts, rest := splitStatements("SELECT 'a;b' FROM t; INSERT INTO t VALUES (1")
	if len(stmts) != 1 || stmts[0] != "SELECT 'a;b' FROM t" || strings.TrimSpace(rest) != "INSERT INTO t VALUES (1" {
		t.Fatalf("split: %q | %q", stmts, rest)
	}
	if _, err := Parse("DELETE FROM t WHERE a >= 1 AND b <> 'x' AND c != 3;"); err != nil {
		t.Fatal(err)
	}
}
