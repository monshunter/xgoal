CREATE TABLE process_invocations (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('planning','attempt','probe')),
    owner_id TEXT NOT NULL CHECK (length(owner_id) > 0),
    goal_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('INTENT','REGISTERED','EXITED','TERMINATED','UNKNOWN')),
    pid INTEGER CHECK (pid > 0),
    pgid INTEGER CHECK (pgid = pid),
    start_identity TEXT CHECK (length(start_identity) > 0),
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (state <> 'REGISTERED' OR (pid IS NOT NULL AND pgid IS NOT NULL AND start_identity IS NOT NULL))
) STRICT;
CREATE INDEX process_invocations_by_owner ON process_invocations(owner_kind,owner_id,generation,state);

-- An Attempt created by the barrier-aware kernel cannot start a process
-- without a durable intent. Legacy Attempts retain the conservative default.
ALTER TABLE attempts ADD COLUMN process_journal_version INTEGER NOT NULL DEFAULT 0 CHECK (process_journal_version IN (0,1));
