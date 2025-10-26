package bot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sirupsen/logrus"

	"example.com/alert-bot/internal/alerts"
	"example.com/alert-bot/internal/config"
	"example.com/alert-bot/internal/prices"
	"example.com/alert-bot/internal/reminder"
)

// TelegramBot инкапсулирует работу с Telegram API.
type TelegramBot struct {
	api           *tgbotapi.BotAPI
	cfg           config.Config
	st            *alerts.DatabaseStorage
	monitorCtx    context.Context
	stopMon       context.CancelFunc
	pricesClients *prices.ExchangeClients // Добавлено поле для клиентов бирж
	scheduler     *reminder.Scheduler
	// Для отслеживания резких изменений цен
	sharpChangeMu        sync.Mutex
	lastSharpChangeAlert map[string]struct {
		Time  time.Time
		Price float64
	} // Время и цена последнего алерта о резком изменении для каждого символа
}

// NewTelegramBot создает экземпляр бота.
func NewTelegramBot(cfg config.Config) (*TelegramBot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}
	api.Debug = false
	logrus.WithField("username", api.Self.UserName).Info("telegram bot authorized")

	st, err := alerts.NewDatabaseStorage(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("database storage init: %w", err)
	}

	pricesClients := prices.NewExchangeClients(cfg)

	bot := &TelegramBot{
		api:           api,
		cfg:           cfg,
		st:            st,
		pricesClients: pricesClients,
		lastSharpChangeAlert: make(map[string]struct {
			Time  time.Time
			Price float64
		}),
		// ⬇️ scheduler создаём ПОСЛЕ объявления bot, но до return
		scheduler: nil, // временно, сразу ниже заполним
	}

	// ⬇️ теперь у нас ЕСТЬ переменная bot и доступ к st.DB()
	bot.scheduler = reminder.NewScheduler(st.DB(), api)

	return bot, nil
}

// Start запускает обработку апдейтов до завершения контекста.
func (b *TelegramBot) Start(ctx context.Context) error {
	if b.api == nil {
		return errors.New("telegram api is not initialized")
	}

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30

	updates := b.api.GetUpdatesChan(updateConfig)

	// Запуск мониторинга цен для алертов
	b.startMonitoring(ctx)
	go b.scheduler.Start(ctx)
	for {
		select {
		case <-ctx.Done():
			// Остановка
			if b.stopMon != nil {
				b.stopMon()
			}
			b.api.StopReceivingUpdates()
			return nil
		case upd, ok := <-updates:
			if !ok {
				return nil
			}
			b.handleUpdate(ctx, upd)
		}
	}
}

func (b *TelegramBot) handleUpdate(ctx context.Context, upd tgbotapi.Update) {
	if upd.Message == nil {
		return
	}

	chatID := upd.Message.Chat.ID
	userID := upd.Message.From.ID
	username := upd.Message.From.UserName
	if username == "" {
		username = upd.Message.From.FirstName
	}
	text := upd.Message.Text

	switch {
	case text == "/chatid":
		b.reply(chatID, fmt.Sprintf("Chat ID: %d\nUser ID: %d\nUsername: %s", chatID, userID, username))
	case strings.HasPrefix(text, "/add"):
		b.cmdAddAlert(ctx, chatID, userID, username, text)
	case text == "/alerts":
		b.cmdListAlerts(chatID)
	case strings.HasPrefix(text, "/del"):
		b.cmdDelAlert(chatID, text)
	case text == "/clearallalerts":
		b.cmdDelAllAlerts(chatID)
	case text == "/allp":
		b.cmdPriceAll(ctx, chatID)
	case strings.HasPrefix(text, "/p"):
		b.cmdPrice(ctx, chatID, text)
	case strings.HasPrefix(text, "/ocall"):
		b.cmdOpenCall(ctx, chatID, userID, username, text)
	case strings.HasPrefix(text, "/ccall"):
		b.cmdCloseCall(ctx, chatID, userID, text)
	case strings.HasPrefix(text, "/sl"):
		b.cmdSetStopLoss(ctx, chatID, userID, text)
	case text == "/mycalls":
		b.cmdMyCalls(ctx, chatID, userID)
	case text == "/allcalls":
		b.cmdAllCalls(ctx, chatID)
	case text == "/callstats":
		b.cmdCallStats(chatID)
	case text == "/mycallstats":
		b.cmdMyCallStats(chatID, userID)
	case text == "/mytrades":
		b.cmdMyTrades(chatID, userID)
	case strings.HasPrefix(text, "/history"):
		b.cmdHistory(chatID, text)
	case text == "/stats":
		b.cmdStats(chatID, userID)
	case text == "/rush":
		b.cmdRush(ctx, chatID, userID)
	case strings.HasPrefix(text, "/remind"):
		b.cmdRemind(ctx, chatID, userID, username, text)
	case strings.HasPrefix(text, "/limit"):
		b.cmdCreateLimitOrder(ctx, chatID, userID, username, text)
	case strings.HasPrefix(text, "/climit"):
		b.cmdCancelLimitOrder(ctx, chatID, userID, text)
	case text == "/myorders":
		b.cmdMyOrders(ctx, chatID, userID)
	case text == "/start":
		b.reply(chatID, "*Way2Million, by Saint\\_Dmitriy*\n\n*Команды:*\n"+
			"/start - список всех команд бота\n"+
			"/chatid - показать Chat ID, User ID и Username\n"+
			"/add TICKER price|pct VALUE - создать алерт\n"+
			"/alerts - показать все активные алерты пользователя\n"+
			"/del ID - удалить алерт по ID\n"+
			"/clearallalerts - удалить все алерты\n"+
			"/p TICKER - показать цену одного символа с изменениями\n"+
			"/allp - показать цены всех токенов из алертов и коллов\n"+
			"/ocall TICKER [long|short] [size] sl [sl PRICE] - открыть колл\n"+
			"/ccall CALLID [size] - закрыть колл по ID\n"+
			"/sl CALLID [price] - установить/обновить стоп-лосс для колла\n"+
			"/limit TICKER [b|s] PRICE % [CALLID](Опционально) - создать лимитный ордер\n"+
			"/climit ORDERID - отменить лимитный ордер\n"+
			"/myorders - показать активные лимитные ордера\n"+
			"/mycalls - показать активные коллы с текущим PnL\n"+
			"/allcalls - показать все коллы всех пользователей\n"+
			"/rush - закрыть все открытые коллы пользователя\n"+
			"/callstats - рейтинг трейдеров за 90 дней\n"+
			"/mycallstats - персональная статистика коллов за 90 дней\n"+
			"/mytrades - статистика по символам за 90 дней\n"+
			"/history - история сработавших алертов\n"+
			"/stats - статистика по активным алертам")
	default:
		// Игнорируем неизвестные команды и сообщения
	}
}

func (b *TelegramBot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Включаем поддержку Markdown для кликабельных ID
	if _, err := b.api.Send(msg); err != nil {
		logrus.WithError(err).Warn("send message failed")
	} else {
		logrus.WithFields(logrus.Fields{"chat_id": chatID}).Debug("message sent")
	}
}

// Напоминания
func (b *TelegramBot) cmdRemind(ctx context.Context, chatID, userID int64, username, txt string) {
	parts := strings.Fields(txt)
	if len(parts) < 3 {
		b.reply(chatID, "Использование: /remind TICKER <время> [текст]\nПримеры: 5m 2h 3d")
		return
	}
	symbol := formatSymbol(parts[1])
	dur, err := parseDuration(parts[2])
	if err != nil {
		b.reply(chatID, "Не разобрал время. Используй: 10m, 2h, 3d")
		return
	}
	custom := strings.Join(parts[3:], " ")

	id, err := b.scheduler.Add(ctx, chatID, userID, username, symbol, custom, dur)
	if err != nil {
		b.reply(chatID, "Ошибка: "+err.Error())
		return
	}
	when := time.Now().Add(dur).Format("15:04 02.01")
	b.reply(chatID, fmt.Sprintf("Напомню про %s в %s (id `%s`)", symbol, when, id))
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("слишком коротко")
	}
	val, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, err
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(val) * time.Minute, nil
	case 'h':
		return time.Duration(val) * time.Hour, nil
	case 'd':
		return time.Duration(val) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("недопустимая единица")
}

// cmdAddAlert обрабатывает команду /add TICKER [price|pct] VALUE
func (b *TelegramBot) cmdAddAlert(ctx context.Context, chatID int64, userID int64, username string, text string) {
	parts := strings.Fields(text)

	// Теперь допускаем как 3, так и 4 части
	if len(parts) < 3 || len(parts) > 4 {
		b.reply(chatID, "Использование: /add TICKER [price|pct] VALUE\nПример: /add BTCUSDT price 50000\nПример: /add BTCUSDT 50000 (по умолчанию price)\nПример: /add BTCUSDT pct 5")
		return
	}

	symbol := formatSymbol(parts[1])
	var alertType string
	var valueStr string

	// Определяем формат команды
	if len(parts) == 4 {
		// Формат: /add TICKER price|pct VALUE
		alertType = parts[2]
		valueStr = parts[3]
	} else {
		// Формат: /add TICKER VALUE (по умолчанию price)
		alertType = "price"
		valueStr = parts[2]
	}

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		b.reply(chatID, "Неверное значение: "+valueStr)
		return
	}

	alert := alerts.Alert{
		ChatID:   chatID,
		UserID:   userID,
		Username: username,
		Symbol:   symbol,
	}

	preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)

	switch alertType {
	case "price":
		alert.TargetPrice = value
		priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
		if err != nil {
			b.reply(chatID, "Ошибка получения цены для "+symbol+": "+err.Error())
			return
		}
		alert.Exchange = priceInfo.Exchange
		alert.Market = priceInfo.Market
		alert, err = b.st.Add(alert)
		if err != nil {
			b.reply(chatID, "Ошибка создания алерта: "+err.Error())
			return
		}
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: `%s`)\n%s на %s %s достигнет %s (текущая: %s)", alert.ID, symbol, alert.Exchange, alert.Market, prices.FormatPrice(value), prices.FormatPrice(priceInfo.CurrentPrice)))

		// Перезапускаем мониторинг с новым символом
		b.restartMonitoring(ctx)
	case "pct":
		alert.TargetPercent = value
		// Получаем текущую цену для базовой
		priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
		if err != nil {
			b.reply(chatID, "Ошибка получения цены для "+symbol+": "+err.Error())
			return
		}
		alert.BasePrice = priceInfo.CurrentPrice
		alert.Market = priceInfo.Market
		alert.Exchange = priceInfo.Exchange
		alert, err = b.st.Add(alert)
		if err != nil {
			b.reply(chatID, "Ошибка создания алерта: "+err.Error())
			return
		}
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: `%s`)\n%s на %s %s изменится на %.2f%% от %s (текущая: %s)", alert.ID, symbol, alert.Exchange, alert.Market, value, prices.FormatPrice(priceInfo.CurrentPrice), prices.FormatPrice(priceInfo.CurrentPrice)))

		// Перезапускаем мониторинг с новым символом
		b.restartMonitoring(ctx)
	default:
		b.reply(chatID, "Тип должен быть 'price' или 'pct'")
	}
}

// cmdOpenCall обрабатывает команду /ocall TICKER [long|short]
func (b *TelegramBot) cmdOpenCall(ctx context.Context, chatID int64, userID int64, username string, text string) {
	parts := strings.Fields(text)
	if len(parts) < 2 || len(parts) > 6 { // Добавляем возможность для 6 частей (ocall TICKER [long|short] [deposit_percent] [sl PRICE])
		b.reply(chatID, "Использование: /ocall TICKER [long|short] [deposit_percent] [sl PRICE]\nПример: /ocall BTC long 40 sl 25000 (открыть лонг по BTC с 40% депозита и стоп-лоссом 25000)\nПример: /ocall ETH short")
		return
	}

	symbol := formatSymbol(parts[1])
	direction := "long"  // по умолчанию
	positionSize := 0.0  // по умолчанию 0%
	stopLossPrice := 0.0 // по умолчанию 0 (без стоп-лосса)

	// Парсинг направления, процента депозита и стоп-лосса
	argIndex := 2

	// Парсинг направления
	if len(parts) > argIndex {
		dirOrPctOrSL := strings.ToLower(parts[argIndex])
		if dirOrPctOrSL == "short" || dirOrPctOrSL == "long" {
			direction = dirOrPctOrSL
			argIndex++
		}
	}

	// Парсинг процента депозита
	if len(parts) > argIndex {
		sizeValStr := parts[argIndex]
		sizeVal, err := strconv.ParseFloat(sizeValStr, 64)
		if err == nil && sizeVal >= 0 {
			positionSize = sizeVal
			argIndex++
		}
	}

	// Парсинг стоп-лосса
	if len(parts) > argIndex && strings.ToLower(parts[argIndex]) == "sl" {
		argIndex++
		if len(parts) > argIndex {
			slVal, err := strconv.ParseFloat(parts[argIndex], 64)
			if err == nil && slVal >= 0 {
				stopLossPrice = slVal
			} else {
				b.reply(chatID, "Неверное значение стоп-лосса. Используйте число >= 0.")
				return
			}
		} else {
			b.reply(chatID, "Укажите цену для стоп-лосса после 'sl'.")
			return
		}
	}

	// Получаем текущую цену
	preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
	priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
	if err != nil {
		b.reply(chatID, "Ошибка получения цены для "+symbol+": "+err.Error())
		return
	}

	// Создаем колл
	call := alerts.Call{
		UserID:         userID,
		Username:       username,
		ChatID:         chatID,
		Symbol:         symbol,
		Direction:      direction,
		EntryPrice:     priceInfo.CurrentPrice,
		Market:         priceInfo.Market,
		DepositPercent: positionSize,  // Сохраняем процент от депозита
		StopLossPrice:  stopLossPrice, // Сохраняем цену стоп-лосса
		Exchange:       priceInfo.Exchange,
	}

	call, err = b.st.OpenCall(call)
	if err != nil {
		b.reply(chatID, "Ошибка создания колла: "+err.Error())
		return
	}

	directionRus := "Long"
	if direction == "short" {
		directionRus = "Short"
	}

	msg := fmt.Sprintf("Колл открыт!\nID: `%s`\nСимвол: %s\nНаправление: %s\nЦена входа: %s",
		call.ID, symbol, directionRus, prices.FormatPrice(call.EntryPrice))

	if call.DepositPercent > 0 {
		msg += fmt.Sprintf("\nПроцент от депозита: %.0f%%", call.DepositPercent)
	}

	if call.StopLossPrice > 0 {
		msg += fmt.Sprintf("\nСтоп-лосс: %s", prices.FormatPrice(call.StopLossPrice))
	}
	msg += fmt.Sprintf("\nБиржа: %s, Рынок: %s", call.Exchange, call.Market)

	b.reply(chatID, msg)
}

// cmdSetStopLoss обрабатывает команду /sl CALLID [price]
func (b *TelegramBot) cmdSetStopLoss(ctx context.Context, chatID int64, userID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) < 2 || len(parts) > 3 {
		b.reply(chatID, "Использование: /sl CALLID [price]\nПример: /sl `abc123de` 25000 (установить стоп-лосс на 25000)\nПример: /sl `abc123de` (удалить стоп-лосс или установить на 0)")
		return
	}

	callID := parts[1]
	stopLossPrice := 0.0 // По умолчанию удаляем стоп-лосс

	if len(parts) == 3 {
		slVal, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || slVal < 0 {
			b.reply(chatID, "Неверное значение стоп-лосса. Используйте число >= 0.")
			return
		}
		stopLossPrice = slVal
	}

	// Получаем информацию о колле, чтобы проверить существование и принадлежность
	call, err := b.st.GetCallByID(callID, userID)
	if err != nil {
		b.reply(chatID, "Колл не найден или не принадлежит вам")
		return
	}

	if call.Status != "open" {
		b.reply(chatID, "Нельзя установить стоп-лосс для закрытого колла")
		return
	}

	// Если цена стоп-лосса не указана, используем цену входа как стоп-лосс по умолчанию.
	if len(parts) == 2 { // Значит, price не указан, только /sl CALLID
		stopLossPrice = call.EntryPrice
	}

	// Обновляем стоп-лосс в БД
	err = b.st.UpdateStopLoss(callID, userID, stopLossPrice)
	if err != nil {
		b.reply(chatID, "Ошибка обновления стоп-лосса: "+err.Error())
		return
	}

	// Отправляем подтверждение
	if stopLossPrice > 0 {
		var replyMsg string
		if len(parts) == 2 { // Стоп-лосс установлен на цену входа
			replyMsg = fmt.Sprintf("Стоп-лосс для колла `%s` установлен на цену входа: %s", callID, prices.FormatPrice(stopLossPrice))
		} else { // Стоп-лосс установлен на указанную цену
			replyMsg = fmt.Sprintf("Стоп-лосс для колла `%s` установлен на %s", callID, prices.FormatPrice(stopLossPrice))
		}
		b.reply(chatID, replyMsg)
	} else { // stopLossPrice == 0, что означает удаление стоп-лосса
		b.reply(chatID, fmt.Sprintf("Стоп-лосс для колла `%s` удален", callID))
	}
}

// cmdCloseCall обрабатывает команду /ccall CALLID [size]
func (b *TelegramBot) cmdCloseCall(ctx context.Context, chatID int64, userID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) < 2 || len(parts) > 3 {
		b.reply(chatID, "Использование: /ccall CALLID [size]\nПример: /ccall `abc123de` 50 (закрыть 50%)\nПример: /ccall `abc123de` (закрыть полностью)")
		return
	}

	callID := parts[1]

	// Получаем информацию о колле из БД
	call, err := b.st.GetCallByID(callID, userID)
	if err != nil {
		b.reply(chatID, "Колл не найден или не принадлежит вам")
		return
	}

	if call.Status != "open" {
		b.reply(chatID, "Колл уже закрыт")
		return
	}

	size := call.Size

	if len(parts) == 3 {
		sizeVal, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || sizeVal <= 0 || sizeVal > call.Size {
			b.reply(chatID, fmt.Sprintf("Неверное значение размера. Используйте число от 1 до текущего размера %.0f.", call.Size))
			return
		}
		size = sizeVal
	}

	// Получаем текущую цену для символа из колла
	preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(call.Symbol)
	priceInfo, err := prices.FetchPriceInfo(b.pricesClients, call.Symbol, preferredExchange, preferredMarket)
	if err != nil {
		b.reply(chatID, fmt.Sprintf("Ошибка получения цены для %s: %s", call.Symbol, err.Error()))
		logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to fetch price info for closing call")
		return
	}

	// Закрываем колл
	err = b.st.CloseCall(callID, userID, priceInfo.CurrentPrice, size)
	if err != nil {
		b.reply(chatID, "Ошибка закрытия колла: "+err.Error())
		return
	}

	// Получаем обновленную информацию о закрытом колле
	updatedCall, err := b.st.GetCallByID(callID, userID)
	if err == nil {
		pnlSign := "+"
		if updatedCall.PnlPercent < 0 {
			pnlSign = ""
		}

		directionRus := "Long"
		if updatedCall.Direction == "short" {
			directionRus = "Short"
		}

		statusMsg := ""
		if updatedCall.Status == "closed" {
			statusMsg = fmt.Sprintf("Колл полностью закрыт!\nID: `%s`\nСимвол: %s\nНаправление: %s\nЦена входа: %s\nЦена выхода: %s\nPnL: %s%.2f%%",
				callID, updatedCall.Symbol, directionRus, prices.FormatPrice(updatedCall.EntryPrice),
				prices.FormatPrice(priceInfo.CurrentPrice), pnlSign, updatedCall.PnlPercent)
		} else {
			statusMsg = fmt.Sprintf("Колл частично закрыт на %.0f%%!\nID: `%s`\nСимвол: %s\nНаправление: %s\nОставшийся размер: %.0f\nЦена входа: %s\nЦена выхода: %s\nPnL на закрытую часть: %s%.2f%%",
				size, callID, updatedCall.Symbol, directionRus, updatedCall.Size, prices.FormatPrice(updatedCall.EntryPrice),
				prices.FormatPrice(priceInfo.CurrentPrice), pnlSign, updatedCall.PnlPercent)
		}
		b.reply(chatID, statusMsg)
	} else {
		b.reply(chatID, fmt.Sprintf("Колл `%s` закрыт по цене %s", callID, prices.FormatPrice(priceInfo.CurrentPrice)))
	}
}

// cmdMyCalls показывает активные коллы пользователя, сгруппированные по тикерам
// cmdMyCalls показывает активные коллы пользователя, сгруппированные по тикерам
func (b *TelegramBot) cmdMyCalls(ctx context.Context, chatID int64, userID int64) {
	calls := b.st.GetUserCalls(userID, true)
	if len(calls) == 0 {
		b.reply(chatID, "У вас нет активных коллов")
		return
	}

	// Группируем коллы по символу и направлению
	type SymbolKey struct {
		Symbol    string
		Direction string
	}

	callsBySymbol := make(map[SymbolKey][]alerts.Call)
	for _, call := range calls {
		key := SymbolKey{Symbol: call.Symbol, Direction: call.Direction}
		callsBySymbol[key] = append(callsBySymbol[key], call)
	}

	// Получаем отсортированные ключи
	var keys []SymbolKey
	for key := range callsBySymbol {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Symbol == keys[j].Symbol {
			return keys[i].Direction < keys[j].Direction
		}
		return keys[i].Symbol < keys[j].Symbol
	})

	var msg strings.Builder
	msg.WriteString("Ваши активные коллы:\n\n")

	var totalPositionSize float64
	var totalPnlToDeposit float64
	symbolIndex := 1

	for _, key := range keys {
		symbolCalls := callsBySymbol[key]

		// Получаем текущую цену для символа
		priceInfo, err := prices.FetchCurrentPrice(b.pricesClients, key.Symbol, symbolCalls[0].Exchange, symbolCalls[0].Market)
		if err != nil {
			logrus.WithError(err).WithField("symbol", key.Symbol).Warn("failed to get current price for symbol group")
			continue
		}
		currentPrice := priceInfo.CurrentPrice

		directionRus := "Long"
		if key.Direction == "short" {
			directionRus = "Short"
		}

		// Заголовок группы
		msg.WriteString(fmt.Sprintf("%d. %s (%s)\n\n", symbolIndex, key.Symbol, directionRus))

		var groupTotalSize float64
		var groupWeightedEntry float64
		var groupWeightedPnl float64

		// Сортируем коллы внутри группы по времени открытия
		sort.Slice(symbolCalls, func(i, j int) bool {
			return symbolCalls[i].OpenedAt.Before(symbolCalls[j].OpenedAt)
		})

		// Собираем информацию о коллах для последующего вывода
		type CallInfo struct {
			ID            string
			EntryPrice    float64
			EffectiveSize float64
			SizeStr       string
			BasePnl       float64
			HoldingTime   string
		}
		var callInfos []CallInfo

		// Выводим каждый колл в группе
		for _, call := range symbolCalls {
			// Базовое изменение цены
			var basePnl float64
			if call.Direction == "long" {
				basePnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
			} else {
				basePnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
			}

			// Вклад в депозит
			pnlToDeposit := call.DepositPercent * (basePnl / 100)

			// Время удержания
			holdingTime := time.Since(call.OpenedAt)
			var holdingStr string
			totalMinutes := int(holdingTime.Minutes())

			if totalMinutes < 60 {
				holdingStr = fmt.Sprintf("%dmin", totalMinutes)
			} else if totalMinutes < 1440 { // меньше 24 часов
				hours := totalMinutes / 60
				holdingStr = fmt.Sprintf("%dh", hours)
			} else {
				days := totalMinutes / 1440
				hours := (totalMinutes % 1440) / 60
				if hours > 0 {
					holdingStr = fmt.Sprintf("%dd:%dh", days, hours)
				} else {
					holdingStr = fmt.Sprintf("%dd", days)
				}
			}

			// Размер с учетом частичного закрытия
			effectiveSize := call.DepositPercent * (call.Size / 100)

			sizeStr := fmt.Sprintf("%.0f%%", effectiveSize)
			if call.Size < 100 {
				sizeStr = fmt.Sprintf("%.0f%% (Осталось %.0f%%)", effectiveSize, call.Size)
			}

			callInfos = append(callInfos, CallInfo{
				ID:            call.ID,
				EntryPrice:    call.EntryPrice,
				EffectiveSize: effectiveSize,
				SizeStr:       sizeStr,
				BasePnl:       basePnl,
				HoldingTime:   holdingStr,
			})

			// Накапливаем для средних значений
			groupTotalSize += effectiveSize
			groupWeightedEntry += call.EntryPrice * effectiveSize
			groupWeightedPnl += pnlToDeposit

			totalPositionSize += effectiveSize
			totalPnlToDeposit += pnlToDeposit
		}

		// Средние значения для группы
		avgEntry := groupWeightedEntry / groupTotalSize

		// Текущий PnL по позиции от среднего входа
		var positionPnl float64
		if key.Direction == "long" {
			positionPnl = ((currentPrice - avgEntry) / avgEntry) * 100
		} else {
			positionPnl = ((avgEntry - currentPrice) / avgEntry) * 100
		}

		pnlGroupSign := "+"
		if groupWeightedPnl < 0 {
			pnlGroupSign = ""
		}
		positionPnlSign := "+"
		if positionPnl < 0 {
			positionPnlSign = ""
		}

		// Выводим информацию о группе
		msg.WriteString(fmt.Sprintf("     Текущая цена: %s\n", prices.FormatPrice(currentPrice)))
		msg.WriteString(fmt.Sprintf("     Средний вход: %s\n", prices.FormatAvgPrice(avgEntry)))
		msg.WriteString(fmt.Sprintf("     Общий размер: %.0f%%\n", groupTotalSize))
		msg.WriteString(fmt.Sprintf("     Текущий PnL: %s%.2f%%\n", positionPnlSign, positionPnl))
		msg.WriteString(fmt.Sprintf("     Общий PnL к депозиту: %s%.2f%%\n", pnlGroupSign, groupWeightedPnl))
		msg.WriteString(fmt.Sprintf("     Биржа: %s, Рынок: %s\n", symbolCalls[0].Exchange, symbolCalls[0].Market))
		msg.WriteString("     Коллы:\n")

		// Выводим список коллов
		for i, info := range callInfos {
			pnlSign := "+"
			if info.BasePnl < 0 {
				pnlSign = ""
			}

			msg.WriteString(fmt.Sprintf("      %d. ID: `%s`, entry: %s, size: %s, PnL: %s%.2f%%, t: %s\n",
				i+1, info.ID, prices.FormatPrice(info.EntryPrice), info.SizeStr, pnlSign, info.BasePnl, info.HoldingTime))
		}

		msg.WriteString("\n")
		symbolIndex++
	}

	// Итоговая статистика
	if totalPositionSize > 0 {
		posInfo := fmt.Sprintf("*Совокупный размер позиций: %.0f%%*", totalPositionSize)
		if totalPositionSize > 100 {
			avgLeverage := totalPositionSize / 100
			posInfo += fmt.Sprintf(" *(~x%.1f)*", avgLeverage)
		}
		msg.WriteString(posInfo + "\n")

		pnlToDepositSign := "+"
		if totalPnlToDeposit < 0 {
			pnlToDepositSign = ""
		}
		msg.WriteString(fmt.Sprintf("*Совокупный PnL к депозиту: %s%.2f%%*\n", pnlToDepositSign, totalPnlToDeposit))
	}

	b.reply(chatID, msg.String())
}

// cmdCallStats показывает статистику коллов всех пользователей за последние 90 дней
func (b *TelegramBot) cmdCallStats(chatID int64) {
	stats := b.st.GetAllUserStats()

	// Получаем все активные коллы для расчета текущего размера позиций и PnL
	activeCalls := b.st.GetAllOpenCalls()
	activeStatsMap := make(map[int64]struct {
		TotalPositionSize float64
		TotalPnlToDeposit float64
	})

	for _, call := range activeCalls {
		if call.DepositPercent > 0 {
			priceInfo, err := prices.FetchCurrentPrice(b.pricesClients, call.Symbol, call.Exchange, call.Market)
			if err != nil {
				logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to get current price for active call stats in cmdCallStats")
				continue
			}
			currentPrice := priceInfo.CurrentPrice

			// Базовое изменение цены
			var basePnl float64
			if call.Direction == "long" {
				basePnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
			} else {
				basePnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
			}

			// Вклад в депозит = размер_позиции × изменение_цены
			pnlToDeposit := call.DepositPercent * (basePnl / 100)

			userActiveStats := activeStatsMap[call.UserID]
			userActiveStats.TotalPositionSize += call.DepositPercent
			userActiveStats.TotalPnlToDeposit += pnlToDeposit
			activeStatsMap[call.UserID] = userActiveStats
		}
	}

	// Обновляем статистику пользователей из БД с активной статистикой
	for i := range stats {
		if active, ok := activeStatsMap[stats[i].UserID]; ok {
			stats[i].TotalActiveDepositPercent = active.TotalPositionSize
			stats[i].TotalPnlToDeposit = active.TotalPnlToDeposit
		}
	}

	// Добавляем пользователей, у которых есть только активные коллы
	for userID, active := range activeStatsMap {
		found := false
		for _, stat := range stats {
			if stat.UserID == userID {
				found = true
				break
			}
		}
		if !found {
			var username string
			for _, call := range activeCalls {
				if call.UserID == userID {
					username = call.Username
					break
				}
			}
			if username == "" {
				username = fmt.Sprintf("User_%d", userID)
			}

			// Получаем депозит для нового пользователя
			initialDeposit, currentDeposit, _ := b.st.GetUserDeposit(userID)

			stats = append(stats, alerts.UserStats{
				UserID:                    userID,
				Username:                  username,
				TotalActiveDepositPercent: active.TotalPositionSize,
				TotalPnlToDeposit:         active.TotalPnlToDeposit,
				InitialDeposit:            initialDeposit,
				CurrentDeposit:            currentDeposit,
				TotalReturnPercent:        ((currentDeposit - initialDeposit) / initialDeposit) * 100,
			})
		}
	}

	// Сортируем по доходности депозита
	sort.Slice(stats, func(i, j int) bool {
		// Приоритет: если есть доходность депозита, сортируем по ней
		if stats[i].TotalReturnPercent != 0 || stats[j].TotalReturnPercent != 0 {
			return stats[i].TotalReturnPercent > stats[j].TotalReturnPercent
		}
		// Если нет доходности депозита, сортируем по текущему PnL активных позиций
		if stats[i].TotalPnlToDeposit != 0 || stats[j].TotalPnlToDeposit != 0 {
			return stats[i].TotalPnlToDeposit > stats[j].TotalPnlToDeposit
		}
		// Иначе по закрытому PnL
		return stats[i].TotalPnl > stats[j].TotalPnl
	})

	// Фильтруем: показываем только тех, у кого есть что показать
	var filteredStats []alerts.UserStats
	for _, stat := range stats {
		if stat.TotalCalls > 0 {
			filteredStats = append(filteredStats, stat)
		}
	}
	if len(filteredStats) == 0 {
		b.reply(chatID, "Нет данных для статистики")
		return
	}

	var msg strings.Builder
	msg.WriteString("📊 *Рейтинг трейдеров за последние 90 дней:*\n\n")

	for i, stat := range filteredStats {
		username := stat.Username
		if username == "" {
			username = fmt.Sprintf("User_%d", stat.UserID)
		}

		msg.WriteString(fmt.Sprintf("%d. *%s*\n", i+1, username))

		// Доходность депозита
		if stat.InitialDeposit > 0 && stat.CurrentDeposit > 0 {
			returnSign := "+"
			if stat.TotalReturnPercent < 0 {
				returnSign = ""
			}
			msg.WriteString(fmt.Sprintf("   💰 Доходность: %s%.2f%% (%.0f → %.0f)\n",
				returnSign, stat.TotalReturnPercent, stat.InitialDeposit, stat.CurrentDeposit))
		}

		// Закрытые сделки
		if stat.ClosedCalls > 0 {
			pnlSign := "+"
			if stat.TotalPnl < 0 {
				pnlSign = ""
			}
			msg.WriteString(fmt.Sprintf("   📊 Закрыто: %d | PnL: %s%.2f%% | WR: %.1f%%\n",
				stat.ClosedCalls, pnlSign, stat.TotalPnl, stat.WinRate))
		}

		// Активные позиции
		if stat.TotalActiveDepositPercent > 0 {
			pnlToDepositSign := "+"
			if stat.TotalPnlToDeposit < 0 {
				pnlToDepositSign = ""
			}

			posInfo := fmt.Sprintf("%.0f%%", stat.TotalActiveDepositPercent)
			if stat.TotalActiveDepositPercent > 100 {
				avgLeverage := stat.TotalActiveDepositPercent / 100
				posInfo = fmt.Sprintf("%.0f%% (~x%.1f)", stat.TotalActiveDepositPercent, avgLeverage)
			}

			msg.WriteString(fmt.Sprintf("   💼 Позиции: %s | PnL: %s%.2f%%\n",
				posInfo, pnlToDepositSign, stat.TotalPnlToDeposit))
		}
		msg.WriteString("\n")
	}

	b.reply(chatID, msg.String())
}

// cmdMyCallStats показывает персональную статистику коллов пользователя за последние 90 дней
func (b *TelegramBot) cmdMyCallStats(chatID int64, userID int64) {
	stats, err := b.st.GetUserStats(userID)
	if err != nil {
		b.reply(chatID, "Ошибка получения статистики: "+err.Error())
		return
	}

	// Получаем активные коллы
	activeCalls := b.st.GetUserCalls(userID, true)
	var totalPositionSize float64
	var totalPnlToDeposit float64

	for _, call := range activeCalls {
		if call.DepositPercent > 0 {
			priceInfo, err := prices.FetchCurrentPrice(b.pricesClients, call.Symbol, call.Exchange, call.Market)
			if err != nil {
				logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to get current price for active call stats")
				continue
			}
			currentPrice := priceInfo.CurrentPrice

			var basePnl float64
			if call.Direction == "long" {
				basePnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
			} else {
				basePnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
			}

			pnlToDeposit := call.DepositPercent * (basePnl / 100)
			totalPositionSize += call.DepositPercent
			totalPnlToDeposit += pnlToDeposit
		}
	}

	// Получаем информацию о депозите
	initialDeposit, currentDeposit, err := b.st.GetUserDeposit(userID)
	if err != nil {
		logrus.WithError(err).Warn("failed to get user deposit")
	}

	if stats.ClosedCalls == 0 && len(activeCalls) == 0 {
		b.reply(chatID, "У вас нет закрытых или активных коллов за последние 90 дней")
		return
	}

	var msg strings.Builder
	msg.WriteString("📊 *Ваша статистика коллов за последние 90 дней:*\n\n")

	// Доходность депозита
	if initialDeposit > 0 && currentDeposit > 0 {
		totalReturn := ((currentDeposit - initialDeposit) / initialDeposit) * 100
		returnSign := "+"
		if totalReturn < 0 {
			returnSign = ""
		}
		msg.WriteString(fmt.Sprintf("💰 *Доходность депозита: %s%.2f%%*\n", returnSign, totalReturn))
		msg.WriteString(fmt.Sprintf("   Начальный: %.0f | Текущий: %.0f\n\n", initialDeposit, currentDeposit))
	}

	// Закрытые сделки
	if stats.ClosedCalls > 0 {
		pnlSign := "+"
		if stats.TotalPnl < 0 {
			pnlSign = ""
		}
		msg.WriteString(fmt.Sprintf("📈 *Закрытые сделки:*\n"))
		msg.WriteString(fmt.Sprintf("   Всего: %d | Winrate: %.1f%%\n", stats.ClosedCalls, stats.WinRate))
		msg.WriteString(fmt.Sprintf("   Общий PnL: %s%.2f%%\n", pnlSign, stats.TotalPnl))

		avgPnlSign := "+"
		if stats.AveragePnl < 0 {
			avgPnlSign = ""
		}
		msg.WriteString(fmt.Sprintf("   Средний PnL: %s%.2f%%\n\n", avgPnlSign, stats.AveragePnl))
	}

	// Активные позиции
	msg.WriteString(fmt.Sprintf("📊 *Активных коллов:* %d\n", len(activeCalls)))

	if totalPositionSize > 0 {
		msg.WriteString(fmt.Sprintf("\n💼 *Активные позиции:*\n"))

		positionInfo := fmt.Sprintf("   Размер: %.0f%%", totalPositionSize)
		if totalPositionSize > 100 {
			avgLeverage := totalPositionSize / 100
			positionInfo += fmt.Sprintf(" (~x%.1f плечо)", avgLeverage)
		}
		msg.WriteString(positionInfo + "\n")

		pnlToDepositSign := "+"
		if totalPnlToDeposit < 0 {
			pnlToDepositSign = ""
		}
		msg.WriteString(fmt.Sprintf("   Текущий PnL: %s%.2f%%\n", pnlToDepositSign, totalPnlToDeposit))
	}

	// Лучший и худший коллы
	if stats.ClosedCalls > 0 {
		msg.WriteString("\n")
		bestCall, worstCall := b.st.GetBestWorstCallsForUser(userID)

		if bestCall != nil {
			directionRus := "Long"
			if bestCall.Direction == "short" {
				directionRus = "Short"
			}
			msg.WriteString(fmt.Sprintf("🚀 *Лучший колл:* +%.2f%% (%s %s)\n", bestCall.PnlPercent, bestCall.Symbol, directionRus))
		}

		if worstCall != nil {
			directionRus := "Long"
			if worstCall.Direction == "short" {
				directionRus = "Short"
			}
			msg.WriteString(fmt.Sprintf("💥 *Худший колл:* %.2f%% (%s %s)\n", worstCall.PnlPercent, worstCall.Symbol, directionRus))
		}
	}

	b.reply(chatID, msg.String())
}

// cmdMyTrades показывает статистику по символам для пользователя за последние 90 дней
func (b *TelegramBot) cmdMyTrades(chatID int64, userID int64) {
	trades := b.st.GetUserTradesBySymbol(userID)
	if len(trades) == 0 {
		b.reply(chatID, "У вас нет сделок за последние 90 дней")
		return
	}

	var msg strings.Builder
	msg.WriteString("📈 *Ваши сделки по символам за последние 90 дней:*\n\n")

	// Получаем отсортированные ключи для стабильного порядка
	symbols := make([]string, 0, len(trades))
	for symbol := range trades {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	for _, symbol := range symbols {
		trade := trades[symbol]

		// Показываем только символы с закрытыми сделками
		if trade.ClosedCalls == 0 {
			continue
		}

		pnlSign := "+"
		if trade.TotalPnl < 0 {
			pnlSign = ""
		}

		msg.WriteString(fmt.Sprintf("*%s* / Сделок: %d / Winrate: %.0f%% / PnL: %s%.0f%%\n",
			symbol, trade.ClosedCalls, trade.WinRate, pnlSign, trade.TotalPnl))
	}

	b.reply(chatID, msg.String())
}

// cmdRush закрывает все открытые коллы пользователя
func (b *TelegramBot) cmdRush(ctx context.Context, chatID int64, userID int64) {
	openCalls := b.st.GetUserCalls(userID, true)
	if len(openCalls) == 0 {
		b.reply(chatID, "У вас нет активных коллов для закрытия.")
		return
	}

	var successCount int
	var failCount int
	var failMessages []string

	for _, call := range openCalls {
		// Получаем текущую цену для символа
		priceInfo, err := prices.FetchPriceInfo(b.pricesClients, call.Symbol, call.Exchange, call.Market)
		if err != nil {
			failCount++
			failMessages = append(failMessages, fmt.Sprintf("Колл `%s` (%s): Ошибка получения цены - %s", call.ID, call.Symbol, err.Error()))
			logrus.WithError(err).WithField("call_id", call.ID).Warn("failed to fetch price for /rush command")
			continue
		}

		// Закрываем колл полностью
		err = b.st.CloseCall(call.ID, call.UserID, priceInfo.CurrentPrice, 100.0)
		if err != nil {
			failCount++
			failMessages = append(failMessages, fmt.Sprintf("Колл `%s` (%s): Ошибка закрытия - %s", call.ID, call.Symbol, err.Error()))
			logrus.WithError(err).WithField("call_id", call.ID).Error("failed to close call for /rush command")
		} else {
			successCount++
		}
	}

	responseMsg := fmt.Sprintf("Попытка закрытия всех активных коллов:\nУспешно закрыто: %d\nНе удалось закрыть: %d", successCount, failCount)
	if failCount > 0 {
		responseMsg += "\n\nОшибки:\n" + strings.Join(failMessages, "\n")
	}
	b.reply(chatID, responseMsg)
}

// CallWithPnL структура для отображения коллов с текущим PnL
type CallWithPnL struct {
	alerts.Call
	CurrentPrice float64
	CurrentPnl   float64
}

// cmdAllCalls показывает все активные коллы всех пользователей, сгруппированные по символам
func (b *TelegramBot) cmdAllCalls(ctx context.Context, chatID int64) {
	calls := b.st.GetAllOpenCalls()
	if len(calls) == 0 {
		b.reply(chatID, "Нет активных коллов")
		return
	}

	// Группируем по символу и направлению
	type SymbolKey struct {
		Symbol    string
		Direction string
	}

	// Сначала получаем цены и PnL для всех коллов
	type CallWithPnL struct {
		Call         alerts.Call
		CurrentPrice float64
		BasePnl      float64
		PnlToDeposit float64
	}

	var callsWithPnl []CallWithPnL
	for _, call := range calls {
		priceInfo, err := prices.FetchCurrentPrice(b.pricesClients, call.Symbol, call.Exchange, call.Market)
		if err != nil {
			logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to get current price for call")
			continue
		}
		currentPrice := priceInfo.CurrentPrice

		var basePnl float64
		if call.Direction == "long" {
			basePnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
		} else {
			basePnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
		}

		pnlToDeposit := call.DepositPercent * (basePnl / 100)

		callsWithPnl = append(callsWithPnl, CallWithPnL{
			Call:         call,
			CurrentPrice: currentPrice,
			BasePnl:      basePnl,
			PnlToDeposit: pnlToDeposit,
		})
	}

	// Группируем по символу и направлению
	callsBySymbol := make(map[SymbolKey][]CallWithPnL)
	for _, cwp := range callsWithPnl {
		key := SymbolKey{Symbol: cwp.Call.Symbol, Direction: cwp.Call.Direction}
		callsBySymbol[key] = append(callsBySymbol[key], cwp)
	}

	// Вычисляем средний PnL для каждой группы для сортировки
	type SymbolGroup struct {
		Key        SymbolKey
		AvgPnl     float64
		TotalCalls int
	}

	var groups []SymbolGroup
	for key, groupCalls := range callsBySymbol {
		var totalPnl float64
		for _, cwp := range groupCalls {
			totalPnl += cwp.BasePnl
		}
		avgPnl := totalPnl / float64(len(groupCalls))
		groups = append(groups, SymbolGroup{
			Key:        key,
			AvgPnl:     avgPnl,
			TotalCalls: len(groupCalls),
		})
	}

	// Сортируем группы по среднему PnL
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].AvgPnl > groups[j].AvgPnl
	})

	var msg strings.Builder
	msg.WriteString("Все активные коллы (отсортированы по PnL):\n\n")

	for groupIndex, group := range groups {
		groupCalls := callsBySymbol[group.Key]

		// Сортируем коллы внутри группы по PnL
		sort.Slice(groupCalls, func(i, j int) bool {
			return groupCalls[i].BasePnl > groupCalls[j].BasePnl
		})

		directionRus := "Long"
		if group.Key.Direction == "short" {
			directionRus = "Short"
		}

		// Заголовок группы
		msg.WriteString(fmt.Sprintf("%d. %s (%s)\n", groupIndex+1, group.Key.Symbol, directionRus))

		// Выводим каждый колл в группе
		for i, cwp := range groupCalls {
			call := cwp.Call
			username := call.Username
			if username == "" {
				username = fmt.Sprintf("User_%d", call.UserID)
			}

			pnlSign := "+"
			if cwp.BasePnl < 0 {
				pnlSign = ""
			}

			msg.WriteString(fmt.Sprintf("   %d. %s\n", i+1, username))
			msg.WriteString(fmt.Sprintf("      Цена входа: %s\n", prices.FormatPrice(call.EntryPrice)))
			msg.WriteString(fmt.Sprintf("      Биржа: %s, Рынок: %s\n", call.Exchange, call.Market))

			if call.Size < 100 {
				msg.WriteString(fmt.Sprintf("      Открытый размер: %.0f%%\n", call.Size))
			}

			msg.WriteString(fmt.Sprintf("      Текущий PnL: %s%.2f%%\n", pnlSign, cwp.BasePnl))
		}
		msg.WriteString("\n")
	}

	b.reply(chatID, msg.String())
}

// cmdListAlerts показывает список алертов пользователя, сгруппированных по символам
func (b *TelegramBot) cmdListAlerts(chatID int64) {
	alertsList := b.st.ListByChat(chatID)
	if len(alertsList) == 0 {
		b.reply(chatID, "У вас нет активных алертов")
		return
	}

	// Группируем алерты по символам
	alertsBySymbol := make(map[string][]alerts.Alert)
	for _, alert := range alertsList {
		alertsBySymbol[alert.Symbol] = append(alertsBySymbol[alert.Symbol], alert)
	}

	// Получаем и сортируем символы
	symbols := make([]string, 0, len(alertsBySymbol))
	for symbol := range alertsBySymbol {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	var msg strings.Builder
	msg.WriteString("Ваши алерты:\n\n")

	for _, symbol := range symbols {
		msg.WriteString(fmt.Sprintf("%s:\n", symbol))

		// Сортируем алерты для этого символа по целевой цене
		symbolAlerts := alertsBySymbol[symbol]
		sort.Slice(symbolAlerts, func(i, j int) bool {
			return symbolAlerts[i].TargetPrice < symbolAlerts[j].TargetPrice
		})

		for i, alert := range symbolAlerts {
			if alert.TargetPrice > 0 {
				msg.WriteString(fmt.Sprintf("%d. Цель %s, ID: `%s`\n",
					i+1, prices.FormatPrice(alert.TargetPrice), alert.ID))
			} else if alert.TargetPercent != 0 {
				msg.WriteString(fmt.Sprintf("%d. Изменение на %.2f%% от %s, ID: `%s`\n",
					i+1, alert.TargetPercent, prices.FormatPrice(alert.BasePrice), alert.ID))
			}
			msg.WriteString(fmt.Sprintf("   Биржа: %s, Рынок: %s\n", alert.Exchange, alert.Market))
		}
		msg.WriteString("\n")
	}

	b.reply(chatID, msg.String())
}

// cmdDelAlert удаляет алерт по ID
func (b *TelegramBot) cmdDelAlert(chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) != 2 {
		b.reply(chatID, "Использование: /del ID")
		return
	}

	id := parts[1]
	deleted, err := b.st.DeleteByID(chatID, id)
	if err != nil {
		b.reply(chatID, "Ошибка удаления: "+err.Error())
		return
	}
	if deleted {
		b.reply(chatID, "Алерт "+id+" удален")
		// Перезапускаем мониторинг после удаления алерта
		b.restartMonitoring(context.Background())
	} else {
		b.reply(chatID, "Алерт не найден")
	}
}

// cmdDelAllAlerts удаляет все алерты пользователя
func (b *TelegramBot) cmdDelAllAlerts(chatID int64) {
	count, err := b.st.DeleteAllByChat(chatID)
	if err != nil {
		b.reply(chatID, "Ошибка удаления: "+err.Error())
		return
	}
	b.reply(chatID, fmt.Sprintf("Удалено алертов: %d", count))
	if count > 0 {
		// Перезапускаем мониторинг после удаления алертов
		b.restartMonitoring(context.Background())
	}
}

// cmdPriceAll показывает цены всех символов с алертами и коллами пользователя
func (b *TelegramBot) cmdPriceAll(ctx context.Context, chatID int64) {
	// Получаем символы из алертов и открытых коллов пользователя
	symbols := b.st.GetSymbolsFromUserAlertsAndCalls(chatID)
	if len(symbols) == 0 {
		b.reply(chatID, "У вас нет активных алертов или коллов")
		return
	}

	msg := "Цены ваших токенов:\n\n"

	for _, symbol := range symbols {
		preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
		priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
		if err != nil {
			msg += fmt.Sprintf("%s: ошибка получения цены\n", symbol)
			logrus.WithError(err).WithField("symbol", symbol).Warn("failed to fetch price info")
			continue
		}

		// Форматируем изменения
		change15m := formatChange(priceInfo.Change15m)
		change1h := formatChange(priceInfo.Change1h)
		change4h := formatChange(priceInfo.Change4h)
		change24h := formatChange(priceInfo.Change24h)

		msg += fmt.Sprintf("%s: %s\n", symbol, prices.FormatPrice(priceInfo.CurrentPrice))
		msg += fmt.Sprintf("15м: %s | 1ч: %s | 4ч: %s | 24ч: %s\n",
			change15m, change1h, change4h, change24h)
		msg += fmt.Sprintf("Биржа: %s, Рынок: %s\n\n", priceInfo.Exchange, priceInfo.Market)
	}

	b.reply(chatID, msg)
}

// cmdPrice показывает цену одного символа с изменениями
func (b *TelegramBot) cmdPrice(ctx context.Context, chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) != 2 {
		b.reply(chatID, "Использование: /price TICKER\nПример: /price BTCUSDT")
		return
	}

	symbol := formatSymbol(parts[1])
	preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
	priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
	if err != nil {
		b.reply(chatID, fmt.Sprintf("%s: ошибка получения цены - %s", symbol, err.Error()))
		logrus.WithError(err).WithField("symbol", symbol).Warn("failed to fetch price info")
		return
	}

	// Форматируем изменения
	change15m := formatChange(priceInfo.Change15m)
	change1h := formatChange(priceInfo.Change1h)
	change4h := formatChange(priceInfo.Change4h)
	change24h := formatChange(priceInfo.Change24h)

	msg := fmt.Sprintf("%s: %s\n", symbol, prices.FormatPrice(priceInfo.CurrentPrice))
	msg += fmt.Sprintf("15м: %s | 1ч: %s | 4ч: %s | 24ч: %s",
		change15m, change1h, change4h, change24h)
	msg += fmt.Sprintf("\nБиржа: %s, Рынок: %s", priceInfo.Exchange, priceInfo.Market)

	b.reply(chatID, msg)
}

// formatChange форматирует процентное изменение
func formatChange(change float64) string {
	if change > 0 {
		return fmt.Sprintf("+%.2f%%", change)
	} else if change < 0 {
		return fmt.Sprintf("%.2f%%", change) // знак минус уже есть в числе
	} else {
		return "0.00%"
	}
}

// checkAlerts проверяет алерты для символа и отправляет уведомления
func (b *TelegramBot) checkAlerts(symbol string, currentPrice float64) {
	alerts := b.st.GetBySymbol(symbol)
	logrus.WithFields(logrus.Fields{
		"symbol": symbol,
		"price":  currentPrice,
		"count":  len(alerts),
	}).Debug("checking alerts for symbol")

	for _, alert := range alerts {
		triggered := false
		var msg string

		logrus.WithFields(logrus.Fields{
			"alert_id":       alert.ID,
			"target_price":   alert.TargetPrice,
			"target_percent": alert.TargetPercent,
			"base_price":     alert.BasePrice,
			"current_price":  currentPrice,
		}).Debug("checking individual alert")

		// Проверка алерта по целевой цене с погрешностью 0.5%
		if alert.TargetPrice > 0 {
			tolerance := alert.TargetPrice * 0.005 // 0.5%

			// Проверяем попадание в диапазон с погрешностью
			if math.Abs(currentPrice-alert.TargetPrice) <= tolerance {
				triggered = true
				msg = fmt.Sprintf("🚨АЛЕРТ! %s достиг %s (текущая: %s)", symbol, prices.FormatPrice(alert.TargetPrice), prices.FormatPrice(currentPrice))
				logrus.WithField("alert_id", alert.ID).Info("price alert triggered")
			}
		}

		// Проверка алерта по проценту
		if !triggered && alert.TargetPercent != 0 && alert.BasePrice > 0 {
			changePct := ((currentPrice - alert.BasePrice) / alert.BasePrice) * 100

			// Проверяем достижение целевого процента (с учетом направления)
			targetReached := false
			if alert.TargetPercent > 0 && changePct >= alert.TargetPercent {
				targetReached = true
			} else if alert.TargetPercent < 0 && changePct <= alert.TargetPercent {
				targetReached = true
			}

			if targetReached {
				triggered = true
				direction := "вырос"
				if alert.TargetPercent < 0 {
					direction = "упал"
				}
				msg = fmt.Sprintf("АЛЕРТ! %s %s на %.2f%% (от %s до %s)",
					symbol, direction, math.Abs(changePct), prices.FormatPrice(alert.BasePrice), prices.FormatPrice(currentPrice))
				logrus.WithFields(logrus.Fields{
					"alert_id":   alert.ID,
					"change_pct": changePct,
					"target_pct": alert.TargetPercent,
				}).Info("percent alert triggered")
			}
		}

		if triggered {
			// Логируем срабатывание алерта
			triggerType := "price"
			if alert.TargetPercent != 0 {
				triggerType = "percent"
			}
			b.st.LogAlertTrigger(alert.ID, symbol, currentPrice, alert.ChatID, alert.UserID, alert.Username, triggerType)

			// Отправляем уведомление
			b.reply(alert.ChatID, msg)

			// Удаляем сработавший алерт
			_, err := b.st.DeleteByID(alert.ChatID, alert.ID)
			if err != nil {
				logrus.WithError(err).WithField("alert_id", alert.ID).Warn("failed to delete triggered alert")
			} else {
				logrus.WithFields(logrus.Fields{
					"alert_id": alert.ID,
					"symbol":   symbol,
					"price":    currentPrice,
				}).Info("alert triggered and deleted")
			}
		}
	}
}

// checkSharpChange проверяет резкие изменения цены для символа
func (b *TelegramBot) checkSharpChange(symbol string, currentPrice float64) {
	// Получаем цену на указанный интервал назад
	intervalAgo := time.Now().Add(-time.Duration(b.cfg.SharpChangeIntervalMin) * time.Minute)

	oldPrice := 0.0
	err := error(nil)

	// Определяем предпочтительную биржу и рынок из существующих алертов или коллов
	preferredExchange := ""
	preferredMarket := ""

	preferredExchange, preferredMarket = b.getPreferredExchangeMarketForSymbol(symbol)

	b.sharpChangeMu.Lock()
	lastAlert, exists := b.lastSharpChangeAlert[symbol]
	b.sharpChangeMu.Unlock()

	// Если есть данные о предыдущем алерте о резком изменении, используем его цену как базовую для следующего расчета.
	// Это обеспечивает, что последующие алерты считаются от цены последнего срабатывания.
	if exists && time.Since(lastAlert.Time) < time.Duration(b.cfg.SharpChangeIntervalMin)*time.Minute {
		oldPrice = lastAlert.Price
	} else {
		oldPrice, err = b.fetchHistoricalPrice(symbol, intervalAgo, preferredExchange, preferredMarket)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"symbol":   symbol,
				"interval": fmt.Sprintf("%dm", b.cfg.SharpChangeIntervalMin),
			}).Debug("failed to get historical price for sharp change check")
			return
		}
	}

	// Вычисляем процентное изменение
	changePct := ((currentPrice - oldPrice) / oldPrice) * 100
	absChangePct := math.Abs(changePct)

	logrus.WithFields(logrus.Fields{
		"symbol":        symbol,
		"current_price": currentPrice,
		"old_price":     oldPrice,
		"change_pct":    changePct,
		"threshold":     b.cfg.SharpChangePercent,
		"interval_min":  b.cfg.SharpChangeIntervalMin,
	}).Debug("checking sharp change")

	// Проверяем, превышает ли изменение пороговое значение
	if absChangePct >= b.cfg.SharpChangePercent {
		// Проверяем, не отправляли ли мы уже алерт недавно для этого символа
		b.sharpChangeMu.Lock()
		lastAlertTime, exists := b.lastSharpChangeAlert[symbol]
		now := time.Now()

		// Отправляем алерт не чаще чем раз в 5 минут для одного символа
		if !exists || now.Sub(lastAlertTime.Time) >= 5*time.Minute {
			b.lastSharpChangeAlert[symbol] = struct {
				Time  time.Time
				Price float64
			}{Time: now, Price: currentPrice}
			b.sharpChangeMu.Unlock()

			// Формируем сообщение
			direction := "вырос"
			if changePct < 0 {
				direction = "упал"
			}

			// Получаем всех пользователей с алертами или коллами на этот символ
			symbolAlerts := b.st.GetBySymbol(symbol)
			symbolCalls := b.st.GetAllOpenCalls()

			// Создаем map уникальных пользователей
			alertedUsers := make(map[int64]alerts.Alert)

			// Добавляем пользователей с алертами
			for _, alert := range symbolAlerts {
				alertedUsers[alert.ChatID] = alert
			}

			// Добавляем пользователей с коллами на данный символ
			for _, call := range symbolCalls {
				if call.Symbol == symbol {
					// Создаем "псевдо-алерт" для пользователя с коллом
					pseudoAlert := alerts.Alert{
						ChatID:   call.ChatID,
						UserID:   call.UserID,
						Username: call.Username,
						Symbol:   call.Symbol,
					}
					alertedUsers[call.ChatID] = pseudoAlert
				}
			}

			// Отправляем уведомление каждому пользователю с алертами на этот символ
			if len(alertedUsers) > 0 {
				msg := fmt.Sprintf("%s %s на %.2f%% за %dм (от %s до %s)",
					symbol, direction, absChangePct, b.cfg.SharpChangeIntervalMin,
					prices.FormatPrice(oldPrice), prices.FormatPrice(currentPrice))

				for chatID, alert := range alertedUsers {
					b.reply(chatID, msg)
					// Логируем резкое изменение. Сохраняем currentPrice как lastTriggerPrice для следующего алерта.
					b.st.LogAlertTrigger("", symbol, currentPrice, chatID, alert.UserID, alert.Username, "sharp_change")
				}

				logrus.WithFields(logrus.Fields{
					"symbol":         symbol,
					"change_pct":     changePct,
					"interval_min":   b.cfg.SharpChangeIntervalMin,
					"notified_chats": len(alertedUsers),
				}).Info("sharp change alert sent")
			}
		} else {
			b.sharpChangeMu.Unlock()
			logrus.WithFields(logrus.Fields{
				"symbol":              symbol,
				"change_pct":          changePct,
				"last_alert_time_ago": now.Sub(lastAlertTime.Time).String(),
			}).Debug("sharp change detected but alert suppressed due to recent notification")
		}
	}
}

// fetchHistoricalPrice получает историческую цену для указанного времени
func (b *TelegramBot) fetchHistoricalPrice(symbol string, timestamp time.Time, preferredExchange, preferredMarket string) (float64, error) {
	return prices.FetchHistoricalPrice(b.pricesClients, symbol, timestamp, preferredExchange, preferredMarket)
}

// cmdHistory показывает историю сработавших алертов пользователя
func (b *TelegramBot) cmdHistory(chatID int64, text string) {
	parts := strings.Fields(text)
	limit := 10 // по умолчанию последние 10

	if len(parts) == 2 {
		if l, err := strconv.Atoi(parts[1]); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}

	triggers := b.st.GetTriggerHistory(chatID, limit)
	if len(triggers) == 0 {
		b.reply(chatID, "У вас нет истории сработавших алертов")
		return
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("Последние %d сработавших алертов:\n\n", len(triggers)))

	for i, trigger := range triggers {
		triggerTypeRus := map[string]string{
			"price":        "Цена",
			"percent":      "Процент",
			"sharp_change": "Резкое изменение",
		}

		typeStr := triggerTypeRus[trigger.TriggerType]
		if typeStr == "" {
			typeStr = trigger.TriggerType
		}

		msg.WriteString(fmt.Sprintf("%d. %s - %s\n", i+1, trigger.Symbol, typeStr))
		msg.WriteString(fmt.Sprintf("   Цена: %s\n", prices.FormatPrice(trigger.TriggerPrice)))
		msg.WriteString(fmt.Sprintf("   Время: %s\n\n", trigger.TriggeredAt.Format("02.01.2006 15:04")))
	}

	b.reply(chatID, msg.String())
}

// cmdStats показывает статистику по символам
func (b *TelegramBot) cmdStats(chatID int64, userID int64) {
	stats := b.st.GetSymbolStats(userID)
	if len(stats) == 0 {
		b.reply(chatID, "Нет данных для статистики")
		return
	}

	var msg strings.Builder
	msg.WriteString("Статистика активных алертов по символам:\n\n")

	// Для сортировки по количеству активных алертов
	type symbolStat struct {
		symbol             string
		activeAlertsCount  int
		totalTriggersCount int
	}

	var sortedStats []symbolStat
	for symbol, stat := range stats {
		sortedStats = append(sortedStats, symbolStat{symbol, stat.ActiveAlerts, stat.TotalTriggers})
	}

	// Сортировка по убыванию количества активных алертов
	sort.Slice(sortedStats, func(i, j int) bool {
		return sortedStats[i].activeAlertsCount > sortedStats[j].activeAlertsCount
	})

	for i, stat := range sortedStats {
		msg.WriteString(fmt.Sprintf("%d. %s: %d активных алертов, %d срабатываний\n", i+1, stat.symbol, stat.activeAlertsCount, stat.totalTriggersCount))
	}

	var totalActiveAlerts int
	for _, stat := range stats {
		totalActiveAlerts += stat.ActiveAlerts
	}

	msg.WriteString(fmt.Sprintf("\nВсего активных алертов: %d\n", totalActiveAlerts))
	msg.WriteString(fmt.Sprintf("Отслеживается символов: %d", len(stats)))

	b.reply(chatID, msg.String())
}

// startMonitoring запускает мониторинг цен для алертов
func (b *TelegramBot) startMonitoring(ctx context.Context) {
	// Останавливаем предыдущий мониторинг если есть
	if b.stopMon != nil {
		b.stopMon()
	}

	// Получаем все символы из алертов
	symbols := b.st.GetAllSymbols()
	logrus.WithField("symbols", symbols).Info("starting monitoring for alert symbols")

	if len(symbols) > 0 {
		// Используем мониторинг с провайдером символов, проверяем каждые 60 секунд
		mon := prices.NewPriceMonitorWithProvider(b.st, b.pricesClients, 0, 60)
		monCtx, cancel := context.WithCancel(ctx)
		b.monitorCtx = monCtx
		b.stopMon = cancel
		go func() {
			_ = mon.Run(monCtx, func(symbol string, oldPrice, newPrice, deltaPct float64) {
				// Логируем цену в историю (периодически)
				b.st.LogPriceHistory(symbol, newPrice)

				// Проверяем алерты пользователей
				alertsForSymbol := b.st.GetBySymbol(symbol)
				callsForSymbol := b.st.GetAllOpenCalls()

				// Фильтруем коллы по символу
				var symbolCalls []alerts.Call
				for _, call := range callsForSymbol {
					if call.Symbol == symbol {
						symbolCalls = append(symbolCalls, call)
					}
				}

				if len(alertsForSymbol) > 0 || len(symbolCalls) > 0 {
					b.checkAlerts(symbol, newPrice)
					// Также проверяем резкие изменения цены
					b.checkSharpChange(symbol, newPrice)
					// проверяем лимитные ордера
					b.checkLimitOrders(symbol, newPrice)

					// Проверяем стоп-лоссы для открытых коллов
					for _, call := range symbolCalls {
						if call.StopLossPrice > 0 {
							triggeredSL := false
							var slMsg string

							if call.Direction == "long" && newPrice <= call.StopLossPrice {
								triggeredSL = true
								slMsg = fmt.Sprintf("🛑 СТОП-ЛОСС! Колл `%s` (%s %s) закрыт по стоп-лоссу: цена %s достигла/пробила %s",
									call.ID, call.Symbol, "Long", prices.FormatPrice(newPrice), prices.FormatPrice(call.StopLossPrice))
							} else if call.Direction == "short" && newPrice >= call.StopLossPrice {
								triggeredSL = true
								slMsg = fmt.Sprintf("🛑 СТОП-ЛОСС! Колл `%s` (%s %s) закрыт по стоп-лоссу: цена %s достигла/пробила %s",
									call.ID, call.Symbol, "Short", prices.FormatPrice(newPrice), prices.FormatPrice(call.StopLossPrice))
							}

							if triggeredSL {
								logrus.WithFields(logrus.Fields{
									"call_id":         call.ID,
									"symbol":          call.Symbol,
									"current_price":   newPrice,
									"stop_loss_price": call.StopLossPrice,
									"direction":       call.Direction,
								}).Info("stop-loss triggered")

								// Закрываем колл полностью оставшимся размером
								err := b.st.CloseCall(call.ID, call.UserID, newPrice, call.Size)
								if err != nil {
									logrus.WithError(err).WithField("call_id", call.ID).Error("failed to close call by stop-loss")
								} else {
									// Отменяем все лимитные ордера, связанные с этим коллом
									err = b.st.CancelLimitOrdersByCallID(call.ID)
									if err != nil {
										logrus.WithError(err).Warn("failed to cancel limit orders after stop-loss")
									}
									b.reply(call.ChatID, slMsg)
								}
							}
						}
					}
				} else {
					logrus.WithField("symbol", symbol).Debug("no alerts or calls for symbol, skipping check")
				}
			})
		}()
	} else {
		logrus.Info("no alert symbols found, monitoring disabled")
	}
}

// restartMonitoring перезапускает мониторинг (вызывается при добавлении алертов)
func (b *TelegramBot) restartMonitoring(ctx context.Context) {
	logrus.Info("restarting monitoring due to alert changes")
	b.startMonitoring(ctx)
}

// formatSymbol добавляет "USDT" к символу, если он не содержит пары со стейблкоином.
func formatSymbol(symbol string) string {
	upperSymbol := strings.ToUpper(symbol)
	if !(strings.HasSuffix(upperSymbol, "USDT") || strings.HasSuffix(upperSymbol, "USD") ||
		strings.HasSuffix(upperSymbol, "BUSD") || strings.HasSuffix(upperSymbol, "DAI") ||
		strings.HasSuffix(upperSymbol, "USDC") || strings.HasSuffix(upperSymbol, "UST")) {
		return upperSymbol + "USDT"
	}
	return upperSymbol
}

// getPreferredExchangeMarketForSymbol пытается определить предпочтительную биржу и рынок для символа
// на основе всех существующих алертов и открытых коллов в системе.
func (b *TelegramBot) getPreferredExchangeMarketForSymbol(symbol string) (string, string) {
	// Проверяем алерты
	alertsForSymbol := b.st.GetBySymbol(symbol)
	for _, alert := range alertsForSymbol {
		if alert.Exchange != "" && alert.Market != "" {
			return alert.Exchange, alert.Market
		}
	}

	// Если не нашли в алертах, проверяем коллы
	callsForSymbol := b.st.GetAllOpenCalls()
	for _, call := range callsForSymbol {
		if call.Symbol == symbol && call.Exchange != "" && call.Market != "" {
			return call.Exchange, call.Market
		}
	}
	return "", ""
}

// cmdCreateLimitOrder обрабатывает команду /limit
func (b *TelegramBot) cmdCreateLimitOrder(ctx context.Context, chatID, userID int64, username, text string) {
	parts := strings.Fields(text)
	if len(parts) < 5 {
		b.reply(chatID, "Использование: /limit TICKER [b|s] PRICE DEPOSIT_PERCENT [CALL_ID]\n"+
			"Примеры:\n"+
			"/limit BTC b 120000 5 - открыть лонг при достижении 120000\n"+
			"/limit BTC s 122000 50 abc123de - закрыть 50% колла abc123de при достижении 122000")
		return
	}

	symbol := formatSymbol(parts[1])
	directionStr := strings.ToLower(parts[2])

	var direction string
	if directionStr == "b" {
		direction = "long"
	} else if directionStr == "s" {
		direction = "short"
	} else {
		b.reply(chatID, "Направление должно быть 'b' (buy/long) или 's' (sell/short)")
		return
	}

	limitPrice, err := strconv.ParseFloat(parts[3], 64)
	if err != nil || limitPrice <= 0 {
		b.reply(chatID, "Неверная цена лимитного ордера")
		return
	}

	depositPercent, err := strconv.ParseFloat(parts[4], 64)
	if err != nil || depositPercent <= 0 {
		b.reply(chatID, "Неверный процент депозита")
		return
	}

	var relatedCallID string
	var sizeToClose float64

	// Если указан ID колла - это ордер на закрытие
	if len(parts) >= 6 {
		relatedCallID = parts[5]

		// Проверяем существование колла
		call, err := b.st.GetCallByID(relatedCallID, userID)
		if err != nil {
			b.reply(chatID, "Колл не найден или не принадлежит вам")
			return
		}

		if call.Status != "open" {
			b.reply(chatID, "Нельзя создать лимитный ордер для закрытого колла")
			return
		}

		// Проверяем совпадение символа
		if call.Symbol != symbol {
			b.reply(chatID, fmt.Sprintf("Символ ордера (%s) не совпадает с символом колла (%s)", symbol, call.Symbol))
			return
		}

		// depositPercent в этом случае - процент от позиции для закрытия
		if depositPercent > 100 {
			b.reply(chatID, "Процент для закрытия не может быть больше 100")
			return
		}

		sizeToClose = call.Size * (depositPercent / 100)

		// Проверяем направление: для лонга используем sell, для шорта - buy
		expectedDirection := "short"
		if call.Direction == "short" {
			expectedDirection = "long"
		}

		if direction != expectedDirection {
			b.reply(chatID, fmt.Sprintf("Для закрытия %s позиции используйте направление %s",
				call.Direction,
				map[string]string{"long": "b", "short": "s"}[expectedDirection]))
			return
		}
	}

	// Получаем текущую цену для информации
	preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
	priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
	if err != nil {
		b.reply(chatID, "Ошибка получения текущей цены для "+symbol+": "+err.Error())
		return
	}

	// Создаем лимитный ордер
	order := alerts.LimitOrder{
		UserID:         userID,
		Username:       username,
		ChatID:         chatID,
		Symbol:         symbol,
		Direction:      direction,
		LimitPrice:     limitPrice,
		DepositPercent: depositPercent,
		RelatedCallID:  relatedCallID,
		SizeToClose:    sizeToClose,
	}

	order, err = b.st.CreateLimitOrder(order)
	if err != nil {
		b.reply(chatID, "Ошибка создания лимитного ордера: "+err.Error())
		return
	}

	// Формируем сообщение
	var msg string
	directionRus := map[string]string{"long": "Long", "short": "Short"}[direction]

	if relatedCallID != "" {
		msg = fmt.Sprintf("✅ Лимитный ордер создан!\nID: `%s`\nСимвол: %s\nТип: Закрытие %.0f%% колла `%s`\nЦена: %s\nТекущая цена: %s",
			order.ID, symbol, depositPercent, relatedCallID,
			prices.FormatPrice(limitPrice), prices.FormatPrice(priceInfo.CurrentPrice))
	} else {
		msg = fmt.Sprintf("✅ Лимитный ордер создан!\nID: `%s`\nСимвол: %s\nНаправление: %s\nЦена: %s\nРазмер: %.0f%% депозита\nТекущая цена: %s",
			order.ID, symbol, directionRus, prices.FormatPrice(limitPrice),
			depositPercent, prices.FormatPrice(priceInfo.CurrentPrice))
	}

	b.reply(chatID, msg)

	// Перезапускаем мониторинг для учета новых ордеров
	b.restartMonitoring(ctx)
}

// cmdCancelLimitOrder обрабатывает команду /climit
func (b *TelegramBot) cmdCancelLimitOrder(ctx context.Context, chatID, userID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) != 2 {
		b.reply(chatID, "Использование: /climit ORDER_ID\nПример: /climit `abc123de`")
		return
	}

	orderID := parts[1]

	err := b.st.CancelLimitOrder(orderID, userID)
	if err != nil {
		b.reply(chatID, "Ошибка отмены ордера: "+err.Error())
		return
	}

	b.reply(chatID, fmt.Sprintf("❌ Лимитный ордер `%s` отменен", orderID))
}

// cmdMyOrders показывает активные лимитные ордера пользователя
func (b *TelegramBot) cmdMyOrders(ctx context.Context, chatID, userID int64) {
	orders := b.st.GetUserLimitOrders(userID)
	if len(orders) == 0 {
		b.reply(chatID, "У вас нет активных лимитных ордеров")
		return
	}

	// Группируем ордера по символам
	ordersBySymbol := make(map[string][]alerts.LimitOrder)
	for _, order := range orders {
		ordersBySymbol[order.Symbol] = append(ordersBySymbol[order.Symbol], order)
	}

	// Получаем отсортированные символы
	symbols := make([]string, 0, len(ordersBySymbol))
	for symbol := range ordersBySymbol {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	var msg strings.Builder
	msg.WriteString("📋 *Ваши активные лимитные ордера:*\n\n")

	for idx, symbol := range symbols {
		symbolOrders := ordersBySymbol[symbol]

		// Получаем текущую цену
		preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
		priceInfo, err := prices.FetchCurrentPrice(b.pricesClients, symbol, preferredExchange, preferredMarket)
		currentPrice := 0.0
		if err == nil {
			currentPrice = priceInfo.CurrentPrice
		}

		msg.WriteString(fmt.Sprintf("%d. *%s*", idx+1, symbol))
		if currentPrice > 0 {
			msg.WriteString(fmt.Sprintf(" (текущая: %s)", prices.FormatPrice(currentPrice)))
		}
		msg.WriteString("\n\n")

		// Сортируем ордера по цене
		sort.Slice(symbolOrders, func(i, j int) bool {
			return symbolOrders[i].LimitPrice < symbolOrders[j].LimitPrice
		})

		for i, order := range symbolOrders {
			directionRus := map[string]string{"long": "Long", "short": "Short"}[order.Direction]

			var orderType string
			if order.RelatedCallID != "" {
				orderType = fmt.Sprintf("Закрытие %.0f%% колла `%s`",
					(order.SizeToClose/100)*100, order.RelatedCallID)
			} else {
				orderType = fmt.Sprintf("%s %.0f%%", directionRus, order.DepositPercent)
			}

			// Рассчитываем разницу с текущей ценой
			var priceDiff string
			if currentPrice > 0 {
				diff := ((order.LimitPrice - currentPrice) / currentPrice) * 100
				sign := "+"
				if diff < 0 {
					sign = ""
				}
				priceDiff = fmt.Sprintf(" (%s%.1f%%)", sign, diff)
			}

			msg.WriteString(fmt.Sprintf("   %d. ID: `%s`\n", i+1, order.ID))
			msg.WriteString(fmt.Sprintf("      Тип: %s\n", orderType))
			msg.WriteString(fmt.Sprintf("      Цена: %s%s\n", prices.FormatPrice(order.LimitPrice), priceDiff))
		}
		msg.WriteString("\n")
	}

	b.reply(chatID, msg.String())
}

// checkLimitOrders проверяет и исполняет лимитные ордера
func (b *TelegramBot) checkLimitOrders(symbol string, currentPrice float64) {
	orders := b.st.GetLimitOrdersBySymbol(symbol)
	if len(orders) == 0 {
		return
	}

	logrus.WithFields(logrus.Fields{
		"symbol": symbol,
		"price":  currentPrice,
		"count":  len(orders),
	}).Debug("checking limit orders for symbol")

	for _, order := range orders {
		triggered := false

		// Проверяем условия исполнения
		// Для long (buy): цена опустилась до/ниже лимитной
		// Для short (sell): цена поднялась до/выше лимитной
		if order.Direction == "long" && currentPrice <= order.LimitPrice {
			triggered = true
		} else if order.Direction == "short" && currentPrice >= order.LimitPrice {
			triggered = true
		}

		if !triggered {
			continue
		}

		logrus.WithFields(logrus.Fields{
			"order_id":        order.ID,
			"symbol":          order.Symbol,
			"direction":       order.Direction,
			"limit_price":     order.LimitPrice,
			"current_price":   currentPrice,
			"related_call_id": order.RelatedCallID,
		}).Info("limit order triggered")

		var msg string

		// Если это ордер на закрытие колла
		if order.RelatedCallID != "" {
			// Получаем информацию о колле
			call, err := b.st.GetCallByID(order.RelatedCallID, order.UserID)
			if err != nil {
				logrus.WithError(err).WithField("order_id", order.ID).Error("failed to get call for limit order")
				b.st.CancelLimitOrder(order.ID, order.UserID)
				continue
			}

			if call.Status != "open" {
				logrus.WithField("order_id", order.ID).Warn("call already closed, cancelling limit order")
				b.st.CancelLimitOrder(order.ID, order.UserID)
				continue
			}

			// Закрываем позицию
			err = b.st.CloseCall(order.RelatedCallID, order.UserID, currentPrice, order.SizeToClose)
			if err != nil {
				logrus.WithError(err).WithField("order_id", order.ID).Error("failed to close call by limit order")
				msg = fmt.Sprintf("⚠️ Ошибка исполнения лимитного ордера `%s`: %s", order.ID, err.Error())
			} else {
				// Получаем обновленную информацию о колле
				updatedCall, _ := b.st.GetCallByID(order.RelatedCallID, order.UserID)

				pnlSign := "+"
				var pnl float64
				if updatedCall != nil {
					pnl = updatedCall.PnlPercent
					if pnl < 0 {
						pnlSign = ""
					}
				}

				if updatedCall != nil && updatedCall.Status == "closed" {
					msg = fmt.Sprintf("✅ Лимитный ордер `%s` исполнен!\nКолл `%s` (%s) полностью закрыт по цене %s\nPnL: %s%.2f%%",
						order.ID, order.RelatedCallID, symbol,
						prices.FormatPrice(currentPrice), pnlSign, pnl)
				} else {
					msg = fmt.Sprintf("✅ Лимитный ордер `%s` исполнен!\nЗакрыто %.0f%% колла `%s` (%s) по цене %s\nPnL на закрытую часть: %s%.2f%%",
						order.ID, (order.SizeToClose/call.Size)*100,
						order.RelatedCallID, symbol, prices.FormatPrice(currentPrice), pnlSign, pnl)
				}

				// Помечаем ордер как исполненный
				b.st.TriggerLimitOrder(order.ID)
			}
		} else {
			// Это ордер на открытие позиции
			preferredExchange, preferredMarket := b.getPreferredExchangeMarketForSymbol(symbol)
			priceInfo, err := prices.FetchPriceInfo(b.pricesClients, symbol, preferredExchange, preferredMarket)
			if err != nil {
				logrus.WithError(err).WithField("order_id", order.ID).Error("failed to get price info for limit order")
				continue
			}

			// Создаем новый колл
			call := alerts.Call{
				UserID:         order.UserID,
				Username:       order.Username,
				ChatID:         order.ChatID,
				Symbol:         symbol,
				Direction:      order.Direction,
				EntryPrice:     currentPrice,
				Market:         priceInfo.Market,
				DepositPercent: order.DepositPercent,
				Exchange:       priceInfo.Exchange,
			}

			call, err = b.st.OpenCall(call)
			if err != nil {
				logrus.WithError(err).WithField("order_id", order.ID).Error("failed to open call by limit order")
				msg = fmt.Sprintf("⚠️ Ошибка исполнения лимитного ордера `%s`: %s", order.ID, err.Error())
			} else {
				directionRus := map[string]string{"long": "Long", "short": "Short"}[order.Direction]
				msg = fmt.Sprintf("✅ Лимитный ордер `%s` исполнен!\nОткрыт колл `%s`\nСимвол: %s\nНаправление: %s\nЦена входа: %s\nРазмер: %.0f%%",
					order.ID, call.ID, symbol, directionRus,
					prices.FormatPrice(currentPrice), order.DepositPercent)

				// Помечаем ордер как исполненный
				b.st.TriggerLimitOrder(order.ID)
			}
		}

		// Отправляем уведомление пользователю
		if msg != "" {
			b.reply(order.ChatID, msg)
		}
	}
}
