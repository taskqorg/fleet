package main

import (
	"regexp"
	"strconv"
	"testing"
)

// lastNumberedMigration: schema.sql's numbered sections end here; later migrations are files in migrations/
// (migrations/README.md).
const lastNumberedMigration = 121

// Requirement: each migration runs once (migrate). Risk: two branches each add the next number, both pass CI alone, and
// after both merge one section is skipped on every database that applied the other. Files have no number to share, so
// schema.sql takes no new section, and a file's name is its id.
func TestMigrationIDsUnique(t *testing.T) {
	last := 0
	for _, m := range regexp.MustCompile(`(?m)^-- migration (\d+)`).FindAllStringSubmatch(schemaSQL, -1) {
		n, _ := strconv.Atoi(m[1])
		if n <= last {
			t.Fatalf("migration %d follows %d in schema.sql: write it as a file in migrations/ (migrations/README.md)", n, last)
		}
		if n > lastNumberedMigration {
			t.Fatalf("migration %d in schema.sql: new migrations are files, migrations/<yyyymmdd>T<hhmm>Z_<slug>.sql (migrations/README.md)", n)
		}
		last = n
	}
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.id] {
			t.Fatalf("migration %s twice", m.id)
		}
		seen[m.id] = true
	}
}
