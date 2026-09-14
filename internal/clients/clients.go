// Package clients implements /api/clients. No referential-integrity guard
// on delete (nothing points at a client), but email uniqueness plays the
// same role sku does for weapons: a 23505 on ux_clients_email_lower turns
// into apperr.Validation({"email": ...}) so ApiClientRepository can rethrow
// it as the client's existing UniqueConstraintException unchanged.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"armory_api/internal/apperr"
	"armory_api/internal/auth"
	"armory_api/internal/httpx"
)

type Client struct {
	ID               int        `json:"id"`
	FullName         string     `json:"fullName"`
	Email            string     `json:"email"`
	Phone            string     `json:"phone"`
	LicenseNumber    string     `json:"licenseNumber"`
	LicenseIssuedAt  time.Time  `json:"licenseIssuedAt"`
	LicenseExpiresAt time.Time  `json:"licenseExpiresAt"`
	// ПР5: заполняются при регистрации покупателя (см. internal/auth) —
	// у клиентов, заведённых раньше продавцом вручную, могут быть пустыми.
	BirthDate      *time.Time `json:"birthDate"`
	PassportSeries *string    `json:"passportSeries"`
	PassportNumber *string    `json:"passportNumber"`
	DeletedAt      *time.Time `json:"deletedAt"`
}

type input struct {
	FullName         string    `json:"fullName"`
	Email            string    `json:"email"`
	Phone            string    `json:"phone"`
	LicenseNumber    string    `json:"licenseNumber"`
	LicenseIssuedAt  time.Time `json:"licenseIssuedAt"`
	LicenseExpiresAt time.Time `json:"licenseExpiresAt"`
}

var emailPattern = regexp.MustCompile(`^[\w.+-]+@[\w-]+\.[\w-]+(\.[\w-]+)*$`)

// validate mirrors Validators used by client_form_screen.dart: required +
// maxLength(120) on fullName, required + email() on email,
// lengthRange(5,20) on phone, lengthRange(3,30) on licenseNumber, both
// dates required and expiresAt strictly after issuedAt.
func (in input) validate() map[string]string {
	errs := map[string]string{}
	if len(in.FullName) == 0 {
		errs["fullName"] = "Обязательное поле"
	} else if len(in.FullName) > 120 {
		errs["fullName"] = "Не более 120 символов"
	}
	if len(in.Email) == 0 {
		errs["email"] = "Обязательное поле"
	} else if !emailPattern.MatchString(strings.TrimSpace(in.Email)) {
		errs["email"] = "Некорректный формат почты"
	}
	if l := len(in.Phone); l < 5 || l > 20 {
		errs["phone"] = "Длина от 5 до 20 символов"
	}
	if l := len(in.LicenseNumber); l < 3 || l > 30 {
		errs["licenseNumber"] = "Длина от 3 до 30 символов"
	}
	if in.LicenseIssuedAt.IsZero() {
		errs["licenseIssuedAt"] = "Укажите дату выдачи"
	}
	if in.LicenseExpiresAt.IsZero() {
		errs["licenseExpiresAt"] = "Укажите срок действия"
	} else if !in.LicenseIssuedAt.IsZero() && !in.LicenseExpiresAt.After(in.LicenseIssuedAt) {
		errs["licenseExpiresAt"] = "Должна быть позже даты выдачи"
	}
	return errs
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var sortColumns = map[string]string{"fullName": "full_name", "email": "email"}

const selectCols = "id, full_name, email, phone, license_number, license_issued_at, license_expires_at, birth_date, passport_series, passport_number, deleted_at"

func scanClient(row pgx.Row) (*Client, error) {
	var c Client
	err := row.Scan(&c.ID, &c.FullName, &c.Email, &c.Phone, &c.LicenseNumber,
		&c.LicenseIssuedAt, &c.LicenseExpiresAt, &c.BirthDate, &c.PassportSeries, &c.PassportNumber, &c.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repo) List(ctx context.Context, search string, includeDeleted bool, sortCol string, asc bool, page, size int) ([]Client, int, error) {
	where := "WHERE ($1 OR deleted_at IS NULL) AND ($2 = '' OR full_name ILIKE '%' || $2 || '%' OR email ILIKE '%' || $2 || '%')"
	order := sortCol
	if !asc {
		order += " DESC"
	} else {
		order += " ASC"
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM clients "+where, includeDeleted, search).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx,
		"SELECT "+selectCols+" FROM clients "+where+" ORDER BY "+order+", id LIMIT $3 OFFSET $4",
		includeDeleted, search, size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Client{}
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.FullName, &c.Email, &c.Phone, &c.LicenseNumber,
			&c.LicenseIssuedAt, &c.LicenseExpiresAt, &c.BirthDate, &c.PassportSeries, &c.PassportNumber, &c.DeletedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, c)
	}
	return items, total, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id int) (*Client, error) {
	return scanClient(r.pool.QueryRow(ctx, "SELECT "+selectCols+" FROM clients WHERE id = $1", id))
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (r *Repo) Create(ctx context.Context, in input) (*Client, error) {
	c, err := scanClient(r.pool.QueryRow(ctx,
		"INSERT INTO clients (full_name, email, phone, license_number, license_issued_at, license_expires_at) "+
			"VALUES ($1, $2, $3, $4, $5, $6) RETURNING "+selectCols,
		in.FullName, in.Email, in.Phone, in.LicenseNumber, in.LicenseIssuedAt, in.LicenseExpiresAt,
	))
	if isUniqueViolation(err, "ux_clients_email_lower") {
		return nil, apperr.Validation("Ошибка валидации", map[string]string{
			"email": "Почта «" + in.Email + "» уже зарегистрирована",
		})
	}
	return c, err
}

func (r *Repo) Update(ctx context.Context, id int, in input) (*Client, error) {
	c, err := scanClient(r.pool.QueryRow(ctx,
		"UPDATE clients SET full_name=$1, email=$2, phone=$3, license_number=$4, license_issued_at=$5, license_expires_at=$6 "+
			"WHERE id=$7 RETURNING "+selectCols,
		in.FullName, in.Email, in.Phone, in.LicenseNumber, in.LicenseIssuedAt, in.LicenseExpiresAt, id,
	))
	if isUniqueViolation(err, "ux_clients_email_lower") {
		return nil, apperr.Validation("Ошибка валидации", map[string]string{
			"email": "Почта «" + in.Email + "» уже зарегистрирована",
		})
	}
	return c, err
}

func (r *Repo) SoftDelete(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE clients SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) HardDelete(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "DELETE FROM clients WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) Restore(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE clients SET deleted_at = NULL WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) DeleteMany(ctx context.Context, ids []int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		"UPDATE clients SET deleted_at = now() WHERE id = ANY($1) AND deleted_at IS NULL", ids,
	)
	return int(tag.RowsAffected()), err
}

// --- HTTP handlers ---

// clients хранит персональные данные (паспорт, лицензия) — в отличие от
// каталога оружия, покупателю видна не вся коллекция, а только собственная
// запись (get() проверяет это сам, т.к. решение зависит от id в пути, а не
// только от роли — RequireRole тут не подходит).
func Routes(pool *pgxpool.Pool, authRepo *auth.Repo) chi.Router {
	repo := NewRepo(pool)
	r := chi.NewRouter()
	r.Use(authRepo.RequireAuth)
	r.Get("/{id}", get(repo))

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(auth.RoleSeller))
		r.Get("/", list(repo))
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
		user := auth.UserFromRequest(req)
		if user.Role == auth.RoleBuyer && (user.ClientID == nil || *user.ClientID != id) {
			apperr.Write(w, apperr.Forbidden("Нельзя посмотреть чужую карточку покупателя"))
			return
		}
		c, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if c == nil {
			apperr.Write(w, apperr.NotFound("Покупатель не найден"))
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
			apperr.Write(w, apperr.NotFound("Покупатель не найден"))
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
			apperr.Write(w, apperr.NotFound("Покупатель не найден"))
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
			apperr.Write(w, apperr.NotFound("Покупатель не найден"))
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
