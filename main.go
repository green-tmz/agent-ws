package main

import (
	"bytes"
	"encoding/json"
	_ "fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	watchPath     = `C:\EVRIMA\surv_server\TheIsle\Saved\Databases\Survival\Players`
	apiURL        = "https://admin.twod.club/api/get-event"
	checkInterval = 2 * time.Second // Интервал проверки изменений
)

type EventData struct {
	SteamID64 string `json:"steamid64"`
	Type      string `json:"type"`
	Event     string `json:"event"`
	Data      string `json:"data"`
}

func main() {
	log.Println("Starting file watcher for:", watchPath)

	// Проверяем существование папки
	if _, err := os.Stat(watchPath); os.IsNotExist(err) {
		log.Fatalf("Directory does not exist: %s", watchPath)
	}

	// Создаем watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("Error creating watcher:", err)
	}
	defer watcher.Close()

	// Добавляем папку для отслеживания
	err = watcher.Add(watchPath)
	if err != nil {
		log.Fatal("Error adding watch path:", err)
	}

	log.Println("Watching directory:", watchPath)

	// Карта для отслеживания предыдущего состояния файлов
	fileStates := make(map[string]time.Time)

	// Инициализация - сканируем существующие файлы
	initFileStates(fileStates)

	// Основной цикл обработки событий
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			handleFileEvent(event, fileStates)

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Watcher error:", err)

		case <-time.After(checkInterval):
			// Периодическая проверка на удаленные файлы
			checkForDeletedFiles(fileStates)
		}
	}
}

func initFileStates(fileStates map[string]time.Time) {
	files, err := os.ReadDir(watchPath)
	if err != nil {
		log.Printf("Error reading directory: %v", err)
		return
	}

	for _, file := range files {
		if !file.IsDir() {
			fullPath := filepath.Join(watchPath, file.Name())
			if info, err := os.Stat(fullPath); err == nil {
				fileStates[fullPath] = info.ModTime()
			}
		}
	}
	log.Printf("Initialized tracking for %d files", len(fileStates))
}

func handleFileEvent(event fsnotify.Event, fileStates map[string]time.Time) {
	filename := event.Name

	// Игнорируем директории
	if info, err := os.Stat(filename); err == nil && info.IsDir() {
		return
	}

	// Получаем steamid из имени файла
	steamID := getSteamIDFromFilename(filename)
	if steamID == "" {
		return
	}

	log.Printf("Event: %s, File: %s", event.Op.String(), filepath.Base(filename))

	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		// Небольшая задержка для гарантии что файл полностью записан
		time.Sleep(100 * time.Millisecond)
		handleFileCreate(filename, steamID, fileStates)

	case event.Op&fsnotify.Write == fsnotify.Write:
		handleFileWrite(filename, steamID, fileStates)

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		handleFileRemove(filename, steamID, fileStates)
	}
}

func handleFileCreate(filename, steamID string, fileStates map[string]time.Time) {
	content, err := readFileContent(filename)
	if err != nil {
		log.Printf("Error reading created file %s: %v", filename, err)
		return
	}

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "add-dino-data",
		Data:      content,
	}

	sendEvent(eventData)
	fileStates[filename] = time.Now()
}

func handleFileWrite(filename, steamID string, fileStates map[string]time.Time) {
	// Проверяем, действительно ли файл изменился
	if info, err := os.Stat(filename); err == nil {
		if oldTime, exists := fileStates[filename]; exists {
			if info.ModTime().Equal(oldTime) {
				return // Файл не изменился
			}
		}
	} else {
		log.Printf("Error stating file %s: %v", filename, err)
		return
	}

	content, err := readFileContent(filename)
	if err != nil {
		log.Printf("Error reading modified file %s: %v", filename, err)
		return
	}

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "change-dino-data",
		Data:      content,
	}

	sendEvent(eventData)

	// Обновляем время модификации
	if info, err := os.Stat(filename); err == nil {
		fileStates[filename] = info.ModTime()
	}
}

func handleFileRemove(filename, steamID string, fileStates map[string]time.Time) {
	// Для удаленных файлов мы не можем прочитать содержимое,
	// но можем использовать кэшированное содержимое если оно есть
	var content string
	if cachedContent, exists := getCachedContent(filename); exists {
		content = cachedContent
	}

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "delete-dino-data",
		Data:      content,
	}

	sendEvent(eventData)
	delete(fileStates, filename)
}

func checkForDeletedFiles(fileStates map[string]time.Time) {
	for filename := range fileStates {
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			// Файл был удален вне событий watcher
			steamID := getSteamIDFromFilename(filename)
			if steamID != "" {
				log.Printf("Detected deleted file: %s", filepath.Base(filename))
				handleFileRemove(filename, steamID, fileStates)
			}
		}
	}
}

func getSteamIDFromFilename(filename string) string {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	return base[:len(base)-len(ext)]
}

func readFileContent(filename string) (string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// Простая кэш-функция для хранения содержимого файлов
// В реальном приложении можно использовать более сложный кэш
func getCachedContent(filename string) (string, bool) {
	// Здесь можно реализовать кэширование содержимого файлов
	// Для простоты возвращаем пустую строку
	return "", false
}

func sendEvent(eventData EventData) {
	jsonData, err := json.Marshal(eventData)
	if err != nil {
		log.Printf("Error marshaling JSON: %v", err)
		return
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "FileWatcher/1.0")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending request: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("Successfully sent event %s for SteamID %s", eventData.Event, eventData.SteamID64)
	} else {
		log.Printf("Error response from server: %d - %s", resp.StatusCode, string(body))
	}
}
