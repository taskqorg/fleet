package main

import "testing"

// fleet-update decides the pre-deploy dump from this number (fleet-self-update.md, step 8).
func TestLatestMigration(t *testing.T) {
	if n := latestMigration("-- migration 3\nSELECT 1;\n-- migration 12\nSELECT 2;\n-- migration 9\n"); n != 12 {
		t.Fatalf("latestMigration = %d", n)
	}
	if latestMigration(schemaSQL) < 100 {
		t.Fatal("schema.sql migrations not found")
	}
}
