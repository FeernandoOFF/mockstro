package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/feernandooff/mockstro/internal/gitrepo"
	"github.com/feernandooff/mockstro/internal/workspacecoord"
)

const gitTimeout = 60 * time.Second

func writeGitError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, gitrepo.ErrInvalidBranch):
		status = http.StatusBadRequest
	case errors.Is(err, gitrepo.ErrDirty), errors.Is(err, gitrepo.ErrNotRepository),
		errors.Is(err, workspacecoord.ErrBusy), errors.Is(err, workspacecoord.ErrClosed),
		errors.Is(err, errNoProject), errors.Is(err, errWorkspaceRunning):
		status = http.StatusConflict
	}
	writeError(w, status, err)
}

func (s *Server) gitInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := s.ws.GitStatus(ctx)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) gitFetch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	st, err := s.ws.Fetch(ctx)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) gitPull(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	message, st, err := s.ws.Pull(ctx)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": message, "status": st})
}

func (s *Server) gitBranches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	branches, err := s.ws.Branches(ctx)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

func (s *Server) gitSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	st, err := s.ws.SwitchBranch(ctx, req.Branch)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
