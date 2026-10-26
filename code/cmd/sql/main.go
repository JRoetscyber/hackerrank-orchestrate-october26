// Command sql runs a read-only query against data/audit.db and prints an aligned table.
//
//	go run ./cmd/sql "SELECT kind, COUNT(*) FROM evidence_documents GROUP BY kind"
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ghostaudit/internal/paths"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sql [-db path] \"<query>\"")
		os.Exit(2)
	}
	args := os.Args[1:]
	dbPath := paths.Join("data", "audit.db")
	if args[0] == "-db" && len(args) >= 3 {
		dbPath, args = args[1], args[2:]
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		fail(err)
	}
	defer db.Close()
	rows, err := db.Query(strings.Join(args, " "))
	if err != nil {
		fail(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var table [][]string
	width := make([]int, len(cols))
	for i, c := range cols {
		width[i] = len(c)
	}
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			fail(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			s := "NULL"
			if v.Valid {
				s = strings.ReplaceAll(v.String, "\n", " ")
			}
			if len(s) > 70 {
				s = s[:67] + "..."
			}
			row[i] = s
			width[i] = max(width[i], len(s))
		}
		table = append(table, row)
	}
	print := func(r []string) {
		for i, s := range r {
			fmt.Printf("%-*s  ", width[i], s)
		}
		fmt.Println()
	}
	print(cols)
	for _, r := range table {
		print(r)
	}
	fmt.Printf("(%d rows)\n", len(table))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
