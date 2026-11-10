package evidence

import "context"

func (s *Store) BatchWorkspace(ctx context.Context, id string) (string, error) {
	var ws string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT workspace_id FROM evidence_batches WHERE id = ?`), id).Scan(&ws)
	if err != nil {
		return "", notFound(err, "evidence.errors.batch_not_found", "id", id)
	}
	return ws, nil
}
