package auth

import (
	"net/http"

	"armory_api/internal/apperr"
)

// GuardHardDelete is called from inside each resource's del() handler,
// which handles both soft and hard delete on the same route
// (DELETE /{id} vs DELETE /{id}?hard=true) — a single RequireRole on the
// route can't tell those apart, so the query-param-specific check lives
// here instead. Restore doesn't need this: it's a distinct route and is
// gated by RequireRole(RoleAdmin) directly.
func GuardHardDelete(req *http.Request) *apperr.AppError {
	if req.URL.Query().Get("hard") != "true" {
		return nil
	}
	user := UserFromRequest(req)
	if user == nil || user.Role != RoleAdmin {
		return apperr.Forbidden("Физическое удаление доступно только администратору")
	}
	return nil
}
