package main

// The schema is part of the binary: schema.sql next to this file is the schema source (committed).

import _ "embed"

//go:embed schema.sql
var schemaSQL string
