ALTER TABLE goals ADD COLUMN planning_generation INTEGER NOT NULL DEFAULT 0 CHECK (planning_generation >= 0);
ALTER TABLE goals ADD COLUMN planning_effect_id TEXT REFERENCES effects(id) ON DELETE RESTRICT;
ALTER TABLE goals ADD COLUMN planning_paused INTEGER NOT NULL DEFAULT 0 CHECK (planning_paused IN (0, 1));
CREATE UNIQUE INDEX one_goal_per_planning_effect ON goals(planning_effect_id) WHERE planning_effect_id IS NOT NULL;
