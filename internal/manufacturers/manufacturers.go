// Package manufacturers implements the /api/manufacturers resource: JSON
// shape, validation and SQL are kept together in one file because the
// resource itself is small — the same split Weapon needed (model / repo /
// handler in separate files) would just be ceremony here.
package manufacturers

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

type Manufacturer struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	Country   string     `json:"country"`
	Founded   int        `json:"founded"`
	DeletedAt *time.Time `json:"deletedAt"`
}

type input struct {
	Name    string `json:"name"`
	Country string `json:"country"`
	Founded int    `json:"founded"`
}

// validate mirrors lib/utils/validators.dart exactly (required + maxLength
// on name/country, intRange(1300,2100) on founded), so a request that
// somehow bypasses client-side validation gets the same messages back.
func (in input) validate() map[string]string {
	errs := map[string]string{}
	if len(in.Name) == 0 {
		errs["name"] = "Обязательное поле"
	} else if len(in.Name) > 120 {
		errs["name"] = "Не более 120 символов"
	}
	if len(in.Country) == 0 {
		errs["country"] = "Обязательное поле"
	} else if len(in.Country) > 60 {
		errs["country"] = "Не более 60 символов"
	}
	if in.Founded < 1300 || in.Founded > 2100 {
		errs["founded"] = "Год основания должен быть от 1300 до 2100"
	}
	return errs
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var sortColumns = map[string]string{"name": "name", "country": "country", "founded": "founded"}

func (r *Repo) List(ctx context.Context, search string, includeDeleted bool, sortCol string, asc bool, page, size int) ([]Manufacturer, int, error) {
	where := "WHERE ($1 OR deleted_at IS NULL) AND ($2 = '' OR name ILIKE '%' || $2 || '%' OR country ILIKE '%' || $2 || '%')"
	order := sortCol
	if !asc {
		order += " DESC"
	} else {
		order += " ASC"
	}

	var total int
	countSQL := "SELECT count(*) FROM manufacturers " + where
	if err := r.pool.QueryRow(ctx, countSQL, includeDeleted, search).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := "SELECT id, name, country, founded, deleted_at FROM manufacturers " + where +
		" ORDER BY " + order + ", id LIMIT $3 OFFSET $4"
	rows, err := r.pool.Query(ctx, listSQL, includeDeleted, search, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Manufacturer{}
	for rows.Next() {
		var m Manufacturer
		if err := rows.Scan(&m.ID, &m.Name, &m.Country, &m.Founded, &m.DeletedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, m)
	}
	return items, total, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id int) (*Manufacturer, error) {
	var m Manufacturer
	err := r.pool.QueryRow(ctx,
		"SELECT id, name, country, founded, deleted_at FROM manufacturers WHERE id = $1", id,
	).Scan(&m.ID, &m.Name, &m.Country, &m.Founded, &m.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Repo) Create(ctx context.Context, in input) (*Manufacturer, error) {
	var m Manufacturer
	err := r.pool.QueryRow(ctx,
		"INSERT INTO manufacturers (name, country, founded) VALUES ($1, $2, $3) RETURNING id, name, country, founded, deleted_at",
		in.Name, in.Country, in.Founded,
	).Scan(&m.ID, &m.Name, &m.Country, &m.Founded, &m.DeletedAt)
	return &m, err
}

func (r *Repo) Update(ctx context.Context, id int, in input) (*Manufacturer, error) {
	var m Manufacturer
	err := r.pool.QueryRow(ctx,
		"UPDATE manufacturers SET name=$1, country=$2, founded=$3 WHERE id=$4 RETURNING id, name, country, founded, deleted_at",
		in.Name, in.Country, in.Founded, id,
	).Scan(&m.ID, &m.Name, &m.Country, &m.Founded, &m.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &m, err
}

// activeWeaponCount backs the same guard PersistentManufacturerRepository
// runs client-side: only weapons that are not themselves soft-deleted count
// against a delete.
func (r *Repo) activeWeaponCount(ctx context.Context, id int) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		"SELECT count(*) FROM weapons WHERE manufacturer_id = $1 AND deleted_at IS NULL", id,
	).Scan(&n)
	return n, err
}

func (r *Repo) guardReferences(ctx context.Context, id int) error {
	n, err := r.activeWeaponCount(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.Conflict("Нельзя удалить: на производителя ссылается "+itoa(n)+" ед. оружия", n)
	}
	return nil
}

func (r *Repo) SoftDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "UPDATE manufacturers SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) HardDelete(ctx context.Context, id int) (bool, error) {
	if err := r.guardReferences(ctx, id); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, "DELETE FROM manufacturers WHERE id = $1", id)
	if isForeignKeyViolation(err) {
		return false, apperr.Conflict("Нельзя удалить: запись ещё используется", 0)
	}
	return tag.RowsAffected() > 0, err
}

func (r *Repo) Restore(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE manufacturers SET deleted_at = NULL WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) DeleteMany(ctx context.Context, ids []int) (int, error) {
	// Mirrors PersistentManufacturerRepository.deleteMany: bulk selection
	// intentionally skips the referential-integrity guard (see the Dart
	// comment there) — there is no per-row place to explain "can't delete,
	// N weapons reference this" for a multi-select action.
	tag, err := r.pool.Exec(ctx,
		"UPDATE manufacturers SET deleted_at = now() WHERE id = ANY($1) AND deleted_at IS NULL", ids,
	)
	return int(tag.RowsAffected()), err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
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
		search := q.Get("search")
		sortCol, asc := httpx.ParseSort(q, sortColumns, "name")
		page, size := httpx.ParsePage(q), httpx.ParseSize(q)

		items, total, err := repo.List(req.Context(), search, httpx.IncludeDeleted(q), sortCol, asc, page, size)
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
		m, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if m == nil {
			apperr.Write(w, apperr.NotFound("Производитель не найден"))
			return
		}
		httpx.WriteOK(w, m)
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
		m, err := repo.Create(req.Context(), in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, m)
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
		m, err := repo.Update(req.Context(), id, in)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if m == nil {
			apperr.Write(w, apperr.NotFound("Производитель не найден"))
			return
		}
		httpx.WriteOK(w, m)
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
			apperr.Write(w, apperr.NotFound("Производитель не найден"))
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
			apperr.Write(w, apperr.NotFound("Производитель не найден"))
			return
		}
		m, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, m)
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
