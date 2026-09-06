package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	basestore "github.com/monshunter/xgoal/internal/store"
)

type Identifier struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	GoalID  string `json:"goal_id"`
	State   string `json:"state"`
	Version int64  `json:"version"`
	Role    string `json:"role,omitempty"`
}

var ErrInvalidIdentifierQuery = errors.New("invalid identifier query")

type IdentifierPage struct {
	Items []Identifier `json:"items"`
	More  bool         `json:"more"`
}

// FindIdentifiers uses a fixed kind allowlist and literal prefix comparison.
// An exact ID sorts first even when it is also a prefix of another ID.
func (s *Store) FindIdentifiers(ctx context.Context, kind, prefix string, limit int) (IdentifierPage, error) {
	page := IdentifierPage{Items: []Identifier{}}
	if limit < 1 || limit > 100 || len(prefix) > 256 || strings.IndexFunc(prefix, unicode.IsControl) >= 0 {
		return page, fmt.Errorf("%w: identifier prefix and limit 1..100 are required", ErrInvalidIdentifierQuery)
	}
	var projection string
	switch kind {
	case "goal":
		projection = `SELECT id,id AS goal_id,state,version,'' AS role FROM goals`
	case "work":
		projection = `SELECT work.id,revision.goal_id,work.state,work.version,'' AS role FROM work_items work JOIN plan_revisions plan ON plan.id=work.plan_revision_id JOIN goal_revisions revision ON revision.id=plan.goal_revision_id`
	case "gate":
		projection = `SELECT id,goal_id,state,version,'' AS role FROM gates`
	case "invocation":
		projection = `SELECT id,goal_id,status AS state,version,role FROM invocations`
	default:
		return page, fmt.Errorf("%w: kind must be goal, work, gate or invocation", ErrInvalidIdentifierQuery)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,state,version,role FROM (`+projection+`) WHERE substr(id,1,length(?))=? ORDER BY id=? DESC,id LIMIT ?`, prefix, prefix, prefix, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Identifier
		item.Kind = kind
		if err := rows.Scan(&item.ID, &item.GoalID, &item.State, &item.Version, &item.Role); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			page.More = true
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

type AmbiguousIdentifier struct {
	Kind, Prefix string
	Candidates   []Identifier
}

func (e *AmbiguousIdentifier) Error() string {
	names := make([]string, len(e.Candidates))
	for i, item := range e.Candidates {
		names[i] = item.ID
	}
	return fmt.Sprintf("%s prefix %q matches multiple IDs: %s; use a longer prefix or an exact ID", e.Kind, e.Prefix, strings.Join(names, ", "))
}

func (s *Store) ResolveIdentifier(ctx context.Context, kind, prefix string) (Identifier, error) {
	if prefix == "" {
		return Identifier{}, fmt.Errorf("%w: identifier is required", ErrInvalidIdentifierQuery)
	}
	page, err := s.FindIdentifiers(ctx, kind, prefix, 10)
	if err != nil {
		return Identifier{}, err
	}
	if len(page.Items) == 0 {
		return Identifier{}, fmt.Errorf("%s %q: %w", kind, prefix, basestore.ErrNotFound)
	}
	if page.Items[0].ID == prefix || len(page.Items) == 1 && !page.More {
		return page.Items[0], nil
	}
	return Identifier{}, &AmbiguousIdentifier{Kind: kind, Prefix: prefix, Candidates: page.Items}
}
