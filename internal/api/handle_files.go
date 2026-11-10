package api

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/watcher"
)

type fileEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir"`
}

func (s *Server) getOutputDir(ctx context.Context, containerID string) (string, error) {
	return watcher.DetectOutputDir(ctx, s.docker, containerID)
}

func (s *Server) resolveOutputDir(ctx context.Context, a *agent.Agent) (string, error) {
	if a.OutputDir != "" {
		return a.OutputDir, nil
	}
	if dir := s.runOutputDirOf(a.Instance); dir != "" {
		return dir, nil
	}
	return s.getOutputDir(ctx, a.ID)
}

func listFiles(dir string) ([]fileEntry, error) {
	// os.Root: the agent controls the output dir's contents, so symlinks must not lead out of it.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var files []fileEntry
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == "." {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, fileEntry{
			Path: path,
			Name: d.Name(),
			Size: info.Size(),
			Dir:  d.IsDir(),
		})
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, "api.files.errors.walk_output_dir")
	}
	return files, nil
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}

	outputDir, err := s.resolveOutputDir(r.Context(), a)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	files, err := listFiles(outputDir)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleServeFile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentForCaller(w, r)
	if !ok {
		return
	}

	filePath := r.PathValue("path")
	if strings.Contains(filePath, "..") {
		writeErrKey(w, r, http.StatusBadRequest, "api.files.errors.invalid_path")
		return
	}

	outputDir, err := s.resolveOutputDir(r.Context(), a)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// os.Root: filePath is request-controlled and the agent controls outputDir, so symlinks must
	// not escape it.
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	defer root.Close()
	f, err := root.Open(strings.TrimPrefix(filePath, "/"))
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.files.errors.file_not_found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeErrKey(w, r, http.StatusNotFound, "api.files.errors.file_not_found")
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}
