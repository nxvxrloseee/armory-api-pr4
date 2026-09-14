// Package auth implements ПР5: регистрация/вход покупателя, непрозрачные
// (не JWT) сессионные токены, и middleware, проверяющий роль на сервере —
// то самое разграничение прав, которое клиент обязан повторить у себя для
// удобства интерфейса, но НЕ может заменить собой (см. README раздел ПР5).
package auth

import "net/http"

// Role — три роли задания. Числовое значение (Level) даёт простое сравнение
// "не ниже такой-то роли", как в примере задания (auth.has(Role.librarian)).
type Role string

const (
	RoleBuyer  Role = "buyer"  // покупатель — аналог reader
	RoleSeller Role = "seller" // продавец (сотрудник магазина) — аналог librarian
	RoleAdmin  Role = "admin"  // администратор
)

func (r Role) Level() int {
	switch r {
	case RoleSeller:
		return 2
	case RoleAdmin:
		return 3
	default:
		return 1
	}
}

func (r Role) Valid() bool {
	return r == RoleBuyer || r == RoleSeller || r == RoleAdmin
}

// User — то, что кладётся в контекст запроса после проверки токена.
type User struct {
	ID       int
	Username string
	FullName string
	Role     Role
	ClientID *int // заполнен только у покупателя — id его же записи в clients
}

type ctxKey struct{}

func WithUser(r *http.Request, u *User) *http.Request {
	return r.WithContext(withUserCtx(r.Context(), u))
}

func UserFromRequest(r *http.Request) *User {
	return userFromCtx(r.Context())
}
