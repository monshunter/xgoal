package sqlite

import (
	"context"
	"errors"
	"fmt"
)

// SnapshotArtifact is an owner reference from a frozen database, not a request
// to traverse arbitrary strings in project configuration or Agent output.
type SnapshotArtifact struct {
	Kind, ID, Path, Hash, AuxHash, State string
	Data, AuxData                        []byte
}

// SnapshotArtifacts must be called on the Store returned by ReadSnapshot.
// Rows are closed before the caller starts file IO. Explicit queries keep new
// persisted artifact owners visible during schema review.
func (s *Store) SnapshotArtifacts(ctx context.Context) ([]SnapshotArtifact, error) {
	queries := []struct{ kind, query string }{
		{"work_packet", `SELECT id,'',packet_hash,'','',X'',X'' FROM attempts WHERE packet_hash<>'' ORDER BY id`},
		{"patch", `SELECT attempt_id,bundle_path,manifest_hash,bundle_hash,state,X'',X'' FROM patch_bundles ORDER BY attempt_id`},
		{"receipt", `SELECT id,'',receipt_hash,'','',receipt_json,X'' FROM validator_runs ORDER BY id`},
		{"review_packet", `SELECT id,packet_path,packet_hash,'','',X'',X'' FROM review_runs ORDER BY id`},
		{"review_result", `SELECT id,result_path,result_hash,'','',X'',X'' FROM review_runs ORDER BY id`},
		{"scenario", `SELECT id,'',payload_hash,'','',X'',X'' FROM evidence_records WHERE kind='SCENARIO' ORDER BY id`},
		{"environment", `SELECT id,'',snapshot_hash,'','',payload_json,X'' FROM environment_snapshots ORDER BY id`},
		{"report", `SELECT goal_id,json_path,json_hash,report_hash,state,json_blob,markdown_blob FROM final_reports ORDER BY goal_id`},
		{"report_markdown", `SELECT goal_id,markdown_path,markdown_hash,'',state,markdown_blob,X'' FROM final_reports ORDER BY goal_id`},
		{"invocation", `SELECT id,'',input_hash,'',status,CAST(input_json AS BLOB),CAST(observation_json AS BLOB) FROM invocations ORDER BY id`},
		{"acceptance", `SELECT id,'',request_hash,'',state,request_json,COALESCE(observation_json,X'') FROM effects WHERE effect_type='acceptance' ORDER BY id`},
		{"planner", `SELECT id,'',request_hash,'',state,request_json,COALESCE(observation_json,X'') FROM effects WHERE effect_type='planner' ORDER BY id`},
		{"workspace", `SELECT id,CASE WHEN execution_model='current-directory' THEN path||'/marker.json' ELSE substr(path,1,length(path)-5)||'/marker.json' END,marker_hash,'',state,X'',X'' FROM workspaces ORDER BY id`},
		{"migration_backup", `SELECT CAST(id AS TEXT),filename,sha256,'','',X'',X'' FROM migration_backups ORDER BY id`},
	}
	var result []SnapshotArtifact
	var size int64
	for _, q := range queries {
		rows, err := s.db.QueryContext(ctx, q.query)
		if err != nil {
			return nil, fmt.Errorf("enumerate %s: %w", q.kind, err)
		}
		for rows.Next() {
			a := SnapshotArtifact{Kind: q.kind}
			if err := rows.Scan(&a.ID, &a.Path, &a.Hash, &a.AuxHash, &a.State, &a.Data, &a.AuxData); err != nil {
				rows.Close()
				return nil, err
			}
			size += int64(len(a.Data) + len(a.AuxData))
			if len(result) >= 100000 || size > 256<<20 {
				rows.Close()
				return nil, errors.New("export reference limit exceeded")
			}
			result = append(result, a)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return result, nil
}
