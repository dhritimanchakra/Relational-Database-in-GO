package main

// main.go - a small SQL shell.
//
//	reldb -db data/reldb.data
//	echo "SELECT * FROM t;" | reldb

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dbPath := flag.String("db", "data/reldb.data", "path to the database file")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	db, err := OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer db.Close()

	interactive := isTerminal(os.Stdin)
	if interactive {
		fmt.Printf("reldb - durable B+tree SQL database (%s)\n", *dbPath)
		fmt.Println(`type SQL ending with ';'  |  .tables  .help  .exit`)
	}
	repl(db, os.Stdin, os.Stdout, interactive)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func repl(db *DB, in io.Reader, out io.Writer, prompt bool) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var buf strings.Builder
	showPrompt := func() {
		if !prompt {
			return
		}
		if buf.Len() == 0 {
			fmt.Fprint(out, "reldb> ")
		} else {
			fmt.Fprint(out, "   ...> ")
		}
	}

	showPrompt()
	for sc.Scan() {
		line := sc.Text()
		if buf.Len() == 0 && strings.HasPrefix(strings.TrimSpace(line), ".") {
			if quit := metaCommand(db, out, strings.TrimSpace(line)); quit {
				return
			}
			showPrompt()
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')

		stmts, rest := splitStatements(buf.String())
		for _, s := range stmts {
			runSQL(db, out, s)
		}
		buf.Reset()
		buf.WriteString(rest)
		if strings.TrimSpace(rest) == "" {
			buf.Reset()
		}
		showPrompt()
	}
	if strings.TrimSpace(buf.String()) != "" {
		runSQL(db, out, buf.String())
	}
}

func runSQL(db *DB, out io.Writer, sql string) {
	res, err := db.Exec(sql)
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return
	}
	printResult(out, res)
}

func metaCommand(db *DB, out io.Writer, cmd string) (quit bool) {
	switch cmd {
	case ".exit", ".quit":
		return true
	case ".tables":
		for _, n := range db.TableNames() {
			fmt.Fprintln(out, n)
		}
	case ".help":
		fmt.Fprintln(out, `CREATE TABLE t (id INT, name TEXT)   -- first column is the PRIMARY KEY
INSERT INTO t VALUES (1, 'alice')
SELECT * | a, b FROM t [WHERE x = 1 AND y > 2]
UPDATE t SET name = 'bob' WHERE id = 1
DELETE FROM t WHERE id = 1
BEGIN; ... COMMIT; | ROLLBACK;
.tables  .help  .exit`)
	default:
		fmt.Fprintf(out, "unknown command %q (try .help)\n", cmd)
	}
	return false
}

func splitStatements(s string) (stmts []string, rest string) {
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			inStr = !inStr
		case s[i] == ';' && !inStr:
			if stmt := strings.TrimSpace(s[start:i]); stmt != "" {
				stmts = append(stmts, stmt)
			}
			start = i + 1
		}
	}
	return stmts, s[start:]
}

func printResult(w io.Writer, r *Result) {
	if r.Cols == nil {
		fmt.Fprintln(w, r.Msg)
		return
	}
	widths := make([]int, len(r.Cols))
	for i, c := range r.Cols {
		widths[i] = len(c)
	}
	for _, row := range r.Rows {
		for i, v := range row {
			widths[i] = max(widths[i], len(v))
		}
	}
	line := func(cells []string) {
		for i, c := range cells {
			if i < len(cells)-1 {
				fmt.Fprintf(w, "%-*s | ", widths[i], c)
			} else {
				fmt.Fprint(w, c)
			}
		}
		fmt.Fprintln(w)
	}
	line(r.Cols)
	for i, wd := range widths {
		fmt.Fprint(w, strings.Repeat("-", wd))
		if i < len(widths)-1 {
			fmt.Fprint(w, "-+-")
		}
	}
	fmt.Fprintln(w)
	for _, row := range r.Rows {
		line(row)
	}
	noun := "rows"
	if len(r.Rows) == 1 {
		noun = "row"
	}
	fmt.Fprintf(w, "(%d %s)\n", len(r.Rows), noun)
}
