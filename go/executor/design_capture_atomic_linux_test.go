//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A real short disk write and a subsequent retry must leave only complete
// archives. The child changes its own file limit without affecting the host.
func TestDesignCaptureAtomicPublicationRecoversFromLocalWriteFailure(t *testing.T) {
	const child = "FLEET_TEST_CAPTURE_FILE_LIMIT"
	if os.Getenv(child) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDesignCaptureAtomicPublicationRecoversFromLocalWriteFailure$")
		cmd.Env = append(os.Environ(), child+"=1")
		if result, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("capture publication child: %v\n%s", err, result)
		}
		return
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "fixture-capture.tar")
	if err := os.WriteFile(existing, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	dirfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirfd)
	signal.Ignore(unix.SIGXFSZ)
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 4, Max: original.Max}); err != nil {
		t.Fatal(err)
	}
	const content = "complete immutable archive"
	_, writtenErr := publishCaptureArchive(dirfd, "fixture", "capture.tar", strings.NewReader(content), int64(len(content)))
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(writtenErr, unix.EFBIG) {
		t.Fatalf("expected a real local write failure, got %v", writtenErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "fixture-capture.tar" {
		t.Fatalf("partial file was exposed: %v %v", entries, err)
	}
	path, err := publishCaptureArchive(dirfd, "fixture", "capture.tar", strings.NewReader(content), int64(len(content)))
	if err != nil || path != "fixture-1-capture.tar" {
		t.Fatalf("the retry did not publish a new complete file: %q %v", path, err)
	}
	for file, expected := range map[string]string{existing: "existing", filepath.Join(dir, path): content} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != expected {
			t.Fatalf("publication incorrectly changed %s: %q %v", file, data, err)
		}
	}
}
