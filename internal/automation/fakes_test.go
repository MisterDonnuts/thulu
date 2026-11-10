package automation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/automation"
)

type fakeTasks struct {
	mu          sync.Mutex
	created     []automation.NewTask
	ids         []string
	comments    map[string][]string
	mentions    map[string][]string
	writeWS     []string
	readWS      []string
	closed      map[string]bool
	missing     map[string]bool
	failNext    error
	tasksBack   []automation.TaskSummary
	queries     []automation.TaskQuery
	stageAgents map[string]string
	quietAgents map[string]bool
}

func newFakeTasks() *fakeTasks {
	return &fakeTasks{
		comments: map[string][]string{},
		mentions: map[string][]string{},
		closed:   map[string]bool{},
		missing:  map[string]bool{},

		stageAgents: map[string]string{},
		quietAgents: map[string]bool{},
	}
}

func (f *fakeTasks) CreateTask(_ context.Context, req automation.NewTask) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", "", err
	}
	id := fmt.Sprintf("task-%d", len(f.created)+1)
	f.created = append(f.created, req)
	f.ids = append(f.ids, id)
	agent := ""
	if req.Stage != "" {
		agent = req.StageAssignments[req.Stage]
	}
	return id, agent, nil
}

func (f *fakeTasks) PlannedAgent(_ context.Context, req automation.NewTask) (string, error) {
	if req.Stage == "" {
		return "", nil
	}
	return req.StageAssignments[req.Stage], nil
}

func (f *fakeTasks) StageAgent(_ context.Context, workspaceID, taskID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readWS = append(f.readWS, workspaceID)
	agent := f.stageAgents[taskID]
	return agent, agent != "" && !f.quietAgents[agent], nil
}

func (f *fakeTasks) CommentTask(_ context.Context, workspaceID, taskID, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeWS = append(f.writeWS, workspaceID)
	f.comments[taskID] = append(f.comments[taskID], body)
	return "title of " + taskID, nil
}

func (f *fakeTasks) MentionAgent(_ context.Context, workspaceID, taskID, agent, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeWS = append(f.writeWS, workspaceID)
	f.mentions[taskID] = append(f.mentions[taskID], agent+": "+body)
	return "title of " + taskID, nil
}

func (f *fakeTasks) TaskOpen(_ context.Context, taskID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missing[taskID] {
		return false, nil
	}
	return !f.closed[taskID], nil
}

func (f *fakeTasks) QueryTasks(_ context.Context, q automation.TaskQuery) ([]automation.TaskSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.tasksBack, nil
}

func (f *fakeTasks) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeTasks) commentCount(taskID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.comments[taskID])
}

type mcpCall struct {
	Server, Tool string
	Args         map[string]any
	APIKey       string
}

type fakeMCP struct {
	mu      sync.Mutex
	calls   []mcpCall
	replies map[string]string
	err     error
	delay   time.Duration
}

func newFakeMCP() *fakeMCP { return &fakeMCP{replies: map[string]string{}} }

func (f *fakeMCP) CallTool(ctx context.Context, server, tool string, args map[string]any, apiKey string) (map[string]any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, mcpCall{server, tool, args, apiKey})
	reply, ok := f.replies[server+"."+tool]
	err := f.err
	delay := f.delay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		reply = "{}"
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": reply}},
	}, nil
}

type fakeScripts struct {
	mu       sync.Mutex
	runs     []automation.ScriptRequest
	owners   []string
	released []string
	result   automation.ScriptResult
	err      error
}

func (f *fakeScripts) RunScript(_ context.Context, owner string, req automation.ScriptRequest) (automation.ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, req)
	f.owners = append(f.owners, owner)
	return f.result, f.err
}

func (f *fakeScripts) ReleaseSandbox(_ context.Context, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, owner)
	return nil
}

type fakeMetrics struct {
	mu      sync.Mutex
	value   float64
	hasData bool
	err     error
	calls   int
}

func (f *fakeMetrics) Aggregate(_ context.Context, _, _, _ string, _ time.Duration, _ string) (float64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.value, f.hasData, f.err
}

func (f *fakeMetrics) set(value float64, hasData bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value, f.hasData = value, hasData
}

type fakeKeys struct {
	mu      sync.Mutex
	minted  int
	revoked []string
}

func (f *fakeKeys) MintAutomationKey(_ context.Context, workspaceID, automationID, runID string, _ time.Duration) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted++
	return "raw-key-" + runID, "key-" + runID, nil
}

func (f *fakeKeys) RevokeAPIKey(_ context.Context, keyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, keyID)
	return nil
}

type fakeCreds struct{ secrets map[string]string }

func (f *fakeCreds) Secret(_ context.Context, _, name string) (string, error) {
	v, ok := f.secrets[name]
	if !ok {
		return "", fmt.Errorf("no such credential %q", name)
	}
	return v, nil
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
