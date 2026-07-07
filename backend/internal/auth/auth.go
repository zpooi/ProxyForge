package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zpooi/ProxyForge/backend/internal/db"
)

const (
	SessionCookie = "wp_session"
	SessionTTL    = 24 * time.Hour
)

type Service struct {
	db *db.DB
}

func New(database *db.DB) *Service {
	return &Service{db: database}
}

func (s *Service) Login(username, password string) (string, error) {
	u, err := s.db.GetUserByUsername(username)
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", errors.New("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return "", errors.New("invalid credentials")
	}
	token := randomToken(32)
	if err := s.db.CreateSession(token, u.ID, SessionTTL); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) Logout(token string) error {
	return s.db.DeleteSession(token)
}

func (s *Service) NeedsSetup() (bool, error) {
	count, err := s.db.CountUsers()
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Service) SetupInitialUser(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if err := validateCredentials(username, password); err != nil {
		return "", err
	}
	needsSetup, err := s.NeedsSetup()
	if err != nil {
		return "", err
	}
	if !needsSetup {
		return "", errors.New("系统已经初始化")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if err := s.db.CreateUser(username, string(hash)); err != nil {
		return "", err
	}
	return s.Login(username, password)
}

func (s *Service) UserFromRequest(r *http.Request) (*AuthUser, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, nil
	}
	token := c.Value
	if token == "" {
		return nil, nil
	}
	u, err := s.db.GetSession(token)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	return &AuthUser{
		ID:                 u.ID,
		Username:           u.Username,
		MustChangePassword: u.MustChangePassword,
		Token:              token,
	}, nil
}

func (s *Service) ChangePassword(userID int64, oldPassword, newPassword string) error {
	return s.ChangeCredentials(userID, oldPassword, "", newPassword)
}

func (s *Service) ChangeCredentials(userID int64, oldPassword, username, newPassword string) error {
	// 直接根据 ID 取
	var currentUsername, hash string
	err := s.db.Conn().QueryRow("SELECT username, password_hash FROM users WHERE id = ?", userID).Scan(&currentUsername, &hash)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)); err != nil {
		return errors.New("旧密码不正确")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = currentUsername
	}
	if err := validateCredentials(username, newPassword); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.db.UpdateUserCredentials(userID, username, string(newHash)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("用户名 %q 已存在", username)
		}
		return err
	}
	return nil
}

func (s *Service) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL.Seconds()),
	})
}

func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type AuthUser struct {
	ID                 int64
	Username           string
	MustChangePassword bool
	Token              string
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Middleware 强制登录；放行 /login 和前端静态资源。
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/login" || path == "/setup" || path == "/style.css" || strings.HasPrefix(path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		if needsSetup, err := s.NeedsSetup(); err != nil {
			http.Error(w, "auth setup check failed", http.StatusInternalServerError)
			return
		} else if needsSetup {
			if strings.HasPrefix(path, "/api/") {
				http.Error(w, "setup required", http.StatusPreconditionRequired)
				return
			}
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		user, err := s.UserFromRequest(r)
		if err != nil || user == nil {
			if strings.HasPrefix(path, "/api/") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if user.MustChangePassword && path != "/settings/password" && path != "/style.css" && !strings.HasPrefix(path, "/assets/") {
			http.Redirect(w, r, "/settings/password", http.StatusFound)
			return
		}
		ctx := WithUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateCredentials(username, password string) error {
	if len(username) < 3 {
		return errors.New("用户名至少 3 位")
	}
	if strings.ContainsAny(username, " \t\r\n:/@") {
		return errors.New("用户名不能包含空格、冒号、斜杠或 @")
	}
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	return nil
}
