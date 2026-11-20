package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime/debug"
	"time"
)

// GET /api/health: public, no secrets. The commit comes from the Go build's VCS stamp, the build time from the
// binary's file time (ponytail: an -ldflags stamp if the file time ever stops meaning build time), and database is
// one round trip through the pool.
var healthApp *App

// healthReply: GET /api/health's body, fields in the byte order the map reply wrote them; a value not known is null.
type healthReply struct {
	AutoUpdate string  `json:"autoUpdate"`
	Built      *string `json:"built"`
	Commit     *string `json:"commit"`
	CommitTime *string `json:"commitTime"`
	Database   string  `json:"database"`
	Dirty      *bool   `json:"dirty"`
	Version    string  `json:"version"`
}

func init() {
	onStart(func(_ context.Context, a *App) { healthApp = a })
	publicRoute("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		out := healthReply{Version: version, AutoUpdate: "on", Database: "unknown"}
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					out.Commit = &s.Value
				case "vcs.time":
					out.CommitTime = &s.Value
				case "vcs.modified":
					dirty := s.Value == "true"
					out.Dirty = &dirty
				}
			}
		}
		if exe, err := os.Executable(); err == nil {
			if st, err := os.Stat(exe); err == nil {
				built := st.ModTime().UTC().Format(time.RFC3339)
				out.Built = &built
			}
		}
		code := 503 // still starting until startAll ran
		if a := healthApp; a != nil {
			code = 200
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if a.store.pool.Ping(ctx) == nil {
				out.Database, out.AutoUpdate = "ok", a.ownAutoUpdate(ctx)
			} else {
				out.Database, code = "unreachable", 503
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(out)
	})
}
