CREATE TABLE final_reports (
    goal_id TEXT PRIMARY KEY REFERENCES goals(id) ON DELETE RESTRICT,
    goal_revision_id TEXT NOT NULL REFERENCES goal_revisions(id) ON DELETE RESTRICT,
    protocol_version TEXT NOT NULL CHECK (protocol_version = 'xgoal.final-report/v1'),
    goal_revision_hash TEXT NOT NULL CHECK (length(goal_revision_hash) = 64),
    config_hash TEXT NOT NULL CHECK (length(config_hash) = 64),
    tree_hash TEXT NOT NULL CHECK (length(tree_hash) IN (40, 64)),
    evidence_set_id TEXT NOT NULL REFERENCES evidence_sets(id) ON DELETE RESTRICT,
    report_hash TEXT NOT NULL UNIQUE CHECK (length(report_hash) = 64),
    json_path TEXT NOT NULL UNIQUE CHECK (length(json_path) > 0),
    json_temp_path TEXT NOT NULL UNIQUE CHECK (length(json_temp_path) > 0),
    json_hash TEXT NOT NULL CHECK (length(json_hash) = 64),
    json_blob BLOB NOT NULL CHECK (json_valid(json_blob)),
    markdown_path TEXT NOT NULL UNIQUE CHECK (length(markdown_path) > 0),
    markdown_temp_path TEXT NOT NULL UNIQUE CHECK (length(markdown_temp_path) > 0),
    markdown_hash TEXT NOT NULL CHECK (length(markdown_hash) = 64),
    markdown_blob BLOB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('PENDING_RENAME', 'COMMITTED')),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX final_reports_by_state
ON final_reports(state, goal_id);
