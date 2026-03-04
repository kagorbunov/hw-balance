# hw-balance

Сервис для вывода средств с защитой от дублей и двойного списания.

## Стек

- Go 1.22+
- PostgreSQL 14+
- REST API

## Структура проекта

```
internal/
├── controller/http  — HTTP-хендлеры, auth, маппинг ошибок
├── adapter/postgres — работа с БД
├── usecase          — бизнес-логика
├── dto              — request/response структуры
└── model            — доменные модели и ошибки
```

## Как работает защита от двойного списания

Вся логика создания withdrawal выполняется в одной транзакции:

1. Вставляем `idempotency_key` через `INSERT ... ON CONFLICT DO NOTHING`
2. Если ключ уже был — проверяем payload hash:
   - совпадает → возвращаем существующий withdrawal
   - не совпадает → 422
3. Блокируем строку баланса через `SELECT ... FOR UPDATE`
4. Проверяем что хватает денег
5. Списываем и создаём withdrawal
6. Связываем idempotency_key с withdrawal_id

`FOR UPDATE` гарантирует что параллельные запросы на один баланс выполняются последовательно.

Если два запроса с одним idempotency_key приходят одновременно — второй ждёт завершения первого (retry с интервалом 50ms, до 5 попыток).

## Запуск

```bash
docker compose up --build
```

API будет доступен на `http://localhost:18088`

Токен для авторизации: `secret-token`

Тестовый баланс: user_id=1, 1000000 USDT

### Без Docker

```bash
# Поднять postgres и применить миграции из db/migrations/

export DATABASE_URL='postgres://app:app@localhost:5432/hw_balance?sslmode=disable'
export AUTH_TOKEN='secret-token'
go run ./cmd/server
```

## API

### POST /v1/withdrawals

Создание вывода.

```bash
curl -X POST http://localhost:18088/v1/withdrawals \
  -H "Authorization: Bearer secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": 1,
    "amount": 100,
    "currency": "USDT",
    "destination": "TRC20-wallet-address",
    "idempotency_key": "order-123"
  }'
```

Ответы:
- 201 — создано (или идемпотентный повтор)
- 400 — невалидные данные
- 401 — нет токена или неверный
- 409 — недостаточно средств
- 422 — idempotency_key уже использован с другими данными

### GET /v1/withdrawals/{id}

```bash
curl http://localhost:18088/v1/withdrawals/uuid-here \
  -H "Authorization: Bearer secret-token"
```

Ответы:
- 200 — ok
- 400 — невалидный uuid
- 404 — не найден

## Тесты

```bash
# unit тесты
make test-unit

# интеграционные (нужна БД)
make up
make test-integration

# e2e тесты (нужен запущенный сервис)
make up
make test-e2e

# все (кроме e2e)
make test
```

### Что покрыто тестами

Unit:
- успешное создание
- недостаточный баланс
- идемпотентность (повтор / mismatch)
- конкурентные запросы

Интеграционные:
- полный flow через HTTP
- конкурентные запросы (10 горутин на один баланс)
- конкурентные идемпотентные запросы
- валидация всех полей
- auth
- health endpoints

E2E (тестируют реальный сервер через HTTP):
- health endpoints
- полный flow создания и получения withdrawals
- идемпотентность (same payload / different payload)
- конкурентные запросы на один баланс
- конкурентные идемпотентные запросы
- валидация всех полей
- аутентификация
- проверка что не утекают internal errors
