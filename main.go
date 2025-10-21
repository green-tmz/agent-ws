package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	_ "strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config структура для конфигурации
type Config struct {
	WatchPath   string `json:"watch_path"`
	LogFile     string `json:"log_file"`
	MaxFileSize int64  `json:"max_file_size"`
}

// LogEntry структура для записи лога
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Event     string    `json:"event"`
	Filename  string    `json:"filename"`
	Content   string    `json:"content,omitempty"`
	Error     string    `json:"error,omitempty"`
}

var (
	config  Config
	logger  *log.Logger
	logFile *os.File
)

func main() {
	// Инициализация конфигурации
	initConfig()

	// Инициализация логгера
	if err := initLogger(); err != nil {
		log.Fatal("Ошибка инициализации логгера:", err)
	}
	defer logFile.Close()

	logger.Println("=== Запуск мониторинга папки ===")
	logger.Printf("Мониторинг папки: %s\n", config.WatchPath)
	logger.Printf("Лог файл: %s\n", config.LogFile)

	// Создаем watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Fatal("Ошибка создания watcher:", err)
	}
	defer watcher.Close()

	// Добавляем папку для мониторинга
	err = watcher.Add(config.WatchPath)
	if err != nil {
		logger.Fatal("Ошибка добавления папки:", err)
	}

	// Также мониторим вложенные папки
	err = filepath.Walk(config.WatchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = watcher.Add(path)
			if err != nil {
				logger.Printf("Ошибка добавления подпапки %s: %v\n", path, err)
			}
		}
		return nil
	})
	if err != nil {
		logger.Printf("Ошибка обхода папок: %v\n", err)
	}

	logger.Println("Мониторинг запущен. Ожидание событий...")

	// Обработка событий
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// Обрабатываем только файлы (игнорируем папки)
			if isDirectory(event.Name) {
				// Если создана новая папка, добавляем её в мониторинг
				if event.Op&fsnotify.Create == fsnotify.Create {
					watcher.Add(event.Name)
				}
				continue
			}

			// Обрабатываем события файлов
			handleFileEvent(event)

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Println("Ошибка watcher:", err)
		}
	}
}

func initConfig() {
	config = Config{
		WatchPath:   `C:\EVRIMA\surv_server\TheIsle\Saved\Databases\Survival\Players`,
		LogFile:     `C:\EVRIMA\file_monitor.log`,
		MaxFileSize: 10 * 1024 * 1024, // 10MB максимальный размер файла для чтения
	}

	// Создаем папку для мониторинга если её нет
	os.MkdirAll(config.WatchPath, 0755)
}

func initLogger() error {
	var err error
	logFile, err = os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	logger = log.New(logFile, "", log.LstdFlags)
	return nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func handleFileEvent(event fsnotify.Event) {
	filename := event.Name
	var logEntry LogEntry

	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		logEntry = LogEntry{
			Timestamp: time.Now(),
			Event:     "CREATE",
			Filename:  filename,
		}
		content, err := readFileContent(filename)
		if err == nil {
			logEntry.Content = content
		} else {
			logEntry.Error = err.Error()
		}

	case event.Op&fsnotify.Write == fsnotify.Write:
		logEntry = LogEntry{
			Timestamp: time.Now(),
			Event:     "MODIFY",
			Filename:  filename,
		}
		content, err := readFileContent(filename)
		if err == nil {
			logEntry.Content = content
		} else {
			logEntry.Error = err.Error()
		}

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		logEntry = LogEntry{
			Timestamp: time.Now(),
			Event:     "DELETE",
			Filename:  filename,
		}

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		logEntry = LogEntry{
			Timestamp: time.Now(),
			Event:     "RENAME",
			Filename:  filename,
		}
	}

	// Записываем в лог
	writeLogEntry(logEntry)
}

func readFileContent(filename string) (string, error) {
	// Проверяем размер файла
	info, err := os.Stat(filename)
	if err != nil {
		return "", err
	}

	if info.Size() > config.MaxFileSize {
		return "", fmt.Errorf("файл слишком большой: %d байт", info.Size())
	}

	// Читаем содержимое файла
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}

	// Если файл пустой, возвращаем пустую строку
	if len(content) == 0 {
		return "[EMPTY FILE]", nil
	}

	// Проверяем, является ли содержимое JSON
	if isJSON(content) {
		return formatJSON(content), nil
	}

	// Для бинарных файлов возвращаем информацию о размере
	if isBinary(content) {
		return fmt.Sprintf("[BINARY DATA - %d bytes]", len(content)), nil
	}

	return string(content), nil
}

func isJSON(content []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(content, &js) == nil
}

func formatJSON(content []byte) string {
	var obj interface{}
	if err := json.Unmarshal(content, &obj); err != nil {
		return string(content)
	}

	formatted, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return string(content)
	}

	return string(formatted)
}

func isBinary(content []byte) bool {
	if len(content) == 0 {
		return false
	}

	// Проверяем первые несколько байт на наличие бинарных данных
	for i := 0; i < len(content) && i < 1024; i++ {
		if content[i] == 0 {
			return true
		}
	}

	// Проверяем процент печатных символов
	printable := 0
	for i := 0; i < len(content) && i < 1024; i++ {
		if content[i] >= 32 && content[i] <= 126 || content[i] == '\n' || content[i] == '\r' || content[i] == '\t' {
			printable++
		}
	}

	ratio := float64(printable) / float64(min(len(content), 1024))
	return ratio < 0.8
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeLogEntry(entry LogEntry) {
	jsonData, err := json.Marshal(entry)
	if err != nil {
		logger.Printf("Ошибка маршалинга лога: %v\n", err)
		return
	}

	logger.Println(string(jsonData))

	// Также выводим в консоль для отладки
	fmt.Printf("%s [%s] %s\n",
		entry.Timestamp.Format("2006-01-02 15:04:05"),
		entry.Event,
		filepath.Base(entry.Filename))
}
