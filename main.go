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
	frontendURL   string
	backendURL    string
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
		frontendURL:   os.Getenv("FRONTEND_URL"),
		backendURL:    os.Getenv("BACKEND_URL"),
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
			msg := tgbotapi.NewMessage(chatID, "👋 Привет! Я буду отправлять уведомления о состоянии серверов в этот чат.")
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
			statusMsg := "📊 Статус мониторинга:\n\n"
			if b.config.frontendURL != "" {
				statusMsg += fmt.Sprintf("Frontend: %s\n", b.config.frontendURL)
			}
			if b.config.backendURL != "" {
				statusMsg += fmt.Sprintf("Backend: %s\n", b.config.backendURL)
			}
			statusMsg += fmt.Sprintf("\nИнтервал проверки: %v", b.config.checkInterval)
			
			msg := tgbotapi.NewMessage(chatID, statusMsg)
			b.api.Send(msg)
		}
	}
}

func (b *Bot) checkServer(client *http.Client) {
	// Проверяем frontend
	frontendErr := b.checkURL(client, b.config.frontendURL, "Frontend")
	// Проверяем backend
	backendErr := b.checkURL(client, b.config.backendURL, "Backend")

	// Если есть ошибки, отправляем уведомления
	if frontendErr != nil || backendErr != nil {
		errorMsg := "🚨 Обнаружены проблемы!\n\n"
		if frontendErr != nil {
			errorMsg += fmt.Sprintf("Frontend (%s):\n%s\n\n", b.config.frontendURL, frontendErr.Error())
		}
		if backendErr != nil {
			errorMsg += fmt.Sprintf("Backend (%s):\n%s\n\n", b.config.backendURL, backendErr.Error())
		}
		errorMsg += fmt.Sprintf("Время: %s", time.Now().Format(time.RFC3339))

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
}

func (b *Bot) checkURL(client *http.Client, url string, serviceName string) error {
	if url == "" {
		return nil // Пропускаем проверку, если URL не задан
	}

	resp, err := client.Get(url)
	if err != nil {
		log.Printf("Проверка %s не удалась: %v", serviceName, err)
		return fmt.Errorf("Ошибка соединения: %s", err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("Проверка %s не удалась: статус %d", serviceName, resp.StatusCode)
		return fmt.Errorf("Статус: %d", resp.StatusCode)
	}

	log.Printf("Проверка %s: OK", serviceName)
	return nil
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

	log.Printf("Бот запущен. Мониторинг серверов:")
	if config.frontendURL != "" {
		log.Printf("Frontend: %s", config.frontendURL)
	}
	if config.backendURL != "" {
		log.Printf("Backend: %s", config.backendURL)
	}
	log.Printf("Интервал проверки: %v", config.checkInterval)

	// Регулярные проверки
	ticker := time.NewTicker(config.checkInterval)
	defer ticker.Stop()

	for range ticker.C {
		bot.checkServer(client)
	}
}
