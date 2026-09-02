CREATE TABLE review_runs (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    reviewer_profile_id TEXT NOT NULL CHECK (length(reviewer_profile_id) > 0),
    implementation_profile_id TEXT NOT NULL CHECK (length(implementation_profile_id) > 0),
    implementation_session_id TEXT NOT NULL CHECK (length(implementation_session_id) > 0),
    reviewer_session_id TEXT NOT NULL CHECK (length(reviewer_session_id) > 0),
    candidate_tree TEXT NOT NULL CHECK (length(candidate_tree) IN (40, 64)),
    packet_path TEXT NOT NULL UNIQUE CHECK (length(packet_path) > 0),
    packet_hash TEXT NOT NULL UNIQUE CHECK (length(packet_hash) = 64),
    result_path TEXT NOT NULL UNIQUE CHECK (length(result_path) > 0),
    result_hash TEXT NOT NULL UNIQUE CHECK (length(result_hash) = 64),
    review_status TEXT NOT NULL CHECK (review_status IN ('approved', 'changes_requested', 'blocked')),
    created_at TEXT NOT NULL,
    CHECK (reviewer_profile_id <> implementation_profile_id)
) STRICT;

CREATE INDEX review_runs_by_attempt
ON review_runs(attempt_id, created_at, id);

CREATE TABLE review_findings (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    review_id TEXT NOT NULL REFERENCES review_runs(id) ON DELETE RESTRICT,
    severity TEXT NOT NULL CHECK (severity IN ('blocker', 'high', 'medium', 'low', 'note')),
    category TEXT NOT NULL CHECK (category IN ('correctness', 'regression', 'test_gap', 'scope', 'security', 'maintainability')),
    path TEXT NOT NULL,
    line INTEGER NOT NULL CHECK (line >= 0),
    claim TEXT NOT NULL CHECK (length(claim) > 0),
    basis TEXT NOT NULL CHECK (length(basis) > 0),
    recommended_fix TEXT NOT NULL CHECK (length(recommended_fix) > 0),
    authority TEXT NOT NULL CHECK (authority = 'INFERENCE'),
    state TEXT NOT NULL CHECK (state IN ('OPEN', 'RESOLVED_BY_PATCH', 'DISPROVED_BY_EVIDENCE', 'WAIVED_BY_HUMAN', 'SUPERSEDED')),
    state_sequence INTEGER NOT NULL CHECK (state_sequence > 0),
    state_reason TEXT NOT NULL CHECK (length(state_reason) > 0),
    created_at TEXT NOT NULL,
    changed_at TEXT NOT NULL,
    UNIQUE (review_id, id)
) STRICT;

CREATE INDEX review_findings_by_review_state
ON review_findings(review_id, state, severity, id);
