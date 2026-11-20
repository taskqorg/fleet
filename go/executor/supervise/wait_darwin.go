//go:build darwin

package supervise

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"time"

	"github.com/ebitengine/purego"
)

// waitid and errno through purego (no cgo).
var (
	waitid   func(idtype, id int32, info *byte, options int32) int32
	errnoPtr func() *int32
)

func init() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	if sym, err := purego.Dlsym(lib, "waitid"); err == nil {
		purego.RegisterFunc(&waitid, sym)
	}
	if sym, err := purego.Dlsym(lib, "__error"); err == nil {
		purego.RegisterFunc(&errnoPtr, sym)
	}
}

// WaitExited blocks until pid exits without reaping it (waitid with WNOWAIT): its PID stays taken until the caller
// reaps it. It reports false, at once, without waitid or when waitid fails: the process may still run.
func WaitExited(pid int) bool {
	if waitid == nil || errnoPtr == nil {
		return false
	}
	runtime.LockOSThread() // errno is per thread
	defer runtime.UnlockOSThread()
	var info [104]byte // siginfo_t
	for {
		if waitid(1 /* P_PID */, int32(pid), &info[0], 0x4|0x20 /* WEXITED|WNOWAIT */) == 0 {
			// macOS also reports a stopped child here (golang/go#19314): only an exit (si_code at offset 8: CLD_EXITED 1,
			// CLD_KILLED 2, CLD_DUMPED 3) counts; otherwise wait again.
			if code := binary.LittleEndian.Uint32(info[8:]); code >= 1 && code <= 3 {
				return true
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if *errnoPtr() != int32(syscall.EINTR) {
			return false
		}
	}
}
