package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The current definition of every function, trigger and policy (with each table's row-level security switches) is
// schema/*.sql, applied after the migrations (applySchemaFiles). Requirement S1 (docs/designs/fleet-standard.md).
// Risk: a migration creates or changes one outside the files, so two definitions disagree and the files stop being
// the one current definition. The files hold the migrated database's own deparsed definitions: an edit is applied by
// the next start, and FLEET_UPDATE_SCHEMA_FILES=1 rewrites the files in that form.
func schemaDefinitions(t *testing.T, e *testCoordEnv) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := e.ownerStore.Read(e.ctx, nil, func(q *Request) error {
		var err error
		out["functions.sql"], err = schemaFunctions(e, q.tx)
		if err != nil {
			return err
		}
		out["triggers.sql"], err = schemaTriggers(e, q.tx)
		if err != nil {
			return err
		}
		out["policies.sql"], err = schemaPolicies(e, q.tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const schemaFileHeader = "-- Current definitions (docs/designs/fleet-standard.md S1), applied after the migrations by applySchemaFiles\n" +
	"-- (store.go) whenever the file changes. Edit here, never in a migration; TestMigrationSchemaFiles keeps the file in\n" +
	"-- the database's deparsed form (FLEET_UPDATE_SCHEMA_FILES=1 go test ./coordinator -run TestMigrationSchemaFiles).\n"

func schemaFunctions(e *testCoordEnv, tx pgx.Tx) (string, error) {
	var b strings.Builder
	b.WriteString(schemaFileHeader)
	rows, err := tx.Query(e.ctx, `SELECT pg_get_functiondef(p.oid), p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')',
	  coalesce((SELECT string_agg(a.grantee::regrole::text, ',' ORDER BY a.grantee::regrole::text) FROM aclexplode(p.proacl) a
	   WHERE a.privilege_type = 'EXECUTE' AND a.grantee <> p.proowner AND a.grantee <> 0), ''),
	  p.proacl IS NOT NULL AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
	 FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace
	 ORDER BY p.proname, pg_get_function_identity_arguments(p.oid)`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var def, sig, grantees string
		var revoked bool
		if err := rows.Scan(&def, &sig, &grantees, &revoked); err != nil {
			return "", err
		}
		b.WriteString("\n" + strings.TrimRight(def, "\n") + ";\n")
		if revoked {
			b.WriteString("REVOKE ALL ON FUNCTION " + sig + " FROM PUBLIC;\n")
		}
		for _, g := range strings.Split(grantees, ",") {
			if g != "" {
				b.WriteString("GRANT EXECUTE ON FUNCTION " + sig + " TO " + g + ";\n")
			}
		}
	}
	return b.String(), rows.Err()
}

func schemaTriggers(e *testCoordEnv, tx pgx.Tx) (string, error) {
	var b strings.Builder
	b.WriteString(schemaFileHeader)
	rows, err := tx.Query(e.ctx, `SELECT c.relname, pg_get_triggerdef(t.oid, true)
	 FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
	 WHERE NOT t.tgisinternal AND c.relnamespace = 'public'::regnamespace ORDER BY c.relname, t.tgname`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	rel := ""
	for rows.Next() {
		var table, def string
		if err := rows.Scan(&table, &def); err != nil {
			return "", err
		}
		if table != rel {
			b.WriteString("\n-- " + table + "\n")
			rel = table
		}
		b.WriteString(strings.Replace(def, "CREATE TRIGGER", "CREATE OR REPLACE TRIGGER", 1) + ";\n")
	}
	return b.String(), rows.Err()
}

func schemaPolicies(e *testCoordEnv, tx pgx.Tx) (string, error) {
	var b strings.Builder
	b.WriteString(schemaFileHeader)
	rows, err := tx.Query(e.ctx, `SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity, coalesce(p.polname, ''),
	  coalesce(p.polpermissive, true), coalesce(p.polcmd::text, ''),
	  coalesce((SELECT string_agg(CASE WHEN r = 0 THEN 'public' ELSE r::regrole::text END, ', ' ORDER BY r) FROM unnest(p.polroles) r), ''),
	  coalesce(pg_get_expr(p.polqual, p.polrelid), ''), coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
	 FROM pg_class c LEFT JOIN pg_policy p ON p.polrelid = c.oid
	 WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p') AND (c.relrowsecurity OR c.relforcerowsecurity OR p.oid IS NOT NULL)
	 ORDER BY c.relname, p.polname`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	rel := ""
	cmds := map[string]string{"r": "SELECT", "a": "INSERT", "w": "UPDATE", "d": "DELETE", "*": "ALL"}
	for rows.Next() {
		var table, name, cmd, roles, using, check string
		var rls, force, permissive bool
		if err := rows.Scan(&table, &rls, &force, &name, &permissive, &cmd, &roles, &using, &check); err != nil {
			return "", err
		}
		if table != rel {
			b.WriteString("\n-- " + table + "\n")
			var sw []string
			if rls {
				sw = append(sw, "ENABLE ROW LEVEL SECURITY")
			}
			if force {
				sw = append(sw, "FORCE ROW LEVEL SECURITY")
			}
			if len(sw) > 0 {
				b.WriteString("ALTER TABLE " + table + " " + strings.Join(sw, ", ") + ";\n")
			}
			rel = table
		}
		if name == "" {
			continue
		}
		s := "CREATE POLICY " + name + " ON " + table
		if !permissive {
			s += " AS RESTRICTIVE"
		}
		s += " FOR " + cmds[cmd] + " TO " + roles
		if using != "" {
			s += " USING (" + using + ")"
		}
		if check != "" {
			s += " WITH CHECK (" + check + ")"
		}
		b.WriteString("DROP POLICY IF EXISTS " + name + " ON " + table + ";\n" + s + ";\n")
	}
	return b.String(), rows.Err()
}

func TestMigrationSchemaFiles(t *testing.T) {
	defs := schemaDefinitions(t, openTestCoordinator(t, false))
	for _, name := range schemaFileOrder {
		got := defs[name]
		path := filepath.Join("schema", name)
		if os.Getenv("FLEET_UPDATE_SCHEMA_FILES") == "1" {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != got {
			t.Errorf("%s differs from the migrated database: a definition changed outside it, or the file is not in deparsed form (FLEET_UPDATE_SCHEMA_FILES=1 go test ./coordinator -run TestMigrationSchemaFiles)", path)
		}
	}
}
