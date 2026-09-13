# armory_api

Бэкенд для `armory_web` (ПР4 «Подключение REST API»): реализует тот же
контракт, что учебный `mock-server.js` (`~/Downloads/api/КОНТРАКТ-API.md`),
для домена оружейного магазина — на Go + PostgreSQL вместо готового
учебного сервера.

## Стек

- **Go + `net/http` + [chi](https://github.com/go-chi/chi)** — без веб-фреймворка
  тяжелее chi: он только маршрутизация и middleware, остальное — стандартная
  библиотека.
- **[pgx v5](https://github.com/jackc/pgx)** (`pgxpool`) напрямую, без ORM —
  сущностей пять, запросы простые, ORM добавил бы косвенность без пользы.
  Ссылочная целостность и уникальность проверяются самой БД (внешние ключи,
  уникальные индексы), а не вручную в коде.
- **PostgreSQL**, реальные junction-таблицы (`weapon_categories`,
  `weapon_designers`) для M:M — то, что на клиенте в ПР3 было осознанной
  денормализацией (списки id в модели), здесь настоящая 3NF-схема.
- Схема (`internal/dbx/schema.sql`) — не набор миграций, а один идемпотентный
  файл (`CREATE TABLE IF NOT EXISTS` + `INSERT ... ON CONFLICT DO NOTHING`),
  применяется целиком при каждом старте. Оправдано тем, что схема здесь
  ровно одна и не меняется версиями.

## Запуск

Один раз — база и роль (уже сделано на этой машине, роль `armory`/пароль
`armory`, база `armory`):

```sh
psql -U postgres -c "CREATE ROLE armory LOGIN PASSWORD 'armory';"
psql -U postgres -c "CREATE DATABASE armory OWNER armory TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'en_US.UTF-8' LC_CTYPE 'en_US.UTF-8';"
```

Дальше просто:

```sh
go run .
```

Слушает `http://localhost:8080/api`, схему и стартовые данные накатывает сам
при каждом запуске (безопасно повторять). Проверка: `http://localhost:8080/api/__health`.

Переменные окружения (все опциональны, показаны значения по умолчанию):

```sh
DATABASE_URL=postgres://armory:armory@localhost:5432/armory
PORT=8080
ALLOWED_ORIGIN=http://localhost:5555   # см. КОНТРАКТ-API.md §6.2
```

## Документация API

`http://localhost:8080/docs` — Swagger UI (страница + `swagger-ui-dist` с
CDN, без отдельного шага сборки), читает спеку с `/api/openapi.yaml`.
Спека — рукописный `internal/openapi/openapi.yaml`, вшита в бинарник через
`go:embed`; правится вручную при изменении API, синхронизация с кодом не
автоматическая.

## Диагностические ручки (как у mock-server.js)

- `GET /api/__health` — проверка живости.
- `POST /api/__reset` — стирает все таблицы и накатывает сид заново
  (аналог `POST /api/__reset` у учебного сервера).
- `?__delay=1500` — задержка ответа (для проверки индикатора загрузки).
- `?__fail=500` — принудительный код ошибки на любой запрос (для проверки
  состояния ошибки).

## Ресурсы

`weapons`, `manufacturers`, `categories`, `designers`, `clients` — у каждого
одинаковый набор: `GET /`, `POST /`, `GET /{id}`, `PUT /{id}`,
`DELETE /{id}` (`?hard=true` — физическое), `POST /{id}/restore`,
`POST /bulk-delete`. Формат ошибок и оболочка списка — как в
КОНТРАКТ-API.md §1.

Аутентификация из контракта (роли `reader`/`librarian`/`admin`) сознательно
не реализована: в приложении нет экрана входа и заданию ПР4 она не нужна —
только на будущее, если её попросят добавить отдельно.
