// Test databases. Default: PGlite in memory (superuser). With FLEET_TEST_PG=postgresql://admin:pass@host:port (a disposable
// server, CI only) every call creates a new database with the PDS18 roles of deploy/infrastructure/fleet-postgres.sql:
// migrations run as the owner fleet (NOSUPERUSER NOBYPASSRLS), the store runs as fleet_app (NOBYPASSRLS NOINHERIT), so
// privilege and row-level-security behaviour is the production one. Roles get passwords here because CI has no peer auth.
import pg from 'pg';
import { createStore } from '../../packages/coordinator/store.js';

export const PG = process.env.FLEET_TEST_PG;
type Store = ReturnType<typeof createStore>;
const url = (user: string, db: string) => { const u = new URL(PG!); u.username = user; u.password = user; u.pathname = '/' + db; return u.toString(); };
let n = 0;

async function admin(sql: string[], db = 'postgres') {
  const u = new URL(PG!); u.pathname = '/' + db;
  const c = new pg.Client({ connectionString: u.toString() }); await c.connect();
  try { for (const s of sql) await c.query(s); } finally { await c.end(); }
}

/** A migrated store (up to upTo), a way to apply the remaining migrations as the owner, and a scoped-caller helper. */
export async function freshDb(upTo = Infinity): Promise<{ s: Store; migrate: () => Promise<void>; scoped: <T>(scope: string, fn: (st: Store) => Promise<T>) => Promise<T> }> {
  if (!PG) {
    const s = createStore('memory://'); await s.migrate(upTo);
    let role = false;
    // PGlite runs as a superuser, which bypasses row-level security: a scoped caller switches to a NOBYPASSRLS role.
    const scoped = async <T>(scope: string, fn: (st: Store) => Promise<T>) => {
      if (!role) { await s.query("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet_app_test') THEN CREATE ROLE fleet_app_test NOLOGIN NOBYPASSRLS; END IF; END $$"); role = true; }
      await s.query('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO fleet_app_test'); await s.query('GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO fleet_app_test');
      return s.tx(async () => { await s.query('SET LOCAL ROLE fleet_app_test'); await s.query("SELECT set_config('fleet.project_ids',$1,true)", [scope]); return fn(s); });
    };
    return { s, migrate: () => s.migrate(), scoped };
  }
  const db = `fleet_t${process.pid}_${++n}`;
  await admin([
    "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet') THEN CREATE ROLE fleet LOGIN PASSWORD 'fleet' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS; END IF; END $$",
    "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet_app') THEN CREATE ROLE fleet_app LOGIN PASSWORD 'fleet_app' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT; END IF; END $$",
    `CREATE DATABASE ${db} OWNER fleet`, `REVOKE ALL ON DATABASE ${db} FROM PUBLIC`, `GRANT CONNECT ON DATABASE ${db} TO fleet_app`,
  ]);
  await admin(['REVOKE CREATE ON SCHEMA public FROM PUBLIC', 'GRANT USAGE ON SCHEMA public TO fleet_app',
    'ALTER DEFAULT PRIVILEGES FOR ROLE fleet IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO fleet_app',
    'ALTER DEFAULT PRIVILEGES FOR ROLE fleet IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO fleet_app'], db);
  const owner = createStore(url('fleet', db)); await owner.migrate(upTo);
  const s = createStore(url('fleet_app', db), { scope: '*' });
  const scoped = async <T>(scope: string, fn: (st: Store) => Promise<T>) => { const st = createStore(url('fleet_app', db), { scope }); try { return await fn(st); } finally { await st.close(); } };
  // s.close() also closes the owner and drops the database, so tests close one store as on PGlite.
  const closeApp = s.close.bind(s);
  s.close = async () => { await closeApp(); await owner.close(); await admin([`DROP DATABASE IF EXISTS ${db} WITH (FORCE)`]); };
  return { s, migrate: () => owner.migrate(), scoped };
}
