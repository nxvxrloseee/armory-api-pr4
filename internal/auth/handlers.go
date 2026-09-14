package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"armory_api/internal/apperr"
)

type registerInput struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	FullName          string `json:"fullName"`
	Email             string `json:"email"`
	Phone             string `json:"phone"`
	BirthDate         string `json:"birthDate"`
	PassportSeries    string `json:"passportSeries"`
	PassportNumber    string `json:"passportNumber"`
	LicenseNumber     string `json:"licenseNumber"`
	LicenseIssuedAt   string `json:"licenseIssuedAt"`
	LicenseExpiresAt  string `json:"licenseExpiresAt"`
}

func (in registerInput) validate() map[string]string {
	errs := map[string]string{}
	if len(in.Username) < 3 || len(in.Username) > 40 {
		errs["username"] = "Длина от 3 до 40 символов"
	}
	if msg := ValidatePasswordStrength(in.Password); msg != "" {
		errs["password"] = msg
	}
	if in.FullName == "" {
		errs["fullName"] = "Обязательное поле"
	}
	if in.Email == "" {
		errs["email"] = "Обязательное поле"
	}
	if in.Phone == "" {
		errs["phone"] = "Обязательное поле"
	}
	if _, err := time.Parse("2006-01-02", in.BirthDate); err != nil {
		errs["birthDate"] = "Некорректная дата"
	}
	if in.PassportSeries == "" {
		errs["passportSeries"] = "Обязательное поле"
	}
	if in.PassportNumber == "" {
		errs["passportNumber"] = "Обязательное поле"
	}
	if in.LicenseNumber == "" {
		errs["licenseNumber"] = "Обязательное поле"
	}
	if _, err := time.Parse(time.RFC3339, withTimeSuffix(in.LicenseIssuedAt)); err != nil {
		errs["licenseIssuedAt"] = "Некорректная дата"
	}
	if _, err := time.Parse(time.RFC3339, withTimeSuffix(in.LicenseExpiresAt)); err != nil {
		errs["licenseExpiresAt"] = "Некорректная дата"
	}
	return errs
}

// withTimeSuffix позволяет присылать и чистую дату (2026-01-15), и полный
// ISO 8601 — на практике форма присылает дату, а не время.
func withTimeSuffix(s string) string {
	if len(s) == 10 {
		return s + "T00:00:00Z"
	}
	return s
}

func Register(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in registerInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		if errs := in.validate(); len(errs) > 0 {
			apperr.Write(w, apperr.Validation("Ошибка валидации", errs))
			return
		}
		hash, err := HashPassword(in.Password)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		birth, _ := time.Parse("2006-01-02", in.BirthDate)
		issued, _ := time.Parse(time.RFC3339, withTimeSuffix(in.LicenseIssuedAt))
		expires, _ := time.Parse(time.RFC3339, withTimeSuffix(in.LicenseExpiresAt))

		user, err := repo.CreateBuyer(req.Context(), in.Username, hash, ClientInput{
			FullName: in.FullName, Email: in.Email, Phone: in.Phone,
			LicenseNumber: in.LicenseNumber, LicenseIssuedAt: issued, LicenseExpiresAt: expires,
			BirthDate: birth, PassportSeries: in.PassportSeries, PassportNumber: in.PassportNumber,
		})
		if ErrUsernameTaken(err) {
			apperr.Write(w, apperr.Validation("Ошибка валидации", map[string]string{"username": "Логин «" + in.Username + "» уже занят"}))
			return
		}
		if ErrClientEmailTaken(err) {
			apperr.Write(w, apperr.Validation("Ошибка валидации", map[string]string{"email": "Почта «" + in.Email + "» уже используется"}))
			return
		}
		if err != nil {
			apperr.Write(w, err)
			return
		}
		writeSession(w, req, repo, user)
	}
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Login(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in loginInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		user, hash, err := repo.FindByUsername(req.Context(), in.Username)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if user == nil || !CheckPassword(hash, in.Password) {
			apperr.Write(w, apperr.Unauthorized("Неверный логин или пароль"))
			return
		}
		writeSession(w, req, repo, user)
	}
}

func writeSession(w http.ResponseWriter, req *http.Request, repo *Repo, user *User) {
	tp, err := repo.CreateSession(req.Context(), user.ID)
	if err != nil {
		apperr.Write(w, err)
		return
	}
	apperr.WriteJSON(w, http.StatusOK, map[string]any{
		"accessToken":  tp.AccessToken,
		"refreshToken": tp.RefreshToken,
		"expiresIn":    int(tp.AccessExpiresAt.Sub(time.Now().UTC()).Seconds()),
		"user":         userJSON(user),
	})
}

func userJSON(u *User) map[string]any {
	m := map[string]any{
		"id": u.ID, "username": u.Username, "fullName": u.FullName, "role": string(u.Role),
	}
	if u.ClientID != nil {
		m["clientId"] = *u.ClientID
	}
	return m
}

type refreshInput struct {
	RefreshToken string `json:"refreshToken"`
}

func Refresh(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in refreshInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		tp, err := repo.Refresh(req.Context(), in.RefreshToken)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if tp == nil {
			apperr.Write(w, apperr.Unauthorized("Токен обновления недействителен или истёк"))
			return
		}
		user, err := repo.UserByAccessToken(req.Context(), tp.AccessToken)
		if err != nil || user == nil {
			apperr.Write(w, apperr.Unauthorized("Не удалось обновить сессию"))
			return
		}
		apperr.WriteJSON(w, http.StatusOK, map[string]any{
			"accessToken":  tp.AccessToken,
			"refreshToken": tp.RefreshToken,
			"expiresIn":    int(tp.AccessExpiresAt.Sub(time.Now().UTC()).Seconds()),
			"user":         userJSON(user),
		})
	}
}

func Me() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		user := UserFromRequest(req)
		if user == nil {
			apperr.Write(w, apperr.Unauthorized("Требуется вход в систему"))
			return
		}
		apperr.WriteJSON(w, http.StatusOK, userJSON(user))
	}
}

func Logout(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		token, ok := bearerToken(req)
		if ok {
			_ = repo.Logout(req.Context(), token)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- admin: пользователи и роли ---

func ListUsers(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		users, err := repo.ListUsers(req.Context())
		if err != nil {
			apperr.Write(w, err)
			return
		}
		items := make([]map[string]any, len(users))
		for i, u := range users {
			items[i] = map[string]any{"id": u.ID, "username": u.Username, "fullName": u.FullName, "role": string(u.Role)}
		}
		apperr.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

type setRoleInput struct {
	Role string `json:"role"`
}

func SetRole(repo *Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := parsePathID(chi.URLParam(req, "id"))
		if !ok {
			apperr.Write(w, apperr.BadRequest("Некорректный идентификатор"))
			return
		}
		var in setRoleInput
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			apperr.Write(w, apperr.BadRequest("Некорректное тело запроса"))
			return
		}
		role := Role(in.Role)
		if !role.Valid() {
			apperr.Write(w, apperr.Validation("Ошибка валидации", map[string]string{"role": "Недопустимая роль"}))
			return
		}
		found, err := repo.SetRole(req.Context(), id, role)
		if err != nil {
			apperr.Write(w, err)
			return
		}
		if !found {
			apperr.Write(w, apperr.NotFound("Пользователь не найден"))
			return
		}
		apperr.WriteJSON(w, http.StatusOK, map[string]string{"message": "Роль обновлена"})
	}
}

func parsePathID(raw string) (int, bool) {
	n := 0
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}
