package handlers

import (
	"net/http"
	"net/url"

	"github.com/zpooi/ProxyForge/backend/internal/auth"
)

func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	if needsSetup, err := h.Auth.NeedsSetup(); err == nil && needsSetup {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if _, err := r.Cookie(auth.SessionCookie); err == nil {
		if u, _ := h.Auth.UserFromRequest(r); u != nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	h.AppPage(w, r)
}

func (h *Handlers) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if needsSetup, err := h.Auth.NeedsSetup(); err == nil && needsSetup {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	token, err := h.Auth.Login(username, password)
	if err != nil {
		http.Redirect(w, r, "/login?error="+url.QueryEscape("用户名或密码错误"), http.StatusFound)
		return
	}
	h.Auth.SetSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handlers) SetupPage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.Auth.NeedsSetup()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !needsSetup {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	h.AppPage(w, r)
}

func (h *Handlers) SetupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	if password != confirm {
		redirectSetupError(w, r, "两次输入的密码不一致")
		return
	}
	token, err := h.Auth.SetupInitialUser(username, password)
	if err != nil {
		redirectSetupError(w, r, err.Error())
		return
	}
	h.Auth.SetSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		_ = h.Auth.Logout(c.Value)
	}
	h.Auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handlers) PasswordPage(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) PasswordSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u := auth.FromRequest(r)
	old := r.FormValue("old_password")
	username := r.FormValue("username")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")
	if newPassword != confirm {
		redirectPasswordError(w, r, "两次输入的密码不一致")
		return
	}
	if err := h.Auth.ChangeCredentials(u.ID, old, username, newPassword); err != nil {
		redirectPasswordError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func redirectPasswordError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/settings/password?error="+url.QueryEscape(msg), http.StatusFound)
}

func redirectSetupError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/setup?error="+url.QueryEscape(msg), http.StatusFound)
}
