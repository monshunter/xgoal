package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
)

// One predicate is shared by scheduling, retry, promotion and completion.
// A consumed decision remains historical fact after its deadline; a fresh
// invocation cannot consume that authorization again. Revocation still blocks.
// The query must alias gates as gate_record and bind the current clock instant.
const gateBlocksExecution = `(gate_record.state<>'APPROVED' OR gate_record.decision<>'ALLOW' OR (gate_record.used=0 AND julianday(gate_record.expires_at)<=julianday(?)))`

func countBlockingRequiredGates(ctx context.Context, q rowQueryer, goalID, workID string, now time.Time) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM gates gate_record WHERE gate_record.goal_id=? AND gate_record.required=1 AND `+gateBlocksExecution+` AND (?='' OR gate_record.work_item_id IS NULL OR gate_record.work_item_id=?)`, goalID, now.UTC().Format(time.RFC3339Nano), workID, workID).Scan(&count)
	return count, err
}

func (s *Store) consumeRetryDecisions(ctx context.Context, tx *sql.Tx, goalID, workID string, event preparedEvent) error {
	consumed, err := prepareEvent(EventInput{Type: "GateContinuationConsumed", ActorType: "kernel", CorrelationID: event.input.CorrelationID, Payload: map[string]any{"work_item_id": workID}})
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM gates WHERE goal_id=? AND work_item_id=? AND reason_code IN ('agent_blocked','checkout_retry_required') AND action='EXEC_COMMAND' AND state='APPROVED' AND decision='ALLOW' AND used<max_uses AND julianday(expires_at)>julianday(?) AND attempt_id=(SELECT attempt_id FROM failure_records WHERE work_item_id=? ORDER BY created_at DESC,id DESC LIMIT 1) ORDER BY id`, goalID, workID, s.source.Now().UTC().Format(time.RFC3339Nano), workID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE gates SET used=used+1,version=version+1,updated_at=? WHERE id=?`, s.source.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "gate", id, consumed); err != nil {
			return err
		}
	}
	return nil
}

// RetryDecisions projects the consumed answers for exactly the newest failure.
// Older answers cannot silently become authority for a different blocked turn.
func (s *Store) RetryDecisions(ctx context.Context, workID string) ([]protocol.PacketDecision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,version,decision_reason FROM gates WHERE work_item_id=? AND reason_code IN ('agent_blocked','checkout_retry_required') AND action='EXEC_COMMAND' AND state='APPROVED' AND decision='ALLOW' AND used>0 AND attempt_id=(SELECT attempt_id FROM failure_records WHERE work_item_id=? ORDER BY created_at DESC,id DESC LIMIT 1) ORDER BY id`, workID, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []protocol.PacketDecision
	for rows.Next() {
		var decision protocol.PacketDecision
		if err := rows.Scan(&decision.GateID, &decision.GateVersion, &decision.Answer); err != nil {
			return nil, err
		}
		decision.Answer = redact.String(decision.Answer)
		result = append(result, decision)
	}
	return result, rows.Err()
}
