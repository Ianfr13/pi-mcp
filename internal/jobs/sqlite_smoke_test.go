package jobs

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteDriverSmoke(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, v INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t(id,v) VALUES('a',1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var v int
	if err := db.QueryRow(`SELECT v FROM t WHERE id='a'`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("select v=%d err=%v", v, err)
	}
}
