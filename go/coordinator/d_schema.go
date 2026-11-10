package main

// The schema is part of the binary: go generate copies packages/coordinator/db/schema.sql (the seam, unchanged) next to
// this file; the copy is not committed.

//go:generate cp ../../packages/coordinator/db/schema.sql schema.sql

import _ "embed"

//go:embed schema.sql
var schemaSQL string
