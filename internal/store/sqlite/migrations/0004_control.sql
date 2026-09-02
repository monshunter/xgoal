CREATE TABLE failure_records (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    work_item_id TEXT,
    attempt_id TEXT,
    failure_class TEXT NOT NULL CHECK (failure_class IN (
        'AGENT_UNAVAILABLE', 'AGENT_PROTOCOL_INVALID', 'AGENT_TIMEOUT', 'AGENT_INTERRUPTED',
        'ENVIRONMENT_PREP_FAILED', 'SCOPE_VIOLATION', 'PATCH_EMPTY', 'PATCH_CONFLICT',
        'VALIDATOR_FAILED', 'VALIDATOR_UNAVAILABLE', 'REVIEW_BLOCKED', 'GOAL_AMBIGUOUS',
        'POLICY_BLOCKED', 'BUDGET_EXHAUSTED', 'NO_MATERIAL_PROGRESS',
        'INTERNAL_INVARIANT_VIOLATION'
    )),
    normalized_error TEXT NOT NULL CHECK (length(normalized_error) > 0),
    validator_definition_hash TEXT NOT NULL,
    base_tree TEXT NOT NULL CHECK (length(base_tree) > 0),
    result_tree TEXT NOT NULL,
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) > 0),
    config_hash TEXT NOT NULL CHECK (length(config_hash) > 0),
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
    strategy TEXT NOT NULL CHECK (length(strategy) > 0),
    snapshot_hash TEXT NOT NULL CHECK (length(snapshot_hash) = 64),
    material_progress INTEGER NOT NULL CHECK (material_progress IN (0, 1)),
    repeat_count INTEGER NOT NULL CHECK (repeat_count > 0),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX failure_records_by_goal_fingerprint
ON failure_records(goal_id, fingerprint, strategy, created_at, id);

CREATE TABLE reconcile_decisions (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    failure_id TEXT NOT NULL UNIQUE REFERENCES failure_records(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN (
        'RETRY_NEW_ATTEMPT', 'DIAGNOSE', 'FIX_WORK_ITEM', 'SWITCH_STRATEGY', 'REPLAN',
        'WAIT_GATE', 'WAIT_BUDGET', 'QUARANTINE', 'STOP_INVARIANT'
    )),
    reason TEXT NOT NULL CHECK (length(reason) > 0),
    plan_revision INTEGER NOT NULL CHECK (plan_revision >= 0),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE budget_limits (
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    work_item_id TEXT NOT NULL DEFAULT '',
    dimension TEXT NOT NULL CHECK (dimension IN (
        'GOAL_ATTEMPTS', 'WORK_ATTEMPTS', 'AGENT_SESSIONS', 'WALL_TIME_MILLIS',
        'CONCURRENCY', 'TOKENS', 'COST_MICROS', 'VALIDATOR_TIME_MILLIS', 'WORKSPACE_BYTES'
    )),
    soft_limit INTEGER NOT NULL CHECK (soft_limit >= 0),
    hard_limit INTEGER NOT NULL CHECK (hard_limit > 0 AND hard_limit >= soft_limit),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (goal_id, work_item_id, dimension)
) STRICT;

CREATE TABLE budget_usage (
    goal_id TEXT NOT NULL,
    work_item_id TEXT NOT NULL DEFAULT '',
    dimension TEXT NOT NULL,
    known INTEGER NOT NULL CHECK (known IN (0, 1)),
    consumed INTEGER NOT NULL CHECK (consumed >= 0),
    version INTEGER NOT NULL CHECK (version > 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (goal_id, work_item_id, dimension),
    FOREIGN KEY (goal_id, work_item_id, dimension)
        REFERENCES budget_limits(goal_id, work_item_id, dimension) ON DELETE RESTRICT,
    CHECK (known = 1 OR consumed = 0)
) STRICT;

CREATE TABLE worker_processes (
    attempt_id TEXT PRIMARY KEY REFERENCES attempts(id) ON DELETE RESTRICT,
    pid INTEGER NOT NULL CHECK (pid > 0),
    pgid INTEGER NOT NULL CHECK (pgid > 0),
    start_identity TEXT NOT NULL CHECK (length(start_identity) > 0),
    state TEXT NOT NULL CHECK (state IN ('RUNNING', 'OBSERVING', 'TERMINATED', 'EXITED', 'LOST')),
    version INTEGER NOT NULL CHECK (version > 0),
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX worker_processes_by_state
ON worker_processes(state, attempt_id);
