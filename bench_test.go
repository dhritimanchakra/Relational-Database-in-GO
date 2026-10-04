//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
)

const benchKeys = 100_000

func benchKey(buf []byte, i int) []byte { return fmt.Appendf(buf[:0], "key-%08d", i) }

func newBenchKV(b *testing.B) *KV {
	b.Helper()
	db := &KV{Path: filepath.Join(b.TempDir(), "bench.db")}
	if err := db.Open(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	val := make([]byte, 64)
	var kb []byte
	db.Begin()
	for i := 0; i < benchKeys; i++ {
		if err := db.Set(benchKey(kb, i), val); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Commit(); err != nil {
		b.Fatal(err)
	}
	return db
}

func BenchmarkPointLookup(b *testing.B) {
	db := newBenchKV(b)
	rng := rand.New(rand.NewSource(1))
	var kb []byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := db.Get(benchKey(kb, rng.Intn(benchKeys))); !ok {
			b.Fatal("missing key")
		}
	}
}

func BenchmarkMixed90R10W(b *testing.B) {
	for _, mode := range []struct {
		name   string
		noSync bool
	}{{"fsync", false}, {"nosync", true}} {
		b.Run(mode.name, func(b *testing.B) {
			db := newBenchKV(b)
			db.NoSync = mode.noSync
			rng := rand.New(rand.NewSource(1))
			val := make([]byte, 64)
			var kb []byte
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				k := benchKey(kb, rng.Intn(benchKeys))
				if i%10 == 9 {
					if err := db.Set(k, val); err != nil {
						b.Fatal(err)
					}
				} else if _, ok := db.Get(k); !ok {
					b.Fatal("missing key")
				}
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
		})
	}
}

func BenchmarkBatchedWrites(b *testing.B) {
	for _, batch := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("batch=%d", batch), func(b *testing.B) {
			db := newBenchKV(b)
			rng := rand.New(rand.NewSource(1))
			val := make([]byte, 64)
			var kb []byte
			b.ResetTimer()
			for i := 0; i < b.N; i += batch {
				db.Begin()
				for j := 0; j < batch && i+j < b.N; j++ {
					if err := db.Set(benchKey(kb, rng.Intn(benchKeys)), val); err != nil {
						b.Fatal(err)
					}
				}
				if err := db.Commit(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "writes/s")
		})
	}
}

func BenchmarkParse(b *testing.B) {
	const q = "SELECT name, age FROM users WHERE id = 42 AND age > 18"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseInsert(b *testing.B) {
	const q = "INSERT INTO users VALUES (42, 'alice', 30)"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLPointSelect(b *testing.B) {
	db, err := OpenDB(filepath.Join(b.TempDir(), "sql.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.kv.NoSync = true
	db.Exec("CREATE TABLE users (id INT, name TEXT, age INT)")
	db.Exec("BEGIN")
	for i := 0; i < 20000; i++ {
		db.Exec(fmt.Sprintf("INSERT INTO users VALUES (%d, 'user%d', %d)", i, i, 20+i%50))
	}
	db.Exec("COMMIT")
	rng := rand.New(rand.NewSource(1))
	qs := make([]string, 1024)
	for i := range qs {
		qs[i] = fmt.Sprintf("SELECT * FROM users WHERE id = %d", rng.Intn(20000))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := db.Exec(qs[i%len(qs)])
		if err != nil || len(res.Rows) != 1 {
			b.Fatal("bad result", err)
		}
	}
}
