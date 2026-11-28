package projectpublisher

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// activatedPublisherListener consumes the single named systemd listener. The
// production caller requires root ownership of both the descriptor and its path.
func activatedPublisherListener(path string, ownerUID uint32) (*net.UnixListener, error) {
	pid, count, name := os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS"), os.Getenv("LISTEN_FDNAMES")
	pidfdID, hasPIDFD := os.LookupEnv("LISTEN_PIDFDID")
	for _, key := range []string{"LISTEN_PID", "LISTEN_PIDFDID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		os.Unsetenv(key)
	}
	if pid != strconv.Itoa(os.Getpid()) || count != "1" || name != "publisher" {
		return nil, errors.New("publisher requires its systemd socket activation")
	}

	const fd = 3 // SD_LISTEN_FDS_START
	if _, err := unix.FcntlInt(fd, unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return nil, errors.New("publisher activation descriptor unavailable")
	}
	file := os.NewFile(fd, "publisher-listener")
	defer file.Close()
	if hasPIDFD && !validActivationPIDFD(pidfdID) {
		return nil, errors.New("publisher activation process identity invalid")
	}

	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != ownerUID {
		return nil, errors.New("publisher activation descriptor ownership or type invalid")
	}
	typ, typeErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	listening, listenErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	addr, addrErr := unix.Getsockname(fd)
	bound, isUnix := addr.(*unix.SockaddrUnix)
	if typeErr != nil || typ != unix.SOCK_STREAM || listenErr != nil || listening != 1 ||
		addrErr != nil || !isUnix || bound.Name != path {
		return nil, errors.New("publisher activation descriptor must listen on the publisher Unix stream")
	}

	if unix.Lstat(filepath.Dir(path), &st) != nil || st.Uid != ownerUID || st.Mode != unix.S_IFDIR|0755 ||
		unix.Lstat(path, &st) != nil || st.Uid != ownerUID || st.Mode != unix.S_IFSOCK|0666 {
		return nil, errors.New("publisher activation pathname ownership or mode invalid")
	}

	listener, err := net.FileListener(file)
	if err != nil {
		return nil, errors.New("publisher activation descriptor adoption failed")
	}
	// The service closes its copy on exit. systemd owns the endpoint and backlog.
	ln, ok := listener.(*net.UnixListener)
	if !ok {
		listener.Close()
		return nil, errors.New("publisher activation descriptor adoption failed")
	}
	ln.SetUnlinkOnClose(false)
	return ln, nil
}

// Recent systemd versions supply the pidfs inode to fence PID reuse.
func validActivationPIDFD(id string) bool {
	ino, err := strconv.ParseUint(id, 10, 64)
	if err != nil || ino == 0 {
		return false
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var fs unix.Statfs_t
	var st unix.Stat_t
	return unix.Fstatfs(fd, &fs) == nil && fs.Type == unix.PID_FS_MAGIC &&
		unix.Fstat(fd, &st) == nil && st.Ino == ino
}
