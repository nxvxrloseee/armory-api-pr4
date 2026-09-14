package auth

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armory_api/internal/apperr"
)

// AuthRoutes — публичные (не требующие токена) ручки входа/регистрации,
// плюс /me и /logout, которым нужен только валидный access-токен, без роли.
func AuthRoutes(repo *Repo) chi.Router {
	r := chi.NewRouter()
	r.Post("/register", Register(repo))
	r.Post("/login", Login(repo))
	r.Post("/refresh", Refresh(repo))
	r.With(repo.RequireAuth).Get("/me", Me())
	r.With(repo.RequireAuth).Post("/logout", Logout(repo))
	return r
}

// UserRoutes — управление пользователями/ролями, только для admin.
func UserRoutes(repo *Repo) chi.Router {
	r := chi.NewRouter()
	r.Use(repo.RequireAuth, RequireRole(RoleAdmin))
	r.Get("/", ListUsers(repo))
	r.Put("/{id}/role", SetRole(repo))
	return r
}

// StatsHandler — единственный экран, недоступный никому, кроме admin
// ("просмотр статистики" из задания). Считает по всем пяти таблицам одним
// проходом — для пяти сущностей учебного проекта это дешевле, чем городить
// отдельный пакет статистики.
func StatsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		stats := map[string]any{}
		queries := map[string]string{
			"weapons":        "SELECT count(*) FROM weapons WHERE deleted_at IS NULL",
			"manufacturers":  "SELECT count(*) FROM manufacturers WHERE deleted_at IS NULL",
			"categories":     "SELECT count(*) FROM categories WHERE deleted_at IS NULL",
			"designers":      "SELECT count(*) FROM designers WHERE deleted_at IS NULL",
			"clients":        "SELECT count(*) FROM clients WHERE deleted_at IS NULL",
			"ordersActive":   "SELECT count(*) FROM orders WHERE status = 'ordered'",
			"ordersPickedUp": "SELECT count(*) FROM orders WHERE status = 'picked_up'",
			"usersTotal":     "SELECT count(*) FROM app_users WHERE deleted_at IS NULL",
		}
		for key, q := range queries {
			var n int
			if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
				apperr.Write(w, err)
				return
			}
			stats[key] = n
		}
		apperr.WriteJSON(w, http.StatusOK, stats)
	}
}
