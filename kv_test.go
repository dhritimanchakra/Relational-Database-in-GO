//go:build linux && (amd64 || arm64)

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func openKV(t *testing.T, path string) *KV {
	t.Helper()
	db := &KV{Path: path}
	if err := db.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func kvKey(i int) []byte { return []byte(fmt.Sprintf("key-%08d", i)) }
func kvVal(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, 40+i%200) }

func mustGet(t *testing.T, db *KV, i int) {
	t.Helper()
	got, ok := db.Get(kvKey(i))
	if !ok {
		t.Fatalf("key %d missing", i)
	}
	if !bytes.Equal(got, kvVal(i)) {
		t.Fatalf("key %d has wrong value", i)
	}
}

func TestKVPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db := openKV(t, path)
	db.NoSync = true // speed: durability itself is tested below
	const n = 5000
	for i := 0; i < n; i++ {
		if err := db.Set(kvKey(i), kvVal(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i += 3 { // delete a third
		if ok, err := db.Del(kvKey(i)); !ok || err != nil {
			t.Fatalf("del %d: %v %v", i, ok, err)
		}
	}
	db.Close()

	db = openKV(t, path)
	defer db.Close()
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			if _, ok := db.Get(kvKey(i)); ok {
				t.Fatalf("deleted key %d came back", i)
			}
		} else {
			mustGet(t, db, i)
		}
	}
}

func TestKVTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.db")
	db := openKV(t, path)
	db.Begin()
	for i := 0; i < 300; i++ {
		db.Set(kvKey(i), kvVal(i))
	}
	mustGet(t, db, 42) // a transaction can read its own writes
	if err := db.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Get(kvKey(42)); ok {
		t.Fatal("rolled-back write is visible")
	}

	// Commit: one durable batch.
	db.Begin()
	for i := 0; i < 300; i++ {
		db.Set(kvKey(i), kvVal(i))
	}
	if err := db.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = openKV(t, path)
	defer db.Close()
	for i := 0; i < 300; i++ {
		mustGet(t, db, i)
	}
	db.Begin()
	db.Set([]byte("keep"), []byte("1"))
	sp := db.Savepoint()
	db.Set([]byte("drop"), []byte("2"))
	db.RollbackTo(sp)
	db.Commit()
	if _, ok := db.Get([]byte("keep")); !ok {
		t.Fatal("savepoint lost earlier write")
	}
	if _, ok := db.Get([]byte("drop")); ok {
		t.Fatal("savepoint did not undo later write")
	}
}

func TestKVTornCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.db")
	db := openKV(t, path)
	for i := 0; i < 50; i++ {
		if err := db.Set(kvKey(i), kvVal(i)); err != nil {
			t.Fatal(err)
		}
	}

	db.Begin()
	for i := 50; i < 100; i++ {
		db.Set(kvKey(i), kvVal(i))
	}
	if err := writePages(db); err != nil {
		t.Fatal(err)
	}
	if err := db.sync(); err != nil {
		t.Fatal(err)
	}

	for _, chunk := range db.mmap.chunks {
		syscall.Munmap(chunk)
	}
	syscall.Close(db.fd)

	db2 := openKV(t, path)
	defer db2.Close()
	for i := 0; i < 50; i++ {
		mustGet(t, db2, i)
	}
	if _, ok := db2.Get(kvKey(75)); ok {
		t.Fatal("uncommitted data visible after crash")
	}

	for i := 50; i < 100; i++ {
		if err := db2.Set(kvKey(i), kvVal(i)); err != nil {
			t.Fatal(err)
		}
		mustGet(t, db2, i)
	}
}

func TestKVFileLockAndBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "l.db")
	db := openKV(t, path)
	second := &KV{Path: path}
	if err := second.Open(); err == nil {
		t.Fatal("a second Open of the same file must fail while it is locked")
	}
	db.Close()

	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, bytes.Repeat([]byte("not a database "), 500), 0o644)
	bad := &KV{Path: junk}
	if err := bad.Open(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("expected a corrupt-file error, got %v", err)
	}
}

func TestCrashHelper(t *testing.T) {
	path := os.Getenv("RELDB_CRASH_DB")
	if path == "" {
		t.Skip("helper process, not a real test")
	}
	start, _ := strconv.Atoi(os.Getenv("RELDB_CRASH_START"))
	db := &KV{Path: path}
	if err := db.Open(); err != nil {
		fmt.Println("ERR", err)
		os.Exit(2)
	}
	for i := start; ; i++ {
		if err := db.Set(kvKey(i), kvVal(i)); err != nil {
			fmt.Println("ERR", err)
			os.Exit(2)
		}
		fmt.Println(i)
	}
}

func TestKVCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes and fsyncs")
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	next := 0
	for round := 0; round < 6; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
		cmd.Env = append(os.Environ(), "RELDB_CRASH_DB="+path, "RELDB_CRASH_START="+strconv.Itoa(next))
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		lastAcked := next - 1
		want := 60 + round*45
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			n, err := strconv.Atoi(sc.Text())
			if err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("helper failed: %s", sc.Text())
			}
			lastAcked = n
			if n-next+1 >= want {
				break
			}
		}
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
		if lastAcked < next {
			t.Fatalf("round %d: helper acknowledged nothing", round)
		}

		db := openKV(t, path)
		for i := 0; i <= lastAcked; i++ {
			mustGet(t, db, i)
		}

		if got, ok := db.Get(kvKey(lastAcked + 1)); ok && !bytes.Equal(got, kvVal(lastAcked+1)) {
			t.Fatalf("round %d: in-flight key is torn", round)
		}
		db.Close()
		next = lastAcked + 1
	}
}

func TestKVBulkLoadStaysSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bulk.db")
	db := openKV(t, path)
	const n = 30000
	db.Begin()
	for i := 0; i < n; i++ {
		if err := db.Set(kvKey(i), kvVal(i)); err != nil {
			t.Fatal(err)
		}
	}
	if pages := len(db.page.temp); pages > 20000 {
		t.Fatalf("transaction buffered %d pages: dead pages are not being recycled", pages)
	}
	if err := db.Commit(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Size() > 64<<20 {
		t.Fatalf("file is %d MB for ~%d MB of data", st.Size()>>20, n*150>>20)
	}
	db.Close()

	db = openKV(t, path)
	defer db.Close()
	for i := 0; i < n; i += 7 {
		mustGet(t, db, i)
	}
}

func TestKVSavepointWithPageReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sp.db")
	db := openKV(t, path)
	db.NoSync = true
	db.Begin()
	for i := 0; i < 3000; i++ {
		db.Set(kvKey(i), kvVal(i))
	}
	sp := db.Savepoint()
	for round := 0; round < 3; round++ {
		for i := 0; i < 3000; i++ {
			db.Set(kvKey(i), []byte("overwritten"))
		}
	}
	for i := 3000; i < 3500; i++ {
		db.Set(kvKey(i), kvVal(i))
	}
	db.RollbackTo(sp)
	db.ReleaseSavepoint()

	for i := 0; i < 3000; i++ {
		mustGet(t, db, i)
	}
	if _, ok := db.Get(kvKey(3200)); ok {
		t.Fatal("write after the savepoint survived the rollback")
	}

	for i := 0; i < 3000; i += 2 {
		db.Set(kvKey(i), []byte("final"))
	}
	if err := db.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = openKV(t, path)
	defer db.Close()
	for i := 0; i < 3000; i++ {
		got, ok := db.Get(kvKey(i))
		want := kvVal(i)
		if i%2 == 0 {
			want = []byte("final")
		}
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("key %d wrong after commit+reopen", i)
		}
	}
}
