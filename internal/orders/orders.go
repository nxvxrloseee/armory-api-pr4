// Package orders implements the missing "Client ↔ Weapon" transaction that
// КОНТРАКТ-API.md models as Loan for the library domain (Reader borrows a
// Book) — here it's a purchase, not a loan: a buyer orders a weapon model,
// a seller hands it over and records the physical unit's serial number.
// This is the screen that makes the "buyer" role genuinely different from
// "just a read-only visitor", per ПР5's requirement that each role has at
// least one function the others don't.
package orders

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armory_api/internal/apperr"
	"armory_api/internal/auth"
	"armory_api/internal/httpx"
)

type Order struct {
	ID           int        `json:"id"`
	ClientID     int        `json:"clientId"`
	WeaponID     int        `json:"weaponId"`
	Status       string     `json:"status"`
	SerialNumber *string    `json:"serialNumber"`
	CreatedAt    time.Time  `json:"createdAt"`
	PickedUpAt   *time.Time `json:"pickedUpAt"`

	// Денормализованные поля только для чтения — чтобы список заказов не
	// заставлял клиент делать ещё по одному запросу на каждую строку.
	ClientName string `json:"clientName"`
	WeaponName string `json:"weaponName"`
	Price      int    `json:"price"`
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const selectColumns = `o.id, o.client_id, o.weapon_id, o.status, o.serial_number, o.created_at, o.picked_up_at,
	c.full_name, w.name, w.price`

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.ClientID, &o.WeaponID, &o.Status, &o.SerialNumber, &o.CreatedAt, &o.PickedUpAt,
		&o.ClientName, &o.WeaponName, &o.Price)
	return o, err
}

// Create — покупка доступна только если у модели есть остаток на складе;
// это та же гарантия, что даёт контракт для выдачи книги
// (copiesAvailable == 0 → 409), только тут решение принимается одним
// UPDATE ... WHERE stock_available > 0 внутри транзакции, чтобы два
// одновременных заказа не увели остаток в минус (race condition, которую
// проверка "сначала SELECT, потом UPDATE" не закрывает).
func (r *Repo) Create(ctx context.Context, clientID, weaponID int) (*Order, *apperr.AppError, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE weapons SET stock_available = stock_available - 1
		 WHERE id = $1 AND deleted_at IS NULL AND stock_available > 0`, weaponID)
	if err != nil {
		return nil, nil, err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM weapons WHERE id = $1 AND deleted_at IS NULL)", weaponID).Scan(&exists)
		if !exists {
			return nil, apperr.NotFound("Оружие не найдено"), nil
		}
		return nil, apperr.Conflict("Нет свободных экземпляров этой модели", 0), nil
	}

	var id int
	if err := tx.QueryRow(ctx,
		`INSERT INTO orders (client_id, weapon_id) VALUES ($1, $2) RETURNING id`,
		clientID, weaponID,
	).Scan(&id); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return r.Get(ctx, id)
}

func (r *Repo) Get(ctx context.Context, id int) (*Order, *apperr.AppError, error) {
	o, err := scanOrder(r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM orders o JOIN clients c ON c.id = o.client_id JOIN weapons w ON w.id = o.weapon_id
		 WHERE o.id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, apperr.NotFound("Заказ не найден"), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &o, nil, nil
}

// List: clientID == nil видит все заказы (продавец/администратор), иначе —
// только свои (покупатель). Пагинация — только page/size, без поиска и
// сортировки: для истории заказов одного покупателя и рабочего списка
// продавца этого достаточно, не стоит копировать весь набор фильтров
// оружия ради двух полей.
func (r *Repo) List(ctx context.Context, clientID *int, page, size int) ([]Order, int, error) {
	where := "WHERE ($1::int IS NULL OR o.client_id = $1)"
	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM orders o "+where, clientID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+selectColumns+` FROM orders o JOIN clients c ON c.id = o.client_id JOIN weapons w ON w.id = o.weapon_id `+
			where+` ORDER BY o.created_at DESC LIMIT $2 OFFSET $3`,
		clientID, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, o)
	}
	return items, total, rows.Err()
}

// Pickup — продавец/администратор фиксирует выдачу и присваивает серийный
// номер конкретного физического экземпляра (склад по сериям не ведём —
// см. комментарий в schema.sql, серийник появляется только в момент выдачи).
func (r *Repo) Pickup(ctx context.Context, id int, serial string) (*Order, *apperr.AppError, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE orders SET status = 'picked_up', serial_number = $1, picked_up_at = now()
		 WHERE id = $2 AND status = 'ordered'`, serial, id)
	if err != nil {
		return nil, nil, err
	}
	if tag.RowsAffected() == 0 {
		exists, appErr := r.exists(ctx, id)
		if appErr != nil {
			return nil, appErr, nil
		}
		if !exists {
			return nil, apperr.NotFound("Заказ не найден"), nil
		}
		return nil, apperr.Conflict("Заказ уже выдан или отменён", 0), nil
	}
	return r.Get(ctx, id)
}

// Cancel возвращает единицу на склад — доступно, пока заказ не выдан.
func (r *Repo) Cancel(ctx context.Context, id int) (*Order, *apperr.AppError, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	var weaponID int
	err = tx.QueryRow(ctx,
		`UPDATE orders SET status = 'cancelled' WHERE id = $1 AND status = 'ordered' RETURNING weapon_id`, id,
	).Scan(&weaponID)
	if err == pgx.ErrNoRows {
		exists, appErr := r.exists(ctx, id)
		if appErr != nil {
			return nil, appErr, nil
		}
		if !exists {
			return nil, apperr.NotFound("Заказ не найден"), nil
		}
		return nil, apperr.Conflict("Заказ уже выдан или отменён", 0), nil
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE weapons SET stock_available = stock_available + 1 WHERE id = $1", weaponID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return r.Get(ctx, id)
}

func (r *Repo) exists(ctx context.Context, id int) (bool, *apperr.AppError) {
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE id = $1)", id).Scan(&exists); err != nil {
		return false, nil
	}
	return exists, nil
}

// clientIDOf returns the ClientID to scope by for the requesting user:
// nil (see all) for seller/admin, their own for a buyer.
func clientIDOf(u *auth.User) *int {
	if u.Role == auth.RoleBuyer {
		return u.ClientID
	}
	return nil
}

// --- HTTP ---

func Routes(pool *pgxpool.Pool, authRepo *auth.Repo) chi.Router {
	repo := NewRepo(pool)
	r := chi.NewRouter()
	r.Use(authRepo.RequireAuth)

	r.Get("/", list(repo))
	r.With(auth.RequireRole(auth.RoleBuyer)).Post("/", create(repo))
	r.Post("/{id}/cancel", cancel(repo))
	r.With(auth.RequireRole(auth.RoleSeller)).Post("/{id}/pickup", pickup(repo))
	return r
}

func list(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		user := auth.UserFromRequest(req)
		q := req.URL.Query()
		page, size := httpx.ParsePage(q), httpx.ParseSize(q)
		items, total, err := repo.List(req.Context(), clientIDOf(user), page, size)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, httpx.NewPage(items, page, size, total))
	}
}

type createInput struct {
	WeaponID int `json:"weaponId"`
}

// create — покупатель заказывает только для себя: clientId берётся из
// собственного аккаунта (auth.User.ClientID), а не из тела запроса, иначе
// один покупатель мог бы оформлять заказы от имени другого, просто подставив
// чужой id в JSON.
func create(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		user := auth.UserFromRequest(req)
		if user.ClientID == nil {
			apperr.Write(w, apperr.Forbidden("У аккаунта нет привязанной карточки покупателя"))
			return
		}
		var in createInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		order, appErr, err := repo.Create(req.Context(), *user.ClientID, in.WeaponID)
		if appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		apperr.WriteJSON(w, http.StatusCreated, order)
	}
}

func cancel(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		user := auth.UserFromRequest(req)
		if user.Role == auth.RoleBuyer {
			order, appErr, err := repo.Get(req.Context(), id)
			if appErr != nil {
				apperr.Write(w, appErr)
				return
			}
			if err != nil {
				apperr.Write(w, err)
				return
			}
			if user.ClientID == nil || order.ClientID != *user.ClientID {
				apperr.Write(w, apperr.Forbidden("Нельзя отменить чужой заказ"))
				return
			}
		}
		order, appErr, err := repo.Cancel(req.Context(), id)
		if appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, order)
	}
}

type pickupInput struct {
	SerialNumber string `json:"serialNumber"`
}

func pickup(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := httpx.PathID(req, chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		var in pickupInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		if in.SerialNumber == "" {
			apperr.Write(w, apperr.Validation("Ошибка валидации", map[string]string{"serialNumber": "Обязательное поле"}))
			return
		}
		order, appErr, err := repo.Pickup(req.Context(), id, in.SerialNumber)
		if appErr != nil {
			apperr.Write(w, appErr)
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		httpx.WriteOK(w, order)
	}
}
