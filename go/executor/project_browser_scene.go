package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

const sceneChromium = "fleet-scene-chromium"
const maxSceneRecipe = 256 << 10
const maxSceneHTML = 4 << 20

var sceneName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var sceneSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errScene = errors.New("private scene unavailable or invalid")

type browserSceneScope struct {
	Checkout  string `json:"checkout"`
	Execution string `json:"execution"`
}

func (s browserSceneScope) directory() string {
	return filepath.Join(s.Checkout, ".egm", "managed-scenes", s.Execution)
}

func (s browserSceneScope) read(name, htmlSHA string) (M, error) {
	if !filepath.IsAbs(s.Checkout) || !sceneName.MatchString(s.Execution) ||
		!sceneName.MatchString(name) || !sceneSHA.MatchString(htmlSHA) {
		return nil, errScene
	}
	dir := []string{".egm", "managed-scenes", s.Execution, name}
	recipe, err := readSceneFile(s.Checkout, append(dir, "recipe.json"), maxSceneRecipe)
	if err != nil {
		return nil, err
	}
	html, err := readSceneFile(s.Checkout, append(dir, "preview.html"), maxSceneHTML)
	if err != nil || sha(html) != htmlSHA {
		return nil, errScene
	}
	return M{"recipe": string(recipe), "html": html}, nil
}

func readSceneFile(checkout string, parts []string, limit int64) ([]byte, error) {
	f, err := openInside(checkout, parts)
	if err != nil {
		return nil, errScene
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1 ||
		owner(st) != os.Geteuid() || st.Size() <= 0 || st.Size() > limit {
		return nil, errScene
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errScene
	}
	return raw, nil
}

// The scene dependency speaks CDP over inherited pipes. It has no debug socket.
func scenePipe(fd uintptr) (*os.File, error) {
	file := os.NewFile(fd, "scene-cdp")
	if file == nil {
		return nil, errors.New("scene CDP pipe missing")
	}
	st, err := file.Stat()
	if err == nil {
		if st.Mode()&os.ModeNamedPipe != 0 {
			return file, nil
		}
		// libuv can implement a child-process pipe with an inherited Unix socketpair.
		if st.Mode()&os.ModeSocket != 0 {
			addr, err := unix.Getpeername(int(fd))
			if _, local := addr.(*unix.SockaddrUnix); err == nil && local {
				return file, nil
			}
		}
	}
	file.Close()
	return nil, errors.New("scene CDP must use local inherited streams")
}

func serveBrowserScene(name, htmlSHA string) error {
	reply, conn, err := mcpOwnerCall(os.Getenv("FLEET_MCP_OWNER"), M{
		"action": "browser-scene", "generation": os.Getenv("FLEET_MCP_GENERATION"),
		"scene": name, "htmlSha256": htmlSHA,
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	return json.NewEncoder(os.Stdout).Encode(M{"recipe": reply["recipe"], "html": reply["html"]})
}

func runSceneChromium(args []string) error {
	if len(args) < 2 || !filepath.IsAbs(args[0]) || !slices.Contains(args[1:], "--remote-debugging-pipe") {
		return errors.New("scene Chromium requires CDP pipes")
	}
	return execSceneChromium(args)
}
