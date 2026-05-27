package main

import (
	"fmt"
)

func (db *DB) execute(stmt Statement) error {
	switch s := stmt.(type) {
	case *CreateStmt:
		return db.execCreate(s)
	case *InsertStmt:
		return db.execInsert(s)
	case *SelectStmt:
		return db.execSelect(s)
	case *DeleteStmt:
		return db.execDelete(s)
	case *UpdateStmt:
		return db.execUpdate(s)
	}
	return fmt.Errorf("unknown statement type")

}
func (db *DB) execCreate(s *CreateStmt) error {
	if err := db.createTable(s); err != nil {
		return err
	}
	fmt.Printf("table %q created\n", s.table)
	return nil
}

func (db *DB) execInsert(s *InsertStmt) error {
	schema, err := db.getSchema(s.table)
	if err != nil {
		return err
	}
	if err := db.insertRow(schema, s.vals); err != nil {
		return err
	}
	fmt.Println("1 row inserted")
	return nil
}

func (db *DB) execSelect(s *SelectStmt) error {
	schema, err := db.getSchema(s.table)
	if err != nil {
		return err
	}
	rows, err := db.scanRows(s.table)
	if err != nil {
		return err
	}
	count := 0
	printHeader(schema, s.cols)
	for _, row := range rows {
		if s.where != nil && !evalExpr(s.where, row) {
			continue
		}
		printRow(row, s.cols)
		count++
	}
	fmt.Printf("(%d rows)\n", count)
	return nil

}

func (db *DB) execDelete(s *DeleteStmt) error {
	_, err := db.getSchema(s.table)
	if err != nil {
		return err
	}
	rows, err := db.scanRows(s.table)
	if err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		if s.where != nil && !evalExpr(s.where, row) {
			continue
		}
		key := rowKey(s.table, rowVals(row))
		deleted, err := db.deleteRow(s.table, key)
		if err != nil {
			return err
		}
		if deleted {
			count++
		}
	}
	fmt.Printf("%d row(s) deleted\n", count)
	return nil
}

func (db *DB) execUpdate(s *UpdateStmt) error {
	schema, err := db.getSchema(s.table)
	if err != nil {
		return err
	}
	rows, err := db.scanRows(s.table)
	if err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		if s.where != nil && !evalExpr(s.where, row) {
			continue
		}
		for col, val := range s.assignments {
			row[col] = val
		}
		vals := schemaOrderedVals(schema, row)
		key := rowKey(s.table, vals)
		data, err := encodeRow(schema, vals)
		if err != nil {
			return err
		}
		if err := db.kv.tree.Insert([]byte(key), data); err != nil {
			return err
		}
		count++
	}
	fmt.Printf("%d row(s) updated\n", count)
	return nil
}
func schemaOrderedVals(schema TableSchema, row map[string]string) []string {
	vals := make([]string, len(schema.Cols))
	for i, col := range schema.Cols {
		vals[i] = row[col.name]
	}
	return vals
}

func rowVals(row map[string]string) []string {
	vals := []string{}
	for _, v := range row {
		vals = append(vals, v)
		break
	}
	return vals
}

func printHeader(schema TableSchema, cols []string) {
	if len(cols) == 1 && cols[0] == "*" {
		for _, col := range schema.Cols {
			fmt.Printf("%-15s", col.name)
		}
	} else {
		for _, col := range cols {
			fmt.Printf("%-15s", col)
		}
	}
	fmt.Println()
}
