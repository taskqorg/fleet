//! Postgres access: a small connection pool, per-route transactions with scope locks, and the schema migrations
//! (docs/designs/fleet-stack.md#trial-slice-l1).
use std::collections::HashMap;
use std::ops::Deref;
use std::sync::{Arc, Mutex};
use tokio::sync::{OwnedMutexGuard, OwnedSemaphorePermit, Semaphore};
use tokio_postgres::{Client, Config, NoTls};

/// An answer other than 200: status and message; `retry` marks a serialization failure or deadlock.
#[derive(Debug)]
pub struct ApiErr {
    pub status: u16,
    pub msg: String,
    pub retry: bool,
}
pub fn fail<T>(status: u16, msg: &str) -> Result<T, ApiErr> {
    Err(ApiErr { status, msg: msg.into(), retry: false })
}
impl From<tokio_postgres::Error> for ApiErr {
    fn from(e: tokio_postgres::Error) -> Self {
        let code = e.code().map(|c| c.code().to_string()).unwrap_or_default();
        match code.as_str() {
            "40001" | "40P01" => ApiErr { status: 500, msg: e.to_string(), retry: true },
            // A write outside the person's scope is refused by row-level security: 403, not 500.
            "42501" => ApiErr { status: 403, msg: "Not in your scope".into(), retry: false },
            _ => {
                eprintln!("database error: {e:?}");
                ApiErr { status: 500, msg: "Internal error".into(), retry: false }
            }
        }
    }
}

/// Row-level security scope of one person (fleet.project_ids, fleet.person); None is the coordinator's own '*' scope.
#[derive(Clone)]
pub struct Scope {
    pub project_ids: String,
    pub person: String,
}

/// Read: one snapshot. Write: read committed under the scope lock named by the key.
pub enum Mode {
    Read,
    Write(String),
}
pub const GLOBAL: &str = "fleet:write";

struct PoolInner {
    cfg: Config,
    idle: Mutex<Vec<Client>>,
    permits: Arc<Semaphore>,
}
pub struct Db {
    pool: Arc<PoolInner>,
    // ponytail: one mutex per key, never removed; a map entry per execution is a few bytes for the process lifetime.
    locks: Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>,
}

pub struct Conn {
    client: Option<Client>,
    pool: Arc<PoolInner>,
    /// A connection dropped while a transaction is open (cancelled request) is closed, never reused.
    in_tx: bool,
    _permit: OwnedSemaphorePermit,
}
impl Deref for Conn {
    type Target = Client;
    fn deref(&self) -> &Client {
        self.client.as_ref().unwrap()
    }
}
impl Drop for Conn {
    fn drop(&mut self) {
        if let Some(c) = self.client.take() {
            if !self.in_tx && !c.is_closed() {
                self.pool.idle.lock().unwrap().push(c)
            }
        }
    }
}

pub async fn connect(cfg: &Config) -> Result<Client, tokio_postgres::Error> {
    let (client, conn) = cfg.connect(NoTls).await?;
    tokio::spawn(async move {
        if let Err(e) = conn.await {
            eprintln!("postgres connection: {e}")
        }
    });
    Ok(client)
}

/// The URL with the operator scope '*' as a startup option, set before the connection runs any query.
pub fn config(url: &str) -> Config {
    let mut cfg: Config = url.parse().unwrap_or_else(|e| panic!("bad database URL: {e}"));
    cfg.options("-c fleet.project_ids=*");
    cfg
}

pub struct Tx {
    c: Conn,
    _lock: Option<OwnedMutexGuard<()>>,
}
impl Deref for Tx {
    type Target = Client;
    fn deref(&self) -> &Client {
        &self.c
    }
}
impl Tx {
    pub async fn commit(mut self) -> Result<(), ApiErr> {
        self.c.batch_execute("COMMIT").await?;
        self.c.in_tx = false;
        Ok(())
    }
    pub async fn rollback(mut self) {
        if self.c.batch_execute("ROLLBACK").await.is_ok() {
            self.c.in_tx = false
        }
    }
}

impl Db {
    pub fn new(cfg: Config, size: usize) -> Db {
        Db {
            pool: Arc::new(PoolInner { cfg, idle: Mutex::new(vec![]), permits: Arc::new(Semaphore::new(size)) }),
            locks: Mutex::new(HashMap::new()),
        }
    }
    pub async fn conn(&self) -> Result<Conn, ApiErr> {
        let permit = self.pool.permits.clone().acquire_owned().await.unwrap();
        let idle = self.pool.idle.lock().unwrap().pop();
        let client = match idle {
            Some(c) => c,
            None => connect(&self.pool.cfg).await?,
        };
        Ok(Conn { client: Some(client), pool: self.pool.clone(), in_tx: false, _permit: permit })
    }
    /// Opens a transaction. A writer first waits in the in-process queue of its scope key (before it takes a connection),
    /// then takes pg_advisory_xact_lock on the same key, which also orders writers in other processes.
    pub async fn begin(&self, mode: &Mode, scope: Option<&Scope>) -> Result<Tx, ApiErr> {
        let lock = match mode {
            Mode::Write(k) => {
                let m = self.locks.lock().unwrap().entry(k.clone()).or_default().clone();
                Some(m.lock_owned().await)
            }
            Mode::Read => None,
        };
        let mut c = self.conn().await?;
        c.in_tx = true;
        c.batch_execute(match mode {
            Mode::Read => "BEGIN READ ONLY ISOLATION LEVEL REPEATABLE READ",
            Mode::Write(_) => "BEGIN ISOLATION LEVEL READ COMMITTED",
        })
        .await?;
        if let Some(s) = scope {
            c.execute("SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)", &[&s.project_ids, &s.person]).await?;
        }
        if let Mode::Write(k) = mode {
            c.execute("SELECT pg_advisory_xact_lock(hashtext($1))", &[k]).await?;
        }
        Ok(Tx { c, _lock: lock })
    }
}

/// Short randomized backoff before a retry, growing with the attempt (up to 200 ms).
pub async fn backoff(attempt: u32) {
    let jitter = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().subsec_nanos() as u64;
    let cap = (5u64 << attempt.min(6)).min(200);
    tokio::time::sleep(std::time::Duration::from_millis(jitter % (cap + 1))).await
}

/// Runs `$body` (an expression using `&$t`) in one transaction; commits on Ok, rolls back on Err, retries serialization
/// failures and deadlocks.
#[macro_export]
macro_rules! in_tx {
    ($db:expr, $mode:expr, $scope:expr, |$t:ident| $body:expr) => {{
        let mut attempt = 0u32;
        loop {
            let $t = match $db.begin(&$mode, $scope).await { Ok(t) => t, Err(e) => break Err(e) };
            let r = $body;
            let e = match r {
                Ok(v) => match $t.commit().await { Ok(()) => break Ok(v), Err(e) => e },
                Err(e) => { $t.rollback().await; e }
            };
            if !e.retry || attempt >= 20 { break Err(e) }
            attempt += 1;
            $crate::db::backoff(attempt).await;
        }
    }};
}

/// Applies packages/coordinator/db/schema.sql: each "-- migration N" section once, in order, in one GLOBAL write,
/// split into statements at a semicolon that ends a line, and recorded in schema_migration. Migration 6's document move is a
/// no-op on an empty database.
pub async fn migrate(url: &str, schema: &str) -> Result<(), ApiErr> {
    let db = Db::new(config(url), 1);
    {
        let c = db.conn().await?;
        c.batch_execute("CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())").await?;
    }
    let done: Vec<i32> = db.conn().await?.query("SELECT version FROM schema_migration", &[]).await?.iter().map(|r| r.get(0)).collect();
    // Sections start at a line "-- migration <digit>..." (a comment line "-- migration runner ..." does not start one).
    let mut sections: Vec<(i32, String)> = vec![];
    for line in schema.lines() {
        let head = line.strip_prefix("-- migration ").filter(|r| r.starts_with(|c: char| c.is_ascii_digit()));
        if let Some(r) = head {
            sections.push((r.chars().take_while(|c| c.is_ascii_digit()).collect::<String>().parse().unwrap(), String::new()));
        } else if let Some((_, body)) = sections.last_mut() {
            body.push_str(line);
            body.push('\n');
        }
    }
    for (version, body) in sections {
        if done.contains(&version) {
            continue;
        }
        let mut statements = vec![];
        let mut cur = String::new();
        for line in body.lines() {
            let t = line.trim_end();
            if let Some(s) = t.strip_suffix(';') {
                cur.push_str(s);
                statements.push(std::mem::take(&mut cur));
            } else {
                cur.push_str(line);
                cur.push('\n');
            }
        }
        statements.push(cur);
        let r: Result<(), ApiErr> = in_tx!(db, Mode::Write(GLOBAL.into()), None, |t| async {
            for s in statements.iter().map(|s| s.trim()).filter(|s| !s.is_empty()) {
                t.batch_execute(s).await.map_err(|e| {
                    eprintln!("migration {version} failed at: {s}");
                    ApiErr::from(e)
                })?;
            }
            t.execute("INSERT INTO schema_migration(version) VALUES($1)", &[&version]).await?;
            Ok(())
        }
        .await);
        r?;
    }
    Ok(())
}
