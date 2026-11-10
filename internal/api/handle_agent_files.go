package api

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

//go:embed scaffold
var scaffoldFS embed.FS

type agentFileEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

var runtimeSkipDirs = []string{"runs", "node_modules", ".git", "feedback"}

func isRuntimeDir(base string) bool {
	return strings.HasPrefix(base, ".") || slices.Contains(runtimeSkipDirs, base)
}

// Only .md files are editable here; docker-compose.yml, Dockerfile and setup.sh change only through
// the settings endpoint.
func editableFile(rel string) bool {
	if strings.HasPrefix(rel, ".") {
		return false
	}
	for _, skip := range runtimeSkipDirs {
		if strings.HasPrefix(rel, skip+"/") || strings.Contains(rel, "/"+skip+"/") {
			return false
		}
	}
	return filepath.Ext(filepath.Base(rel)) == ".md"
}

func (s *Server) agentDir(typeName string) (string, error) {
	if typeName == "" || strings.ContainsAny(typeName, "/\\") {
		return "", apperr.Invalid("api.agents.errors.invalid_type_name")
	}
	dir := filepath.Join(s.agentsDir, "agents", typeName)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", apperr.NotFound("api.agents.errors.agent_not_found", "name", typeName)
	}
	return abs, nil
}

func resolveAgentFile(root, rel string) (string, error) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", apperr.Invalid("api.agents.errors.empty_path")
	}
	full := filepath.Join(root, rel)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return "", apperr.Invalid("api.agents.errors.path_escapes_dir")
	}
	return abs, nil
}

func (s *Server) handleListAgentFiles(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store != nil {
		s.storedAgentFiles(w, r, store, tenant)
		return
	}
	dir, err := s.agentDir(r.PathValue("name"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}

	out := []agentFileEntry{}
	walkErr := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isRuntimeDir(filepath.Base(rel)) {
				return filepath.SkipDir
			}
			out = append(out, agentFileEntry{Path: rel, Kind: "dir"})
			return nil
		}
		if !editableFile(rel) {
			return nil
		}
		out = append(out, agentFileEntry{Path: rel, Kind: "file", Size: info.Size()})
		return nil
	})
	if walkErr != nil {
		writeError(w, r, http.StatusInternalServerError, walkErr)
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) agentFilePath(w http.ResponseWriter, r *http.Request) (dir, rel, full string, ok bool) {
	dir, err := s.agentDir(r.PathValue("name"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return "", "", "", false
	}
	rel = r.PathValue("path")
	if !editableFile(rel) {
		writeErrKey(w, r, http.StatusForbidden, "api.agents.errors.file_not_editable")
		return "", "", "", false
	}
	full, err = resolveAgentFile(dir, rel)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return "", "", "", false
	}
	return dir, rel, full, true
}

func (s *Server) handleReadAgentFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store != nil {
		s.storedAgentFileRead(w, r, store, tenant)
		return
	}
	dir, rel, _, ok := s.agentFilePath(w, r)
	if !ok {
		return
	}
	// Read through os.Root (safepath): resolveAgentFile's prefix check does not see symlinks
	// escaping dir.
	data, err := safepath.ReadFile(dir, rel)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	//i18n:ignore HTTP header value, not a message
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleWriteAgentFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store != nil {
		s.storedAgentFileWrite(w, r, store, tenant)
		return
	}
	_, _, full, ok := s.agentFilePath(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.invalidateCatalog("")
	writeJSON(w, http.StatusOK, map[string]int{"size": len(body)})
}

func (s *Server) handleDeleteAgentFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store != nil {
		s.storedAgentFileDelete(w, r, store, tenant)
		return
	}
	_, _, full, ok := s.agentFilePath(w, r)
	if !ok {
		return
	}
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.file_not_found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.invalidateCatalog("")
	writeStatus(w, "deleted")
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Template string `json:"template"`
		newAgentShape
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || strings.ContainsAny(body.Name, "/\\ ") {
		writeErrKey(w, r, http.StatusBadRequest, "api.agents.errors.invalid_agent_name")
		return
	}

	if store != nil {
		created, err := s.createStoredAgent(r.Context(), store, tenant, body.Name, body.Template, actorID(r), body.newAgentShape)
		if err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		// Agents deleted before deletes dropped bindings left them behind; a reused name would run
		// on the old agent's LLM profile.
		logx.Warn(s.dropAgentBinding(r.Context(), tenant, body.Name),
			"api.agents.log.drop_stale_binding", "agent", body.Name)
		s.invalidateCatalog(tenant)
		s.auditAgentDef(r, tenant, body.Name, "api.agents.audit.agent_created")
		writeJSON(w, http.StatusCreated, created)
		return
	}

	dstDir := filepath.Join(s.agentsDir, "agents", body.Name)
	if _, err := os.Stat(dstDir); err == nil {
		writeErrKey(w, r, http.StatusConflict, "api.agents.errors.agent_exists")
		return
	}

	if body.Template == "" {
		if err := s.scaffoldAgent(dstDir, body.Name); err != nil {
			logx.Warn(os.RemoveAll(dstDir), "api.agents.log.cleanup_partial_dir", "path", dstDir)
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	} else {
		srcDir, err := s.agentDir(body.Template)
		if err != nil {
			writeErrKey(w, r, http.StatusBadRequest, "api.agents.errors.template_not_found", "name", body.Template)
			return
		}
		if err := copyDir(srcDir, dstDir); err != nil {
			logx.Warn(os.RemoveAll(dstDir), "api.agents.log.cleanup_partial_dir", "path", dstDir)
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		const composeName = "docker-compose.yml"
		if data, err := safepath.ReadFile(dstDir, composeName); err == nil {
			rewritten := strings.ReplaceAll(string(data), `thulu.type: "`+body.Template+`"`, `thulu.type: "`+body.Name+`"`)
			logx.Warn(safepath.WriteFile(dstDir, composeName, []byte(rewritten), 0o644),
				"api.agents.log.rewrite_type_label", "dir", dstDir, "file", composeName)
		}
	}

	s.respondCreatedDir(w, r, dstDir, body.Name, body.newAgentShape)
}

func (s *Server) respondCreatedDir(w http.ResponseWriter, r *http.Request, dstDir, name string, shape newAgentShape) {
	s.invalidateCatalog("")
	discovered, discErr := s.agents("").All()
	logx.Warn(discErr, "api.agents.log.discover_failed", "path", s.agentsDir)
	for i := range discovered {
		if discovered[i].Name != name {
			continue
		}
		at := discovered[i]
		if shape.Description != "" || shape.ValidStages != nil || len(shape.Skills) > 0 {
			shape.apply(&at)
			if status, err := writeAgentSettings(dstDir, name, settingsOf(&at)); err != nil {
				writeError(w, r, status, err)
				return
			}
			s.invalidateCatalog("")
		}
		writeJSON(w, http.StatusCreated, at)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) scaffoldAgent(dstDir, name string) error {
	data := struct{ Name string }{Name: name}
	walkErr := fs.WalkDir(scaffoldFS, "scaffold", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("scaffold", p)
		if err != nil {
			return apperr.Wrap(err, "api.agents.errors.scaffold_rel", "path", p)
		}
		if d.IsDir() {
			return scaffoldMkdir(dstDir, rel)
		}
		return scaffoldFile(dstDir, rel, p, data)
	})
	if walkErr != nil {
		return apperr.Wrap(walkErr, "api.agents.errors.scaffold", "path", dstDir)
	}
	return nil
}

func scaffoldMkdir(dstDir, rel string) error {
	target := dstDir
	if rel != "." {
		target = filepath.Join(dstDir, rel)
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return apperr.Wrap(err, "api.agents.errors.mkdir", "path", target)
	}
	return nil
}

func scaffoldFile(dstDir, rel, p string, data struct{ Name string }) error {
	content, err := scaffoldFS.ReadFile(p)
	if err != nil {
		return apperr.Wrap(err, "api.agents.errors.read_embedded", "path", p)
	}
	outRel := rel
	if strings.HasSuffix(rel, ".tmpl") {
		tmpl, terr := template.New(rel).Parse(string(content))
		if terr != nil {
			return apperr.Wrap(terr, "api.agents.errors.parse_template", "path", rel)
		}
		var buf strings.Builder
		if eerr := tmpl.Execute(&buf, data); eerr != nil {
			return apperr.Wrap(eerr, "api.agents.errors.render_template", "path", rel)
		}
		content = []byte(buf.String())
		outRel = strings.TrimSuffix(rel, ".tmpl")
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(outRel, ".sh") {
		mode = 0o755
	}
	full := filepath.Join(dstDir, outRel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return apperr.Wrap(err, "api.agents.errors.mkdir_parent", "path", filepath.Dir(full))
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		return apperr.Wrap(err, "api.agents.errors.write_file", "path", full)
	}
	return nil
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	// Disable, don't delete: tasks reference the agent per stage, and removing the row breaks
	// history and /run/bundle for in-flight runs.
	if store != nil {
		if err := store.SetDisabled(r.Context(), tenant, name, true); err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		// Drop the binding so a later agent reusing the name starts on the workspace default, not
		// the deleted agent's profile.
		if err := s.dropAgentBinding(r.Context(), tenant, name); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		s.invalidateCatalog(tenant)
		s.auditAgentDef(r, tenant, name, "api.agents.audit.agent_deleted")
		writeStatus(w, "disabled")
		return
	}

	dir, err := s.agentDir(name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.invalidateCatalog("")
	writeStatus(w, "deleted")
}

func (s *Server) dropAgentBinding(ctx context.Context, tenant, name string) error {
	if s.secretsStore == nil || tenant == "" {
		return nil
	}
	return s.secretsStore.DeleteAgentBinding(ctx, tenant, name)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, info.Mode())
		}
		if info.IsDir() {
			if isRuntimeDir(filepath.Base(rel)) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode())
		}
		in, err := safepath.Open(src, rel)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := safepath.Create(dst, rel)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
