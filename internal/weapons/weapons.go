// Package weapons implements /api/weapons — the one resource with real M:M
// (categoryIds/designerIds go through junction tables) and two foreign-key
// families to validate: manufacturerId (a plain column) and category/
// designer ids (junction rows). Both existence checks are delegated to
// Postgres itself: a bad id fails as a foreign_key_violation, which create/
// update translate into the same 422 field errors the client already knows
// how to show, instead of running extra existence SELECTs by hand.
package weapons

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"armory_api/internal/apperr"
	"armory_api/internal/auth"
	"armory_api/internal/httpx"
)

type Weapon struct {
	ID             int        `json:"id"`
	Name           string     `json:"name"`
	SKU            string     `json:"sku"`
	Year           int        `json:"year"`
	Caliber        string     `json:"caliber"`
	ManufacturerID int        `json:"manufacturerId"`
	CategoryIDs    []int      `json:"categoryIds"`
	DesignerIDs    []int      `json:"designerIds"`
	Price          int        `json:"price"`
	StockTotal     int        `json:"stockTotal"`
	StockAvailable int        `json:"stockAvailable"`
	DeletedAt      *time.Time `json:"deletedAt"`
}

type input struct {
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	Year           int    `json:"year"`
	Caliber        string `json:"caliber"`
	ManufacturerID int    `json:"manufacturerId"`
	CategoryIDs    []int  `json:"categoryIds"`
	DesignerIDs    []int  `json:"designerIds"`
	Price          int    `json:"price"`
	StockTotal     int    `json:"stockTotal"`
	StockAvailable int    `json:"stockAvailable"`
}

// validate mirrors the field-by-field checks in weapon_form_screen.dart
// (Validators.required/maxLength/lengthRange/intRange/positiveInt, plus the
// hand-written stockAvailable<=stockTotal check). manufacturerId presence
// and category/designer id existence are NOT checked here — those go
// through the database as foreign keys, see mapForeignKeyError.
func (in input) validate() map[string]string {
	errs := map[string]string{}
	if len(in.Name) == 0 {
		errs["name"] = "Обязательное поле"
	} else if len(in.Name) > 120 {
		errs["name"] = "Не более 120 символов"
	}
	if l := len(in.SKU); l < 3 || l > 40 {
		errs["sku"] = "Длина от 3 до 40 символов"
	}
	if in.Year < 1870 || in.Year > 2100 {
		errs["year"] = "Год должен быть от 1870 до 2100"
	}
	if len(in.Caliber) == 0 {
		errs["caliber"] = "Обязательное поле"
	} else if len(in.Caliber) > 30 {
		errs["caliber"] = "Не более 30 символов"
	}
	if in.ManufacturerID == 0 {
		errs["manufacturerId"] = "Выберите производителя"
	}
	if len(in.CategoryIDs) == 0 {
		errs["categoryIds"] = "Выберите хотя бы одно значение"
	}
	if len(in.DesignerIDs) == 0 {
		errs["designerIds"] = "Выберите хотя бы одно значение"
	}
	if in.Price <= 0 {
		errs["price"] = "Введите положительное число"
	}
	if in.StockTotal <= 0 {
		errs["stockTotal"] = "Введите положительное число"
	}
	if in.StockAvailable < 0 {
		errs["stockAvailable"] = "Введите неотрицательное число"
	} else if in.StockAvailable > in.StockTotal {
		errs["stockAvailable"] = "Не больше остатка «Всего на складе»"
	}
	return errs
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var sortColumns = map[string]string{"name": "name", "year": "year", "price": "price"}

func (r *Repo) List(ctx context.Context, search string, categoryID, manufacturerID, designerID, yearFrom, yearTo *int, includeDeleted bool, sortCol string, asc bool, page, size int) ([]Weapon, int, error) {
	// Filtering on categoryId/designerId needs an EXISTS against the
	// junction table; everything else is a plain column predicate on w.
	where := `WHERE ($1 OR w.deleted_at IS NULL)
		AND ($2 = '' OR w.name ILIKE '%' || $2 || '%' OR w.sku ILIKE '%' || $2 || '%')
		AND ($3::int IS NULL OR EXISTS (SELECT 1 FROM weapon_categories wc WHERE wc.weapon_id = w.id AND wc.category_id = $3))
		AND ($4::int IS NULL OR w.manufacturer_id = $4)
		AND ($5::int IS NULL OR EXISTS (SELECT 1 FROM weapon_designers wd WHERE wd.weapon_id = w.id AND wd.designer_id = $5))
		AND ($6::int IS NULL OR w.year >= $6)
		AND ($7::int IS NULL OR w.year <= $7)`
	args := []any{includeDeleted, search, categoryID, manufacturerID, designerID, yearFrom, yearTo}

	order := "w." + sortCol
	if !asc {
		order += " DESC"
	} else {
		order += " ASC"
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM weapons w "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := r.pool.Query(ctx,
		`SELECT w.id, w.name, w.sku, w.year, w.caliber, w.manufacturer_id, w.price, w.stock_total, w.stock_available, w.deleted_at
		 FROM weapons w `+where+` ORDER BY `+order+`, w.id LIMIT $8 OFFSET $9`,
		listArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	ids := []int{}
	items := []Weapon{}
	for rows.Next() {
		var wp Weapon
		if err := rows.Scan(&wp.ID, &wp.Name, &wp.SKU, &wp.Year, &wp.Caliber, &wp.ManufacturerID,
			&wp.Price, &wp.StockTotal, &wp.StockAvailable, &wp.DeletedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		items = append(items, wp)
		ids = append(ids, wp.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if err := r.attachRelations(ctx, items, ids); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// attachRelations fills CategoryIDs/DesignerIDs for a batch of already
// loaded weapons with two IN-queries instead of N+1 round trips per row.
func (r *Repo) attachRelations(ctx context.Context, items []Weapon, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	index := make(map[int]int, len(items))
	for i, it := range items {
		index[it.ID] = i
		items[i].CategoryIDs = []int{}
		items[i].DesignerIDs = []int{}
	}

	catRows, err := r.pool.Query(ctx, "SELECT weapon_id, category_id FROM weapon_categories WHERE weapon_id = ANY($1) ORDER BY category_id", ids)
	if err != nil {
		return err
	}
	for catRows.Next() {
		var weaponID, categoryID int
		if err := catRows.Scan(&weaponID, &categoryID); err != nil {
			catRows.Close()
			return err
		}
		i := index[weaponID]
		items[i].CategoryIDs = append(items[i].CategoryIDs, categoryID)
	}
	catRows.Close()
	if err := catRows.Err(); err != nil {
		return err
	}

	desRows, err := r.pool.Query(ctx, "SELECT weapon_id, designer_id FROM weapon_designers WHERE weapon_id = ANY($1) ORDER BY designer_id", ids)
	if err != nil {
		return err
	}
	for desRows.Next() {
		var weaponID, designerID int
		if err := desRows.Scan(&weaponID, &designerID); err != nil {
			desRows.Close()
			return err
		}
		i := index[weaponID]
		items[i].DesignerIDs = append(items[i].DesignerIDs, designerID)
	}
	desRows.Close()
	return desRows.Err()
}

func (r *Repo) Get(ctx context.Context, id int) (*Weapon, error) {
	var wp Weapon
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, sku, year, caliber, manufacturer_id, price, stock_total, stock_available, deleted_at
		 FROM weapons WHERE id = $1`, id,
	).Scan(&wp.ID, &wp.Name, &wp.SKU, &wp.Year, &wp.Caliber, &wp.ManufacturerID,
		&wp.Price, &wp.StockTotal, &wp.StockAvailable, &wp.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := []Weapon{wp}
	if err := r.attachRelations(ctx, items, []int{wp.ID}); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// mapForeignKeyError turns a violated FK into the same field name the
// client-side validator would have used, so 422 handling in the Flutter
// form doesn't need to know whether the check ran on the client or here.
func mapForeignKeyError(err error) *apperr.AppError {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return nil
	}
	switch pgErr.ConstraintName {
	case "weapons_manufacturer_id_fkey":
		return apperr.Validation("Ошибка валидации", map[string]string{"manufacturerId": "Производитель не найден"})
	case "weapon_categories_category_id_fkey":
		return apperr.Validation("Ошибка валидации", map[string]string{"categoryIds": "Категория не найдена"})
	case "weapon_designers_designer_id_fkey":
		return apperr.Validation("Ошибка валидации", map[string]string{"designerIds": "Конструктор не найден"})
	default:
		return apperr.Conflict("Нарушено ограничение целостности", 0)
	}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (r *Repo) Create(ctx context.Context, in input) (*Weapon, *apperr.AppError, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	var wp Weapon
	err = tx.QueryRow(ctx,
		`INSERT INTO weapons (name, sku, year, caliber, manufacturer_id, price, stock_total, stock_available)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, name, sku, year, caliber, manufacturer_id, price, stock_total, stock_available, deleted_at`,
		in.Name, in.SKU, in.Year, in.Caliber, in.ManufacturerID, in.Price, in.StockTotal, in.StockAvailable,
	).Scan(&wp.ID, &wp.Name, &wp.SKU, &wp.Year, &wp.Caliber, &wp.ManufacturerID,
		&wp.Price, &wp.StockTotal, &wp.StockAvailable, &wp.DeletedAt)
	if isUniqueViolation(err, "ux_weapons_sku_lower") {
		return nil, apperr.Validation("Ошибка валидации", map[string]string{
			"sku": "Артикул «" + in.SKU + "» уже используется",
		}), nil
	}
	if appErr := mapForeignKeyError(err); appErr != nil {
		return nil, appErr, nil
	}
	if err != nil {
		return nil, nil, err
	}

	if err := replaceRelations(ctx, tx, wp.ID, in.CategoryIDs, in.DesignerIDs); err != nil {
		if appErr := mapForeignKeyError(err); appErr != nil {
			return nil, appErr, nil
		}
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	wp.CategoryIDs, wp.DesignerIDs = in.CategoryIDs, in.DesignerIDs
	return &wp, nil, nil
}

func (r *Repo) Update(ctx context.Context, id int, in input) (*Weapon, *apperr.AppError, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	var wp Weapon
	err = tx.QueryRow(ctx,
		`UPDATE weapons SET name=$1, sku=$2, year=$3, caliber=$4, manufacturer_id=$5, price=$6, stock_total=$7, stock_available=$8
		 WHERE id=$9
		 RETURNING id, name, sku, year, caliber, manufacturer_id, price, stock_total, stock_available, deleted_at`,
		in.Name, in.SKU, in.Year, in.Caliber, in.ManufacturerID, in.Price, in.StockTotal, in.StockAvailable, id,
	).Scan(&wp.ID, &wp.Name, &wp.SKU, &wp.Year, &wp.Caliber, &wp.ManufacturerID,
		&wp.Price, &wp.StockTotal, &wp.StockAvailable, &wp.DeletedAt)
	if isUniqueViolation(err, "ux_weapons_sku_lower") {
		return nil, apperr.Validation("Ошибка валидации", map[string]string{
			"sku": "Артикул «" + in.SKU + "» уже используется",
		}), nil
	}
	if appErr := mapForeignKeyError(err); appErr != nil {
		return nil, appErr, nil
	}
	if err == pgx.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	if err := replaceRelations(ctx, tx, id, in.CategoryIDs, in.DesignerIDs); err != nil {
		if appErr := mapForeignKeyError(err); appErr != nil {
			return nil, appErr, nil
		}
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	wp.CategoryIDs, wp.DesignerIDs = in.CategoryIDs, in.DesignerIDs
	return &wp, nil, nil
}

func replaceRelations(ctx context.Context, tx pgx.Tx, weaponID int, categoryIDs, designerIDs []int) error {
	if _, err := tx.Exec(ctx, "DELETE FROM weapon_categories WHERE weapon_id = $1", weaponID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM weapon_designers WHERE weapon_id = $1", weaponID); err != nil {
		return err
	}
	for _, cid := range categoryIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO weapon_categories (weapon_id, category_id) VALUES ($1, $2)", weaponID, cid); err != nil {
			return err
		}
	}
	for _, did := range designerIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO weapon_designers (weapon_id, designer_id) VALUES ($1, $2)", weaponID, did); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) SoftDelete(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE weapons SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) HardDelete(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "DELETE FROM weapons WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) Restore(ctx context.Context, id int) (bool, error) {
	tag, err := r.pool.Exec(ctx, "UPDATE weapons SET deleted_at = NULL WHERE id = $1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repo) DeleteMany(ctx context.Context, ids []int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		"UPDATE weapons SET deleted_at = now() WHERE id = ANY($1) AND deleted_at IS NULL", ids,
	)
	return int(tag.RowsAffected()), err
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
		r.Delete("/{id}", del(repo)) // ?hard=true дополнительно требует admin — см. GuardHardDelete внутри
	})
	r.With(auth.RequireRole(auth.RoleAdmin)).Post("/{id}/restore", restore(repo))
	return r
}

func list(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		sortCol, asc := httpx.ParseSort(q, sortColumns, "name")
		page, size := httpx.ParsePage(q), httpx.ParseSize(q)

		categoryID := optInt(q, "categoryId")
		manufacturerID := optInt(q, "manufacturerId")
		designerID := optInt(q, "designerId")
		yearFrom := optInt(q, "yearFrom")
		yearTo := optInt(q, "yearTo")

		items, total, err := repo.List(req.Context(), q.Get("search"), categoryID, manufacturerID, designerID,
			yearFrom, yearTo, httpx.IncludeDeleted(q), sortCol, asc, page, size)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, httpx.NewPage(items, page, size, total))
	}
}

func optInt(q url.Values, key string) *int {
	n, ok := httpx.QueryInt(q, key)
	if !ok {
		return nil
	}
	return &n
}

func get(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		wp, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if wp == nil {
			apperr.Write(w, apperr.NotFound("Оружие не найдено"))
			return
		}
		httpx.WriteOK(w, wp)
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
		wp, appErr, err := repo.Create(req.Context(), in)
		if appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, wp)
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
		wp, appErr, err := repo.Update(req.Context(), id, in)
		if appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if wp == nil {
			apperr.Write(w, apperr.NotFound("Оружие не найдено"))
			return
		}
		httpx.WriteOK(w, wp)
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
			apperr.Write(w, apperr.NotFound("Оружие не найдено"))
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
			apperr.Write(w, apperr.NotFound("Оружие не найдено"))
			return
		}
		wp, err := repo.Get(req.Context(), id)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, wp)
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
