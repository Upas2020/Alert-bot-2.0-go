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
)

// TelegramBot инкапсулирует работу с Telegram API.
type TelegramBot struct {
	api        *tgbotapi.BotAPI
	cfg        config.Config
	st         *alerts.DatabaseStorage
	monitorCtx context.Context
	stopMon    context.CancelFunc

	// Для отслеживания резких изменений цен
	sharpChangeMu       sync.Mutex
	lastSharpChangeTime map[string]time.Time // Время последнего алерта о резком изменении для каждого символа
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
	return &TelegramBot{
		api:                 api,
		cfg:                 cfg,
		st:                  st,
		lastSharpChangeTime: make(map[string]time.Time),
	}, nil
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
	case text == "/delallalerts":
		b.cmdDelAllAlerts(chatID)
	case text == "/pall":
		b.cmdPriceAll(ctx, chatID)
	case strings.HasPrefix(text, "/p"):
		b.cmdPrice(ctx, chatID, text)
	case strings.HasPrefix(text, "/ocall"):
		b.cmdOpenCall(ctx, chatID, userID, username, text)
	case strings.HasPrefix(text, "/ccall"):
		b.cmdCloseCall(ctx, chatID, userID, text)
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
		b.cmdStats(chatID)
	case text == "/start":
		b.reply(chatID, "Way2Million, powered by Saint_Dmitriy\n\n*Цены:*\n/p TICKER - показать текущую цену\n/pall - показать цену всех токенов, по которым есть активные алерты/коллы\n\n*Алерты:*\n/addalert TICKER price|pct VALUE - создать алерт\n/alerts - список активных алертов\n/dell alertid- удалить алерт\n\n*Коллы:*\n/ocall TICKER [long|short] - открыть колл (по умолчанию long)\n/ccall CALLID - закрыть колл\n/mycalls - мои активные коллы\n/allcalls - коллы всех пользователей\n\n*Статистика:*\n/callstats - рейтинг трейдеров за 90 дней\n/mycallstats - моя общая статистика за 90 дней\n/mytrades - моя статистика по токенам\n/stats- статистика по сработавшим алертам\n/history - история сработавших алертов")
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

// cmdAddAlert обрабатывает команду /add TICKER price|pct VALUE
func (b *TelegramBot) cmdAddAlert(ctx context.Context, chatID int64, userID int64, username string, text string) {
	parts := strings.Fields(text)
	if len(parts) != 4 {
		b.reply(chatID, "Использование: /add TICKER price|pct VALUE\nПример: /add BTCUSDT price 50000")
		return
	}

	symbol := strings.ToUpper(parts[1])
	alertType := parts[2]
	valueStr := parts[3]

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

	switch alertType {
	case "price":
		alert.TargetPrice = value
		alert, err = b.st.Add(alert)
		if err != nil {
			b.reply(chatID, "Ошибка создания алерта: "+err.Error())
			return
		}
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: `%s`)\n%s достигнет %s", alert.ID, symbol, prices.FormatPrice(value)))

		// Перезапускаем мониторинг с новым символом
		b.restartMonitoring(ctx)
	case "pct":
		alert.TargetPercent = value
		// Получаем текущую цену для базовой
		price, err := prices.FetchSpotPrice(nil, symbol)
		if err != nil {
			b.reply(chatID, "Ошибка получения цены для "+symbol+": "+err.Error())
			return
		}
		alert.BasePrice = price
		alert, err = b.st.Add(alert)
		if err != nil {
			b.reply(chatID, "Ошибка создания алерта: "+err.Error())
			return
		}
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: `%s`)\n%s изменится на %.2f%% от %s", alert.ID, symbol, value, prices.FormatPrice(price)))

		// Перезапускаем мониторинг с новым символом
		b.restartMonitoring(ctx)
	default:
		b.reply(chatID, "Тип должен быть 'price' или 'pct'")
	}
}

// cmdOpenCall обрабатывает команду /ocall TICKER [long|short]
func (b *TelegramBot) cmdOpenCall(ctx context.Context, chatID int64, userID int64, username string, text string) {
	parts := strings.Fields(text)
	if len(parts) < 2 || len(parts) > 3 {
		b.reply(chatID, "Использование: /ocall TICKER [long|short]\nПример: /ocall BTCUSDT long")
		return
	}

	symbol := strings.ToUpper(parts[1])
	direction := "long" // по умолчанию

	if len(parts) == 3 {
		dir := strings.ToLower(parts[2])
		if dir == "short" || dir == "long" {
			direction = dir
		} else {
			b.reply(chatID, "Направление должно быть 'long' или 'short'")
			return
		}
	}

	// Получаем текущую цену
	currentPrice, err := prices.FetchSpotPrice(nil, symbol)
	if err != nil {
		b.reply(chatID, "Ошибка получения цены для "+symbol+": "+err.Error())
		return
	}

	// Создаем колл
	call := alerts.Call{
		UserID:     userID,
		Username:   username,
		ChatID:     chatID,
		Symbol:     symbol,
		Direction:  direction,
		EntryPrice: currentPrice,
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

	b.reply(chatID, fmt.Sprintf("Колл открыт!\nID: `%s`\nСимвол: %s\nНаправление: %s\nЦена входа: %s",
		call.ID, symbol, directionRus, prices.FormatPrice(currentPrice)))
}

// cmdCloseCall обрабатывает команду /ccall CALLID
func (b *TelegramBot) cmdCloseCall(ctx context.Context, chatID int64, userID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) != 2 {
		b.reply(chatID, "Использование: /ccall CALLID\nПример: /ccall `abc123de`")
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

	// Получаем текущую цену для символа из колла
	currentPrice, err := prices.FetchSpotPrice(nil, call.Symbol)
	if err != nil {
		b.reply(chatID, "Ошибка получения цены для "+call.Symbol+": "+err.Error())
		return
	}

	// Закрываем колл
	err = b.st.CloseCall(callID, userID, currentPrice)
	if err != nil {
		b.reply(chatID, "Ошибка закрытия колла: "+err.Error())
		return
	}

	// Получаем обновленную информацию о закрытом колле
	closedCall, err := b.st.GetCallByID(callID, userID)
	if err == nil && closedCall.Status == "closed" {
		pnlSign := "+"
		if closedCall.PnlPercent < 0 {
			pnlSign = ""
		}

		directionRus := "Long"
		if closedCall.Direction == "short" {
			directionRus = "Short"
		}

		b.reply(chatID, fmt.Sprintf("Колл закрыт!\nID: `%s`\nСимвол: %s\nНаправление: %s\nЦена входа: %s\nЦена выхода: %s\nPnL: %s%.2f%%",
			callID, call.Symbol, directionRus, prices.FormatPrice(closedCall.EntryPrice),
			prices.FormatPrice(currentPrice), pnlSign, closedCall.PnlPercent))
	} else {
		b.reply(chatID, fmt.Sprintf("Колл `%s` закрыт по цене %s", callID, prices.FormatPrice(currentPrice)))
	}
}

// cmdMyCalls показывает активные коллы пользователя
func (b *TelegramBot) cmdMyCalls(ctx context.Context, chatID int64, userID int64) {
	calls := b.st.GetUserCalls(userID, true) // только открытые
	if len(calls) == 0 {
		b.reply(chatID, "У вас нет активных коллов")
		return
	}

	var msg strings.Builder
	msg.WriteString("Ваши активные коллы:\n\n")

	for i, call := range calls {
		// Получаем текущую цену для расчета текущего PnL
		currentPrice, err := prices.FetchSpotPrice(nil, call.Symbol)
		if err != nil {
			logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to get current price for call")
			currentPrice = call.EntryPrice // используем цену входа если не можем получить текущую
		}

		// Вычисляем текущий PnL
		var currentPnl float64
		if call.Direction == "long" {
			currentPnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
		} else {
			currentPnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
		}

		pnlSign := "+"
		if currentPnl < 0 {
			pnlSign = ""
		}

		directionRus := "Long"
		if call.Direction == "short" {
			directionRus = "Short"
		}

		msg.WriteString(fmt.Sprintf("%d. %s (%s) - ID: `%s`\n", i+1, call.Symbol, directionRus, call.ID))
		msg.WriteString(fmt.Sprintf("   Цена входа: %s\n", prices.FormatPrice(call.EntryPrice)))
		msg.WriteString(fmt.Sprintf("   Текущая цена: %s\n", prices.FormatPrice(currentPrice)))
		msg.WriteString(fmt.Sprintf("   Текущий PnL: %s%.2f%%\n\n", pnlSign, currentPnl))
	}

	b.reply(chatID, msg.String())
}

// cmdCallStats показывает статистику коллов всех пользователей за последние 90 дней
func (b *TelegramBot) cmdCallStats(chatID int64) {
	stats := b.st.GetAllUserStats()
	if len(stats) == 0 {
		b.reply(chatID, "Нет данных по коллам за последние 90 дней")
		return
	}

	var msg strings.Builder
	msg.WriteString("📊 *Рейтинг трейдеров за последние 90 дней:*\n\n")

	for i, stat := range stats {
		pnlSign := "+"
		if stat.TotalPnl < 0 {
			pnlSign = ""
		}

		username := stat.Username
		if username == "" {
			username = fmt.Sprintf("User_%d", stat.UserID)
		}

		msg.WriteString(fmt.Sprintf("%d. *%s*\n", i+1, username))
		msg.WriteString(fmt.Sprintf("   💰 PnL: %s%.2f%% | 🎯 Winrate: %.1f%% | 📊 Сделок: %d\n\n",
			pnlSign, stat.TotalPnl, stat.WinRate, stat.ClosedCalls))
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

	if stats.ClosedCalls == 0 {
		b.reply(chatID, "У вас нет закрытых коллов за последние 90 дней")
		return
	}

	var msg strings.Builder
	msg.WriteString("📊 *Ваша статистика коллов за последние 90 дней:*\n\n")

	// Общий PnL
	pnlSign := "+"
	if stats.TotalPnl < 0 {
		pnlSign = ""
	}
	msg.WriteString(fmt.Sprintf("💰 *Совокупный PnL:* %s%.2f%%\n", pnlSign, stats.TotalPnl))

	// Средний PnL
	avgPnlSign := "+"
	if stats.AveragePnl < 0 {
		avgPnlSign = ""
	}
	msg.WriteString(fmt.Sprintf("📈 *Средний PnL:* %s%.2f%%\n", avgPnlSign, stats.AveragePnl))

	// Winrate
	msg.WriteString(fmt.Sprintf("🎯 *Winrate:* %.1f%% (%d/%d)\n",
		stats.WinRate, stats.WinningCalls, stats.ClosedCalls))

	// Общая статистика
	msg.WriteString(fmt.Sprintf("📋 *Всего коллов:* %d\n", stats.TotalCalls))
	msg.WriteString(fmt.Sprintf("✅ *Закрыто коллов:* %d\n", stats.ClosedCalls))
	msg.WriteString(fmt.Sprintf("📊 *Активных коллов:* %d\n\n", stats.TotalCalls-stats.ClosedCalls))

	// Лучший и худший коллы с деталями
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

// CallWithPnL структура для отображения коллов с текущим PnL
type CallWithPnL struct {
	alerts.Call
	CurrentPrice float64
	CurrentPnl   float64
}

// cmdAllCalls показывает все активные коллы всех пользователей
func (b *TelegramBot) cmdAllCalls(ctx context.Context, chatID int64) {
	calls := b.st.GetAllOpenCalls()
	if len(calls) == 0 {
		b.reply(chatID, "Нет активных коллов")
		return
	}

	// Получаем текущие цены и вычисляем PnL для сортировки
	var callsWithPnl []CallWithPnL
	for _, call := range calls {
		currentPrice, err := prices.FetchSpotPrice(nil, call.Symbol)
		if err != nil {
			logrus.WithError(err).WithField("symbol", call.Symbol).Warn("failed to get current price for call")
			currentPrice = call.EntryPrice
		}

		var currentPnl float64
		if call.Direction == "long" {
			currentPnl = ((currentPrice - call.EntryPrice) / call.EntryPrice) * 100
		} else {
			currentPnl = ((call.EntryPrice - currentPrice) / call.EntryPrice) * 100
		}

		callsWithPnl = append(callsWithPnl, CallWithPnL{
			Call:         call,
			CurrentPrice: currentPrice,
			CurrentPnl:   currentPnl,
		})
	}

	// Сортируем по текущему PnL (по убыванию)
	sort.Slice(callsWithPnl, func(i, j int) bool {
		return callsWithPnl[i].CurrentPnl > callsWithPnl[j].CurrentPnl
	})

	var msg strings.Builder
	msg.WriteString("Все активные коллы (отсортированы по PnL):\n\n")

	for i, callPnl := range callsWithPnl {
		call := callPnl.Call

		pnlSign := "+"
		if callPnl.CurrentPnl < 0 {
			pnlSign = ""
		}

		directionRus := "Long"
		if call.Direction == "short" {
			directionRus = "Short"
		}

		username := call.Username
		if username == "" {
			username = fmt.Sprintf("User_%d", call.UserID)
		}

		msg.WriteString(fmt.Sprintf("%d. %s - %s (%s)\n", i+1, username, call.Symbol, directionRus))
		msg.WriteString(fmt.Sprintf("   Цена входа: %s\n", prices.FormatPrice(call.EntryPrice)))
		msg.WriteString(fmt.Sprintf("   Текущий PnL: %s%.2f%%\n\n", pnlSign, callPnl.CurrentPnl))
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
		priceInfo, err := prices.FetchPriceInfo(nil, symbol)
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
		msg += fmt.Sprintf("15м: %s | 1ч: %s | 4ч: %s | 24ч: %s\n\n",
			change15m, change1h, change4h, change24h)
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

	symbol := strings.ToUpper(parts[1])
	priceInfo, err := prices.FetchPriceInfo(nil, symbol)
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
				msg = fmt.Sprintf("АЛЕРТ! %s достиг %s (текущая: %s)", symbol, prices.FormatPrice(alert.TargetPrice), prices.FormatPrice(currentPrice))
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
	oldPrice, err := b.fetchHistoricalPrice(symbol, intervalAgo)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"symbol":   symbol,
			"interval": fmt.Sprintf("%dm", b.cfg.SharpChangeIntervalMin),
		}).Debug("failed to get historical price for sharp change check")
		return
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
		lastAlertTime, exists := b.lastSharpChangeTime[symbol]
		now := time.Now()

		// Отправляем алерт не чаще чем раз в 5 минут для одного символа
		if !exists || now.Sub(lastAlertTime) >= 5*time.Minute {
			b.lastSharpChangeTime[symbol] = now
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
				msg := fmt.Sprintf("РЕЗКОЕ ИЗМЕНЕНИЕ! %s %s на %.2f%% за %dм (от %s до %s)",
					symbol, direction, absChangePct, b.cfg.SharpChangeIntervalMin,
					prices.FormatPrice(oldPrice), prices.FormatPrice(currentPrice))

				for chatID, alert := range alertedUsers {
					b.reply(chatID, msg)
					// Логируем резкое изменение
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
				"last_alert_time_ago": now.Sub(lastAlertTime).String(),
			}).Debug("sharp change detected but alert suppressed due to recent notification")
		}
	}
}

// fetchHistoricalPrice получает историческую цену для указанного времени
func (b *TelegramBot) fetchHistoricalPrice(symbol string, timestamp time.Time) (float64, error) {
	return prices.FetchHistoricalPrice(nil, symbol, timestamp)
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
func (b *TelegramBot) cmdStats(chatID int64) {
	stats := b.st.GetSymbolStats()
	if len(stats) == 0 {
		b.reply(chatID, "Нет данных для статистики")
		return
	}

	var msg strings.Builder
	msg.WriteString("Статистика активных алертов по символам:\n\n")

	// Сортируем по количеству алертов
	type symbolStat struct {
		symbol string
		count  int
	}

	var sortedStats []symbolStat
	for symbol, count := range stats {
		sortedStats = append(sortedStats, symbolStat{symbol, count})
	}

	// Простая сортировка по убыванию
	for i := 0; i < len(sortedStats)-1; i++ {
		for j := i + 1; j < len(sortedStats); j++ {
			if sortedStats[j].count > sortedStats[i].count {
				sortedStats[i], sortedStats[j] = sortedStats[j], sortedStats[i]
			}
		}
	}

	for i, stat := range sortedStats {
		msg.WriteString(fmt.Sprintf("%d. %s: %d алертов\n", i+1, stat.symbol, stat.count))
	}

	totalAlerts := 0
	for _, count := range stats {
		totalAlerts += count
	}

	msg.WriteString(fmt.Sprintf("\nВсего активных алертов: %d\n", totalAlerts))
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
		mon := prices.NewPriceMonitorWithProvider(b.st, 0, 60)
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
