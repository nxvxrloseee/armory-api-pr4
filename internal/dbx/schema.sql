-- Idempotent on purpose: executed on every server start (CREATE TABLE IF NOT
-- EXISTS / ON CONFLICT DO NOTHING) instead of a migration tool, since this
-- project has exactly one environment and one schema version.

CREATE TABLE IF NOT EXISTS manufacturers (
    id serial PRIMARY KEY,
    name varchar(120) NOT NULL,
    country varchar(60) NOT NULL,
    founded integer NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS categories (
    id serial PRIMARY KEY,
    name varchar(60) NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS designers (
    id serial PRIMARY KEY,
    full_name varchar(120) NOT NULL,
    country varchar(60) NOT NULL,
    active_since integer NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS clients (
    id serial PRIMARY KEY,
    full_name varchar(120) NOT NULL,
    email varchar(255) NOT NULL,
    phone varchar(20) NOT NULL,
    license_number varchar(30) NOT NULL,
    license_issued_at timestamptz NOT NULL,
    license_expires_at timestamptz NOT NULL,
    deleted_at timestamptz
);

-- ПР5: покупатель регистрируется под собственным логином, и при регистрации
-- заполняет те же личные данные, что раньше вносил только продавец через
-- форму клиента — отсюда три новых поля, нужных для выдачи оружия.
ALTER TABLE clients ADD COLUMN IF NOT EXISTS birth_date date;
ALTER TABLE clients ADD COLUMN IF NOT EXISTS passport_series varchar(10);
ALTER TABLE clients ADD COLUMN IF NOT EXISTS passport_number varchar(20);

-- ПР5: пользователи приложения. Роль покупателя всегда привязана к своей
-- записи в clients (client_id) — это и есть "покупатель — это клиент,
-- получивший логин"; у продавца/администратора client_id пустой.
CREATE TABLE IF NOT EXISTS app_users (
    id serial PRIMARY KEY,
    username varchar(60) NOT NULL,
    password_hash text NOT NULL,
    full_name varchar(120) NOT NULL,
    role varchar(20) NOT NULL CHECK (role IN ('buyer', 'seller', 'admin')),
    client_id integer REFERENCES clients(id),
    deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_app_users_username_lower ON app_users (lower(username));

-- Непрозрачные (не JWT) токены: проще для учебного проекта — не нужна
-- подпись/библиотека, "разлогинить" — это просто удалить строку. access_token
-- живёт недолго (ACCESS_TOKEN_TTL_SECONDS), refresh_token — неделю.
CREATE TABLE IF NOT EXISTS sessions (
    access_token varchar(64) PRIMARY KEY,
    refresh_token varchar(64) NOT NULL,
    user_id integer NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_sessions_refresh_token ON sessions (refresh_token);

-- Заказ — недостающая в ПР2–4 связь "покупатель ↔ оружие" (аналог Loan из
-- КОНТРАКТ-API.md, только на покупку, а не на выдачу во временное
-- пользование). serial_number проставляется продавцом при выдаче, не при
-- оформлении заказа — своей единицы товара на складе система не ведёт,
-- только общий остаток на модели (weapons.stock_available).
CREATE TABLE IF NOT EXISTS orders (
    id serial PRIMARY KEY,
    client_id integer NOT NULL REFERENCES clients(id),
    weapon_id integer NOT NULL REFERENCES weapons(id),
    status varchar(20) NOT NULL DEFAULT 'ordered' CHECK (status IN ('ordered', 'picked_up', 'cancelled')),
    serial_number varchar(60),
    created_at timestamptz NOT NULL DEFAULT now(),
    picked_up_at timestamptz
);

CREATE TABLE IF NOT EXISTS weapons (
    id serial PRIMARY KEY,
    name varchar(120) NOT NULL,
    sku varchar(40) NOT NULL,
    year integer NOT NULL,
    caliber varchar(30) NOT NULL,
    manufacturer_id integer NOT NULL REFERENCES manufacturers(id),
    price integer NOT NULL,
    stock_total integer NOT NULL,
    stock_available integer NOT NULL,
    deleted_at timestamptz
);

-- M:M — a real junction table now, replacing the id-list-on-the-model
-- denormalization the client used while everything lived in SharedPreferences.
CREATE TABLE IF NOT EXISTS weapon_categories (
    weapon_id integer NOT NULL REFERENCES weapons(id) ON DELETE CASCADE,
    category_id integer NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (weapon_id, category_id)
);

CREATE TABLE IF NOT EXISTS weapon_designers (
    weapon_id integer NOT NULL REFERENCES weapons(id) ON DELETE CASCADE,
    designer_id integer NOT NULL REFERENCES designers(id) ON DELETE CASCADE,
    PRIMARY KEY (weapon_id, designer_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_weapons_sku_lower ON weapons (lower(sku));
CREATE UNIQUE INDEX IF NOT EXISTS ux_clients_email_lower ON clients (lower(email));

INSERT INTO manufacturers (id, name, country, founded) VALUES
    (1, 'Кречет Армз', 'Россия', 1998),
    (2, 'Норд Сталь', 'Россия', 1975),
    (3, 'Falcon Ordnance', 'США', 1954),
    (4, 'Alpine Precision', 'Германия', 1932),
    (5, 'Solar Defense Works', 'Чехия', 2001),
    (6, 'Ironclad Manufacturing', 'США', 1889),
    (7, 'Полярная Сталь', 'Россия', 1962),
    (8, 'Meridian Arms Co.', 'Италия', 1947)
ON CONFLICT (id) DO NOTHING;

INSERT INTO categories (id, name) VALUES
    (1, 'Пистолеты'),
    (2, 'Винтовки'),
    (3, 'Дробовики'),
    (4, 'Пистолеты-пулемёты'),
    (5, 'Ножи'),
    (6, 'Оптика и аксессуары')
ON CONFLICT (id) DO NOTHING;

INSERT INTO designers (id, full_name, country, active_since) VALUES
    (1, 'Виктор Соколов', 'Россия', 1995),
    (2, 'Игорь Дронов', 'Россия', 1978),
    (3, 'James Calloway', 'США', 1970),
    (4, 'Hans Richter', 'Германия', 1960),
    (5, 'Pavel Novak', 'Чехия', 2005),
    (6, 'Robert Hayes', 'США', 1955)
ON CONFLICT (id) DO NOTHING;

INSERT INTO weapons (id, name, sku, year, caliber, manufacturer_id, price, stock_total, stock_available) VALUES
    (1, 'Пистолет Кречет-9', 'KR-P9-001', 2015, '9x19mm', 1, 45000, 12, 7),
    (2, 'Пистолет Кречет-17', 'KR-P17-002', 2019, '9x19mm', 1, 52000, 8, 3),
    (3, 'Винтовка Норд-БР8', 'NS-VR8-010', 2012, '7.62x54', 2, 98000, 5, 2),
    (4, 'Винтовка Норд-Сокол', 'NS-VRS-011', 2018, '5.45x39', 2, 87000, 6, 4),
    (5, 'Дробовик Норд-Гром', 'NS-SG12-020', 2010, '12/76', 2, 41000, 10, 6),
    (6, 'Falcon M9 Compact', 'FO-M9C-030', 2016, '9x19mm', 3, 61000, 9, 5),
    (7, 'Falcon Sentinel-15', 'FO-SNT15-031', 2020, '5.56x45', 3, 145000, 4, 1),
    (8, 'Falcon Streetsweeper', 'FO-SW12-032', 2008, '12/70', 3, 39000, 7, 7),
    (9, 'Alpine AP-100', 'AP-AP100-040', 2013, '9x19mm', 4, 72000, 6, 2),
    (10, 'Alpine Jagdgewehr-7', 'AP-JG7-041', 2005, '.308 Win', 4, 132000, 3, 3),
    (11, 'Solar SDW-Compakt', 'SDW-CMP-050', 2017, '9x19mm', 5, 68000, 5, 0),
    (12, 'Solar Viper-PDW', 'SDW-VPR-051', 2021, '9x19mm', 5, 74000, 4, 4),
    (13, 'Ironclad Trailblazer', 'IC-TB-060', 1998, '12/70', 6, 33000, 11, 9),
    (14, 'Ironclad Ranger-30', 'IC-RG30-061', 2004, '.30-06', 6, 91000, 6, 5),
    (15, 'Ironclad Bowie Classic', 'IC-BWC-062', 1990, '—', 6, 8500, 20, 18),
    (16, 'Полярная Сталь Барс', 'PS-BARS-070', 2011, '9x18mm', 7, 38000, 8, 3),
    (17, 'Полярная Сталь Клинок-М', 'PS-KLM-071', 1985, '—', 7, 6200, 15, 14),
    (18, 'Meridian Duetto-12', 'MRD-D12-080', 2003, '12/76', 8, 105000, 3, 1),
    (19, 'Meridian Ottica-4x', 'MRD-OPT4-081', 2022, '—', 8, 18500, 12, 10),
    (20, 'Meridian Falco-9', 'MRD-F9-082', 2014, '9x19mm', 8, 56000, 7, 4),
    (21, 'Кречет Тактик', 'KR-TAK-090', 2022, '7.62x39', 1, 112000, 4, 2),
    (22, 'Falcon Prism-6x', 'FO-PR6-091', 2019, '—', 3, 27000, 9, 6)
ON CONFLICT (id) DO NOTHING;

INSERT INTO weapon_categories (weapon_id, category_id) VALUES
    (1,1), (2,1), (3,2), (4,2), (5,3), (6,1), (7,2), (8,3),
    (9,1), (9,6), (10,2), (10,6), (11,4), (12,4), (13,3), (14,2),
    (15,5), (16,1), (17,5), (18,3), (19,6), (20,1), (21,2), (22,6)
ON CONFLICT DO NOTHING;

INSERT INTO weapon_designers (weapon_id, designer_id) VALUES
    (1,1), (2,1), (3,2), (4,2), (5,2), (6,3), (7,3), (8,3),
    (9,4), (9,1), (10,4), (11,5), (12,5), (13,6), (14,6),
    (15,6), (16,2), (17,2), (18,6), (19,6), (20,6), (21,1), (22,3), (22,6)
ON CONFLICT DO NOTHING;

INSERT INTO clients (id, full_name, email, phone, license_number, license_issued_at, license_expires_at) VALUES
    (1, 'Андрей Волков', 'a.volkov@example.com', '+7 900 111-22-33', 'RU-77-000145', '2022-03-10T00:00:00Z', '2027-03-10T00:00:00Z'),
    (2, 'Мария Кузнецова', 'm.kuznetsova@example.com', '+7 900 222-33-44', 'RU-77-000398', '2021-07-22T00:00:00Z', '2026-07-22T00:00:00Z'),
    (3, 'Сергей Титов', 's.titov@example.com', '+7 900 333-44-55', 'RU-78-001102', '2019-01-15T00:00:00Z', '2024-01-15T00:00:00Z')
ON CONFLICT (id) DO NOTHING;

UPDATE clients SET birth_date = '1990-05-14', passport_series = '4510', passport_number = '123456'
    WHERE id = 1 AND birth_date IS NULL;

-- ПР5: тестовые учётки на все три роли (пароли — в README). Покупатель
-- привязан к уже существующей записи клиента (id=1) — так у него сразу
-- есть за что смотреть в "моих заказах" при первом же запуске.
INSERT INTO app_users (id, username, password_hash, full_name, role, client_id) VALUES
    (1, 'pokupatel', '$2a$10$BhfRso0pjCKSOTPGbfh4l.8kO/y71SvJ/CNPrFeIYLyUAOoYD7Lea', 'Андрей Волков', 'buyer', 1),
    (2, 'prodavec', '$2a$10$I3dJnbQIG4eSm.Ad5TXvbOcqFxey4aguxLkZrdpsKf5fZ8J3tNavi', 'Ольга Смирнова', 'seller', NULL),
    (3, 'admin', '$2a$10$3CsXyOQFe4qlDYLqYVMwOuKq0tPos5Cp74lAGEzPW56Crf6tD/Pvq', 'Администратор', 'admin', NULL)
ON CONFLICT (id) DO NOTHING;

-- Один уже выданный заказ — чтобы "мои заказы" и статистика администратора
-- не были пустыми на первом запуске. Активный (ordered) заказ намеренно не
-- сеется — иначе stock_available модели пришлось бы согласовывать вручную.
INSERT INTO orders (id, client_id, weapon_id, status, serial_number, picked_up_at) VALUES
    (1, 2, 6, 'picked_up', 'FO-M9C-030-SN-0007', now() - interval '3 days')
ON CONFLICT (id) DO NOTHING;

SELECT setval(pg_get_serial_sequence('manufacturers', 'id'), GREATEST((SELECT max(id) FROM manufacturers), 1));
SELECT setval(pg_get_serial_sequence('categories', 'id'), GREATEST((SELECT max(id) FROM categories), 1));
SELECT setval(pg_get_serial_sequence('designers', 'id'), GREATEST((SELECT max(id) FROM designers), 1));
SELECT setval(pg_get_serial_sequence('weapons', 'id'), GREATEST((SELECT max(id) FROM weapons), 1));
SELECT setval(pg_get_serial_sequence('clients', 'id'), GREATEST((SELECT max(id) FROM clients), 1));
SELECT setval(pg_get_serial_sequence('app_users', 'id'), GREATEST((SELECT max(id) FROM app_users), 1));
SELECT setval(pg_get_serial_sequence('orders', 'id'), GREATEST((SELECT max(id) FROM orders), 1));
