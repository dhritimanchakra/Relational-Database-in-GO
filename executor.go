package main

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

type Result struct {
	Cols     []string
	Rows     [][]string
	Affected int
	Msg      string
}

// Exec parses and runs one SQL statement.
func (db *DB) Exec(sql string) (*Result, error) {
	stmt, err := Parse(sql)
	if err != nil {
		return nil, err
	}
	return db.ExecStmt(stmt)
}

func (db *DB) ExecStmt(stmt Statement) (*Result, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	switch stmt.(type) {
	case *BeginStmt:
		if err := db.kv.Begin(); err != nil {
			return nil, err
		}
		return &Result{Msg: "BEGIN"}, nil
	case *CommitStmt:
		if err := db.kv.Commit(); err != nil {
			db.resetCache()
			return nil, err
		}
		return &Result{Msg: "COMMIT"}, nil
	case *RollbackStmt:
		if err := db.kv.Rollback(); err != nil {
			return nil, err
		}
		db.resetCache()
		return &Result{Msg: "ROLLBACK"}, nil
	}

	implicit := !db.kv.InTx()
	if implicit {
		if err := db.kv.Begin(); err != nil {
			return nil, err
		}
	}
	sp := db.kv.Savepoint()
	res, err := db.run(stmt)
	if err != nil {
		db.kv.RollbackTo(sp)
		db.kv.ReleaseSavepoint()
		if implicit {
			_ = db.kv.Rollback()
		}
		db.resetCache()
		return nil, err
	}
	db.kv.ReleaseSavepoint()
	if implicit {
		if err := db.kv.Commit(); err != nil {
			db.resetCache()
			return nil, fmt.Errorf("commit failed: %w", err)
		}
	}
	return res, nil
}

func (db *DB) run(stmt Statement) (*Result, error) {
	switch s := stmt.(type) {
	case *CreateStmt:
		return db.execCreate(s)
	case *InsertStmt:
		return db.execInsert(s)
	case *SelectStmt:
		return db.execSelect(s)
	case *UpdateStmt:
		return db.execUpdate(s)
	case *DeleteStmt:
		return db.execDelete(s)
	}
	return nil, errors.New("unsupported statement")
}

type boundCond struct {
	idx int // column index in the schema
	op  TokenType
	val Value
}

func bindWhere(s *TableSchema, where []Cond) ([]boundCond, error) {
	out := make([]boundCond, 0, len(where))
	for _, c := range where {
		idx := s.colIndex(c.Col)
		if idx < 0 {
			return nil, fmt.Errorf("no such column %q in table %q", c.Col, s.Name)
		}
		v, err := literalToValue(s.Cols[idx], c.Lit)
		if err != nil {
			return nil, err
		}
		out = append(out, boundCond{idx: idx, op: c.Op, val: v})
	}
	return out, nil
}

func (c boundCond) matches(row Row) bool {
	cmp := compareValues(row[c.idx], c.val)
	switch c.op {
	case TK_EQ:
		return cmp == 0
	case TK_NE:
		return cmp != 0
	case TK_LT:
		return cmp < 0
	case TK_LE:
		return cmp <= 0
	case TK_GT:
		return cmp > 0
	case TK_GE:
		return cmp >= 0
	}
	return false
}

func matchAll(conds []boundCond, row Row) bool {
	for _, c := range conds {
		if !c.matches(row) {
			return false
		}
	}
	return true
}
func (db *DB) query(s *TableSchema, conds []boundCond, fn func(Row) bool) error {
	for _, c := range conds {
		if c.idx == 0 && c.op == TK_EQ {
			row, ok, err := db.getRow(s, c.val)
			if err != nil || !ok {
				return err
			}
			if matchAll(conds, row) {
				fn(row)
			}
			return nil
		}
	}

	start := rowPrefix(s.Name)
	var uppers []boundCond
	for _, c := range conds {
		if c.idx != 0 {
			continue
		}
		switch c.op {
		case TK_GT, TK_GE:
			if k := rowKeyFor(s.Name, c.val); bytes.Compare(k, start) > 0 {
				start = k
			}
		case TK_LT, TK_LE:
			uppers = append(uppers, c)
		}
	}
	return db.scanRows(s, start, func(row Row) bool {
		for _, u := range uppers {
			cmp := compareValues(row[0], u.val)
			if cmp > 0 || (cmp == 0 && u.op == TK_LT) {
				return false
			}
		}
		if matchAll(conds, row) {
			return fn(row)
		}
		return true
	})
}

func (db *DB) collect(s *TableSchema, where []Cond) ([]Row, error) {
	conds, err := bindWhere(s, where)
	if err != nil {
		return nil, err
	}
	var rows []Row
	err = db.query(s, conds, func(r Row) bool {
		rows = append(rows, r)
		return true
	})
	return rows, err
}

func (db *DB) execCreate(st *CreateStmt) (*Result, error) {
	_, exists, err := db.lookupSchema(st.Table)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("table %q already exists", st.Table)
	}
	if err := db.saveSchema(&TableSchema{Name: st.Table, Cols: st.Cols}); err != nil {
		return nil, err
	}
	return &Result{Msg: fmt.Sprintf("table %q created (primary key: %s)", st.Table, st.Cols[0].Name)}, nil
}

func (db *DB) execInsert(st *InsertStmt) (*Result, error) {
	s, err := db.getSchema(st.Table)
	if err != nil {
		return nil, err
	}
	if len(st.Vals) != len(s.Cols) {
		return nil, fmt.Errorf("table %q has %d columns, got %d values", s.Name, len(s.Cols), len(st.Vals))
	}
	row := make(Row, len(s.Cols))
	for i, lit := range st.Vals {
		if row[i], err = literalToValue(s.Cols[i], lit); err != nil {
			return nil, err
		}
	}
	if _, exists, err := db.getRow(s, row[0]); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("duplicate primary key %s in table %q", row[0], s.Name)
	}
	if err := db.putRow(s, row); err != nil {
		return nil, err
	}
	return &Result{Affected: 1, Msg: "1 row inserted"}, nil
}

func (db *DB) execSelect(st *SelectStmt) (*Result, error) {
	s, err := db.getSchema(st.Table)
	if err != nil {
		return nil, err
	}
	var idxs []int
	var names []string
	if len(st.Cols) == 1 && st.Cols[0] == "*" {
		for i, c := range s.Cols {
			idxs, names = append(idxs, i), append(names, c.Name)
		}
	} else {
		for _, name := range st.Cols {
			i := s.colIndex(name)
			if i < 0 {
				return nil, fmt.Errorf("no such column %q in table %q", name, s.Name)
			}
			idxs, names = append(idxs, i), append(names, name)
		}
	}
	rows, err := db.collect(s, st.Where)
	if err != nil {
		return nil, err
	}
	res := &Result{Cols: names, Rows: make([][]string, 0, len(rows)), Affected: len(rows)}
	for _, r := range rows {
		out := make([]string, len(idxs))
		for j, i := range idxs {
			out[j] = r[i].String()
		}
		res.Rows = append(res.Rows, out)
	}
	return res, nil
}

func (db *DB) execUpdate(st *UpdateStmt) (*Result, error) {
	s, err := db.getSchema(st.Table)
	if err != nil {
		return nil, err
	}
	type set struct {
		idx int
		val Value
	}
	sets := make([]set, 0, len(st.Sets))
	for _, a := range st.Sets {
		i := s.colIndex(a.Col)
		if i < 0 {
			return nil, fmt.Errorf("no such column %q in table %q", a.Col, s.Name)
		}
		v, err := literalToValue(s.Cols[i], a.Lit)
		if err != nil {
			return nil, err
		}
		sets = append(sets, set{i, v})
	}
	rows, err := db.collect(s, st.Where)
	if err != nil {
		return nil, err
	}
	for _, old := range rows {
		upd := make(Row, len(old))
		copy(upd, old)
		for _, a := range sets {
			upd[a.idx] = a.val
		}
		if compareValues(upd[0], old[0]) != 0 {
			if _, exists, err := db.getRow(s, upd[0]); err != nil {
				return nil, err
			} else if exists {
				return nil, fmt.Errorf("duplicate primary key %s in table %q", upd[0], s.Name)
			}
			if _, err := db.delRow(s, old[0]); err != nil {
				return nil, err
			}
		}
		if err := db.putRow(s, upd); err != nil {
			return nil, err
		}
	}
	return &Result{Affected: len(rows), Msg: strconv.Itoa(len(rows)) + " row(s) updated"}, nil
}

func (db *DB) execDelete(st *DeleteStmt) (*Result, error) {
	s, err := db.getSchema(st.Table)
	if err != nil {
		return nil, err
	}
	rows, err := db.collect(s, st.Where)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, err := db.delRow(s, r[0]); err != nil {
			return nil, err
		}
	}
	return &Result{Affected: len(rows), Msg: strconv.Itoa(len(rows)) + " row(s) deleted"}, nil
}
