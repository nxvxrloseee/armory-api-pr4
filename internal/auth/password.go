package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword/CheckPassword — единственное место, где пароль вообще
// встречается рядом с хешем. Клиент передаёт пароль как есть по HTTPS
// (в учебном проекте — по HTTP на localhost), хеширует только сервер: если
// бы хеширование делал клиент, сам хеш стал бы новым паролем и ничего не
// защищал бы (см. отчёт ПР5, раздел про CORS/безопасность).
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ValidatePasswordStrength mirrors the live check the assignment wants on
// the client (ПР5, оценка «4»): минимум 8 символов, хотя бы одна цифра,
// хотя бы один спецсимвол. Проверяется и на сервере — форма на клиенте
// можно обойти, поле пароля при регистрации — нет.
func ValidatePasswordStrength(pw string) string {
	if len(pw) < 8 {
		return "Пароль должен быть не короче 8 символов"
	}
	hasDigit, hasSpecial := false, false
	for _, r := range pw {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == ' ':
			// letters/spaces don't count as either bucket
		default:
			hasSpecial = true
		}
	}
	if !hasDigit {
		return "Пароль должен содержать хотя бы одну цифру"
	}
	if !hasSpecial {
		return "Пароль должен содержать хотя бы один специальный символ"
	}
	return ""
}
