package auth

import (
	"context"
	"net/http"
)

type contextKey struct{}

func WithUser(ctx context.Context, u *AuthUser) context.Context {
	return context.WithValue(ctx, contextKey{}, u)
}

func FromContext(ctx context.Context) *AuthUser {
	v := ctx.Value(contextKey{})
	if v == nil {
		return nil
	}
	u, _ := v.(*AuthUser)
	return u
}

// FromRequest 从 *http.Request 中取出当前登录用户。
func FromRequest(r *http.Request) *AuthUser {
	return FromContext(r.Context())
}
