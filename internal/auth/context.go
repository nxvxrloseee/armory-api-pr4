package auth

import "context"

var userCtxKey = ctxKey{}

func withUserCtx(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

func userFromCtx(ctx context.Context) *User {
	u, _ := ctx.Value(userCtxKey).(*User)
	return u
}
