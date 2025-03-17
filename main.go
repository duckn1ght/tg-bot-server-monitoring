package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

type Config struct {
	botToken      string
	serverURL     string
	checkInterval time.Duration
}

type Bot struct {
	config      *Config
	api         *tgbotapi.BotAPI
	activeChats sync.Map
}

const activeChatsFile = "active_chats.json"

// saveActiveChats сохраняет активные чаты в JSON файл
func (b *Bot) saveActiveChats() error {
	chats := make([]int64, 0)
	b.activeChats.Range(func(key, value interface{}) bool {
		chats = append(chats, key.(int64))
		return true
	})

	data, err := json.Marshal(chats)
	if err != nil {
		return fmt.Errorf("ошибка маршалинга чатов: %w", err)
	}

	if err := os.WriteFile(activeChatsFile, data, 0644); err != nil {
		return fmt.Errorf("ошибка сохранения чатов: %w", err)
	}

	return nil
}

// loadActiveChats загружает активные чаты из JSON файла
func (b *Bot) loadActiveChats() error {
	data, err := os.ReadFile(activeChatsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // файл не существует, это нормально при первом запуске
		}
		return fmt.Errorf("ошибка чтения файла чатов: %w", err)
	}

	var chats []int64
	if err := json.Unmarshal(data, &chats); err != nil {
		return fmt.Errorf("ошибка анмаршалинга чатов: %w", err)
	}

	for _, chatID := range chats {
		b.activeChats.Store(chatID, true)
		log.Printf("Загружен активный чат: %d", chatID)
	}

	return nil
}

func loadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("ошибка загрузки .env файла: %w", err)
	}

	interval, err := strconv.Atoi(os.Getenv("CHECK_INTERVAL"))
	if err != nil {
		interval = 60000 // значение по умолчанию: 1 минута
	}

	return &Config{
		botToken:      os.Getenv("TELEGRAM_BOT_TOKEN"),
		serverURL:     os.Getenv("SERVER_URL"),
		checkInterval: time.Duration(interval) * time.Millisecond,
	}, nil
}

func (b *Bot) handleUpdates() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID

		switch update.Message.Command() {
		case "start":
			b.activeChats.Store(chatID, true)
			msg := tgbotapi.NewMessage(chatID, "👋 Привет! Я буду отправлять уведомления о состоянии сервера в этот чат.")
			b.api.Send(msg)
			log.Printf("Новый чат активирован: %d", chatID)
			if err := b.saveActiveChats(); err != nil {
				log.Printf("Ошибка сохранения активных чатов: %v", err)
			}

		case "stop":
			b.activeChats.Delete(chatID)
			msg := tgbotapi.NewMessage(chatID, "Уведомления отключены. Чтобы включить их снова, используйте /start")
			b.api.Send(msg)
			log.Printf("Чат деактивирован: %d", chatID)
			if err := b.saveActiveChats(); err != nil {
				log.Printf("Ошибка сохранения активных чатов: %v", err)
			}

		case "status":
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Мониторинг сервера %s\nИнтервал проверки: %v",
				b.config.serverURL, b.config.checkInterval))
			b.api.Send(msg)
		}
	}
}

func (b *Bot) checkServer(client *http.Client) {
	resp, err := client.Get(b.config.serverURL)

	var errorMsg string
	if err != nil {
		errorMsg = fmt.Sprintf("🚨 Ошибка сервера!\n\nURL: %s\nСообщение: %s\nВремя: %s",
			b.config.serverURL, err.Error(), time.Now().Format(time.RFC3339))
		log.Printf("Проверка сервера не удалась: %v", err)
	} else if resp.StatusCode >= 400 {
		errorMsg = fmt.Sprintf("🚨 Ошибка сервера!\n\nURL: %s\nСтатус: %d\nВремя: %s",
			b.config.serverURL, resp.StatusCode, time.Now().Format(time.RFC3339))
		log.Printf("Проверка сервера не удалась: статус %d", resp.StatusCode)
	} else {
		log.Printf("Проверка сервера: OK")
		return
	}

	// Отправляем уведомления во все активные чаты
	b.activeChats.Range(func(key, value interface{}) bool {
		chatID := key.(int64)
		msg := tgbotapi.NewMessage(chatID, errorMsg)
		if _, err := b.api.Send(msg); err != nil {
			log.Printf("Не удалось отправить уведомление в чат %d: %v", chatID, err)
		} else {
			log.Printf("Уведомление отправлено в чат %d", chatID)
		}
		return true
	})
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Ошибка загрузки конфигурации: %v", err)
	}

	api, err := tgbotapi.NewBotAPI(config.botToken)
	if err != nil {
		log.Fatalf("Ошибка инициализации бота: %v", err)
	}

	bot := &Bot{
		config: config,
		api:    api,
	}

	// Загружаем сохраненные активные чаты
	if err := bot.loadActiveChats(); err != nil {
		log.Printf("Ошибка загрузки активных чатов: %v", err)
	}

	// Запускаем обработку команд в отдельной горутине
	go bot.handleUpdates()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	log.Printf("Бот запущен. Мониторинг сервера %s", config.serverURL)
	log.Printf("Интервал проверки: %v", config.checkInterval)

	// Регулярные проверки
	ticker := time.NewTicker(config.checkInterval)
	defer ticker.Stop()

	for range ticker.C {
		bot.checkServer(client)
	}
}
