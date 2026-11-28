package projectpublisher

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Exercise fd 3 in a child so malformed activation cannot affect the test
// runner. Keep the parent's listener open as systemd does across restarts.
func TestPublisherSocketActivation(t *testing.T) {
	if path := os.Getenv("TEST_PUBLISHER_SOCKET"); path != "" {
		checkPublisherActivation(t, path)
		return
	}
	dir, err := os.MkdirTemp("/tmp", "fleet-publisher-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	publisherChmod(t, dir, 0755)
	path := filepath.Join(dir, "publisher.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	publisherChmod(t, path, 0666)
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	regular, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()

	start := func(t *testing.T, file *os.File, address, vars, want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", vars+`; exec "$@"`, "fixture", os.Args[0], "-test.run=^TestPublisherSocketActivation$")
		cmd.Env = []string{"TEST_PUBLISHER_SOCKET=" + address, "TEST_PUBLISHER_ERROR=" + want}
		if file != nil {
			cmd.ExtraFiles = []*os.File{file}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("activation fixture: %v\n%s", err, out)
		}
	}
	const (
		env           = "export LISTEN_PID=$$ LISTEN_FDS=1 LISTEN_FDNAMES=publisher"
		badEnv        = "publisher requires its systemd socket activation"
		badDescriptor = "publisher activation descriptor ownership or type invalid"
		badStream     = "publisher activation descriptor must listen on the publisher Unix stream"
		badPath       = "publisher activation pathname ownership or mode invalid"
	)
	for _, tc := range []struct {
		name             string
		fd               *os.File
		path, vars, want string
	}{
		{"first start", stream, path, env, ""},
		{"restart on the same listener", stream, path, env, ""},
		{"missing activation", stream, path, "unset LISTEN_PID LISTEN_FDS LISTEN_FDNAMES", badEnv},
		{"wrong PID", stream, path, env + "; export LISTEN_PID=0", badEnv},
		{"multiple descriptors", stream, path, env + "; export LISTEN_FDS=2", badEnv},
		{"wrong name", stream, path, env + "; export LISTEN_FDNAMES=other", badEnv},
		{"current pidfd identity", stream, path, env + "; export TEST_PUBLISHER_PIDFD=1", ""},
		{"stale pidfd identity", stream, path, env + "; export LISTEN_PIDFDID=1", "publisher activation process identity invalid"},
		{"non-socket fd", regular, path, env, badDescriptor},
		{"wrong descriptor owner", stream, path, env + "; export TEST_WRONG_PUBLISHER_OWNER=1", badDescriptor},
		{"wrong address", stream, path + ".other", env, badStream},
	} {
		t.Run(tc.name, func(t *testing.T) { start(t, tc.fd, tc.path, tc.vars, tc.want) })
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("service close changed the socket pathname: %v", err)
	}

	for _, typ := range []int{unix.SOCK_DGRAM, unix.SOCK_STREAM} {
		bound := filepath.Join(dir, "non-listening-"+strconv.Itoa(typ))
		fd, err := unix.Socket(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(fd), "non-listener")
		defer file.Close()
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: bound}); err != nil {
			t.Fatal(err)
		}
		start(t, file, bound, env, badStream)
	}
	publisherChmod(t, dir, 0777)
	start(t, stream, path, env, badPath)
	publisherChmod(t, dir, 0755)
	publisherChmod(t, path, 0600)
	start(t, stream, path, env, badPath)
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	start(t, stream, path, env, badPath)
}

func checkPublisherActivation(t *testing.T, path string) {
	if os.Getenv("TEST_PUBLISHER_PIDFD") != "" {
		fd, err := unix.PidfdOpen(os.Getpid(), 0)
		if errors.Is(err, unix.ENOSYS) {
			t.Skip("kernel lacks pidfd support")
		}
		if err != nil {
			t.Fatal(err)
		}
		var fs unix.Statfs_t
		var st unix.Stat_t
		fsErr, statErr := unix.Fstatfs(fd, &fs), unix.Fstat(fd, &st)
		unix.Close(fd)
		if fsErr != nil || statErr != nil {
			t.Fatalf("pidfd metadata: %v, %v", fsErr, statErr)
		}
		if fs.Type != unix.PID_FS_MAGIC {
			t.Skip("kernel lacks pidfs process identities")
		}
		t.Setenv("LISTEN_PIDFDID", strconv.FormatUint(st.Ino, 10))
	}
	owner := uint32(os.Geteuid())
	if os.Getenv("TEST_WRONG_PUBLISHER_OWNER") != "" {
		owner++
	}
	ln, err := activatedPublisherListener(path, owner)
	for _, key := range []string{"LISTEN_PID", "LISTEN_PIDFDID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if _, exists := os.LookupEnv(key); exists {
			t.Fatalf("%s leaked beyond activation", key)
		}
	}
	if want := os.Getenv("TEST_PUBLISHER_ERROR"); want != "" {
		if ln != nil {
			ln.Close()
		}
		if err == nil || err.Error() != want {
			t.Fatalf("activation error = %v, want %q", err, want)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := unix.FcntlInt(3, unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("inherited fd 3 remains open: %v", err)
	}
	raw, err := ln.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagsErr error
	if err := raw.Control(func(fd uintptr) {
		flags, flagsErr = unix.FcntlInt(fd, unix.F_GETFD, 0)
	}); err != nil || flagsErr != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("adopted fd not close-on-exec: flags=%d, errors=%v, %v", flags, err, flagsErr)
	}
	client, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := ln.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if uid, ok := withPeer(context.Background(), conn).Value(peerKey{}).(uint32); !ok || uid != uint32(os.Geteuid()) {
		t.Fatalf("accepted peer UID = %d (available: %v), want %d", uid, ok, os.Geteuid())
	}
}

func publisherChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
