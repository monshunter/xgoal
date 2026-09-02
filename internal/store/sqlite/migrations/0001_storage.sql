CREATE TABLE migration_backups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_version INTEGER NOT NULL CHECK (source_version >= 0),
    target_version INTEGER NOT NULL CHECK (target_version > source_version),
    filename TEXT NOT NULL UNIQUE,
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE store_metadata (
    key TEXT PRIMARY KEY,
    value BLOB NOT NULL
) STRICT;

CREATE TABLE goals (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    state TEXT NOT NULL CHECK (state IN (
        'DRAFT',
        'READY',
        'RUNNING',
        'WAITING',
        'VERIFYING',
        'COMPLETED',
        'CANCELLED'
    )),
    active_revision_id TEXT NOT NULL DEFAULT '',
    final_tree TEXT NOT NULL DEFAULT '',
    final_evidence_set_id TEXT NOT NULL DEFAULT '',
    final_report_hash TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (state = 'COMPLETED'
            AND length(final_tree) > 0
            AND length(final_evidence_set_id) > 0
            AND length(final_report_hash) > 0)
        OR
        (state <> 'COMPLETED'
            AND final_tree = ''
            AND final_evidence_set_id = ''
            AND final_report_hash = '')
    )
) STRICT;

CREATE TABLE goal_revisions (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    raw_goal TEXT NOT NULL CHECK (length(raw_goal) > 0),
    contract_json BLOB NOT NULL CHECK (json_valid(contract_json)),
    contract_hash TEXT NOT NULL CHECK (length(contract_hash) = 64),
    frozen_at TEXT NOT NULL,
    UNIQUE (goal_id, revision)
) STRICT;

CREATE TABLE plan_revisions (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_revision_id TEXT NOT NULL REFERENCES goal_revisions(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    graph_hash TEXT NOT NULL CHECK (length(graph_hash) = 64),
    status TEXT NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'SUPERSEDED')),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (goal_revision_id, revision)
) STRICT;

CREATE UNIQUE INDEX one_active_plan_per_goal_revision
ON plan_revisions(goal_revision_id)
WHERE status = 'ACTIVE';

CREATE TABLE work_items (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    plan_revision_id TEXT NOT NULL REFERENCES plan_revisions(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN (
        'PENDING',
        'READY',
        'CLAIMED',
        'RUNNING',
        'VERIFYING',
        'RECONCILING',
        'WAITING',
        'COMPLETED',
        'CANCELLED'
    )),
    title TEXT NOT NULL CHECK (length(title) > 0),
    objective_json BLOB NOT NULL CHECK (json_valid(objective_json)),
    scope_json BLOB NOT NULL CHECK (json_valid(scope_json)),
    acceptance_json BLOB NOT NULL CHECK (json_valid(acceptance_json)),
    validator_ids_json BLOB NOT NULL CHECK (json_valid(validator_ids_json)),
    recommended_role TEXT NOT NULL CHECK (recommended_role IN ('planner', 'implementer', 'reviewer')),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX work_items_by_plan_state
ON work_items(plan_revision_id, state, id);

CREATE TABLE work_dependencies (
    from_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    to_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    dependency_type TEXT NOT NULL CHECK (dependency_type = 'HARD'),
    PRIMARY KEY (from_id, to_id),
    CHECK (from_id <> to_id)
) STRICT;

CREATE TABLE events (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    aggregate_type TEXT NOT NULL CHECK (length(aggregate_type) > 0),
    aggregate_id TEXT NOT NULL CHECK (length(aggregate_id) > 0),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (length(event_type) > 0),
    actor_type TEXT NOT NULL CHECK (length(actor_type) > 0),
    actor_id TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    payload_json BLOB NOT NULL CHECK (json_valid(payload_json)),
    created_at TEXT NOT NULL,
    UNIQUE (aggregate_type, aggregate_id, sequence)
) STRICT;

CREATE INDEX events_by_created_at
ON events(created_at, id);

CREATE TABLE idempotency_records (
    scope TEXT NOT NULL CHECK (length(scope) > 0),
    key TEXT NOT NULL CHECK (length(key) > 0),
    request_json BLOB NOT NULL CHECK (json_valid(request_json)),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    state TEXT NOT NULL CHECK (state IN ('IN_PROGRESS', 'COMPLETED')),
    response_status INTEGER CHECK (response_status BETWEEN 100 AND 599),
    response_json BLOB CHECK (response_json IS NULL OR json_valid(response_json)),
    response_hash TEXT CHECK (response_hash IS NULL OR length(response_hash) = 64),
    created_at TEXT NOT NULL,
    completed_at TEXT,
    PRIMARY KEY (scope, key),
    CHECK (
        (state = 'IN_PROGRESS'
            AND response_status IS NULL
            AND response_json IS NULL
            AND response_hash IS NULL
            AND completed_at IS NULL)
        OR
        (state = 'COMPLETED'
            AND response_status IS NOT NULL
            AND response_json IS NOT NULL
            AND response_hash IS NOT NULL
            AND completed_at IS NOT NULL)
    )
) STRICT;

CREATE TABLE effects (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    effect_key TEXT NOT NULL UNIQUE CHECK (length(effect_key) > 0),
    effect_type TEXT NOT NULL CHECK (length(effect_type) > 0),
    state TEXT NOT NULL CHECK (state IN (
        'REQUESTED',
        'EXECUTING',
        'OBSERVING',
        'RECOVERING',
        'SUCCEEDED',
        'FAILED'
    )),
    request_json BLOB NOT NULL CHECK (json_valid(request_json)),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    observation_json BLOB CHECK (observation_json IS NULL OR json_valid(observation_json)),
    observation_hash TEXT CHECK (observation_hash IS NULL OR length(observation_hash) = 64),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX effects_by_state
ON effects(state, id);

CREATE TABLE attempts (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    agent_profile_id TEXT NOT NULL CHECK (length(agent_profile_id) > 0),
    state TEXT NOT NULL CHECK (state IN (
        'CREATED',
        'PREPARING',
        'STARTING',
        'RUNNING',
        'COLLECTING',
        'VALIDATING',
        'REVIEWING',
        'PROMOTING',
        'SUCCEEDED',
        'FAILED',
        'TIMED_OUT',
        'INTERRUPTED',
        'INVALID_OUTPUT',
        'QUARANTINED'
    )),
    base_tree TEXT NOT NULL,
    result_tree TEXT NOT NULL DEFAULT '',
    packet_hash TEXT NOT NULL,
    result_kind TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX attempts_by_work
ON attempts(work_item_id, created_at, id);

CREATE TABLE leases (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
    holder TEXT NOT NULL CHECK (length(holder) > 0),
    generation INTEGER NOT NULL CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'RELEASED', 'EXPIRED', 'REVOKED')),
    acquired_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    UNIQUE (work_item_id, generation)
) STRICT;

CREATE UNIQUE INDEX one_active_lease_per_work
ON leases(work_item_id)
WHERE state = 'ACTIVE';

CREATE TABLE gates (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    work_item_id TEXT REFERENCES work_items(id) ON DELETE RESTRICT,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE RESTRICT,
    reason_code TEXT NOT NULL CHECK (length(reason_code) > 0),
    state TEXT NOT NULL CHECK (state IN ('OPEN', 'APPROVED', 'DENIED', 'EXPIRED', 'REVOKED')),
    facts_json BLOB NOT NULL CHECK (json_valid(facts_json)),
    unknowns_json BLOB NOT NULL CHECK (json_valid(unknowns_json)),
    options_json BLOB NOT NULL CHECK (json_valid(options_json)),
    recommendation TEXT NOT NULL CHECK (length(recommendation) > 0),
    action TEXT NOT NULL CHECK (action IN (
        'READ_FILE',
        'WRITE_FILE',
        'EXEC_COMMAND',
        'CONNECT_PROVIDER',
        'ACCESS_PROJECT_NETWORK',
        'READ_ENV',
        'USE_PROVIDER_CREDENTIAL',
        'USE_PROJECT_SECRET',
        'MODIFY_VALIDATOR',
        'MODIFY_GIT_HISTORY',
        'PUSH_REMOTE',
        'PUBLISH_ARTIFACT',
        'DEPLOY_PRODUCTION',
        'DELETE_EXTERNAL_DATA',
        'EXPAND_SCOPE'
    )),
    scope_json BLOB NOT NULL CHECK (json_valid(scope_json)),
    expires_at TEXT NOT NULL,
    max_uses INTEGER NOT NULL CHECK (max_uses > 0),
    used INTEGER NOT NULL DEFAULT 0 CHECK (used >= 0 AND used <= max_uses),
    revocable INTEGER NOT NULL CHECK (revocable IN (0, 1)),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    decision TEXT CHECK (decision IS NULL OR decision IN ('ALLOW', 'DENY')),
    decided_by TEXT,
    decision_reason TEXT,
    decided_at TEXT,
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (state = 'OPEN'
            AND decision IS NULL
            AND decided_by IS NULL
            AND decision_reason IS NULL
            AND decided_at IS NULL)
        OR
        (state = 'APPROVED'
            AND decision = 'ALLOW'
            AND decided_by IS NOT NULL
            AND decision_reason IS NOT NULL
            AND decided_at IS NOT NULL)
        OR
        (state = 'DENIED'
            AND decision = 'DENY'
            AND decided_by IS NOT NULL
            AND decision_reason IS NOT NULL
            AND decided_at IS NOT NULL)
        OR state IN ('EXPIRED', 'REVOKED')
    )
) STRICT;

CREATE INDEX gates_by_goal_state
ON gates(goal_id, state, required, id);

CREATE TABLE goal_completion_facts (
    goal_id TEXT PRIMARY KEY REFERENCES goals(id) ON DELETE RESTRICT,
    integration_tree TEXT NOT NULL CHECK (length(integration_tree) > 0),
    expected_tree TEXT NOT NULL CHECK (length(expected_tree) > 0),
    criteria_json BLOB NOT NULL CHECK (json_valid(criteria_json)),
    open_blocking_findings INTEGER NOT NULL CHECK (open_blocking_findings >= 0),
    scope_policy_passed INTEGER NOT NULL CHECK (scope_policy_passed IN (0, 1)),
    final_validation_current INTEGER NOT NULL CHECK (final_validation_current IN (0, 1)),
    final_evidence_set_id TEXT NOT NULL CHECK (length(final_evidence_set_id) > 0),
    final_report_hash TEXT NOT NULL CHECK (length(final_report_hash) > 0),
    human_acceptance_required INTEGER NOT NULL CHECK (human_acceptance_required IN (0, 1)),
    human_acceptance_satisfied INTEGER NOT NULL CHECK (human_acceptance_satisfied IN (0, 1)),
    version INTEGER NOT NULL CHECK (version > 0),
    updated_at TEXT NOT NULL
) STRICT;
