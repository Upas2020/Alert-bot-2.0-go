package bot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

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
	st         *alerts.Storage
	monitorCtx context.Context
	stopMon    context.CancelFunc
}

// NewTelegramBot создает экземпляр бота.
func NewTelegramBot(cfg config.Config) (*TelegramBot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}
	api.Debug = false
	logrus.WithField("username", api.Self.UserName).Info("telegram bot authorized")

	st, err := alerts.NewStorage("data")
	if err != nil {
		return nil, fmt.Errorf("alerts storage init: %w", err)
	}
	return &TelegramBot{api: api, cfg: cfg, st: st}, nil
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
	text := upd.Message.Text

	switch {
	case text == "/chatid":
		b.reply(chatID, fmt.Sprintf("Chat ID: %d", chatID))
	case strings.HasPrefix(text, "/addalert"):
		b.cmdAddAlert(ctx, chatID, text)
	case text == "/alerts":
		b.cmdListAlerts(chatID)
	case strings.HasPrefix(text, "/delalert"):
		b.cmdDelAlert(chatID, text)
	case text == "/delallalerts":
		b.cmdDelAllAlerts(chatID)
	case text == "/priceall":
		b.cmdPriceAll(ctx, chatID)
	case strings.HasPrefix(text, "/price"):
		b.cmdPrice(ctx, chatID, text)
	case text == "/start":
		b.reply(chatID, "Ссылка на г")
	default:
		if text != "" {
			b.reply(chatID, "Эхо: "+text)
		}
	}
}

func (b *TelegramBot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		logrus.WithError(err).Warn("send message failed")
	} else {
		logrus.WithFields(logrus.Fields{"chat_id": chatID}).Debug("message sent")
	}
}

// cmdAddAlert обрабатывает команду /addalert TICKER price|pct VALUE
func (b *TelegramBot) cmdAddAlert(ctx context.Context, chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) != 4 {
		b.reply(chatID, "Использование: /addalert TICKER price|pct VALUE\nПример: /addalert BTCUSDT price 50000")
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
		ChatID: chatID,
		Symbol: symbol,
	}

	switch alertType {
	case "price":
		alert.TargetPrice = value
		alert, err = b.st.Add(alert)
		if err != nil {
			b.reply(chatID, "Ошибка создания алерта: "+err.Error())
			return
		}
		// Используем форматированную цену
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: %s)\n%s достигнет %s", alert.ID, symbol, prices.FormatPrice(value)))

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
		// Используем форматированную цену
		b.reply(chatID, fmt.Sprintf("Алерт создан (ID: %s)\n%s изменится на %.2f%% от %s", alert.ID, symbol, value, prices.FormatPrice(price)))

		// Перезапускаем мониторинг с новым символом
		b.restartMonitoring(ctx)
	default:
		b.reply(chatID, "Тип должен быть 'price' или 'pct'")
	}
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
				msg.WriteString(fmt.Sprintf("%d. Цель %s, ID: %s\n",
					i+1, prices.FormatPrice(alert.TargetPrice), alert.ID))
			} else if alert.TargetPercent != 0 {
				msg.WriteString(fmt.Sprintf("%d. Изменение на %.2f%% от %s, ID: %s\n",
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
		b.reply(chatID, "Использование: /delalert ID")
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

// cmdPriceAll показывает цены всех символов с алертами пользователя
func (b *TelegramBot) cmdPriceAll(ctx context.Context, chatID int64) {
	userAlerts := b.st.ListByChat(chatID)
	if len(userAlerts) == 0 {
		b.reply(chatID, "У вас нет активных алертов")
		return
	}

	// Собираем уникальные символы пользователя
	symbolsMap := make(map[string]struct{})
	for _, alert := range userAlerts {
		symbolsMap[alert.Symbol] = struct{}{}
	}

	msg := "Цены ваших токенов:\n\n"

	for symbol := range symbolsMap {
		priceInfo, err := prices.FetchPriceInfo(nil, symbol)
		if err != nil {
			msg += fmt.Sprintf("%s: ошибка получения цены\n", symbol)
			logrus.WithError(err).WithField("symbol", symbol).Warn("failed to fetch price info")
			continue
		}

		// Форматируем изменения с эмодзи для визуальной индикации
		change15m := formatChange(priceInfo.Change15m)
		change1h := formatChange(priceInfo.Change1h)
		change4h := formatChange(priceInfo.Change4h)
		change24h := formatChange(priceInfo.Change24h)

		// Используем форматированную цену
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

// formatChange форматирует процентное изменение без эмодзи
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
				// Используем форматированные цены
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
				// Используем форматированные цены
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
		// Используем улучшенный мониторинг с провайдером символов
		mon := prices.NewPriceMonitorWithProvider(b.st, 0, 60) // используем storage как провайдер символов, проверяем каждые 60 секунд
		monCtx, cancel := context.WithCancel(ctx)
		b.monitorCtx = monCtx
		b.stopMon = cancel
		go func() {
			_ = mon.Run(monCtx, func(symbol string, oldPrice, newPrice, deltaPct float64) {
				// Проверяем, есть ли еще алерты для этого символа
				alertsForSymbol := b.st.GetBySymbol(symbol)
				if len(alertsForSymbol) > 0 {
					b.checkAlerts(symbol, newPrice)
				} else {
					logrus.WithField("symbol", symbol).Debug("no alerts for symbol, skipping check")
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
