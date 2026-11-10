package main

// Previews: after an image or video artifact is stored, ffmpeg builds a 512 px thumbnail (and a poster frame for a video)
// at the lowest CPU priority, without network access, within FLEET_PREVIEW_TIMEOUT_SECONDS (default 120) and
// FLEET_PREVIEW_MEMORY_BYTES (default 1 GiB). Results are uploaded as artifacts that point to their original. A failure
// is logged and never fails the execution.

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ffmpegVersion = sync.OnceValue(func() string {
	out, err := exec.Command("ffmpeg", "-version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	if len(f) < 3 {
		return ""
	}
	return f[2]
})

func setting(name string, def uint64) uint64 {
	if v, err := strconv.ParseUint(os.Getenv(name), 10, 64); err == nil {
		return v
	}
	return def
}

// demuxer for a media type; anything else gets no preview. Forcing it (with the file protocol only) keeps agent-made
// playlists and concat lists from making ffmpeg read other files.
func demuxer(media string) string {
	switch media {
	case "image/png":
		return "png_pipe"
	case "image/jpeg":
		return "jpeg_pipe"
	case "image/webp":
		return "webp_pipe"
	case "image/gif":
		return "gif"
	case "video/mp4", "video/quicktime":
		return "mov"
	case "video/webm", "video/x-matroska":
		return "matroska"
	}
	return ""
}

// previewCommand: low priority, no network. Memory cap on Linux through `prlimit --as` in the command line (Go has no
// pre_exec); RLIMIT_AS is not enforced on macOS, where the time bound and idle priority remain.
func previewCommand(src, out string, poster bool, format string) *exec.Cmd {
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file", "-f", format, "-i", "file:" + src}
	if !poster {
		args = append(args, "-vf", "scale='min(512,iw)':-2")
	}
	args = append(args, "-frames:v", "1", out)
	var wrap []string
	switch {
	case runtime.GOOS == "darwin":
		wrap = []string{"-n", "19", "sandbox-exec", "-p", "(version 1)(allow default)(deny network*)"}
	case unshareWorks():
		wrap = []string{"-n", "19", "unshare", "--user", "--map-root-user", "--net"}
	default:
		wrap = []string{"-n", "19"}
	}
	if runtime.GOOS == "linux" && exists("/usr/bin/prlimit") {
		wrap = append(wrap, "prlimit", fmt.Sprintf("--as=%d", setting("FLEET_PREVIEW_MEMORY_BYTES", 1<<30)))
	}
	c := exec.Command("nice", append(append(wrap, "ffmpeg"), args...)...)
	return c
}

// unshareWorks: unshare can make a user and network namespace here. Ubuntu 24.04 blocks unprivileged user namespaces
// (kernel.apparmor_restrict_unprivileged_userns=1), so unshare exits 1 and every preview would fail; there the preview
// runs without the network namespace (still nice, prlimit and the time bound).
var unshareWorks = sync.OnceValue(func() bool {
	return exists("/usr/bin/unshare") && exec.Command("/usr/bin/unshare", "--user", "--map-root-user", "--net", "true").Run() == nil
})

func runPreview(c *exec.Cmd) error {
	var se strings.Builder
	c.Stderr = &se
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("ffmpeg exit %d", c.ProcessState.ExitCode())
		}
		return nil
	case <-time.After(time.Duration(setting("FLEET_PREVIEW_TIMEOUT_SECONDS", 120)) * time.Second):
		c.Process.Kill()
		<-done
		return fmt.Errorf("ffmpeg time limit")
	}
}

// buildPreviews builds the previews of a stored upload and queues them for upload.
func buildPreviews(db *sql.DB, body M, response any) {
	media := jStr(body, "mediaType")
	format := demuxer(media)
	if format == "" || ffmpegVersion() == "" || !jIsStr(body, "path") {
		return
	}
	src := jStr(body, "path")
	name := "artifact"
	if jIsStr(body, "name") {
		name = jStr(body, "name")
	}
	roles := []struct {
		role   string
		poster bool
	}{{"thumbnail", false}}
	if strings.HasPrefix(media, "video/") {
		roles = append([]struct {
			role   string
			poster bool
		}{{"poster", true}}, roles...)
	}
	for _, r := range roles {
		id := newUUID()
		out := filepath.Join(filepath.Dir(src), id+".jpg")
		if err := runPreview(previewCommand(src, out, r.poster, format)); err != nil {
			fmt.Fprintf(os.Stderr, "preview error for %s: %s\n", name, err)
			continue
		}
		enqueue(db, id, "upload", M{"path": out, "spool": true, "previews": false, "name": name + "." + r.role + ".jpg", "mediaType": "image/jpeg",
			"executionId": body["executionId"], "required": false, "derivedFrom": jAt(response, "artifactId"), "role": r.role})
	}
}
