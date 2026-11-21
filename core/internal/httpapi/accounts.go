package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/edge"
)

type accountsAPI struct {
	auth *auth.Service
}

type credentials struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

// readJSON decodes a small JSON body; it answers 400 itself and returns false on error.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

// fail maps an auth error to its status; anything unexpected is logged and hidden.
func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrInvalid), errors.Is(err, auth.ErrWeakPassword):
		status = http.StatusBadRequest
	case errors.Is(err, auth.ErrBadCredentials), errors.Is(err, auth.ErrNoSession):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrNoAccount):
		status = http.StatusNotFound
	case errors.Is(err, auth.ErrSetupDone), errors.Is(err, auth.ErrAccountExists), errors.Is(err, auth.ErrLastAdmin):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrThrottled):
		status = http.StatusTooManyRequests
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		slog.Error("api", "err", err)
		msg = "internal error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// clientIP is the address the attempt limits count by: the client's, as the
// trusted proxy forwards it (edge.ClientIP).
func clientIP(r *http.Request) string { return edge.ClientIP(r) }

func (a *accountsAPI) setupNeeded(w http.ResponseWriter, r *http.Request) {
	needed, err := a.auth.SetupNeeded(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needed": needed})
}

// setup creates the first admin and signs them in. It answers 409 once any account exists.
func (a *accountsAPI) setup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	acc, err := a.auth.Setup(r.Context(), c.Name, c.DisplayName, c.Password)
	if err != nil {
		fail(w, err)
		return
	}
	a.signIn(w, r, acc, http.StatusCreated)
}

func (a *accountsAPI) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	token, acc, err := a.auth.Login(r.Context(), c.Name, c.Password, clientIP(r))
	if err != nil {
		fail(w, err)
		return
	}
	auth.SetCookie(w, r, token, a.auth.SessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{"account": acc, "token": token})
}

func (a *accountsAPI) signIn(w http.ResponseWriter, r *http.Request, acc auth.Account, status int) {
	token, err := a.auth.NewSession(r.Context(), acc.Name)
	if err != nil {
		fail(w, err)
		return
	}
	auth.SetCookie(w, r, token, a.auth.SessionTTL)
	writeJSON(w, status, map[string]any{"account": acc, "token": token})
}

func (a *accountsAPI) logout(w http.ResponseWriter, r *http.Request) {
	if token := auth.TokenFrom(r); token != "" {
		if err := a.auth.Logout(r.Context(), token); err != nil {
			fail(w, err)
			return
		}
	}
	auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *accountsAPI) me(w http.ResponseWriter, r *http.Request) {
	acc, _ := auth.AccountFrom(r.Context())
	writeJSON(w, http.StatusOK, acc)
}

func (a *accountsAPI) list(w http.ResponseWriter, r *http.Request) {
	accounts, err := a.auth.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (a *accountsAPI) create(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if c.Role == "" {
		c.Role = auth.RoleMember
	}
	acc, err := a.auth.Create(r.Context(), c.Name, c.DisplayName, c.Password, c.Role)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

func (a *accountsAPI) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	acc, err := a.auth.Update(r.Context(), r.PathValue("name"), body.Role, body.Disabled)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}
