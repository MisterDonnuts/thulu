package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Real content is ~650 KB; this cap bounds a symlink loop or a runaway skill directory.
const bundleMaxBytes = 32 << 20

// Must match where deploy/Dockerfile copies the agent content.
const defaultContentDir = "/app/content"

// Kept apart from the agents dir: in Kubernetes that is an emptyDir reseeded from a ConfigMap on
// every restart, while OCTOPUS_CONTENT_DIR may point at an operator-edited volume.
func (s *Server) contentRoot() string {
	if dir := strings.TrimSpace(os.Getenv("OCTOPUS_CONTENT_DIR")); dir != "" {
		return dir
	}
	if isDir(defaultContentDir) {
		return defaultContentDir
	}
	// Catalog.Dir() is already the root holding skills/, tools/ and agents/: types.Discover appends
	// "agents" itself.
	if s.agentCatalog != nil {
		if d := s.agentCatalog.Dir(); d != "" {
			return d
		}
	}
	return "."
}

func (s *Server) content(ctx context.Context, ws string) contentSource {
	if s.agentStore == nil {
		return withNativeTools{fileContent{root: s.contentRoot()}}
	}
	return withNativeTools{catalogContent{store: s.agentStore, tenant: s.catalogTenant(ctx, ws)}}
}

func (s *Server) handleRunBundle(w http.ResponseWriter, r *http.Request) {
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() {
		// No fallback for unscoped keys: an owner-level caller has no run, and guessing one would
		// reopen the cross-agent leak this endpoint closes.
		writeErrKey(w, r, http.StatusForbidden, "api.run_bundle.errors.run_key_required")
		return
	}
	agentName, stage, ok := s.resolveBundleIdentity(w, r, scope)
	if !ok {
		return
	}
	ws, _ := s.callerWorkspace(r)
	at, found := s.agentTypeByName(ws, agentName)
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.run_bundle.errors.agent_not_in_catalog", "agent", agentName)
		return
	}
	if scope.Agent == auth.AuditorAgent {
		at = auditorBundleView(at, string(stage))
	}
	// A recording chat gets thulu-browser whatever its stage grants, so its bundle must carry the
	// skills that ride on it.
	if scope.ChatID != "" {
		at = *s.evidenceAugment(r.Context(), scope.ChatID, &at)
	}
	// Must agree with the SKILLS the launch set: the optimizer's skill only ships when the launch
	// turned the optimizer on.
	if s.tokenOptimizerOn(r.Context(), ws, &at) {
		at.ActiveWhen = append(slices.Clone(at.ActiveWhen), types.SkillWhenTokenOptimizer)
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Octopus-Agent", at.Name)
	w.Header().Set("X-Octopus-Stage", string(stage))

	// The ETag is valid only because the archive is a pure function of the ordered (path, sha256)
	// list: fixed mode, zero mtime, sorted paths.
	src := s.content(r.Context(), ws)
	if tag, tagErr := bundleETag(r.Context(), src, at, string(stage)); tagErr == nil && tag != "" {
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "no-cache")
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, tag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	if err := writeRunBundle(r.Context(), w, src, at, string(stage)); err != nil {
		// Headers are already sent: log and cut the stream so the client's untar fails; a truncated
		// bundle must never look like a small one.
		// The stage is caller-supplied, so it is not logged.
		log.Printf("[api] %s", i18n.Log("api.run_bundle.log.write_failed", "run", scope.TaskID+scope.ChatID, "agent", at.Name, "err", err))
	}
}

// The auditor reads the agent's auditor/ prompts with its own skills. It has no MCP servers, and the
// skills the stage grants the agent are not its grants.
func auditorBundleView(at types.AgentType, stage string) types.AgentType {
	at.SkillsByStage = nil
	if at.Auditor != nil && len(at.Auditor.SkillsByStage[stage]) > 0 {
		at.SkillsByStage = map[string][]string{stage: at.Auditor.SkillsByStage[stage]}
	}
	at.SystemSkills = nil
	at.MCPByStage = nil
	return at
}

// Identity comes from the key, not the task's stage→agent map: mentions and chats are not on the
// board.
// Keys minted before runs carried their agent fall back to the task so in-flight runs survive a
// rollout.
func (s *Server) resolveBundleIdentity(
	w http.ResponseWriter, r *http.Request, scope auth.RunScope,
) (agentName string, stage types.Stage, ok bool) {
	asked := types.Stage(strings.TrimSpace(r.URL.Query().Get("stage")))
	if asked != "" && !types.ValidStageID(string(asked)) {
		writeErrKey(w, r, http.StatusBadRequest, "api.run_bundle.errors.stage_not_slug")
		return "", "", false
	}
	agentName = scope.Agent
	if agentName == auth.AuditorAgent {
		agentName = scope.LLMAgent
	}
	if agentName != "" {
		// ?stage= applies only when the key carries no stage, so a container cannot ask for another
		// stage's grants.
		stage = types.Stage(scope.Stage)
		if stage == "" {
			stage = asked
		}
		return agentName, stage, true
	}

	t, err := s.taskStore.Get(r.Context(), scope.TaskID)
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.run_bundle.errors.task_not_found", "task", scope.TaskID)
		return "", "", false
	}
	stage = asked
	if stage == "" {
		stage = t.Stage
	}
	agentName = t.AssignedAgent(stage)
	if agentName == "" {
		agentName = t.AssignedAgent(t.Stage)
	}
	if agentName == "" {
		writeErrKey(w, r, http.StatusConflict, "api.run_bundle.errors.no_agent_for_stage", "task", scope.TaskID, "stage", stage)
		return "", "", false
	}
	return agentName, stage, true
}

// Mirrors the entrypoint's prompts/<stage>.md → prompts/main.md fallback
// (images/snippets/run-bundle.sh, exits 93).
// Fails open: an unreadable content store is not evidence of a missing prompt.
func (s *Server) agentHasPrompt(ctx context.Context, ws, agentName, stage string) bool {
	files, err := s.content(ctx, ws).Files(ctx, agentstore.KindAgent, agentName)
	if err != nil {
		return true
	}
	for i := range files {
		switch files[i].Path {
		case "prompts/" + stage + ".md", "prompts/main.md":
			return true
		}
	}
	return false
}

func bundleETag(ctx context.Context, src contentSource, at types.AgentType, stage string) (string, error) {
	sum := sha256.New()
	fmt.Fprintf(sum, "skill-meta %d\n", skillMetaVersion) //i18n:ignore a hash input
	add := func(kind, owner, prefix string, only []string) error {
		files, err := src.Files(ctx, kind, owner)
		if err != nil {
			return err
		}
		for i := range files {
			if len(only) > 0 && !slices.Contains(only, files[i].Path) {
				continue
			}
			// Hash the body, not a stored address: the filesystem source has no sha column, and the
			// ETag must not differ by backend.
			fmt.Fprintf(sum, "%s%s %x\n", prefix, files[i].Path, sha256.Sum256(files[i].Body))
		}
		return nil
	}
	if err := add(agentstore.KindAgent, at.Name, "", nil); err != nil {
		return "", err
	}
	if err := add(agentstore.KindSkill, "_base", "skills/_base/", nil); err != nil {
		return "", err
	}
	for _, name := range at.StageSkills(stage) {
		if !safeComponent(name) {
			continue
		}
		if err := add(agentstore.KindSkill, name, "skills/"+name+"/", nil); err != nil {
			return "", err
		}
	}
	for _, entry := range at.MCPByStage[stage] {
		name := mcpServerName(entry)
		if !safeComponent(name) {
			continue
		}
		if err := add(agentstore.KindTool, name, "tools/"+name+"/",
			[]string{agentstore.ToolManualFile}); err != nil {
			return "", err
		}
	}
	return `"` + hex.EncodeToString(sum.Sum(nil)) + `"`, nil
}

func etagMatches(header, tag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == tag {
			return true
		}
	}
	return false
}

func (s *Server) agentTypeByName(ws, name string) (types.AgentType, bool) {
	src := s.agents(ws)
	if src == nil {
		return types.AgentType{}, false
	}
	all, err := src.All()
	if err != nil {
		return types.AgentType{}, false
	}
	for i := range all {
		if all[i].Name == name {
			return all[i], true
		}
	}
	return types.AgentType{}, false
}

// Layout must match where the entrypoint untars it (prompts/, auditor/, skills/<name>/,
// tools/<name>/skill.md); extraction does no rewriting.
func writeRunBundle(ctx context.Context, w io.Writer, src contentSource, at types.AgentType, stage string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	// Close explicitly and return errors: Close flushes the last block, and the 200 is already
	// sent.
	if err := fillBundle(ctx, tw, src, at, stage); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return apperr.Wrap(err, "api.run_bundle.errors.close_tar")
	}
	if err := gz.Close(); err != nil {
		return apperr.Wrap(err, "api.run_bundle.errors.close_gzip")
	}
	return nil
}

func fillBundle(ctx context.Context, tw *tar.Writer, src contentSource, at types.AgentType, stage string) error {
	budget := int64(bundleMaxBytes)

	if err := addOwner(ctx, tw, src, agentstore.KindAgent, at.Name, "", &budget); err != nil {
		return err
	}

	// _base is not a grant: skills.sh also loads it unconditionally.
	if err := addOwner(ctx, tw, src, agentstore.KindSkill, "_base", "skills/_base/", &budget); err != nil {
		return err
	}
	for _, name := range at.StageSkills(stage) {
		if !safeComponent(name) {
			continue
		}
		if err := addSkill(ctx, tw, src, name, &budget); err != nil {
			return err
		}
	}

	for _, entry := range at.MCPByStage[stage] {
		name := mcpServerName(entry)
		if !safeComponent(name) {
			continue
		}
		if err := addOwnerFiles(ctx, tw, src, agentstore.KindTool, name,
			"tools/"+name+"/", []string{agentstore.ToolManualFile}, &budget); err != nil {
			return err
		}
	}
	return nil
}

// Read by skills-native.sh as key=value lines. The daemon parses the YAML because sed kept a quoted
// description's quotes and cut a folded one.
const skillMetaFile = ".skill-meta"

// Bump when skillMeta's output changes, so cached bundles with the old shape are not reused.
const skillMetaVersion = 1

func addSkill(ctx context.Context, tw *tar.Writer, src contentSource, name string, budget *int64) error {
	files, err := src.Files(ctx, agentstore.KindSkill, name)
	if err != nil {
		return apperr.Wrap(err, "api.run_bundle.errors.read_owner", "kind", agentstore.KindSkill, "owner", name)
	}
	prefix := "skills/" + name + "/"
	var meta []byte
	for i := range files {
		if err := addBytes(tw, prefix+files[i].Path, files[i].Body, budget); err != nil {
			return err
		}
		if files[i].Path == agentstore.SkillMetaFile {
			meta = skillMeta(files[i].Body)
		}
	}
	if meta == nil {
		return nil
	}
	return addBytes(tw, prefix+skillMetaFile, meta, budget)
}

func skillMeta(body []byte) []byte {
	var y skillYaml
	if err := yaml.Unmarshal(body, &y); err != nil {
		return nil
	}
	desc := strings.Join(strings.Fields(y.Description), " ")
	enforcement := strings.Join(strings.Fields(y.Enforcement), "")
	//i18n:ignore keys of a file a script reads
	return []byte("enforcement=" + enforcement + "\ndescription=" + desc + "\n")
}

func addOwner(ctx context.Context, tw *tar.Writer, src contentSource,
	kind, owner, prefix string, budget *int64,
) error {
	return addOwnerFiles(ctx, tw, src, kind, owner, prefix, nil, budget)
}

func addOwnerFiles(ctx context.Context, tw *tar.Writer, src contentSource,
	kind, owner, prefix string, only []string, budget *int64,
) error {
	files, err := src.Files(ctx, kind, owner)
	if err != nil {
		return apperr.Wrap(err, "api.run_bundle.errors.read_owner", "kind", kind, "owner", owner)
	}
	for i := range files {
		if len(only) > 0 && !slices.Contains(only, files[i].Path) {
			continue
		}
		if err := addBytes(tw, prefix+files[i].Path, files[i].Body, budget); err != nil {
			return err
		}
	}
	return nil
}

func mcpServerName(entry string) string { return types.MCPServerName(entry) }

// Skill and MCP names come from operator-edited labels and get joined onto paths: reject anything
// that could traverse.
func safeComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func addBytes(tw *tar.Writer, name string, body []byte, budget *int64) error {
	size := int64(len(body))
	if size > *budget {
		return apperr.Internal("api.run_bundle.errors.too_large", "max", int64(bundleMaxBytes), "path", name)
	}
	*budget -= size

	// Fixed mode and zero mtime keep the archive reproducible, which the ETag relies on.
	if hErr := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: size,
	}); hErr != nil {
		return apperr.Wrap(hErr, "api.run_bundle.errors.tar_header", "path", name)
	}
	if _, cErr := tw.Write(body); cErr != nil {
		return apperr.Wrap(cErr, "api.run_bundle.errors.tar_body", "path", name)
	}
	return nil
}
