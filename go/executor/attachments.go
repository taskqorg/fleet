package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Message attachments (docs/designs/fleet-conversations.md, Attachments). The start payload lists each attachment with
// its path under FLEET_INPUT_DIR; stageInputs downloads it there before the harness starts. attachTurn adds the list to
// the turn text with absolute paths, types and sizes, so every harness can read the files; Codex also gets each image
// as a localImage input item (codexSession.turnRequest), and Claude gets the input folder as an added directory
// (claudeCommand: --add-dir), so its Read tool opens the files, images included.

// nativeImage: the image types the harnesses accept as image input.
func nativeImage(media string) bool {
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func sizeText(n float64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", n/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", n/(1<<10))
	}
	return fmt.Sprintf("%.0f B", n)
}

func unsafeRune(r rune) bool { return r < 0x20 || r >= 0x7f && r < 0xa0 || r == 0x2028 || r == 0x2029 }

// attachTurn: the turn text with the attachment list appended, and the local paths of attached images.
func attachTurn(p M, inputDir, harness string) {
	in, _ := decodeAs[struct {
		Attachments []json.RawMessage `json:"attachments"`
		Text        json.RawMessage   `json:"text"`
		AgentConfig json.RawMessage   `json:"agentConfig"`
	}](p)
	var profile map[string]json.RawMessage // agentConfig; not an object reads as none
	json.Unmarshal(in.AgentConfig, &profile)
	list := in.Attachments
	if len(list) == 0 {
		return
	}
	var lines []string
	images := []any{}
	for _, raw := range list {
		// An item that is not an object, or a field of another type, reads as absent (the item is then skipped).
		var a struct {
			Path      json.RawMessage `json:"path"`
			MediaType json.RawMessage `json:"mediaType"`
			Size      json.RawMessage `json:"size"`
		}
		json.Unmarshal(raw, &a)
		rel, _ := rawStr(a.Path)
		// stageInputs refuses the same paths; a control character would let a name start a line of its own in the turn.
		if strings.HasPrefix(rel, "/") || anyPart(rel, func(x string) bool { return x == "" || x == "." || x == ".." }) || strings.IndexFunc(rel, unsafeRune) >= 0 {
			continue
		}
		abs := filepath.Join(inputDir, filepath.FromSlash(rel))
		media, _ := rawStr(a.MediaType)
		if strings.IndexFunc(media, unsafeRune) >= 0 {
			media = "application/octet-stream"
		}
		var size float64
		json.Unmarshal(a.Size, &size)
		note := ""
		if nativeImage(media) {
			switch harness {
			case "codex":
				images = append(images, abs)
				note = "; also attached as an image"
			case "claude":
				// The tool grant Command passes as --tools decides: without Read the agent is not told to use it.
				grant := any(p) // the grant Command passes as --tools: the profile's tools when it names them
				if _, named := profile["tools"]; named {
					grant = p["agentConfig"]
				}
				if tools, err := claudeNativeTools(grant); err == nil && slices.Contains(tools, "Read") {
					note = "; an image: open it with the Read tool to see it"
				}
			}
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, %s%s)", abs, media, sizeText(size), note))
	}
	if len(lines) == 0 {
		return
	}
	text, _ := rawStr(in.Text)
	if text != "" {
		text += "\n\n"
	}
	p["text"] = text + "Attached files (sent with this message, in FLEET_INPUT_DIR):\n" + strings.Join(lines, "\n")
	if len(images) > 0 {
		p["localImages"] = images
	}
}
