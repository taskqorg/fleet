package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// write: the receipt shape (command row and its outbox row in one commit), ack printed only after Commit returns.
func write(path, tag string) {
	j, err := Open(path)
	must(err)
	out := bufio.NewWriter(os.Stdout)
	for i := 0; ; i++ {
		id := fmt.Sprintf("%s-%d", tag, i)
		must(j.Commit(func(j *Journal) ([]Op, error) {
			if _, ok := j.Rows["commands"][id]; ok {
				return nil, ErrConflict
			}
			body, _ := json.Marshal(map[string]string{"executionId": tag, "kind": "run"})
			row, _ := json.Marshal(map[string]string{"route": "/api/hosts/receipts", "body": id})
			return []Op{{"commands", id, body}, {"outbox", fmt.Sprint(j.Seq), row}}, nil
		}))
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
		j, err := Open(path)
		must(err)
		have := map[string]int{}
		for id := range j.Rows["commands"] {
			have[id]++
		}
		for _, row := range j.Rows["outbox"] {
			var o struct{ Body string }
			json.Unmarshal(row, &o)
			have[o.Body]++
		}
		for id := range ids {
			acked++
			if have[id] != 2 {
				lost++
			}
		}
		if len(j.Rows["commands"]) != len(j.Rows["outbox"]) {
			fmt.Println("torn commit")
			os.Exit(1)
		}
		j.f.Close()
	}
	fmt.Printf("crash: %d rounds, %d acknowledged, %d lost\n", rounds, acked, lost)
	if lost > 0 {
		os.Exit(1)
	}
}

func main() {
	switch os.Args[1] {
	case "write":
		write(os.Args[2], os.Args[3])
	case "crash":
		crash(os.Args[2], 20)
	}
}
