#!/usr/bin/env bash
# snippet: i18n
# Everything below is generated from internal/i18n/shell/locales; see
# docs/i18n.md.

# BEGIN i18n: written by `go test ./internal/i18n/shell -update` from
# internal/i18n/shell/locales. Edit the catalog, then regenerate.
#
# msg KEY [NAME VALUE]... prints a message in the reader's language;
# msg_en prints it in English, for logs and for text kept outside Thulu.
# A count chooses between a message's plural forms.
# The language is OCTOPUS_LOCALE, which the daemon passes to the container.
_i18n_locale() {
  # shellcheck disable=SC2066 # one source here; the terminal variant loops over four
  for _i18n_v in "${OCTOPUS_LOCALE:-}"; do
    case $_i18n_v in
    '') ;;
    es | es[_.@-]*) echo es; return ;;
    *) echo en; return ;;
    esac
  done
  echo en
}
# shellcheck disable=SC2016 # literal text: a $ or ` in a message is for the reader to type
_i18n_tmpl() {
  case $1 in
  'en:agent.errors.adapter_no_engine_run') _i18n_t='adapter {path} did not define engine_run' ;;
  'en:agent.errors.agent_name_required') _i18n_t='AGENT_NAME env is required in kube mode' ;;
  'en:agent.errors.bundle_contents') _i18n_t='contents: {files}' ;;
  'en:agent.errors.bundle_daemon_said') _i18n_t='daemon said: {body}' ;;
  'en:agent.errors.bundle_fetch_failed') _i18n_t='could not fetch the run bundle from {url} (http={code}).' ;;
  'en:agent.errors.bundle_no_api') _i18n_t='run-bundle needs OCTOPUS_API and OCTOPUS_API_KEY (agent launched outside the dispatcher?)' ;;
  'en:agent.errors.bundle_no_identity') _i18n_t='run bundle carries no prompts/{stage}.md nor prompts/main.md — the agent has no identity' ;;
  'en:agent.errors.bundle_refusing') _i18n_t='The agent would run without its prompts, rules and skills — refusing to start.' ;;
  'en:agent.errors.bundle_unreadable') _i18n_t='run bundle is not a readable tar.gz (truncated download?)' ;;
  'en:agent.errors.claude_install_busy') _i18n_t='waited {seconds} s for another container to finish installing Claude Code {version} in {dir}; giving up' ;;
  'en:agent.errors.claude_install_failed') _i18n_t='could not install Claude Code {version} from the npm registry. This agent image installs it on first use, so it needs to reach registry.npmjs.org (or the registry in NPM_CONFIG_REGISTRY). Without network access, build the agent image with --build-arg CLAUDE_CODE_INSTALL=build (see "Claude Code in the agent images" in docs/enterprise-setup.md)' ;;
  'en:agent.errors.claude_no_auth') _i18n_t='claude-code has no credentials. Set up an LLM profile for the workspace in Settings (or, on your own daemon, OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 and ./setup-auth.sh)' ;;
  'en:agent.errors.claude_no_personal_login') _i18n_t='claude-code runs on a personal login, but its volume holds none. Connect your Claude account again in Settings → AI engines' ;;
  'en:agent.errors.codex_no_auth') _i18n_t='codex has no credentials (OPENAI_API_KEY). Set up a codex LLM profile for the workspace in Settings' ;;
  'en:agent.errors.empty_message') _i18n_t='empty message' ;;
  'en:agent.errors.engine_cli_missing') _i18n_t='engine {engine} selected but its CLI ({bin}) is not in this image — rebuild the agent images (make agent-images)' ;;
  'en:agent.errors.engine_no_adapter') _i18n_t='engine '\''{engine}'\'' has no adapter at {path}' ;;
  'en:agent.errors.identity_token_write_failed') _i18n_t='could not write the identity token to {file}' ;;
  'en:agent.errors.no_gosu') _i18n_t='gosu is missing from the image; refusing to run the agent as root' ;;
  'en:agent.errors.no_prompt_file') _i18n_t='setup.sh did not set PROMPT_FILE' ;;
  'en:agent.errors.opencode_config_failed') _i18n_t='could not build the OpenCode config for this run' ;;
  'en:agent.errors.opencode_no_auth') _i18n_t='opencode has no credentials (LLM_API_KEY). Set up an opencode LLM profile for the workspace in Settings' ;;
  'en:agent.errors.opencode_no_model') _i18n_t='opencode has no model for a {provider} profile. Set the model on the profile in Settings' ;;
  'en:agent.errors.prompt_not_found') _i18n_t='prompt not found: {path}' ;;
  'en:agent.errors.prompt_src_unset') _i18n_t='PROMPT_SRC is not set before sourcing prompt-init.sh' ;;
  'en:agent.errors.still_root') _i18n_t='still root after the privilege drop; refusing to run the agent as root' ;;
  'en:agent.errors.vertex_write_failed') _i18n_t='could not write the Vertex service account to {file}' ;;
  'en:agent.labels.result_commit') _i18n_t='agent result ({instance})' ;;
  'en:agent.log.auditor_banner') _i18n_t='  AUDITOR — evaluating stage '\''{stage}'\''' ;;
  'en:agent.log.auditor_feedback') _i18n_t='  Feedback: {path}' ;;
  'en:agent.log.auditor_finished') _i18n_t='  AUDITOR finished (exit {code})' ;;
  'en:agent.log.auditor_no_feedback') _i18n_t='auditor did not produce feedback.md' ;;
  'en:agent.log.auditor_settings') _i18n_t='  Engine: {engine}
  Model:  {model}
  Turns:  {turns}
  Skills: {skills}' ;;
  'en:agent.log.auditor_skill_no_prompt') _i18n_t='auditor skill '\''{name}'\'' has no prompt.md' ;;
  'en:agent.log.brief_fetch_failed') _i18n_t='could not fetch the brief (http={code}) — continuing without the task' ;;
  'en:agent.log.brief_fetching') _i18n_t='fetching brief for task {task} from {api}' ;;
  'en:agent.log.brief_injected') _i18n_t='brief injected into the prompt' ;;
  'en:agent.log.brief_no_merged') _i18n_t='MERGED is not set — cannot inject the brief' ;;
  'en:agent.log.bundle_unpacked') _i18n_t='run-bundle: unpacked for {agent}/{stage} — skills: {skills}' ;;
  'en:agent.log.ca_trusted') _i18n_t='CA:        extra bundle trusted ({bundle})' ;;
  'en:agent.log.chat_banner') _i18n_t='  {agent} — chat container ({chat})
  Model:     {model}
  MCP tools: {tools}
  MCP args:  {args}' ;;
  'en:agent.log.chat_serving_execagent') _i18n_t='Serving chat turns via in-pod exec agent on {addr} (Kubernetes)…' ;;
  'en:agent.log.chat_waiting') _i18n_t='Waiting for chat turns via '\''docker exec -i ... /workspace/snippets/chat-turn.sh'\''…' ;;
  'en:agent.log.claude_installed') _i18n_t='Claude Code {version} installed in {seconds} s' ;;
  'en:agent.log.claude_installing') _i18n_t='Installing Claude Code {version} from the npm registry (first use; later runs reuse it)' ;;
  'en:agent.log.codex_no_mcp_remote') _i18n_t='mcp-remote is not in the image, so the SSE MCP servers are left out — rebuild (make agent-images)' ;;
  'en:agent.log.default') _i18n_t='default' ;;
  'en:agent.log.dns_needs_root') _i18n_t='OCTOPUS_FALLBACK_DNS needs a root start to edit resolv.conf; ignored' ;;
  'en:agent.log.dns_not_ip') _i18n_t='ignoring fallback DNS entry '\''{entry}'\'' (not an IP)' ;;
  'en:agent.log.docker_socket_denied') _i18n_t='could not grant node the docker socket'\''s group (gid {gid})' ;;
  'en:agent.log.docker_socket_joined') _i18n_t='Docker socket: node joined group {group} (gid {gid})' ;;
  'en:agent.log.feedback_upload_failed') _i18n_t='could not upload feedback.md — the auditor verdict will not be applied' ;;
  'en:agent.log.flags_upload_failed') _i18n_t='could not upload flags.yaml — the review gate will not see these findings' ;;
  'en:agent.log.hook_running') _i18n_t='running hook {project}/{stage}.sh (in parallel)' ;;
  'en:agent.log.index_upload_failed') _i18n_t='could not upload _index.md to the Reports tab' ;;
  'en:agent.log.mcp_browser_not_ready') _i18n_t='this run'\''s thulu-browser did not answer on {url} within {seconds}s — its tools fail until it does' ;;
  'en:agent.log.mcp_config_written') _i18n_t='MCP config written: {file}' ;;
  'en:agent.log.mcp_no_servers') _i18n_t='MCP_TOOLS is set but OCTOPUS_MCP_SERVERS lists no server — not writing {file}' ;;
  'en:agent.log.mcp_server_not_listed') _i18n_t='MCP_TOOLS includes '\''{name}'\'' but the daemon did not list it in OCTOPUS_MCP_SERVERS (not registered, or not granted) — skipping' ;;
  'en:agent.log.mcp_skills_injected') _i18n_t='MCP skills injected into prompt: {names}' ;;
  'en:agent.log.mcp_skills_needs_merged') _i18n_t='mcp-skills.sh needs $MERGED set and present' ;;
  'en:agent.log.mcp_tools_not_mounted') _i18n_t='{dir} not mounted — MCP skills are not injected' ;;
  'en:agent.log.none') _i18n_t='(none)' ;;
  'en:agent.log.only_base') _i18n_t='(only _base)' ;;
  'en:agent.log.opencode_config_removed') _i18n_t='removed {path}: an OpenCode run reads only the config Thulu writes for it' ;;
  'en:agent.log.project_prompt_injected') _i18n_t='injected the prompt of {project} ({stage})' ;;
  'en:agent.log.prompt_stage_appended') _i18n_t='appending stage-specific {path}' ;;
  'en:agent.log.prompt_stage_using') _i18n_t='using stage-specific {path}' ;;
  'en:agent.log.result_push_failed') _i18n_t='push to {ref} failed for {repo}' ;;
  'en:agent.log.resuming_run') _i18n_t='Resuming from previous run' ;;
  'en:agent.log.resuming_session') _i18n_t='resuming session {session}' ;;
  'en:agent.log.resuming_without_session') _i18n_t='resuming without a session: new session from the transcript' ;;
  'en:agent.log.run_finished') _i18n_t='  {instance} — Finished (exit {code})' ;;
  'en:agent.log.run_info') _i18n_t='Engine:    {engine}
Instance:  {instance}
Model:     {model}
Effort:    {effort}
Max turns: {max_turns}
Prompt:    {prompt}
Output:    {output}' ;;
  'en:agent.log.session_none') _i18n_t='the engine left no session to save' ;;
  'en:agent.log.session_resume_failed') _i18n_t='native resume of session {session} failed before any output — starting a new session' ;;
  'en:agent.log.session_snapshot_missing') _i18n_t='session {session}: no snapshot (http={code}) — the engine starts a new session if it cannot resume' ;;
  'en:agent.log.session_snapshot_restored') _i18n_t='session {session}: snapshot restored into {dir}' ;;
  'en:agent.log.session_snapshot_uploaded') _i18n_t='session {session}: snapshot uploaded' ;;
  'en:agent.log.session_transcript_missing') _i18n_t='no conversation transcript (http={code})' ;;
  'en:agent.log.session_transcript_saved') _i18n_t='conversation transcript saved at {file}' ;;
  'en:agent.log.session_upload_failed') _i18n_t='session {session}: snapshot upload failed (http={code}) — the next resume starts from the transcript' ;;
  'en:agent.log.skill_no_prompt') _i18n_t='skill '\''{name}'\'' has no prompt.md' ;;
  'en:agent.log.skill_no_prompt_in') _i18n_t='Skill '\''{name}'\'' has no prompt.md in {dir}' ;;
  'en:agent.log.skill_not_found') _i18n_t='Skill '\''{name}'\'' not found in {dir}' ;;
  'en:agent.log.skill_not_in_bundle') _i18n_t='skill '\''{name}'\'' not in the bundle' ;;
  'en:agent.log.skills_dir_missing') _i18n_t='{dir} does not exist, skills were not loaded' ;;
  'en:agent.log.skills_native_dir_missing') _i18n_t='{dir} missing — no skills to materialize' ;;
  'en:agent.log.skills_native_needs_merged') _i18n_t='skills-native.sh requires $MERGED' ;;
  'en:agent.log.skills_native_no_dir') _i18n_t='engine {engine} has no skills directory — every skill stays in the prompt' ;;
  'en:agent.log.skills_native_summary') _i18n_t='skills-native: {count} materialized in {dir}; inline: {inline}' ;;
  'en:agent.log.skills_needs_merged') _i18n_t='skills.sh needs $MERGED set (source prompt-init.sh first)' ;;
  'en:agent.log.steering_on') _i18n_t='Steering:  on ({queue})' ;;
  'en:agent.log.tokopt_host_not_ready') _i18n_t='the token optimizer'\''s worker host is not listening on {socket}; in-pod delegations will fail' ;;
  'en:agent.log.tokopt_worker_no_token') _i18n_t='the token optimizer'\''s in-pod worker has no token (OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN is empty), so its host does not start; in-pod delegations will fail' ;;
  'en:agent.prompts.auditor_context') _i18n_t='## Run context

- Agent: `{agent}`
- Stage: `{stage}`
- Run under audit: `{instance}`
- Agent exit code: `{exit_code}`{exit_note}
- Repositories (read-only): `{projects_dir}`
- Timestamp: {timestamp}

## The task and the run under audit

{brief}

## Required output

Write **a single file** at `{output_dir}/feedback.md` with this exact format:

```markdown
---
verdict: continue | kick_back | needs_more_turns | block
kick_back_to: <stage>   # only if verdict is kick_back
score: 8/10
---

# Notes

Briefly explain your verdict (3–10 lines). Mention concrete issues if any.
```

Verdict semantics:
- **continue** — the agent did its job for this stage. The task follows its course: it advances if the stage advances on its own.
- **kick_back** — work needs significant rework. Set `kick_back_to` to a previous stage from the **Flow** the task lists.
- **needs_more_turns** — the agent ran out of turns mid-task. Re-running with extra turns will finish the work.
- **block** — work is incomplete or wrong but the right action is unclear. The human will decide.

Default to `continue` if the agent'\''s output meets the stage'\''s expectations and there are no clear blockers.' ;;
  'en:agent.prompts.auditor_generic') _i18n_t='# Auditor — generic

You are the auditor. The main agent just finished its run for this task and stage.
Your job is to evaluate its work and write `feedback.md` with a verdict.

The repositories are mounted read-only: read and judge, do not rerun work.' ;;
  'en:agent.prompts.auditor_max_turns') _i18n_t=' (likely max-turns hit)' ;;
  'en:agent.prompts.auditor_no_brief') _i18n_t='(the brief could not be fetched: judge from the repositories alone)' ;;
  'en:agent.prompts.brief_header') _i18n_t='# ═══════════════════════════════════════════════════════════════
# ASSIGNED TASK — read this before you start
# ═══════════════════════════════════════════════════════════════' ;;
  'en:agent.prompts.brief_mention_comment') _i18n_t='## ⟶ You were called in this comment

> {comment}

Answer this specifically. The rest of thread `{thread}` is context: it is the conversation you are continuing.' ;;
  'en:agent.prompts.brief_mention_finish') _i18n_t='## How to finish (mention)

You were called in a comment: this is a **question**, not a stage run.
- The code is mounted **read-only** at `{projects_dir}`: the task'\''s worktree or, if it has none yet, the project'\''s default branch. You can read it, not write it. If the answer needs a code change, say so in your reply: the person applies it to the stage with the thread'\''s Apply button.
- If you need another agent, mention it (`@name`) in your reply and it answers in this thread. Agents can speak 3 times in a row in a thread; after that they wait for a person to write.
- Write your reply in `{output_dir}/comment.md` (markdown, conversational, to the point). It is posted as your reply in thread `{thread}`. If you comment with the `task_add_comment` tool, pass `parent_id: "{thread}"` so it lands in the same thread.
- To show a capture, upload it with `task_attach` and embed it in your reply. Files left in `{output_dir}/screenshots/` are uploaded only when Thulu runs on Docker.
- The task'\''s stage does NOT move, nothing is committed, NO auditor runs.' ;;
  'en:agent.prompts.brief_stage_finish') _i18n_t='## How to finish

- Your report goes in `{output_dir}/_index.md` and is published in the task'\''s Reports tab (markdown, 256 KB at most). A short message for the conversation goes in `{output_dir}/comment.md`.
- To attach captures or files, use `task_attach` from `octopus-api`, or `evidence_publish` if you filmed with `thulu-browser`. Files left in `{output_dir}/screenshots/` are uploaded only when Thulu runs on Docker.
- What you changed in the repos is committed on its own when the run ends, so you do not need `git commit`. If git refuses to write (under Docker `.git` is read-only), leave your changes where they are: do not copy `.git` or point it elsewhere. If you open the PR with `pr_create`, it is linked to the task.
- Do not move the task to another stage. If the stage advances on its own, Thulu moves it when you finish cleanly, once the approvals holding it are settled.' ;;
  'en:agent.prompts.chat_first_message') _i18n_t='## The user'\''s first message' ;;
  'en:agent.prompts.context') _i18n_t='## Run context
- INSTANCE_ID: {instance}
- Output dir: {output_dir}
- Timestamp: {timestamp}' ;;
  'en:agent.prompts.native_skill_files') _i18n_t='## Other files in this skill
Read one when the instructions above point to it.' ;;
  'en:agent.prompts.native_skill_scripts') _i18n_t='## Scripts' ;;
  'en:agent.prompts.output_only') _i18n_t='What this run produces (report, captures, logs) goes in `{output_dir}`. Code is edited in the repos under `{projects_dir}`.' ;;
  'en:agent.prompts.project_context_header') _i18n_t='# ═══════════════════════════════════════════════════════════════
# Project context — the repository'\''s own instructions and results
# ═══════════════════════════════════════════════════════════════' ;;
  'en:agent.prompts.project_hook_result') _i18n_t='## [{project}] Hook `{stage}.sh` — exit={exit_code}

Ran inside `{dir}` before you started.
Full log ({lines} lines) in `$OUTPUT_DIR/.project-hooks/{project}-{stage}.log`.

```
{output}
```' ;;
  'en:agent.prompts.project_stage_prompt') _i18n_t='## [{project}] Prompt for stage `{stage}`' ;;
  'en:agent.prompts.resume_more_turns') _i18n_t='Continue where you left off. You have {turns} more turns.' ;;
  'en:agent.prompts.session_message') _i18n_t='## The new message' ;;
  'en:agent.prompts.session_transcript') _i18n_t='## The conversation so far

The session this run continues could not be restored, so a new one starts here. Below is the transcript of the earlier runs on this task: pick up where it ends and do not redo what it shows done.' ;;
  'en:agent.prompts.skill_fallback_description') _i18n_t='Skill: {name}' ;;
  'en:agent.prompts.skill_files') _i18n_t='### Other files ({name})
Read one when the instructions above point to it.' ;;
  'en:agent.prompts.skill_heading') _i18n_t='# Skill: {name}' ;;
  'en:agent.prompts.skill_scripts') _i18n_t='### Available scripts ({name})' ;;
  'en:agent.prompts.tool_heading') _i18n_t='## Tool: {name}' ;;
  'es:agent.errors.adapter_no_engine_run') _i18n_t='el adapter {path} no definió engine_run' ;;
  'es:agent.errors.agent_name_required') _i18n_t='la variable AGENT_NAME es obligatoria en modo kube' ;;
  'es:agent.errors.bundle_contents') _i18n_t='contenido: {files}' ;;
  'es:agent.errors.bundle_daemon_said') _i18n_t='el daemon dijo: {body}' ;;
  'es:agent.errors.bundle_fetch_failed') _i18n_t='no se pudo bajar el bundle de la corrida de {url} (http={code}).' ;;
  'es:agent.errors.bundle_no_api') _i18n_t='run-bundle necesita OCTOPUS_API y OCTOPUS_API_KEY (¿el agente se lanzó por fuera del dispatcher?)' ;;
  'es:agent.errors.bundle_no_identity') _i18n_t='el bundle de la corrida no trae prompts/{stage}.md ni prompts/main.md — el agente no tiene identidad' ;;
  'es:agent.errors.bundle_refusing') _i18n_t='El agente correría sin sus prompts, reglas y skills — no arranca.' ;;
  'es:agent.errors.bundle_unreadable') _i18n_t='el bundle de la corrida no es un tar.gz legible (¿descarga cortada?)' ;;
  'es:agent.errors.claude_install_busy') _i18n_t='esperé {seconds} s a que otro contenedor terminara de instalar Claude Code {version} en {dir}; me rindo' ;;
  'es:agent.errors.claude_install_failed') _i18n_t='no se pudo instalar Claude Code {version} desde el registro de npm. Esta imagen de agente lo instala la primera vez que se usa, así que necesita llegar a registry.npmjs.org (o al registro de NPM_CONFIG_REGISTRY). Sin acceso a la red, construí la imagen del agente con --build-arg CLAUDE_CODE_INSTALL=build (ver "Claude Code in the agent images" en docs/enterprise-setup.md)' ;;
  'es:agent.errors.claude_no_auth') _i18n_t='claude-code sin autenticación. Configurá un perfil LLM para el workspace en Settings (o, en un daemon propio, OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 y ./setup-auth.sh)' ;;
  'es:agent.errors.claude_no_personal_login') _i18n_t='claude-code corre con una sesión personal, pero su volumen no tiene ninguna. Volvé a conectar tu cuenta de Claude en Settings → Motores de IA' ;;
  'es:agent.errors.codex_no_auth') _i18n_t='codex sin autenticación (OPENAI_API_KEY). Configurá un perfil LLM de codex para el workspace en Settings' ;;
  'es:agent.errors.empty_message') _i18n_t='mensaje vacío' ;;
  'es:agent.errors.engine_cli_missing') _i18n_t='se eligió el engine {engine} pero su CLI ({bin}) no está en esta imagen — reconstruí las imágenes de agentes (make agent-images)' ;;
  'es:agent.errors.engine_no_adapter') _i18n_t='engine '\''{engine}'\'' sin adapter en {path}' ;;
  'es:agent.errors.identity_token_write_failed') _i18n_t='no pude escribir el token de identidad en {file}' ;;
  'es:agent.errors.no_gosu') _i18n_t='falta gosu en la imagen; no se corre el agente como root' ;;
  'es:agent.errors.no_prompt_file') _i18n_t='setup.sh no seteó PROMPT_FILE' ;;
  'es:agent.errors.opencode_config_failed') _i18n_t='no se pudo armar la configuración de OpenCode para esta corrida' ;;
  'es:agent.errors.opencode_no_auth') _i18n_t='opencode sin autenticación (LLM_API_KEY). Configurá un perfil LLM de opencode para el workspace en Settings' ;;
  'es:agent.errors.opencode_no_model') _i18n_t='opencode no tiene modelo para un perfil {provider}. Poné el modelo en el perfil, en Settings' ;;
  'es:agent.errors.prompt_not_found') _i18n_t='Prompt no encontrado: {path}' ;;
  'es:agent.errors.prompt_src_unset') _i18n_t='PROMPT_SRC no definido antes de source prompt-init.sh' ;;
  'es:agent.errors.still_root') _i18n_t='sigue siendo root después de bajar privilegios; no se corre el agente como root' ;;
  'es:agent.errors.vertex_write_failed') _i18n_t='no pude escribir la service account de Vertex en {file}' ;;
  'es:agent.labels.result_commit') _i18n_t='resultado del agente ({instance})' ;;
  'es:agent.log.auditor_banner') _i18n_t='  AUDITOR — evaluando la etapa '\''{stage}'\''' ;;
  'es:agent.log.auditor_feedback') _i18n_t='  Feedback: {path}' ;;
  'es:agent.log.auditor_finished') _i18n_t='  AUDITOR terminó (exit {code})' ;;
  'es:agent.log.auditor_no_feedback') _i18n_t='el auditor no generó feedback.md' ;;
  'es:agent.log.auditor_settings') _i18n_t='  Engine: {engine}
  Modelo: {model}
  Turnos: {turns}
  Skills: {skills}' ;;
  'es:agent.log.auditor_skill_no_prompt') _i18n_t='la skill del auditor '\''{name}'\'' no tiene prompt.md' ;;
  'es:agent.log.brief_fetch_failed') _i18n_t='no se pudo obtener brief (http={code}) — continuando sin tarea' ;;
  'es:agent.log.brief_fetching') _i18n_t='buscando el brief de la tarea {task} en {api}' ;;
  'es:agent.log.brief_injected') _i18n_t='brief inyectado al prompt' ;;
  'es:agent.log.brief_no_merged') _i18n_t='MERGED no definido — no se puede inyectar brief' ;;
  'es:agent.log.bundle_unpacked') _i18n_t='run-bundle: desempaquetado para {agent}/{stage} — skills: {skills}' ;;
  'es:agent.log.ca_trusted') _i18n_t='CA:        bundle extra de confianza ({bundle})' ;;
  'es:agent.log.chat_banner') _i18n_t='  {agent} — contenedor de chat ({chat})
  Modelo:    {model}
  Tools MCP: {tools}
  Args MCP:  {args}' ;;
  'es:agent.log.chat_serving_execagent') _i18n_t='Atendiendo turnos de chat con el exec agent del pod en {addr} (Kubernetes)…' ;;
  'es:agent.log.chat_waiting') _i18n_t='Esperando turnos de chat por '\''docker exec -i ... /workspace/snippets/chat-turn.sh'\''…' ;;
  'es:agent.log.claude_installed') _i18n_t='Claude Code {version} instalado en {seconds} s' ;;
  'es:agent.log.claude_installing') _i18n_t='Instalando Claude Code {version} desde el registro de npm (primer uso; las corridas siguientes lo reutilizan)' ;;
  'es:agent.log.codex_no_mcp_remote') _i18n_t='mcp-remote no está en la imagen, así que quedan afuera los servidores MCP por SSE — reconstruila (make agent-images)' ;;
  'es:agent.log.default') _i18n_t='por defecto' ;;
  'es:agent.log.dns_needs_root') _i18n_t='OCTOPUS_FALLBACK_DNS necesita arrancar como root para editar resolv.conf; se ignora' ;;
  'es:agent.log.dns_not_ip') _i18n_t='se ignora la entrada de DNS de respaldo '\''{entry}'\'' (no es una IP)' ;;
  'es:agent.log.docker_socket_denied') _i18n_t='no se pudo dar a node el grupo del socket de Docker (gid {gid})' ;;
  'es:agent.log.docker_socket_joined') _i18n_t='Socket de Docker: node se sumó al grupo {group} (gid {gid})' ;;
  'es:agent.log.feedback_upload_failed') _i18n_t='no se pudo subir feedback.md — el veredicto del auditor no se va a aplicar' ;;
  'es:agent.log.flags_upload_failed') _i18n_t='no se pudo subir flags.yaml — el gate de revisión no va a ver estos hallazgos' ;;
  'es:agent.log.hook_running') _i18n_t='ejecutando hook {project}/{stage}.sh (paralelo)' ;;
  'es:agent.log.index_upload_failed') _i18n_t='no se pudo subir _index.md a la pestaña Reports' ;;
  'es:agent.log.mcp_browser_not_ready') _i18n_t='el thulu-browser de este run no respondió en {url} en {seconds}s — sus tools fallan hasta que responda' ;;
  'es:agent.log.mcp_config_written') _i18n_t='Config de MCP escrita: {file}' ;;
  'es:agent.log.mcp_no_servers') _i18n_t='MCP_TOOLS está seteada pero OCTOPUS_MCP_SERVERS no trae ningún servidor — no se escribe {file}' ;;
  'es:agent.log.mcp_server_not_listed') _i18n_t='MCP_TOOLS incluye '\''{name}'\'' pero el daemon no lo puso en OCTOPUS_MCP_SERVERS (no está registrado o no está concedido) — se saltea' ;;
  'es:agent.log.mcp_skills_injected') _i18n_t='Skills de MCP inyectadas al prompt: {names}' ;;
  'es:agent.log.mcp_skills_needs_merged') _i18n_t='mcp-skills.sh requiere $MERGED seteado y existente' ;;
  'es:agent.log.mcp_tools_not_mounted') _i18n_t='{dir} no montado — skills de MCP no se inyectan' ;;
  'es:agent.log.none') _i18n_t='(ninguno)' ;;
  'es:agent.log.only_base') _i18n_t='(sólo _base)' ;;
  'es:agent.log.opencode_config_removed') _i18n_t='se borró {path}: una corrida de OpenCode lee sólo la configuración que le escribe Thulu' ;;
  'es:agent.log.project_prompt_injected') _i18n_t='inyectado prompt de {project} ({stage})' ;;
  'es:agent.log.prompt_stage_appended') _i18n_t='agregando el prompt de la etapa {path}' ;;
  'es:agent.log.prompt_stage_using') _i18n_t='usando stage-specific {path}' ;;
  'es:agent.log.result_push_failed') _i18n_t='falló el push a {ref} para {repo}' ;;
  'es:agent.log.resuming_run') _i18n_t='Retomando desde la corrida anterior' ;;
  'es:agent.log.resuming_session') _i18n_t='retomando sesión {session}' ;;
  'es:agent.log.resuming_without_session') _i18n_t='retomando sin sesión: sesión nueva desde el transcript' ;;
  'es:agent.log.run_finished') _i18n_t='  {instance} — Finalizado (exit {code})' ;;
  'es:agent.log.run_info') _i18n_t='Engine:     {engine}
Instancia:  {instance}
Modelo:     {model}
Esfuerzo:   {effort}
Turnos máx: {max_turns}
Prompt:     {prompt}
Output:     {output}' ;;
  'es:agent.log.session_none') _i18n_t='el engine no dejó ninguna sesión para guardar' ;;
  'es:agent.log.session_resume_failed') _i18n_t='el resume nativo de la sesión {session} falló antes de producir salida — arrancando una sesión nueva' ;;
  'es:agent.log.session_snapshot_missing') _i18n_t='sesión {session}: no hay snapshot (http={code}) — si no puede retomar, el engine arranca una sesión nueva' ;;
  'es:agent.log.session_snapshot_restored') _i18n_t='sesión {session}: snapshot restaurado en {dir}' ;;
  'es:agent.log.session_snapshot_uploaded') _i18n_t='sesión {session}: snapshot subido' ;;
  'es:agent.log.session_transcript_missing') _i18n_t='no hay transcript de la conversación (http={code})' ;;
  'es:agent.log.session_transcript_saved') _i18n_t='transcript de la conversación guardado en {file}' ;;
  'es:agent.log.session_upload_failed') _i18n_t='sesión {session}: no se pudo subir el snapshot (http={code}) — el próximo resume arranca desde el transcript' ;;
  'es:agent.log.skill_no_prompt') _i18n_t='la skill '\''{name}'\'' no tiene prompt.md' ;;
  'es:agent.log.skill_no_prompt_in') _i18n_t='Skill '\''{name}'\'' sin prompt.md en {dir}' ;;
  'es:agent.log.skill_not_found') _i18n_t='Skill '\''{name}'\'' no encontrada en {dir}' ;;
  'es:agent.log.skill_not_in_bundle') _i18n_t='la skill '\''{name}'\'' no está en el bundle' ;;
  'es:agent.log.skills_dir_missing') _i18n_t='{dir} no existe, skills no se cargaron' ;;
  'es:agent.log.skills_native_dir_missing') _i18n_t='falta {dir} — no hay skills para materializar' ;;
  'es:agent.log.skills_native_needs_merged') _i18n_t='skills-native.sh requiere $MERGED' ;;
  'es:agent.log.skills_native_no_dir') _i18n_t='el engine {engine} no tiene carpeta de skills — todas las skills quedan en el prompt' ;;
  'es:agent.log.skills_native_summary') _i18n_t='skills-native: {count} materializadas en {dir}; inline: {inline}' ;;
  'es:agent.log.skills_needs_merged') _i18n_t='skills.sh requiere $MERGED seteado (source prompt-init.sh antes)' ;;
  'es:agent.log.steering_on') _i18n_t='Steering:  activo ({queue})' ;;
  'es:agent.log.tokopt_host_not_ready') _i18n_t='el host de workers del Token optimizer no escucha en {socket}; las delegaciones dentro del pod van a fallar' ;;
  'es:agent.log.tokopt_worker_no_token') _i18n_t='el worker del Token optimizer dentro del pod no tiene token (OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN está vacía), así que su host no arranca; las delegaciones dentro del pod van a fallar' ;;
  'es:agent.prompts.auditor_context') _i18n_t='## Contexto de la corrida

- Agente: `{agent}`
- Etapa: `{stage}`
- Corrida auditada: `{instance}`
- Código de salida del agente: `{exit_code}`{exit_note}
- Repositorios (sólo lectura): `{projects_dir}`
- Timestamp: {timestamp}

## La tarea y la corrida auditada

{brief}

## Output requerido

Escribí **un solo archivo** en `{output_dir}/feedback.md` con este formato exacto:

```markdown
---
verdict: continue | kick_back | needs_more_turns | block
kick_back_to: <etapa>   # sólo si verdict es kick_back
score: 8/10
---

# Notas

Explicá brevemente tu veredicto (3–10 líneas). Mencioná problemas concretos si los hay.
```

Qué significa cada veredicto:
- **continue** — el agente hizo su trabajo en esta etapa. La tarea sigue su curso: avanza si la etapa avanza sola.
- **kick_back** — el trabajo necesita rehacerse en serio. Poné en `kick_back_to` una etapa anterior del **Flujo** que lista la tarea.
- **needs_more_turns** — al agente se le acabaron los turnos a mitad de la tarea. Volver a correrlo con más turnos termina el trabajo.
- **block** — el trabajo está incompleto o mal pero no está claro qué hacer. Decide una persona.

Por defecto, `continue` si el output del agente cumple lo que la etapa espera y no hay bloqueos claros.' ;;
  'es:agent.prompts.auditor_generic') _i18n_t='# Auditor — genérico

Sos el auditor. El agente principal acaba de terminar su corrida para esta tarea y etapa.
Tu trabajo es evaluar lo que hizo y escribir `feedback.md` con un veredicto.

Los repositorios están montados en sólo lectura: leé y juzgá, no vuelvas a hacer el trabajo.' ;;
  'es:agent.prompts.auditor_max_turns') _i18n_t=' (probablemente se le acabaron los turnos)' ;;
  'es:agent.prompts.auditor_no_brief') _i18n_t='(no se pudo obtener el brief: juzgá sólo a partir de los repositorios)' ;;
  'es:agent.prompts.brief_header') _i18n_t='# ═══════════════════════════════════════════════════════════════
# TAREA ASIGNADA — leé esto antes de empezar
# ═══════════════════════════════════════════════════════════════' ;;
  'es:agent.prompts.brief_mention_comment') _i18n_t='## ⟶ Te invocaron en este comentario

> {comment}

Respondé puntualmente a esto. El resto del hilo `{thread}` es contexto: es la conversación que estás continuando.' ;;
  'es:agent.prompts.brief_mention_finish') _i18n_t='## Cómo terminar correctamente (mención)

Te invocaron en un comentario — esto es una **consulta**, no un run de stage.
- El código está montado **read-only** en `{projects_dir}`: el worktree de la tarea o, si todavía no tiene, la rama default del proyecto. Lo podés leer, no escribir. Si la respuesta requiere cambiar código, decilo en tu respuesta: la persona lo aplica a la etapa con el botón Aplicar del hilo.
- Si necesitás a otro agente, arrobalo (`@nombre`) en tu respuesta y contesta en este hilo. Los agentes pueden hablar 3 veces seguidas en un hilo; después espera a que escriba una persona.
- Escribí tu respuesta en `{output_dir}/comment.md` (markdown, coloquial, al grano). Se postea como respuesta tuya en el hilo `{thread}`. Si comentás con la tool `task_add_comment`, pasá `parent_id: "{thread}"` para que caiga en el mismo hilo.
- Para mostrar una captura, subila con `task_attach` y embebela en tu respuesta. Lo que dejes en `{output_dir}/screenshots/` se sube sólo cuando Thulu corre en Docker.
- NO se mueve el stage de la tarea, NO se commitea, NO corre auditor.' ;;
  'es:agent.prompts.brief_stage_finish') _i18n_t='## Cómo terminar

- Tu reporte va en `{output_dir}/_index.md` y se publica en la pestaña Reports de la tarea (markdown, 256 KB como máximo). Un mensaje corto para la conversación va en `{output_dir}/comment.md`.
- Para adjuntar capturas o archivos usá `task_attach` de `octopus-api`, o `evidence_publish` si filmaste con `thulu-browser`. Lo que dejes en `{output_dir}/screenshots/` se sube sólo cuando Thulu corre en Docker.
- Lo que cambiaste en los repos se commitea solo cuando termina la corrida, así que no hace falta `git commit`. Si git no te deja escribir (en Docker `.git` es de sólo lectura), dejá los cambios donde están: no copies `.git` ni lo apuntes a otro lado. Si abrís el PR con `pr_create`, queda vinculado a la tarea.
- No muevas la tarea de etapa. Si la etapa avanza sola, Thulu la mueve cuando terminás bien, una vez resueltas las aprobaciones que la frenan.' ;;
  'es:agent.prompts.chat_first_message') _i18n_t='## Primer mensaje del usuario' ;;
  'es:agent.prompts.context') _i18n_t='## Contexto de ejecución
- INSTANCE_ID: {instance}
- Output dir: {output_dir}
- Timestamp: {timestamp}' ;;
  'es:agent.prompts.native_skill_files') _i18n_t='## Otros archivos de esta skill
Leé uno cuando las instrucciones de arriba lo nombren.' ;;
  'es:agent.prompts.native_skill_scripts') _i18n_t='## Scripts' ;;
  'es:agent.prompts.output_only') _i18n_t='Lo que produce esta corrida (reporte, capturas, logs) va en `{output_dir}`. El código se edita en los repos de `{projects_dir}`.' ;;
  'es:agent.prompts.project_context_header') _i18n_t='# ═══════════════════════════════════════════════════════════════
# Project context — instrucciones y resultados específicos del repo
# ═══════════════════════════════════════════════════════════════' ;;
  'es:agent.prompts.project_hook_result') _i18n_t='## [{project}] Hook `{stage}.sh` — exit={exit_code}

Ejecutado dentro de `{dir}` antes de levantarte.
Log completo ({lines} líneas) en `$OUTPUT_DIR/.project-hooks/{project}-{stage}.log`.

```
{output}
```' ;;
  'es:agent.prompts.project_stage_prompt') _i18n_t='## [{project}] Prompt para stage `{stage}`' ;;
  'es:agent.prompts.resume_more_turns') _i18n_t='Continuá donde dejaste. Tenés {turns} turnos más.' ;;
  'es:agent.prompts.session_message') _i18n_t='## El mensaje nuevo' ;;
  'es:agent.prompts.session_transcript') _i18n_t='## La conversación hasta acá

La sesión que continúa esta corrida no se pudo restaurar, así que acá arranca una nueva. Abajo está el transcript de las corridas anteriores en esta tarea: seguí desde donde termina y no rehagas lo que ya muestra hecho.' ;;
  'es:agent.prompts.skill_fallback_description') _i18n_t='Skill: {name}' ;;
  'es:agent.prompts.skill_files') _i18n_t='### Otros archivos ({name})
Leé uno cuando las instrucciones de arriba lo nombren.' ;;
  'es:agent.prompts.skill_heading') _i18n_t='# Skill: {name}' ;;
  'es:agent.prompts.skill_scripts') _i18n_t='### Scripts disponibles ({name})' ;;
  'es:agent.prompts.tool_heading') _i18n_t='## Tool: {name}' ;;
  *) return 1 ;;
  esac
}
_i18n_render() {
  _i18n_loc=$1 _i18n_key=$2 _i18n_n='' _i18n_name=''
  shift 2
  for _i18n_a in "$@"; do
    if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi
    if [ "$_i18n_name" = count ]; then _i18n_n=$_i18n_a; fi
    _i18n_name=
  done
  _i18n_form=:other
  if [ "$_i18n_n" = 1 ]; then _i18n_form=:one; fi
  _i18n_tmpl "$_i18n_loc:$_i18n_key$_i18n_form" || _i18n_tmpl "$_i18n_loc:$_i18n_key" ||
    _i18n_tmpl "en:$_i18n_key$_i18n_form" || _i18n_tmpl "en:$_i18n_key" || _i18n_t=$_i18n_key
  _i18n_out=
  while :; do
    case $_i18n_t in *'{'*) ;; *) break ;; esac
    _i18n_out=$_i18n_out${_i18n_t%%'{'*}
    _i18n_t=${_i18n_t#*'{'}
    _i18n_ph=${_i18n_t%%'}'*} _i18n_hit=
    case $_i18n_t in
    *'}'*)
      for _i18n_a in "$@"; do
        if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi
        if [ "$_i18n_name" = "$_i18n_ph" ]; then _i18n_hit=1 _i18n_val=$_i18n_a; fi
        _i18n_name=
      done
      ;;
    esac
    if [ -n "$_i18n_hit" ]; then
      _i18n_out=$_i18n_out$_i18n_val
      _i18n_t=${_i18n_t#*'}'}
    else
      _i18n_out=$_i18n_out'{'
    fi
  done
  printf '%s\n' "$_i18n_out$_i18n_t"
}
msg() { _i18n_render "$(_i18n_locale)" "$@"; }
msg_en() { _i18n_render en "$@"; }
# END i18n
