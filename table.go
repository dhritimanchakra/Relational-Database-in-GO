package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type TableSchema struct {
	Name string
	Cols []Column
}

type DB struct {
	kv     *C
	tables map[string]TableSchema
}

func newDB() *DB {
	return &DB{
		kv:     newC(),
		tables: map[string]TableSchema{},
	}
}

func schemaKey(table string) string {
	return fmt.Sprintf("__schema__%s", table)
}

func (db *DB) saveSchema(schema TableSchema) error {
	data, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	return db.kv.tree.Insert([]byte(schemaKey(schema.Name)), data)
}

func (db *DB) loadSchema(table string) (TableSchema, error) {
	val, ok := db.kv.tree.Get([]byte(schemaKey(table)))
	if !ok {
		return TableSchema{}, fmt.Errorf("table %q does not exist", table)
	}
	var schema TableSchema
	if err := json.Unmarshal(val, &schema); err != nil {
		return TableSchema{}, err
	}
	return schema, nil
}
func (db *DB) createTable(stmt *CreateStmt) error {
	if _, ok := db.tables[stmt.table]; ok {
		return fmt.Errorf("table %q already exists", stmt.table)
	}
	schema := TableSchema{Name: stmt.table, Cols: stmt.cols}
	db.tables[stmt.table] = schema
	return db.saveSchema(schema)

}

func (db *DB) getSchema(table string) (TableSchema, error) {
	if schema, ok := db.tables[table]; ok {
		return schema, nil
	}
	schema, err := db.loadSchema(table)
	if err != nil {
		return TableSchema{}, err
	}
	db.tables[table] = schema
	return schema, nil
}

func rowKey(table string, vals []string) string {
	return fmt.Sprintf("%s__%s", table, strings.Join(vals[:1], "_"))
}

func encodeRow(schema TableSchema, vals []string) ([]byte, error) {
	if len(vals) != len(schema.Cols) {
		return nil, fmt.Errorf("expected %d values got %d", len(schema.Cols), len(vals))
	}
	row := map[string]string{}
	for i, col := range schema.Cols {
		row[col.name] = vals[i]
	}
	return json.Marshal(row)
}
func decodeRow(data []byte) (map[string]string, error) {
	row := map[string]string{}
	err := json.Unmarshal(data, &row)
	return row, err
}

func (db *DB) insertRow(schema TableSchema, vals []string) error {
	key := rowKey(schema.Name, vals)
	data, err := encodeRow(schema, vals)
	if err != nil {
		return err
	}
	return db.kv.tree.Insert([]byte(key), data)

}
func (db *DB) scanRows(table string) ([]map[string]string, error) {
	prefix := []byte(table + "__")
	var rows []map[string]string
	var scanNode func(node BNode) error
	scanNode = func(node BNode) error {
		for i := uint16(0); i < node.nkeys(); i++ {
			key := node.getKey(i)
			if len(key) == 0 {
				continue
			}
			if strings.HasPrefix(string(key), string(prefix)) {
				val := node.getVal(i)
				row, err := decodeRow(val)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if node.btype() == BNODE_NODE {
				child := BNode(db.kv.tree.get(node.getPtr(i)))
				if err := scanNode(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if db.kv.tree.root == 0 {
		return nil, nil
	}
	root := BNode(db.kv.tree.get(db.kv.tree.root))
	if err := scanNode(root); err != nil {
		return nil, err
	}
	return rows, nil
}
func (db *DB) deleteRow(table string, key string) (bool, error) {
	return db.kv.tree.Delete([]byte(key))
}

func printRow(row map[string]string, cols []string) {
	if len(cols) == 1 && cols[0] == "*" {
		for k, v := range row {
			fmt.Printf("%s: %s  ", k, v)
		}
		fmt.Println()
		return
	}
	for _, col := range cols {
		fmt.Printf("%s: %s  ", col, row[col])
	}
	fmt.Println()
}
