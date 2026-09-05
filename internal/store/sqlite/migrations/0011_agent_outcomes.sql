-- Preserve historical classes and decisions, including removed accounting history.
CREATE TABLE failure_records_v11 (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE RESTRICT,
    work_item_id TEXT,
    attempt_id TEXT,
    failure_class TEXT NOT NULL CHECK (failure_class IN (
        'AGENT_BLOCKED', 'AGENT_FAILED', 'AGENT_UNAVAILABLE', 'AGENT_PROTOCOL_INVALID', 'AGENT_TIMEOUT', 'AGENT_INTERRUPTED',
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


CREATE TABLE reconcile_decisions_v11 (
    id TEXT PRIMARY KEY CHECK (length(id) > 0),
    failure_id TEXT NOT NULL UNIQUE REFERENCES failure_records_v11(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN (
        'RETRY_NEW_ATTEMPT', 'DIAGNOSE', 'FIX_WORK_ITEM', 'SWITCH_STRATEGY', 'REPLAN',
        'WAIT_GATE', 'WAIT_BUDGET', 'QUARANTINE', 'STOP_INVARIANT'
    )),
    reason TEXT NOT NULL CHECK (length(reason) > 0),
    plan_revision INTEGER NOT NULL CHECK (plan_revision >= 0),
    created_at TEXT NOT NULL
) STRICT;

INSERT INTO failure_records_v11 SELECT * FROM failure_records;
INSERT INTO reconcile_decisions_v11 SELECT * FROM reconcile_decisions;
DROP TABLE reconcile_decisions;
DROP TABLE failure_records;
ALTER TABLE failure_records_v11 RENAME TO failure_records;
ALTER TABLE reconcile_decisions_v11 RENAME TO reconcile_decisions;
CREATE INDEX failure_records_by_goal_fingerprint
ON failure_records(goal_id, fingerprint, strategy, created_at, id);

ALTER TABLE work_items ADD COLUMN auto_retry_count INTEGER NOT NULL DEFAULT 0 CHECK(auto_retry_count>=0);

-- A Kernel-resolved planning blocker is historical, not a revoked permission.
UPDATE gates SET required=0 WHERE required=1
  AND json_extract(facts_json,'$.owner')='planning'
  AND ((state='REVOKED' AND decision IS NULL AND decided_by IS NULL AND (
    reason_code='PLANNING_GRAPH_INCOMPLETE' OR EXISTS (
      SELECT 1 FROM events e WHERE e.aggregate_type='gate' AND e.aggregate_id=gates.id
        AND e.event_type='PlanningGateResolved' AND e.actor_type='kernel'
    ))) OR (state='APPROVED' AND decision='ALLOW' AND EXISTS (
      SELECT 1 FROM events e WHERE e.aggregate_type='goal' AND e.aggregate_id=gates.goal_id
        AND e.actor_type='kernel' AND e.created_at>=gates.created_at
        AND julianday(e.created_at)<julianday(gates.expires_at) AND (
          (e.event_type='PlanningRetried' AND json_extract(e.payload_json,'$.generation')>json_extract(gates.facts_json,'$.generation')) OR
          (e.event_type='PlanningPublished' AND json_extract(e.payload_json,'$.planning_generation')>=json_extract(gates.facts_json,'$.generation'))
        )
    )));
