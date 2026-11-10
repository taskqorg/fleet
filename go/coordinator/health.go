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

func init() {
	onStart(func(_ context.Context, a *App) { healthApp = a })
	publicRoute("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"commit": nil, "commitTime": nil, "dirty": nil, "built": nil, "database": "unknown"}
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					out["commit"] = s.Value
				case "vcs.time":
					out["commitTime"] = s.Value
				case "vcs.modified":
					out["dirty"] = s.Value == "true"
				}
			}
		}
		if exe, err := os.Executable(); err == nil {
			if st, err := os.Stat(exe); err == nil {
				out["built"] = st.ModTime().UTC().Format(time.RFC3339)
			}
		}
		code := 503 // still starting until startAll ran
		if a := healthApp; a != nil {
			code = 200
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if a.store.pool.Ping(ctx) == nil {
				out["database"] = "ok"
			} else {
				out["database"], code = "unreachable", 503
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(out)
	})
}
