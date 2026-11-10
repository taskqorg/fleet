// Kernel-call spike: identity-bound signalling of an owned process, built with CGO_ENABLED=0.
// The executor records a child's identity when it starts it and later signals only that incarnation.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "reuse" {
		reuse()
		return
	}
	// A child that keeps its pid but becomes another program image: on darwin exec gives it a new pid version, the same
	// condition the kernel uses for a reused pid.
	c := exec.Command("/bin/sh", "-c", "sleep 0.5; exec sleep 30")
	must(c.Start())
	pid := c.Process.Pid
	id, err := identity(pid)
	must(err)
	tok, err := capture(pid)
	must(err)
	fmt.Printf("pid %d identity %s\n", pid, id)
	time.Sleep(time.Second)
	now, _ := identity(pid)
	fmt.Printf("after exec: identity %s (start time unchanged: %v)\n", now, now == id)
	err = signal(tok, syscall.SIGKILL)
	fmt.Printf("signal with the old token: %v (want an error)\n", err)
	if err == nil {
		os.Exit(1)
	}
	tok, err = capture(pid)
	must(err)
	must(signal(tok, syscall.SIGKILL))
	fmt.Println("signal with the current token: delivered")
	c.Wait()
	// Real reuse: the pid is gone; the old token must not reach whatever gets the pid next.
	if err := signal(tok, 0); err == nil {
		fmt.Println("signal after exit succeeded")
		os.Exit(1)
	}
	fmt.Println("signal after exit: refused")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// reuse: wait until another process takes a dead child's pid, then signal it with the dead child's token.
func reuse() {
	c := exec.Command("/bin/sleep", "30")
	must(c.Start())
	old := c.Process.Pid
	tok, err := capture(old)
	must(err)
	c.Process.Kill()
	c.Wait()
	for n := 1; ; n++ {
		c := exec.Command("/bin/sleep", "30")
		if c.Start() != nil {
			continue
		}
		if c.Process.Pid != old {
			c.Process.Kill()
			c.Wait()
			continue
		}
		err := signal(tok, syscall.SIGKILL)
		alive := c.Process.Signal(syscall.Signal(0)) == nil
		fmt.Printf("pid %d reused after %d starts; old token: %v; new process alive: %v\n", old, n, err, alive)
		c.Process.Kill()
		c.Wait()
		if err == nil || !alive {
			os.Exit(1)
		}
		return
	}
}
