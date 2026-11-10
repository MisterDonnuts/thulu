package sandbox

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

func encodePorts(m map[int]int) (string, error) {
	if m == nil {
		return "{}", nil
	}
	stringKeys := make(map[string]int, len(m))
	for k, v := range m {
		stringKeys[strconv.Itoa(k)] = v
	}
	b, err := json.Marshal(stringKeys)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodePorts(raw string) (map[int]int, error) {
	if raw == "" || raw == "{}" {
		return map[int]int{}, nil
	}
	stringKeys := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &stringKeys); err != nil {
		return nil, err
	}
	out := make(map[int]int, len(stringKeys))
	for k, v := range stringKeys {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out[n] = v
	}
	return out, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

type scanFunc func(dest ...any) error

func scanSandbox(scan scanFunc) (*Sandbox, error) {
	var (
		sb         Sandbox
		statusStr  string
		hostPorts  string
		releasedAt sql.NullTime
	)
	err := scan(
		&sb.TaskID, &sb.WorkspaceID, &sb.ContainerID, &sb.Image, &statusStr, &hostPorts,
		&sb.CreatedAt, &sb.LastActivityAt, &releasedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sb.Status = Status(statusStr)
	sb.HostPorts, err = decodePorts(hostPorts)
	if err != nil {
		return nil, err
	}
	if releasedAt.Valid {
		t := releasedAt.Time
		sb.ReleasedAt = &t
	}
	return &sb, nil
}

func scanSandboxes(rows *sql.Rows) ([]*Sandbox, error) {
	var out []*Sandbox
	for rows.Next() {
		sb, err := scanSandbox(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}
