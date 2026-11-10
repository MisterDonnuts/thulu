package api

import (
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

var eventSummaries = sync.OnceValue(func() *i18n.Retranslator {
	return i18n.NewRetranslator(i18n.Default(), isEventSummaryKey).WithValues(eventSummaryValues)
})

func isEventSummaryKey(key string) bool {
	switch {
	case strings.Contains(key, ".timeline."), strings.Contains(key, ".events."):
		return true
	case strings.HasPrefix(key, "task.comments."), strings.HasPrefix(key, "api.chats.comments."):
		return true
	}
	return key == "api.tasks.audit.task_created" || key == "api.automations.audit.task_created"
}

func eventSummaryValues(locale, _ string, vals map[string]any) {
	for _, name := range []string{"stage", "from", "to"} {
		v, ok := vals[name].(string)
		if !ok || v == "" {
			continue
		}
		if label, ok := stagepkg.LocalizeSeededName(v, locale); ok {
			vals[name] = label
		}
	}
}

func localizeEventSummaries(events []task.WorkflowEvent, locale string) []task.WorkflowEvent {
	rt := eventSummaries()
	for i := range events {
		events[i].Summary, _ = rt.Translate(events[i].Summary, locale)
	}
	return events
}
