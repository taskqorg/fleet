// Test databases.
// - Default: PGlite in memory. It runs as a superuser, so it cannot show privilege or row-level-security errors.
// - FLEET_TEST_PG=postgresql://admin:pass@host:port (a disposable server; CI job postgres-roles): the database "fleet" is
//   dropped and created again for every test by running deploy/infrastructure/fleet-postgres.sql itself (psql), so roles
//   and grants are exactly the PDS18 ones: migrations run as the owner fleet, the store as fleet_app. The roles get the
//   password of their own name here, because CI has no peer authentication. Tests using it must run one file at a time
//   (--test-concurrency=1): they share the database "fleet".
// - FLEET_TEST_DATABASE_URL: one existing disposable database, used as is by the concurrency and load tests for manual
//   runs; when it is unset and FLEET_TEST_PG is set, those tests use the owner URL of a fresh "fleet" database.
import { execFileSync } from 'node:child_process';
import pg from 'pg';
import { createStore, type Store } from '../../packages/coordinator/store.ts';

export const PG = process.env.FLEET_TEST_PG;
const SQL = new URL('../../deploy/infrastructure/fleet-postgres.sql', import.meta.url).pathname;
const withDb = (base: string, db: string, user?: string) => { const u = new URL(base); if (user) { u.username = user; u.password = user; } u.pathname = '/' + db; return u.toString(); };
const ownerUrl = () => withDb(PG!, 'fleet', 'fleet'), appUrl = () => withDb(PG!, 'fleet', 'fleet_app');

async function admin(sql: string[]) {
  const c = new pg.Client({ connectionString: withDb(PG!, 'postgres') }); await c.connect();
  try { const out = []; for (const s of sql) out.push((await c.query(s)).rows); return out; } finally { await c.end(); }
}

// The attributes fleet-postgres.sql creates. The script skips roles that already exist, so a server with differently
// configured roles would test something else: fail clearly instead.
const EXPECTED: Record<string, { rolsuper: boolean; rolbypassrls: boolean; rolinherit?: boolean; rolcanlogin: boolean }> = {
  fleet: { rolsuper: false, rolbypassrls: false, rolcanlogin: true }, fleet_app: { rolsuper: false, rolbypassrls: false, rolinherit: false, rolcanlogin: true },
};
let prepared = false;
/** Drop and recreate the database "fleet" with deploy/infrastructure/fleet-postgres.sql; returns its owner URL. */
export async function recreateFleetDatabase(): Promise<string> {
  await admin(["DROP DATABASE IF EXISTS fleet WITH (FORCE)"]);
  execFileSync('psql', [withDb(PG!, 'postgres'), '-v', 'ON_ERROR_STOP=1', '-q', '-f', SQL], { stdio: ['ignore', 'ignore', 'inherit'] });
  if (!prepared) {
    const [roles] = await admin(["SELECT rolname, rolsuper, rolbypassrls, rolinherit, rolcanlogin FROM pg_roles WHERE rolname IN ('fleet','fleet_app')"]);
    for (const [name, want] of Object.entries(EXPECTED)) {
      const got = roles.find((r: any) => r.rolname === name);
      if (!got) throw new Error(`fleet-postgres.sql did not create role ${name}`);
      for (const [k, v] of Object.entries(want)) if (got[k] !== v) throw new Error(`role ${name} has ${k}=${got[k]}, fleet-postgres.sql expects ${v}: use a fresh test server`);
    }
    await admin(["ALTER ROLE fleet PASSWORD 'fleet'", "ALTER ROLE fleet_app PASSWORD 'fleet_app'"]);
    prepared = true;
  }
  return ownerUrl();
}

/** A migrated store (up to upTo), a way to apply the remaining migrations as the owner, and a scoped-caller helper. */
export async function freshDb(upTo = Infinity): Promise<{ s: Store; migrate: () => Promise<void>; scoped: <T>(scope: string, fn: (st: Store) => Promise<T>) => Promise<T> }> {
  if (!PG) {
    const s = createStore('memory://'); await s.migrate(upTo);
    let granted = false;
    // PGlite runs as a superuser, which bypasses row-level security: a scoped caller switches to a NOBYPASSRLS role.
    const scoped = async <T>(scope: string, fn: (st: Store) => Promise<T>) => {
      if (!granted) {
        await s.query("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet_app_test') THEN CREATE ROLE fleet_app_test NOLOGIN NOBYPASSRLS; END IF; END $$");
        await s.query('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO fleet_app_test'); await s.query('GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO fleet_app_test');
        granted = true;
      }
      return s.tx(async () => { await s.query('SET LOCAL ROLE fleet_app_test'); await s.query("SELECT set_config('fleet.project_ids',$1,true)", [scope]); return fn(s); });
    };
    return { s, migrate: () => s.migrate(), scoped };
  }
  const owner = createStore(await recreateFleetDatabase()); await owner.migrate(upTo);
  const s = createStore(appUrl(), { scope: '*' });
  const scoped = async <T>(scope: string, fn: (st: Store) => Promise<T>) => { const st = createStore(appUrl(), { scope }); try { return await fn(st); } finally { await st.close(); } };
  // s.close() also closes the owner store, so tests close one store as on PGlite.
  const closeApp = s.close.bind(s);
  s.close = async () => { await closeApp(); await owner.close(); };
  return { s, migrate: () => owner.migrate(), scoped };
}
