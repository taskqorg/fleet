-- The executor journal (SQLite). Each "-- version N" section runs once, in order, and sets PRAGMA user_version to N.
-- Add a change as a new section at the end; never edit an applied one. Sections run inside one transaction, so
-- statements SQLite refuses there (VACUUM, PRAGMA journal_mode) do not belong here.

-- version 1
CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,terminal INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,native TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS delegation_requests(id TEXT PRIMARY KEY,body TEXT NOT NULL,response TEXT);
CREATE TABLE IF NOT EXISTS outbox(route TEXT NOT NULL,body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outbox_failed(route TEXT NOT NULL,body TEXT NOT NULL,status INTEGER NOT NULL,reason TEXT NOT NULL,failed_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);

-- version 2
-- Process ownership survives an executor crash: a launch row is written before spawn, owned rows (pid with its start-time
-- identity) as processes are observed; both go when the run is reaped. A restart kills what survives before polling.
CREATE TABLE launches(execution TEXT PRIMARY KEY,command TEXT NOT NULL,session TEXT NOT NULL,grp INTEGER NOT NULL DEFAULT 0);
CREATE TABLE owned(execution TEXT NOT NULL,pid INTEGER NOT NULL,identity TEXT NOT NULL,PRIMARY KEY(execution,pid));
