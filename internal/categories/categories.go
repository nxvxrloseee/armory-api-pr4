// Package categories implements /api/categories. Structurally identical to
// manufacturers (search+sort+page list, CRUD, soft/hard delete guarded by
// active-weapon references) except the guard query joins through the
// weapon_categories M:M table instead of a direct foreign-key column.
package categories

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

type Category struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	DeletedAt *time.Time `json:"deletedAt"`
}

type input struct {
	Name string `json:"name"`
}

func (in input) validate() map[string]string {
	errs := map[string]string{}
	if len(in.Name) == 0 {
		errs["name"] = "Обязательное поле"
	} else if len(in.Name) > 60 {
		errs["name"] = "Не более 60 символов"
	}
	return errs
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) List(ctx context.Context, search string, includeDeleted bool, asc bool, page, size int) ([]Category, int, error) {
	where := "WHERE ($1 OR deleted_at IS NULL) AND ($2 = '' OR name ILIKE '%' || $2 || '%')"
	order := "name ASC"
	if !asc {
		order = "name DESC"
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM categories "+where, includeDeleted, search).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx,
		"SELECT id, name, deleted_at FROM categories "+where+" ORDER BY "+order+", id LIMIT $3 OFFSET $4",
		includeDeleted, search, size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.DeletedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, c)
	}
	return items, total, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id int) (*Category, error) {
	var c Category
	err := r.pool.QueryRow(ctx, "SELECT id, name, deleted_at FROM categories WHERE id = $1", id).
		Scan(&c.ID, &c.Name, &c.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repo) Create(ctx context.Context, in input) (*Category, error) {
	var c Category
	err := r.pool.QueryRow(ctx,
		"INSERT INTO categories (name) VALUES ($1) RETURNING id, name, deleted_at", in.Name,
	).Scan(&c.ID, &c.Name, &c.DeletedAt)
	return &c, err
}

func (r *Repo) Update(ctx context.Context, id int, in input) (*Category, error) {
	var c Category
	err := r.pool.QueryRow(ctx,
		"UPDATE categories SET name=$1 WHERE id=$2 RETURNING id, name, deleted_at", in.Name, id,
	).Scan(&c.ID, &c.Name, &c.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (r *Repo) activeWeaponCount(ctx context.Context, id int) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM weapon_categories wc
		 JOIN weapons w ON w.id = wc.weapon_id
		 WHERE wc.category_id = $1 AND w.deleted_at IS NULL`, id,
	).Scan(&n)
	return n, err
}

func (r *Repo) guardReferences(ctx context.Context, id int) error {
	n, err := r.activeWeaponCount(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.Conflict("Нельзя удалить: на категорию ссылается "+itoa(n)+" ед. оружия", n)
	}
	return nil
}

func (r *Repo) SoftDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "UPDATE categories SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) HardDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "DELETE FROM categories WHERE id = $1", id)
	if isForeignKeyViolation(err) {
		return false, apperr.Conflict("Нельзя удалить: запись ещё используется", 0)
	}
	return tag.RowsAffected() > 0, err
}

func (r *Repo) Restore(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE categories SET deleted_at = NULL WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) DeleteMany(ctx context.Context, ids []int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		"UPDATE categories SET deleted_at = now() WHERE id = ANY($1) AND deleted_at IS NULL", ids,
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
		_, asc := httpx.ParseSort(q, map[string]string{"name": "name"}, "name")
		page, size := httpx.ParsePage(q), httpx.ParseSize(q)
		items, total, err := repo.List(req.Context(), q.Get("search"), httpx.IncludeDeleted(q), asc, page, size)
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
		c, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if c == nil {
			apperr.Write(w, apperr.NotFound("Категория не найдена"))
			return
		}
		httpx.WriteOK(w, c)
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
		c, err := repo.Create(req.Context(), in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, c)
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
		c, err := repo.Update(req.Context(), id, in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if c == nil {
			apperr.Write(w, apperr.NotFound("Категория не найдена"))
			return
		}
		httpx.WriteOK(w, c)
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
			apperr.Write(w, apperr.NotFound("Категория не найдена"))
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
			apperr.Write(w, apperr.NotFound("Категория не найдена"))
			return
		}
		c, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, c)
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
