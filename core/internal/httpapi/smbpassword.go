package httpapi

import (
	"net/http"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
)

// smbPasswordAPI lets each member draw, replace and revoke their own SMB
// password. The account is always the session's: no route names another one.
type smbPasswordAPI struct {
	auth *auth.Service
}

// smbPasswordCreated is the answer to a new password: the only time the
// password itself leaves the core.
type smbPasswordCreated struct {
	auth.SMBPasswordStatus
	Password string `json:"password"`
}

func (a *smbPasswordAPI) status(w http.ResponseWriter, r *http.Request) {
	acc, _ := auth.AccountFrom(r.Context())
	st, err := a.auth.SMBPasswordStatus(r.Context(), acc.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// create draws a new SMB password, replacing the account's previous one.
func (a *smbPasswordAPI) create(w http.ResponseWriter, r *http.Request) {
	acc, _ := auth.AccountFrom(r.Context())
	password, st, err := a.auth.NewSMBPassword(r.Context(), acc.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, smbPasswordCreated{SMBPasswordStatus: st, Password: password})
}

func (a *smbPasswordAPI) revoke(w http.ResponseWriter, r *http.Request) {
	acc, _ := auth.AccountFrom(r.Context())
	if err := a.auth.RevokeSMBPassword(r.Context(), acc.Name); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
