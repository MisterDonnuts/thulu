package task

import (
	"context"
	"time"
)

type Role string

const (
	RoleUser   Role = "user"
	RoleSystem Role = "system"
)

type Message struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	Stage    Stage  `json:"stage,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Role     Role   `json:"role"`
	Author   string `json:"author,omitempty"`
	Body     string `json:"body"`
	Delivery string `json:"delivery,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

const messageCols = `id, task_id, run_id, chat_id, stage, agent, role, author, body, delivery, created_at`

func (s *Store) AddMessage(ctx context.Context, m *Message) (*Message, error) {
	m.ID = mustGenerateID()
	m.CreatedAt = time.Now().UTC()
	if m.Stage == "" {
		m.Stage = s.stageOf(ctx, m.TaskID)
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_messages (`+messageCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
		m.ID, m.TaskID, m.RunID, m.ChatID, m.Stage, m.Agent, m.Role, m.Author, m.Body, m.Delivery, m.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.touchTask(ctx, m.TaskID, m.CreatedAt)
	return m, nil
}

func (s *Store) UpdateMessageDelivery(ctx context.Context, id, delivery, agent, runID, chatID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_messages SET delivery = ?, agent = ?, run_id = ?, chat_id = ? WHERE id = ?`),
		delivery, agent, runID, chatID, id,
	)
	return err
}

func (s *Store) ListMessages(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+messageCols+` FROM task_messages WHERE task_id = ? ORDER BY created_at ASC`), taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TaskID, &m.RunID, &m.ChatID, &m.Stage, &m.Agent,
			&m.Role, &m.Author, &m.Body, &m.Delivery, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// task_id is part of the lookup so a caller authorized for one task cannot read another task's
// message by id.
func (s *Store) GetMessage(ctx context.Context, taskID, id string) (*Message, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+messageCols+` FROM task_messages WHERE id = ? AND task_id = ?`), id, taskID)
	var m Message
	if err := row.Scan(&m.ID, &m.TaskID, &m.RunID, &m.ChatID, &m.Stage, &m.Agent,
		&m.Role, &m.Author, &m.Body, &m.Delivery, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}
