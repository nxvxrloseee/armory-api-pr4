// Package designers implements /api/designers — same shape as
// manufacturers (three sortable fields, active-weapon guard on delete), but
// the guard joins through weapon_designers instead of a direct column.
package designers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"armory_api/internal/apperr"
	"armory_api/internal/auth"
	"armory_api/internal/httpx"
)

type Designer struct {
	ID          int        `json:"id"`
	FullName    string     `json:"fullName"`
	Country     string     `json:"country"`
	ActiveSince int        `json:"activeSince"`
	DeletedAt   *time.Time `json:"deletedAt"`
}

type input struct {
	FullName    string `json:"fullName"`
	Country     string `json:"country"`
	ActiveSince int    `json:"activeSince"`
}

func (in input) validate() map[string]string {
	errs := map[string]string{}
	if len(in.FullName) == 0 {
		errs["fullName"] = "Обязательное поле"
	} else if len(in.FullName) > 120 {
		errs["fullName"] = "Не более 120 символов"
	}
	if len(in.Country) == 0 {
		errs["country"] = "Обязательное поле"
	} else if len(in.Country) > 60 {
		errs["country"] = "Не более 60 символов"
	}
	if in.ActiveSince < 1300 || in.ActiveSince > 2100 {
		errs["activeSince"] = "Год должен быть от 1300 до 2100"
	}
	return errs
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var sortColumns = map[string]string{"fullName": "full_name", "country": "country", "activeSince": "active_since"}

func (r *Repo) List(ctx context.Context, search string, includeDeleted bool, sortCol string, asc bool, page, size int) ([]Designer, int, error) {
	where := "WHERE ($1 OR deleted_at IS NULL) AND ($2 = '' OR full_name ILIKE '%' || $2 || '%' OR country ILIKE '%' || $2 || '%')"
	order := sortCol
	if !asc {
		order += " DESC"
	} else {
		order += " ASC"
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM designers "+where, includeDeleted, search).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx,
		"SELECT id, full_name, country, active_since, deleted_at FROM designers "+where+" ORDER BY "+order+", id LIMIT $3 OFFSET $4",
		includeDeleted, search, size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Designer{}
	for rows.Next() {
		var d Designer
		if err := rows.Scan(&d.ID, &d.FullName, &d.Country, &d.ActiveSince, &d.DeletedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, d)
	}
	return items, total, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id int) (*Designer, error) {
	var d Designer
	err := r.pool.QueryRow(ctx, "SELECT id, full_name, country, active_since, deleted_at FROM designers WHERE id = $1", id).
		Scan(&d.ID, &d.FullName, &d.Country, &d.ActiveSince, &d.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Repo) Create(ctx context.Context, in input) (*Designer, error) {
	var d Designer
	err := r.pool.QueryRow(ctx,
		"INSERT INTO designers (full_name, country, active_since) VALUES ($1, $2, $3) RETURNING id, full_name, country, active_since, deleted_at",
		in.FullName, in.Country, in.ActiveSince,
	).Scan(&d.ID, &d.FullName, &d.Country, &d.ActiveSince, &d.DeletedAt)
	return &d, err
}

func (r *Repo) Update(ctx context.Context, id int, in input) (*Designer, error) {
	var d Designer
	err := r.pool.QueryRow(ctx,
		"UPDATE designers SET full_name=$1, country=$2, active_since=$3 WHERE id=$4 RETURNING id, full_name, country, active_since, deleted_at",
		in.FullName, in.Country, in.ActiveSince, id,
	).Scan(&d.ID, &d.FullName, &d.Country, &d.ActiveSince, &d.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

func (r *Repo) activeWeaponCount(ctx context.Context, id int) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM weapon_designers wd
		 JOIN weapons w ON w.id = wd.weapon_id
		 WHERE wd.designer_id = $1 AND w.deleted_at IS NULL`, id,
	).Scan(&n)
	return n, err
}

func (r *Repo) guardReferences(ctx context.Context, id int) error {
	n, err := r.activeWeaponCount(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.Conflict("Нельзя удалить: с конструктором связано "+itoa(n)+" ед. оружия", n)
	}
	return nil
}

func (r *Repo) SoftDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "UPDATE designers SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) HardDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "DELETE FROM designers WHERE id = $1", id)
	if isForeignKeyViolation(err) {
		return false, apperr.Conflict("Нельзя удалить: запись ещё используется", 0)
	}
	return tag.RowsAffected() > 0, err
}

func (r *Repo) Restore(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE designers SET deleted_at = NULL WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) DeleteMany(ctx context.Context, ids []int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		"UPDATE designers SET deleted_at = now() WHERE id = ANY($1) AND deleted_at IS NULL", ids,
	)
	return int(tag.RowsAffected()), err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// --- HTTP handlers ---

func Routes(pool *pgxpool.Pool, authRepo *auth.Repo) chi.Router {
	repo := NewRepo(pool)
	r := chi.NewRouter()
	r.Use(authRepo.RequireAuth)
	r.Get("/", list(repo))
	r.Get("/{id}", get(repo))

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(auth.RoleSeller))
		r.Post("/", create(repo))
		r.Post("/bulk-delete", bulkDelete(repo))
		r.Put("/{id}", update(repo))
		r.Delete("/{id}", del(repo))
	})
	r.With(auth.RequireRole(auth.RoleAdmin)).Post("/{id}/restore", restore(repo))
	return r
}

func list(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		sortCol, asc := httpx.ParseSort(q, sortColumns, "fullName")
		page, size := httpx.ParsePage(q), httpx.ParseSize(q)
		items, total, err := repo.List(req.Context(), q.Get("search"), httpx.IncludeDeleted(q), sortCol, asc, page, size)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, httpx.NewPage(items, page, size, total))
	}
}

func get(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		d, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if d == nil {
			apperr.Write(w, apperr.NotFound("Конструктор не найден"))
			return
		}
		httpx.WriteOK(w, d)
	}
}

func decodeInput(req *http.Request, w http.ResponseWriter) (input, bool) {
	var in input
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
		return input{}, false
	}
	if errs := in.validate(); len(errs) > 0 {
		apperr.Write(w, apperr.Validation("Ошибка валидации", errs))
		return input{}, false
	}
	return in, true
}

func create(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		in, ok := decodeInput(req, w)
		if !ok {
			return
		}
		d, err := repo.Create(req.Context(), in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, d)
	}
}

func update(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		in, ok := decodeInput(req, w)
		if !ok {
			return
		}
		d, err := repo.Update(req.Context(), id, in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if d == nil {
			apperr.Write(w, apperr.NotFound("Конструктор не найден"))
			return
		}
		httpx.WriteOK(w, d)
	}
}

func del(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		if appErr := auth.GuardHardDelete(req); appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		hard := req.URL.Query().Get("hard") == "true"
		var found bool
		var err error
		if hard {
			found, err = repo.HardDelete(req.Context(), id)
		} else {
			found, err = repo.SoftDelete(req.Context(), id)
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if !found {
			apperr.Write(w, apperr.NotFound("Конструктор не найден"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func restore(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		found, err := repo.Restore(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if !found {
			apperr.Write(w, apperr.NotFound("Конструктор не найден"))
			return
		}
		d, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, d)
	}
}

func bulkDelete(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			IDs []int `json:"ids"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		n, err := repo.DeleteMany(req.Context(), body.IDs)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, map[string]int{"deleted": n})
	}
}
