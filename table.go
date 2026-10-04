package main

// table.go - maps SQL tables onto the durable key-value store.
//
// Key space (all in one B+tree):
//
//	"s\x00" + table                 -> JSON schema
//	"t\x00" + table + "\x00" + pk   -> row (binary-encoded, all columns)
//
// The primary key is the FIRST column. INT keys are encoded big-endian with
// the sign bit flipped, so byte order == numeric order and range scans work.
// TEXT keys are stored as raw bytes. Row values use varints, so a row is
// far smaller than the JSON used before.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type TableSchema struct {
	Name string   `json:"name"`
	Cols []Column `json:"cols"`
}

func (s *TableSchema) colIndex(name string) int {
	for i, c := range s.Cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

type DB struct {
	mu     sync.Mutex
	kv     *KV
	tables map[string]*TableSchema // schema cache; rebuilt lazily from the tree
}

func OpenDB(path string) (*DB, error) {
	kv := &KV{Path: path}
	if err := kv.Open(); err != nil {
		return nil, err
	}
	return &DB{kv: kv, tables: map[string]*TableSchema{}}, nil
}

// Close discards any open (uncommitted) transaction and closes the file.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.kv.InTx() {
		_ = db.kv.Rollback()
	}
	return db.kv.Close()
}

func (db *DB) resetCache() { db.tables = map[string]*TableSchema{} }

// ------------------------------------------------------------------ values

type Value struct {
	T ColType
	I int64
	S string
}

type Row []Value

func (v Value) String() string {
	if v.T == COL_INT {
		return strconv.FormatInt(v.I, 10)
	}
	return v.S
}

func compareValues(a, b Value) int {
	if a.T == COL_INT {
		switch {
		case a.I < b.I:
			return -1
		case a.I > b.I:
			return 1
		}
		return 0
	}
	return strings.Compare(a.S, b.S)
}

// literalToValue type-checks a SQL literal against a column.
func literalToValue(col Column, lit Literal) (Value, error) {
	switch col.Type {
	case COL_INT:
		if !lit.IsNum {
			return Value{}, fmt.Errorf("column %q is INT but got string '%s'", col.Name, lit.Val)
		}
		n, err := strconv.ParseInt(lit.Val, 10, 64)
		if err != nil {
			return Value{}, fmt.Errorf("column %q: %q is not a valid 64-bit integer", col.Name, lit.Val)
		}
		return Value{T: COL_INT, I: n}, nil
	default:
		if lit.IsNum {
			return Value{}, fmt.Errorf("column %q is TEXT but got number %s (quote it)", col.Name, lit.Val)
		}
		return Value{T: COL_TEXT, S: lit.Val}, nil
	}
}

// ------------------------------------------------------------------ keys

func schemaKey(table string) []byte { return append([]byte{'s', 0}, table...) }

func rowPrefix(table string) []byte {
	b := make([]byte, 0, len(table)+3)
	b = append(b, 't', 0)
	b = append(b, table...)
	return append(b, 0)
}

func rowKeyFor(table string, pk Value) []byte {
	b := rowPrefix(table)
	if pk.T == COL_INT {
		return binary.BigEndian.AppendUint64(b, uint64(pk.I)^(1<<63))
	}
	return append(b, pk.S...)
}

// ------------------------------------------------------------------ row codec

var errCorruptRow = errors.New("corrupt row")

func encodeRow(s *TableSchema, row Row) ([]byte, error) {
	if len(row) != len(s.Cols) {
		return nil, fmt.Errorf("expected %d values, got %d", len(s.Cols), len(row))
	}
	buf := make([]byte, 0, 64)
	for i, c := range s.Cols {
		if c.Type == COL_INT {
			buf = binary.AppendVarint(buf, row[i].I)
		} else {
			buf = binary.AppendUvarint(buf, uint64(len(row[i].S)))
			buf = append(buf, row[i].S...)
		}
	}
	return buf, nil
}

func decodeRow(s *TableSchema, data []byte) (Row, error) {
	row := make(Row, len(s.Cols))
	for i, c := range s.Cols {
		if c.Type == COL_INT {
			v, n := binary.Varint(data)
			if n <= 0 {
				return nil, errCorruptRow
			}
			row[i] = Value{T: COL_INT, I: v}
			data = data[n:]
		} else {
			l, n := binary.Uvarint(data)
			if n <= 0 || uint64(len(data)-n) < l {
				return nil, errCorruptRow
			}
			row[i] = Value{T: COL_TEXT, S: string(data[n : n+int(l)])} // copies out of the mmap
			data = data[n+int(l):]
		}
	}
	return row, nil
}

// ------------------------------------------------------------------ schemas

// lookupSchema returns (nil, false, nil) if the table does not exist.
func (db *DB) lookupSchema(table string) (*TableSchema, bool, error) {
	if s, ok := db.tables[table]; ok {
		return s, true, nil
	}
	val, ok := db.kv.Get(schemaKey(table))
	if !ok {
		return nil, false, nil
	}
	s := &TableSchema{}
	if err := json.Unmarshal(val, s); err != nil {
		return nil, false, fmt.Errorf("corrupt schema for %q: %w", table, err)
	}
	db.tables[table] = s
	return s, true, nil
}

func (db *DB) getSchema(table string) (*TableSchema, error) {
	s, ok, err := db.lookupSchema(table)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", table)
	}
	return s, nil
}

func (db *DB) saveSchema(s *TableSchema) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := db.kv.Set(schemaKey(s.Name), data); err != nil {
		return err
	}
	db.tables[s.Name] = s
	return nil
}

func (db *DB) tableNames() []string {
	prefix := []byte{'s', 0}
	var names []string
	db.kv.Scan(prefix, func(k, _ []byte) bool {
		if !bytes.HasPrefix(k, prefix) {
			return false
		}
		names = append(names, string(k[len(prefix):]))
		return true
	})
	return names
}

// TableNames lists the tables in the database.
func (db *DB) TableNames() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.tableNames()
}

// ------------------------------------------------------------------ rows

func (db *DB) getRow(s *TableSchema, pk Value) (Row, bool, error) {
	val, ok := db.kv.Get(rowKeyFor(s.Name, pk))
	if !ok {
		return nil, false, nil
	}
	row, err := decodeRow(s, val)
	return row, err == nil, err
}

// putRow inserts or overwrites the row stored under row[0].
func (db *DB) putRow(s *TableSchema, row Row) error {
	data, err := encodeRow(s, row)
	if err != nil {
		return err
	}
	return db.kv.Set(rowKeyFor(s.Name, row[0]), data)
}

func (db *DB) delRow(s *TableSchema, pk Value) (bool, error) {
	return db.kv.Del(rowKeyFor(s.Name, pk))
}

// scanRows walks the table in primary-key order, starting at key `start`
// (any key inside the table's key range), until fn returns false.
func (db *DB) scanRows(s *TableSchema, start []byte, fn func(Row) bool) error {
	prefix := rowPrefix(s.Name)
	var scanErr error
	db.kv.Scan(start, func(k, v []byte) bool {
		if !bytes.HasPrefix(k, prefix) {
			return false // walked past the end of this table
		}
		row, err := decodeRow(s, v)
		if err != nil {
			scanErr = err
			return false
		}
		return fn(row)
	})
	return scanErr
}
