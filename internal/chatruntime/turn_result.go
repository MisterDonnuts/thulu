package chatruntime

import (
	"iter"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/engine"
)

// collectTurn reads what one turn's engine printed into its result: the reply, the tools it called,
// what those calls changed in Thulu, and what the turn cost.
func collectTurn(events iter.Seq[engine.Event]) *TurnResult {
	c := turnCollector{res: &TurnResult{}, pending: map[string]engine.Event{}, seenMsg: map[string]bool{}}
	for ev := range events {
		c.add(ev)
	}
	return c.res
}

type turnCollector struct {
	res     *TurnResult
	pending map[string]engine.Event // tool calls waiting for their result, by id
	seenMsg map[string]bool
}

func (c *turnCollector) add(ev engine.Event) {
	res := c.res
	if ev.SessionID != "" {
		res.SessionID = ev.SessionID
	}
	if ev.Model != "" && res.Model == "" {
		res.Model = ev.Model
	}
	switch ev.Kind {
	case engine.EventAssistant:
		// A subagent's requests are not the chat's context.
		if ev.Usage != nil && ev.ParentToolUseID == "" && (ev.MessageID == "" || !c.seenMsg[ev.MessageID]) {
			c.seenMsg[ev.MessageID] = true
			res.ContextTokens = int64(ev.Usage.PromptSize() + ev.Usage.OutputTokens)
		}
	case engine.EventToolUse:
		res.Tools = append(res.Tools, chat.ToolCall{Name: ev.ToolName, Hint: engine.ToolHint(ev.ToolInput, maxToolHint)})
		if ev.ToolUseID != "" {
			c.pending[ev.ToolUseID] = ev
		}
	case engine.EventToolResult:
		c.toolResult(ev)
	case engine.EventResult:
		c.result(ev)
	default:
		// Init, rate limits and the person's own message say nothing about the turn's result.
	}
}

func (c *turnCollector) toolResult(ev engine.Event) {
	use, ok := c.pending[ev.ToolUseID]
	if !ok {
		return
	}
	delete(c.pending, ev.ToolUseID)
	if fx, ok := thuluEffect(use, ev); ok {
		c.res.Effects = append(c.res.Effects, fx)
	}
}

func (c *turnCollector) result(ev engine.Event) {
	res := c.res
	res.Text = ev.Result
	res.IsError = ev.IsError
	res.NumTurns = ev.NumTurns
	res.CostUSD = ev.CostUSD
	res.StopReason = ev.StopReason
	if ev.Usage != nil {
		res.InputTokens = int64(ev.Usage.InputTokens)
		res.OutputTokens = int64(ev.Usage.OutputTokens)
		res.CacheRead = int64(ev.Usage.CacheRead)
		res.CacheCreation = int64(ev.Usage.CacheCreation)
	}
}

// The MCP server whose calls change the board. Claude Code names its tools mcp__<server>__<tool>;
// Codex and OpenCode join server and tool with a dot or an underscore.
const thuluServer = "octopus-api"

func thuluTool(name string) (string, bool) {
	for _, sep := range []string{"mcp__" + thuluServer + "__", thuluServer + ".", thuluServer + "_"} {
		if i := strings.Index(name, sep); i >= 0 && (i == 0 || sep[0] != 'm') {
			return name[i+len(sep):], true
		}
	}
	return "", false
}

var createdIDRe = regexp.MustCompile(`"id"\s*:\s*"([0-9a-f]{8,32})"`)

const maxEffectDetail = 200

// thuluEffect turns one octopus-api call that changes a task into what the chat shows under the
// reply ("created #id"). Reads are not effects.
func thuluEffect(use, result engine.Event) (chat.Effect, bool) {
	tool, ok := thuluTool(use.ToolName)
	if !ok {
		return chat.Effect{}, false
	}
	in := use.ToolInput
	fx := chat.Effect{TaskID: inputString(in, "id")}
	switch tool {
	case "task_create", "task_create_on_board", "subtask_create", "task_clone":
		fx.Action = chat.EffectCreated
		fx.TaskID = ""
		if m := createdIDRe.FindStringSubmatch(result.ResultHead); m != nil {
			fx.TaskID = m[1]
		}
		if tool == "subtask_create" {
			fx.OtherTaskID = inputString(in, "parent_id")
		}
	case "task_update":
		fx.Action = chat.EffectUpdated
		if stage := inputString(in, "stage"); stage != "" {
			fx.Action, fx.Detail = chat.EffectMoved, stage
		}
	case "task_move_to_board":
		fx.Action, fx.Detail = chat.EffectMoved, inputString(in, "stage")
	case "tasks_move_batch":
		fx.Action = chat.EffectMoved
		if ids, ok := in["task_ids"].([]any); ok && len(ids) > 0 {
			if first, ok := ids[0].(string); ok {
				fx.TaskID = first
			}
			fx.Count = len(ids)
		}
	case "task_add_comment":
		fx.Action = chat.EffectCommented
	case "task_add_dependency":
		fx.Action, fx.OtherTaskID, fx.Detail = chat.EffectLinked, inputString(in, "depends_on_task_id"), inputString(in, "kind")
	case "task_attach":
		fx.Action, fx.Detail = chat.EffectAttached, inputString(in, "filename")
	case "task_delete":
		fx.Action = chat.EffectDeleted
	case "task_relaunch":
		fx.Action = chat.EffectRelaunched
	case "task_section_set", "task_criteria_set", "task_checklist_check":
		fx.Action = chat.EffectUpdated
	default:
		return chat.Effect{}, false
	}
	if result.IsError {
		fx.Error = true
		fx.Detail = trimDetail(firstNonEmpty(result.ResultTail, result.ResultHead))
	}
	return fx, true
}

func inputString(in map[string]any, key string) string {
	s, ok := in[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func trimDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxEffectDetail {
		return string(r[:maxEffectDetail]) + "…"
	}
	return s
}
