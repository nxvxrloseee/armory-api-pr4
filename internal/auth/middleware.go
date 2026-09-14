package auth

import (
	"net/http"
	"strings"

	"armory_api/internal/apperr"
)

// RequireAuth reads "Authorization: Bearer <token>", resolves it to a user
// and puts the user in the request context. Any missing/invalid/expired
// token is the same 401 — this is the ONE place that can never be skipped
// by a client-side trick, unlike the redirect() on the Flutter side, which
// is only UI convenience.
func (r *Repo) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		token, ok := bearerToken(req)
		if !ok {
			apperr.Write(w, apperr.Unauthorized("Требуется вход в систему"))
			return
		}
		user, err := r.UserByAccessToken(req.Context(), token)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if user == nil {
			apperr.Write(w, apperr.Unauthorized("Сессия истекла или недействительна"))
			return
		}
		next.ServeHTTP(w, WithUser(req, user))
	})
}

// RequireRole must run AFTER RequireAuth (it reads the user RequireAuth put
// in the context). Kept as a separate middleware, not folded into
// RequireAuth, so read routes can require только auth while write routes
// additionally require a role — one auth check, composable role checks.
func RequireRole(min Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user := UserFromRequest(req)
			if user == nil {
				apperr.Write(w, apperr.Unauthorized("Требуется вход в систему"))
				return
			}
			if user.Role.Level() < min.Level() {
				apperr.Write(w, apperr.Forbidden("Недостаточно прав для этого действия"))
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func bearerToken(req *http.Request) (string, bool) {
	h := req.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return token, token != ""
}
