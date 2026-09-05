ALTER TABLE workspaces ADD COLUMN execution_model TEXT NOT NULL DEFAULT 'git-worktree'
    CHECK (execution_model IN ('git-worktree', 'current-directory'));
ALTER TABLE workspaces ADD COLUMN execution_path TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN execution_metadata BLOB NOT NULL DEFAULT X'7b7d' CHECK (json_valid(execution_metadata));
ALTER TABLE goals ADD COLUMN execution_model TEXT NOT NULL DEFAULT 'git-worktree'
    CHECK (execution_model IN ('git-worktree', 'current-directory'));
