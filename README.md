# Alert Bot 2.0 (Go)

## Требования
- Go 1.21+
- Git (опционально)
- Созданный Telegram Bot и токен (`BOT_TOKEN`)

## Быстрый старт

1. Создайте файл `.env` в корне проекта и добавьте строку:

```
BOT_TOKEN=ваш_токен_бота
```

2. Установите зависимости:

```
go get github.com/joho/godotenv
go get github.com/sirupsen/logrus
go get github.com/go-telegram-bot-api/telegram-bot-api/v5
go get github.com/google/uuid
```

3. Настройте переменные окружения:

```
# Уровень логирования: debug, info, warn, error
LOG_LEVEL=debug
```

4. Запуск (через Makefile):

```
make run
```

или напрямую:

```
go run ./cmd/bot
```

5. Сборка:

```
make build
```

6. Линт/проверки (без установки сторонних линтеров):

```
make lint
```

## Структура проекта

```
.
├─ cmd/
│  └─ bot/
│     └─ main.go
├─ internal/
|  ├─ alerts
|  ├─ prices 
│  ├─ bot/
│  │  └─ bot.go
│  └─ config/
│     └─ config.go
├─ .gitignore
├─ go.mod
├─ Makefile
└─ README.md
```

## Команды бота

### Основные команды
- `/start` — приветственное сообщение
- `/chatid` — показать ID чата для настройки алертов
- `/p` - показать цену
- `/pall` - показать цены всех токенов из списка алертов

### Управление алертами
- `/add TICKER price VALUE` — создать алерт по целевой цене
  - Пример: `/addalert BTCUSDT price 50000`
- `/add TICKER pct VALUE` — создать алерт по проценту изменения
  - Пример: `/addalert BTCUSDT pct 5` (изменится на 5% от текущей цены)
- `/alerts` — показать все ваши алерты
- `/del ID` — удалить алерт по ID
- `/delallalerts` — удалить все ваши алерты

### Поведение алертов
- Алерты сохраняются в `data/alerts.json`
- При срабатывании алерт автоматически удаляется
- Алерты по проценту используют цену на момент создания как базовую
- Мониторинг работает каждые 10 секунд для всех активных алертов
- Алерты на цену срабатывают при погрешности <0.5%

## Переменные окружения
- `BOT_TOKEN` — токен Telegram-бота. 

