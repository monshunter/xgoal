-- Observation projection only. Process ownership remains process_invocations;
-- Goal/Work/effects remain the execution authority.
CREATE TABLE invocations (
    id TEXT PRIMARY KEY,
    goal_id TEXT NOT NULL REFERENCES goals(id),
    role TEXT NOT NULL CHECK (role IN ('planner','implementer','reviewer','acceptance')),
    input_json TEXT NOT NULL CHECK (json_valid(input_json)),
    input_hash TEXT NOT NULL CHECK (length(input_hash)=64),
    observation_json TEXT NOT NULL CHECK (json_valid(observation_json)),
    status TEXT NOT NULL CHECK (status IN ('running','returned','failed','interrupted')),
    cursor INTEGER NOT NULL DEFAULT 0 CHECK (cursor>=0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version>0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX invocations_goal_role ON invocations(goal_id,role,created_at,id);
