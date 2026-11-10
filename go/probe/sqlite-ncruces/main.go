// Store spike: the executor journal's transaction shape on a pure-Go SQLite driver, with a kill -9 crash test,
// two concurrent writer processes, and a read of an existing executor journal.sqlite.
package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const schema = `CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,terminal INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,native TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS delegation_requests(id TEXT PRIMARY KEY,body TEXT NOT NULL,response TEXT);
CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL);`

// Same settings as the Rust executor: WAL, synchronous=FULL, 5 s busy timeout, writer lock taken at BEGIN.
func open(path string) *sql.DB {
	db, err := sql.Open(driverName, "file:"+path+"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	must(err)
	_, err = db.Exec(schema)
	must(err)
	return db
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// write: the receipt shape (command row and its outbox row in one transaction), ack printed only after commit.
func write(path, tag string) {
	db := open(path)
	out := bufio.NewWriter(os.Stdout)
	for i := 0; ; i++ {
		id := fmt.Sprintf("%s-%d", tag, i)
		tx, err := db.Begin()
		must(err)
		_, err = tx.Exec("INSERT INTO commands(id,body) VALUES(?,?)", id, `{"executionId":"`+tag+`","kind":"run"}`)
		must(err)
		_, err = tx.Exec("INSERT INTO outbox(route,body) VALUES('/api/hosts/receipts',?)", id)
		must(err)
		must(tx.Commit())
		fmt.Fprintln(out, id)
		out.Flush()
	}
}

func crash(path string, rounds int) {
	lost, acked := 0, 0
	for r := 0; r < rounds; r++ {
		var procs []*exec.Cmd
		ids := make(chan string, 1<<20)
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			c := exec.Command(os.Args[0], "write", path, fmt.Sprintf("r%dw%d", r, w))
			c.Stderr = os.Stderr
			p, _ := c.StdoutPipe()
			must(c.Start())
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := bufio.NewScanner(p)
				for s.Scan() {
					ids <- s.Text()
				}
			}()
			procs = append(procs, c)
		}
		time.Sleep(time.Duration(100+rand.Intn(300)) * time.Millisecond)
		for _, c := range procs {
			c.Process.Kill() // SIGKILL
		}
		for _, c := range procs {
			c.Wait()
		}
		wg.Wait()
		close(ids)
		db := open(path)
		have := map[string]int{}
		for _, q := range []string{"SELECT id FROM commands", "SELECT body FROM outbox"} {
			rows, err := db.Query(q)
			must(err)
			for rows.Next() {
				var id string
				rows.Scan(&id)
				have[id]++
			}
		}
		for id := range ids {
			acked++
			if have[id] != 2 {
				lost++
			}
		}
		var a, b int
		db.QueryRow("SELECT (SELECT count(*) FROM commands),(SELECT count(*) FROM outbox)").Scan(&a, &b)
		if a != b {
			fmt.Println("torn transaction: commands", a, "outbox", b)
			os.Exit(1)
		}
		db.Close()
	}
	fmt.Printf("crash: %d rounds, %d acknowledged, %d lost\n", rounds, acked, lost)
	if lost > 0 {
		os.Exit(1)
	}
}

// compat: read a journal written by the Rust executor, including its json_extract query.
func compat(path string) {
	db := open(path)
	var tables []string
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	must(err)
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	var stops int
	must(db.QueryRow("SELECT count(*) FROM commands WHERE json_extract(body,'$.kind')='stop'").Scan(&stops))
	var mode string
	db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	fmt.Printf("compat: tables %s, stop commands %d, journal_mode %s\n", strings.Join(tables, ","), stops, mode)
}

func main() {
	switch os.Args[1] {
	case "write":
		write(os.Args[2], os.Args[3])
	case "crash":
		crash(os.Args[2], 20)
	case "compat":
		compat(os.Args[2])
	}
}
