package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func (h *MCPHandler) WithInitScripts(s InitScripts) *MCPHandler {
	h.initScripts = s
	return h
}

func initToolDefs() []map[string]any {
	projectArg := map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.init_project_arg")}
	return []map[string]any{
		{
			"name":        "init_status",
			"description": i18n.Text("sandbox.tools.init_status_description"),
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"project": projectArg},
			},
		},
		{
			"name":        "init_run",
			"description": i18n.Text("sandbox.tools.init_run_description"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": projectArg,
					"params": map[string]any{
						"type":                 "object",
						"description":          i18n.Text("sandbox.tools.init_run_params"),
						"additionalProperties": map[string]any{"type": []string{"string", "number", "boolean"}},
					},
					"wait": map[string]any{"type": "boolean", "description": i18n.Text("sandbox.tools.init_run_wait")},
				},
			},
		},
		{
			"name":        "init_script_get",
			"description": i18n.Text("sandbox.tools.init_get_description"),
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"project": projectArg},
			},
		},
		{
			"name":        "init_script_update",
			"description": i18n.Text("sandbox.tools.init_update_description"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": projectArg,
					"script":  map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.init_update_script")},
					"manifest": map[string]any{
						"type":        "object",
						"description": i18n.Text("sandbox.tools.init_update_manifest"),
						"properties": map[string]any{
							"params": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name":        map[string]any{"type": "string"},
										"description": map[string]any{"type": "string"},
										"default":     map[string]any{"type": "string"},
										"required":    map[string]any{"type": "boolean"},
									},
									"required": []string{"name"},
								},
							},
							"ports": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
							"ready": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"command":         map[string]any{"type": "string"},
									"timeout_seconds": map[string]any{"type": "integer"},
								},
							},
							"workdir": map[string]any{"type": "string"},
							"setup": map[string]any{
								"type": "string", "description": i18n.Text("sandbox.tools.init_update_setup"),
							},
							"tools": map[string]any{
								"type":                 "object",
								"description":          i18n.Text("sandbox.tools.init_update_tools"),
								"additionalProperties": map[string]any{"type": "string"},
							},
							"cache": map[string]any{
								"type":        "object",
								"description": i18n.Text("sandbox.tools.init_update_cache"),
								"properties": map[string]any{
									"key":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
									"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								},
							},
							"files": map[string]any{
								"type":                 "object",
								"description":          i18n.Text("sandbox.tools.init_update_files"),
								"additionalProperties": map[string]any{"type": "string"},
							},
						},
					},
					"note": map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.init_update_note")},
				},
				"required": []string{"script", "note"},
			},
		},
	}
}

type initSession struct {
	owner    string
	opts     AcquireDockerOpts
	projects []InitProject
}

func (h *MCPHandler) initSessionFor(ctx context.Context, sess *mcpSession) (*initSession, error) {
	if h.initScripts == nil {
		return nil, apperr.Unavailable("sandbox.errors.init_unavailable")
	}
	ownerID, wsPath, wsID, mounts, err := h.resolver.ResolveWorkspace(ctx, sess.taskID)
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.init_resolve")
	}
	if ownerID == "" {
		ownerID = sess.taskID
	}
	projects, err := h.initScripts.TaskProjects(ctx, sess.taskID)
	if err != nil {
		return nil, err
	}
	return &initSession{
		owner:    ownerID,
		opts:     AcquireDockerOpts{WorkspaceID: wsID, WorkspacePath: wsPath, ProjectsPaths: mounts},
		projects: projects,
	}, nil
}

func (s *initSession) pick(name string) (InitProject, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if len(s.projects) == 1 {
			return s.projects[0], nil
		}
		return InitProject{}, apperr.Invalid("sandbox.errors.init_project_required", "projects", s.names())
	}
	for _, p := range s.projects {
		if p.Name == name || p.Dir == name || p.ID == name {
			return p, nil
		}
	}
	return InitProject{}, apperr.NotFound("sandbox.errors.init_project_not_in_task", "project", name, "projects", s.names())
}

func (s *initSession) names() string {
	out := make([]string, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p.Name)
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ", ")
}

func (h *MCPHandler) target(sess *mcpSession, is *initSession, p InitProject) *initTarget {
	return &initTarget{owner: is.owner, stage: sess.stage, opts: is.opts, taskID: sess.taskID, proj: p}
}

func initFailure(err error) any {
	if errors.Is(err, errSandboxQueued) {
		return toolTextResult([]string{`{"queued":true}`, i18n.Text("sandbox.tools.queued_retry")}, true)
	}
	return toolErrorResult(apperr.Text(err))
}

type initProjectArgs struct {
	Project string `json:"project"`
}

type initStatusEntry struct {
	Project   string              `json:"project"`
	ProjectID string              `json:"project_id"`
	Dir       string              `json:"dir"`
	Mounted   bool                `json:"mounted"`
	HasScript bool                `json:"has_script"`
	Revision  int                 `json:"revision,omitempty"`
	Params    []project.InitParam `json:"params,omitempty"`
	Ports     []int               `json:"ports,omitempty"`
	Ready     string              `json:"ready_command,omitempty"`
	Workdir   string              `json:"workdir,omitempty"`
	HasSetup  bool                `json:"has_setup,omitempty"`
	Tools     map[string]string   `json:"tools,omitempty"`
	Cache     *project.InitCache  `json:"cache,omitempty"`
	Files     []string            `json:"files,omitempty"`
	Run       InitRun             `json:"run"`
}

func (h *MCPHandler) toolInitStatus(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args initProjectArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "init_status", "error", apperr.Text(err)))
	}
	is, err := h.initSessionFor(ctx, sess)
	if err != nil {
		return initFailure(err)
	}
	projects := is.projects
	if args.Project != "" {
		p, err := is.pick(args.Project)
		if err != nil {
			return initFailure(err)
		}
		projects = []InitProject{p}
	}
	entries := make([]initStatusEntry, 0, len(projects))
	for _, p := range projects {
		e := initStatusEntry{Project: p.Name, ProjectID: p.ID, Dir: p.Dir, Mounted: p.Mounted}
		if p.Script != nil {
			e.HasScript = true
			e.Revision = p.Script.Revision
			e.Params = p.Script.Manifest.Params
			e.Ports = p.Script.Manifest.Ports
			e.Ready = p.Script.Manifest.Ready.Command
			e.Workdir = p.Script.Manifest.Workdir
			e.HasSetup = p.Script.Manifest.Setup != ""
			e.Tools = p.Script.Manifest.Tools
			if c := p.Script.Manifest.Cache; len(c.Key) > 0 || len(c.Paths) > 0 {
				e.Cache = &c
			}
			for f := range p.Script.Manifest.Files {
				e.Files = append(e.Files, f)
			}
			sort.Strings(e.Files)
		}
		e.Run = InitRun{State: InitStateNever, LogPath: initLogPath(p.Dir)}
		run, ok := h.mgr.lastInitRun(ctx, is.owner, p.Dir)
		if !ok {
			run, ok = h.mgr.recoverInitRun(ctx, is.owner, p.Dir)
		}
		if ok {
			e.Run = h.mgr.refreshInit(ctx, h.target(sess, is, p), run)
		}
		entries = append(entries, e)
	}
	return toolTextResult([]string{
		mustJSON(map[string]any{"projects": entries}),
		i18n.Text("sandbox.tools.init_status_hint"),
	}, false)
}

type initRunArgs struct {
	Project string                     `json:"project"`
	Params  map[string]json.RawMessage `json:"params"`
	Wait    *bool                      `json:"wait"`
}

func (h *MCPHandler) toolInitRun(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args initRunArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "init_run", "error", apperr.Text(err)))
	}
	is, err := h.initSessionFor(ctx, sess)
	if err != nil {
		return initFailure(err)
	}
	p, err := is.pick(args.Project)
	if err != nil {
		return initFailure(err)
	}
	if p.Script == nil {
		return toolErrorResult(i18n.Text("sandbox.tools.init_no_script", "project", p.Name))
	}
	if !p.Mounted {
		return toolErrorResult(i18n.Text("sandbox.tools.init_not_mounted", "project", p.Name))
	}
	params, err := resolveInitParams(p.Script.Manifest, args.Params)
	if err != nil {
		return initFailure(err)
	}
	t := h.target(sess, is, p)
	t.params = params
	wait := args.Wait == nil || *args.Wait

	res, err := h.mgr.runInit(ctx, t, wait)
	if err != nil {
		return initFailure(err)
	}
	summary := i18n.Text("sandbox.tools.init_run_summary",
		"project", p.Name, "state", res.State, "elapsed", (time.Duration(res.ElapsedMs) * time.Millisecond).Round(time.Second).String())
	if s := setupSummary(res.Setup); s != "" {
		summary += "\n" + s
	}
	return toolTextResult([]string{mustJSON(res), summary},
		res.State == InitStateFailed || res.State == InitStateTimeout)
}

func (h *MCPHandler) toolInitScriptGet(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args initProjectArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "init_script_get", "error", apperr.Text(err)))
	}
	is, err := h.initSessionFor(ctx, sess)
	if err != nil {
		return initFailure(err)
	}
	p, err := is.pick(args.Project)
	if err != nil {
		return initFailure(err)
	}
	if p.Script == nil {
		body := mustJSON(map[string]any{
			"project": p.Name, "revision": 0, "script": "", "manifest": project.DefaultInitManifest(),
		})
		return toolTextResult([]string{body, i18n.Text("sandbox.tools.init_none_yet", "project", p.Name)}, false)
	}
	return toolTextResult([]string{mustJSON(map[string]any{
		"project":      p.Name,
		"revision":     p.Script.Revision,
		"script":       p.Script.Script,
		"manifest":     p.Script.Manifest,
		"author_kind":  p.Script.AuthorKind,
		"author_label": p.Script.AuthorLabel,
		"note":         p.Script.Note,
		"updated_at":   p.Script.CreatedAt,
	})}, false)
}

type initUpdateArgs struct {
	Project  string               `json:"project"`
	Script   string               `json:"script"`
	Manifest project.InitManifest `json:"manifest"`
	Note     string               `json:"note"`
}

func (h *MCPHandler) toolInitScriptUpdate(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args initUpdateArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "init_script_update", "error", apperr.Text(err)))
	}
	is, err := h.initSessionFor(ctx, sess)
	if err != nil {
		return initFailure(err)
	}
	p, err := is.pick(args.Project)
	if err != nil {
		return initFailure(err)
	}
	// The author comes from the session's credential in ctx; nothing in args may name one.
	saved, err := h.initScripts.SaveInitScript(ctx, sess.taskID, p.ID,
		project.InitScriptInput{Script: args.Script, Manifest: args.Manifest, Note: args.Note})
	if err != nil {
		return initFailure(err)
	}
	return toolTextResult([]string{
		mustJSON(map[string]any{
			"project":      p.Name,
			"revision":     saved.Revision,
			"manifest":     saved.Manifest,
			"author_label": saved.AuthorLabel,
		}),
		i18n.Text("sandbox.tools.init_saved", "project", p.Name, "revision", saved.Revision),
	}, false)
}

func setupSummary(r *InitSetupReport) string {
	if r == nil {
		return ""
	}
	ms := func(n int64) string {
		return (time.Duration(n) * time.Millisecond).Round(100 * time.Millisecond).String()
	}
	switch r.Status {
	case SetupStateSkipped:
		return i18n.Text("sandbox.tools.init_setup_summary_skipped", "key", shortKey(r.Key))
	case SetupStateRestored:
		return i18n.Text("sandbox.tools.init_setup_summary_restored", "key", shortKey(r.Snapshot), "elapsed", ms(r.RestoreMs))
	case SetupStateRan:
		if r.NoCache {
			return i18n.Text("sandbox.tools.init_setup_summary_ran_no_cache", "elapsed", ms(r.SetupMs))
		}
		return i18n.Text("sandbox.tools.init_setup_summary_ran", "elapsed", ms(r.SetupMs))
	case SetupStateFailed:
		return setupFailedMessage(r)
	case SetupStateRunning:
		return i18n.Text("sandbox.tools.init_setup_summary_running")
	}
	return ""
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12]
	}
	return k
}

func decodeToolArgs(raw json.RawMessage, v any) error {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func initSupersededMessage() string { return i18n.Text("sandbox.tools.init_superseded") }
