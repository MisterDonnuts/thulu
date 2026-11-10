package initwizard

import (
	"errors"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	engineClaude   = "claude-code"
	engineCodex    = "codex"
	engineOpenCode = "opencode"
)

var wizardEngines = []string{engineClaude, engineCodex, engineOpenCode}

const openCodeBaseURL = "https://api.deepseek.com/v1"

type EngineCredential struct {
	Engine  string
	Kind    string
	Key     string
	BaseURL string
	Model   string
}

func engineLabel(name string) string {
	switch name {
	case engineClaude:
		return i18n.Text("initwizard.engines.claude.label")
	case engineCodex:
		return i18n.Text("initwizard.engines.codex.label")
	case engineOpenCode:
		return i18n.Text("initwizard.engines.opencode.label")
	}
	return name
}

func engineName(name string) string {
	switch name {
	case engineClaude:
		return "Claude Code" //i18n:ignore product name
	case engineCodex:
		return "Codex" //i18n:ignore product name
	case engineOpenCode:
		return "OpenCode" //i18n:ignore product name
	}
	return name
}

func engineDetail(name string) string {
	switch name {
	case engineClaude:
		return i18n.Text("initwizard.engines.claude.detail")
	case engineCodex:
		return i18n.Text("initwizard.engines.codex.detail")
	case engineOpenCode:
		return i18n.Text("initwizard.engines.opencode.detail")
	}
	return ""
}

func (iv *Interviewer) askEngines() error {
	iv.section(i18n.Text("initwizard.sections.engines.title"), i18n.Text("initwizard.sections.engines.blurb"))

	opts := make([]Option, 0, len(wizardEngines))
	checked := []int{}
	previous := iv.PreviousEngines
	if len(previous) == 0 {
		previous = []string{engineClaude}
	}
	for i, name := range wizardEngines {
		opts = append(opts, Option{Value: name, Label: engineLabel(name), Detail: engineDetail(name)})
		if containsName(previous, name) {
			checked = append(checked, i)
		}
	}
	for {
		selected, err := iv.P.MultiChoice(i18n.Text("initwizard.prompts.engines"), opts, checked)
		if err != nil {
			return err
		}
		if len(selected) == 0 && !iv.P.cannotAskAgain() {
			iv.P.narrate("initwizard.cli.engines_none")
			continue
		}
		if len(selected) == 0 {
			selected = []string{engineClaude}
		}
		iv.Cfg.Engines = selected
		break
	}
	for _, name := range iv.Cfg.Engines {
		if err := iv.askEngineCredential(name); err != nil {
			return err
		}
	}
	if len(iv.Cfg.Credentials) == 0 {
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.llm_key.step"),
			i18n.Text("initwizard.deferred.llm_key.skipped_effect"),
			i18n.Text("initwizard.deferred.llm_key.skipped_finish"))
	}
	return nil
}

func (iv *Interviewer) askEngineCredential(name string) error {
	iv.P.Printf("")
	switch name {
	case engineClaude:
		iv.P.narrate("initwizard.cli.claude_credential_help")
		key, err := iv.askKey(i18n.Text("initwizard.prompts.claude_key"), func(k string) bool {
			return strings.HasPrefix(k, "sk-ant-api") || strings.HasPrefix(k, "sk-ant-oat")
		}, "initwizard.cli.claude_key_shape")
		if err != nil || key == "" {
			return err
		}
		iv.Cfg.Credentials = append(iv.Cfg.Credentials, EngineCredential{Engine: name, Key: key})
	case engineCodex:
		iv.P.narrate("initwizard.cli.codex_credential_help")
		key, err := iv.askKey(i18n.Text("initwizard.prompts.openai_key"), func(k string) bool {
			return strings.HasPrefix(k, "sk-") && !strings.HasPrefix(k, "sk-ant-")
		}, "initwizard.cli.openai_key_shape")
		if err != nil || key == "" {
			return err
		}
		iv.Cfg.Credentials = append(iv.Cfg.Credentials, EngineCredential{Engine: name, Key: key})
	case engineOpenCode:
		iv.P.narrate("initwizard.cli.opencode_credential_help")
		key, err := iv.askKey(i18n.Text("initwizard.prompts.opencode_key"), nil, "")
		if err != nil || key == "" {
			return err
		}
		base, err := iv.askChecked(i18n.Text("initwizard.prompts.opencode_base_url"), openCodeBaseURL,
			invalidBaseURL, "initwizard.cli.invalid_base_url", "initwizard.errors.invalid_base_url")
		if err != nil {
			return err
		}
		model, err := iv.P.Text(i18n.Text("initwizard.prompts.opencode_model"), opencode.DefaultModel)
		if err != nil {
			return err
		}
		iv.Cfg.Credentials = append(iv.Cfg.Credentials, EngineCredential{
			Engine: name, Key: key, BaseURL: strings.TrimRight(base, "/"), Model: model,
		})
	}
	return nil
}

func (iv *Interviewer) askKey(label string, ok func(string) bool, shapeKey string) (string, error) {
	for {
		key, err := iv.P.Secret(label)
		switch {
		case errors.Is(err, ErrSkipped):
			return "", nil
		case err != nil:
			return "", err
		}
		if ok == nil || ok(key) {
			return key, nil
		}
		iv.P.narrate(shapeKey)
		if iv.P.cannotAskAgain() {
			return "", nil
		}
	}
}

func invalidBaseURL(raw string) (string, bool) {
	if _, ok := githubWebURL(raw); !ok {
		return raw, true
	}
	return "", false
}
