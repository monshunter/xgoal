CREATE TABLE workspaces (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('ATTEMPT', 'VALIDATION')),
    path TEXT NOT NULL UNIQUE CHECK (length(path) > 0),
    common_dir TEXT NOT NULL CHECK (length(common_dir) > 0),
    base_commit TEXT NOT NULL CHECK (length(base_commit) > 0),
    base_tree TEXT NOT NULL CHECK (length(base_tree) > 0),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    marker_hash TEXT NOT NULL UNIQUE CHECK (length(marker_hash) = 64),
    state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'CLEANED', 'FAILED')),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX workspaces_by_attempt_state
ON workspaces(attempt_id, state, kind, id);

CREATE TABLE patch_bundles (
    attempt_id TEXT PRIMARY KEY REFERENCES attempts(id) ON DELETE RESTRICT,
    base_commit TEXT NOT NULL CHECK (length(base_commit) > 0),
    base_tree TEXT NOT NULL CHECK (length(base_tree) > 0),
    manifest_hash TEXT NOT NULL UNIQUE CHECK (length(manifest_hash) = 64),
    bundle_hash TEXT NOT NULL UNIQUE CHECK (length(bundle_hash) = 64),
    bundle_path TEXT NOT NULL UNIQUE CHECK (length(bundle_path) > 0),
    state TEXT NOT NULL CHECK (state IN ('VALID', 'INVALID', 'QUARANTINED')),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE environment_snapshots (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    snapshot_hash TEXT NOT NULL UNIQUE CHECK (length(snapshot_hash) = 64),
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) = 64),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    base_tree TEXT NOT NULL CHECK (length(base_tree) > 0),
    isolation_level TEXT NOT NULL CHECK (isolation_level = 'L0'),
    payload_json BLOB NOT NULL CHECK (json_valid(payload_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE validator_definitions (
    definition_hash TEXT PRIMARY KEY CHECK (length(definition_hash) = 64),
    id TEXT NOT NULL CHECK (length(id) > 0),
    validator_type TEXT NOT NULL CHECK (validator_type IN ('scope', 'command', 'file_assertion', 'runtime_probe', 'git_assertion')),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    definition_json BLOB NOT NULL CHECK (json_valid(definition_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE validator_registrations (
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    base_commit TEXT NOT NULL CHECK (length(base_commit) > 0),
    base_tree TEXT NOT NULL CHECK (length(base_tree) > 0),
    validator_id TEXT NOT NULL CHECK (length(validator_id) > 0),
    definition_hash TEXT NOT NULL REFERENCES validator_definitions(definition_hash) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (config_hash, base_commit, validator_id),
    UNIQUE (config_hash, base_commit, validator_id, definition_hash)
) STRICT;

CREATE INDEX validator_registrations_by_definition
ON validator_registrations(definition_hash, config_hash, base_commit, validator_id);

CREATE TABLE validator_runs (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    definition_hash TEXT NOT NULL REFERENCES validator_definitions(definition_hash) ON DELETE RESTRICT,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE RESTRICT,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    receipt_hash TEXT NOT NULL UNIQUE CHECK (length(receipt_hash) = 64),
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) = 64),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    environment_hash TEXT NOT NULL CHECK (length(environment_hash) = 64),
    tree_hash TEXT NOT NULL CHECK (length(tree_hash) IN (40, 64)),
    result TEXT NOT NULL CHECK (result IN ('PASSED', 'FAILED', 'TIMED_OUT', 'UNAVAILABLE')),
    receipt_json BLOB NOT NULL CHECK (json_valid(receipt_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX validator_runs_by_definition_tree
ON validator_runs(definition_hash, tree_hash, created_at, id);

CREATE TABLE evidence_records (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    protocol_version TEXT NOT NULL CHECK (length(protocol_version) > 0),
    kind TEXT NOT NULL CHECK (length(kind) > 0),
    subject_id TEXT NOT NULL CHECK (length(subject_id) > 0),
    producer TEXT NOT NULL CHECK (length(producer) > 0),
    authority TEXT NOT NULL CHECK (authority IN ('DECISION', 'DETERMINISTIC', 'FACT', 'INFERENCE', 'CLAIM')),
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) = 64),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    definition_hash TEXT NOT NULL CHECK (length(definition_hash) = 64),
    environment_hash TEXT NOT NULL CHECK (length(environment_hash) = 64),
    tree_hash TEXT NOT NULL CHECK (length(tree_hash) IN (40, 64)),
    payload_hash TEXT NOT NULL CHECK (length(payload_hash) = 64),
    receipt_hash TEXT NOT NULL DEFAULT '' CHECK (receipt_hash = '' OR length(receipt_hash) = 64),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX evidence_records_by_subject
ON evidence_records(subject_id, kind, created_at, id);

CREATE TABLE evidence_state_changes (
    evidence_id TEXT NOT NULL REFERENCES evidence_records(id) ON DELETE RESTRICT,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL CHECK (state IN ('CURRENT', 'STALE', 'SUPERSEDED', 'INVALID')),
    reason TEXT NOT NULL CHECK (length(reason) > 0),
    changed_at TEXT NOT NULL,
    PRIMARY KEY (evidence_id, sequence)
) STRICT;

CREATE TABLE evidence_sets (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    phase TEXT NOT NULL CHECK (phase IN ('CHANGE', 'FINAL')),
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) = 64),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    tree_hash TEXT NOT NULL CHECK (length(tree_hash) IN (40, 64)),
    set_hash TEXT NOT NULL UNIQUE CHECK (length(set_hash) = 64),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE evidence_set_members (
    evidence_set_id TEXT NOT NULL REFERENCES evidence_sets(id) ON DELETE RESTRICT,
    evidence_id TEXT NOT NULL REFERENCES evidence_records(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (evidence_set_id, evidence_id),
    UNIQUE (evidence_set_id, ordinal)
) STRICT;

CREATE TABLE promotions (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    goal_revision INTEGER NOT NULL CHECK (goal_revision > 0),
    work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id) ON DELETE RESTRICT,
    lease_id TEXT NOT NULL REFERENCES leases(id) ON DELETE RESTRICT,
    lease_generation INTEGER NOT NULL CHECK (lease_generation > 0),
    bundle_hash TEXT NOT NULL REFERENCES patch_bundles(bundle_hash) ON DELETE RESTRICT,
    evidence_set_id TEXT NOT NULL REFERENCES evidence_sets(id) ON DELETE RESTRICT,
    old_commit TEXT NOT NULL CHECK (length(old_commit) > 0),
    old_tree TEXT NOT NULL CHECK (length(old_tree) > 0),
    candidate_tree TEXT NOT NULL CHECK (length(candidate_tree) > 0),
    integration_ref TEXT NOT NULL CHECK (length(integration_ref) > 0),
    integration_commit TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('REQUESTED', 'COMMIT_CREATED', 'REF_UPDATED', 'OBSERVED', 'FAILED')),
    effect_id TEXT NOT NULL UNIQUE REFERENCES effects(id) ON DELETE RESTRICT,
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (state = 'REQUESTED' AND integration_commit = '')
        OR
        (state IN ('COMMIT_CREATED', 'REF_UPDATED', 'OBSERVED') AND length(integration_commit) > 0)
        OR state = 'FAILED'
    )
) STRICT;
