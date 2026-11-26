package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strings"
	"time"
)

const postgresBackupCause = "problem:backup:missing"

func init() {
	onStart(func(ctx context.Context, a *App) {
		host, _ := os.Hostname()
		if short, _, _ := strings.Cut(host, "."); short != "pds18" {
			return
		}
		account, err := user.Current()
		if err != nil || account.Username != "fleet" {
			return
		}
		every(ctx, "postgres-backup", time.Minute, func(ctx context.Context) error {
			text := postgresBackupProblem("/backup/fleet/daily", time.Now())
			var problems []Problem
			if text != "" {
				problems = []Problem{{Cause: postgresBackupCause, Text: text, RefreshText: true}}
			}
			return a.store.Write(ctx, nil, []string{"problems:postgres-backup"}, func(q *Request) error {
				return problemsSync(q, "postgres-backup", problems)
			})
		})
	})
}

func postgresBackupProblem(dir string, now time.Time) string {
	latest, err := latestPostgresDump(dir)
	switch {
	case err != nil:
		return fmt.Sprintf("pds18 nightly PostgreSQL backup freshness is unknown. %v", err)
	case latest.IsZero():
		return "pds18 has no completed nightly PostgreSQL dump in /backup/fleet/daily."
	case latest.After(now):
		return "pds18 nightly PostgreSQL backup freshness is unknown. The newest dump has a future timestamp."
	case now.Sub(latest) > 26*time.Hour:
		return fmt.Sprintf("pds18 nightly PostgreSQL dump is older than 26 hours. Last completed %s.", latest.UTC().Format(time.RFC3339))
	default:
		return ""
	}
}

func latestPostgresDump(dir string) (time.Time, error) {
	var latest time.Time
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return latest, nil
	}
	if err != nil {
		return latest, err
	}
	for _, entry := range entries {
		if _, err := time.Parse("fleet-20060102.dump", entry.Name()); err != nil || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // retention removed an older dump after ReadDir
		}
		if err != nil {
			return latest, err
		}
		if info.Size() > 0 && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest, nil
}
