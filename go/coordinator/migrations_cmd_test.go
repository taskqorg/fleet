package main

import (
	"strings"
	"testing"
)

// fleet-update decides the pre-deploy dump from these ids (fleet-self-update.md, step 8): every numbered section of
// schema.sql and every file of migrations/, in apply order.
func TestMigrationIDs(t *testing.T) {
	out, err := migrationIDs()
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Split(out, "\n")
	if len(ids) < 100 || ids[0] != "1" {
		t.Fatalf("migration ids = %.80q", out)
	}
}
