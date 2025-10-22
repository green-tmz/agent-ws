package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	watchPath     = `C:\EVRIMA\surv_server\TheIsle\Saved\Databases\Survival\Players`
	apiURL        = "https://admin.twod.club/api/get-event"
	checkInterval = 2 * time.Second
	logFile       = `C:\EVRIMA\file_watcher.log`
	maxRetries    = 3
	retryDelay    = 2 * time.Second
)

type EventData struct {
	SteamID64 string `json:"steamid64"`
	Type      string `json:"type"`
	Event     string `json:"event"`
	Data      string `json:"data"`
}

type ApiResponse struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"response_body"`
	Timestamp  string `json:"timestamp"`
	EventType  string `json:"event_type"`
	SteamID    string `json:"steam_id"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
	IsHTML     bool   `json:"is_html"`
}

var (
	fileLogger    *log.Logger
	logFileHandle *os.File
	httpClient    *http.Client
	fileCache     map[string]string // Кэш для хранения содержимого файлов
)

func main() {
	// Инициализация кэша
	fileCache = make(map[string]string)

	// Инициализация логгера
	if err := initLogger(); err != nil {
		log.Fatal("Error initializing logger:", err)
	}
	defer logFileHandle.Close()

	// Инициализация HTTP клиента
	initHTTPClient()

	fileLogger.Println("=== Starting file watcher ===")
	fileLogger.Printf("Watch path: %s", watchPath)
	fileLogger.Printf("API URL: %s", apiURL)

	log.Println("Starting file watcher for:", watchPath)

	// Проверяем существование папки
	if _, err := os.Stat(watchPath); os.IsNotExist(err) {
		fileLogger.Fatalf("Directory does not exist: %s", watchPath)
	}

	// Создаем watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fileLogger.Fatal("Error creating watcher:", err)
	}
	defer watcher.Close()

	// Добавляем папку для отслеживания
	err = watcher.Add(watchPath)
	if err != nil {
		fileLogger.Fatal("Error adding watch path:", err)
	}

	fileLogger.Println("Watching directory:", watchPath)
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
			fileLogger.Println("Watcher error:", err)
			log.Println("Watcher error:", err)

		case <-time.After(checkInterval):
			// Периодическая проверка на удаленные файлы
			checkForDeletedFiles(fileStates)
		}
	}
}

func initLogger() error {
	var err error
	logFileHandle, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return err
	}

	// Настраиваем логгер для записи в файл
	fileLogger = log.New(logFileHandle, "", log.LstdFlags|log.Lmicroseconds)
	return nil
}

func initHTTPClient() {
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func initFileStates(fileStates map[string]time.Time) {
	files, err := os.ReadDir(watchPath)
	if err != nil {
		fileLogger.Printf("Error reading directory: %v", err)
		return
	}

	for _, file := range files {
		if !file.IsDir() {
			fullPath := filepath.Join(watchPath, file.Name())
			if info, err := os.Stat(fullPath); err == nil {
				fileStates[fullPath] = info.ModTime()
				// Кэшируем содержимое существующих файлов
				content, err := readFileContent(fullPath)
				if err == nil {
					fileCache[fullPath] = content
					fileLogger.Printf("Cached content for file: %s, Content: %s", filepath.Base(fullPath), truncateBody(content))
				} else {
					fileLogger.Printf("Error caching file %s: %v", filepath.Base(fullPath), err)
				}
			}
		}
	}
	fileLogger.Printf("Initialized tracking for %d files", len(fileStates))
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

	fileLogger.Printf("File event: %s, File: %s, SteamID: %s", event.Op.String(), filepath.Base(filename), steamID)
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
		fileLogger.Printf("Error reading created file %s: %v", filename, err)
		return
	}

	// Кэшируем содержимое
	fileCache[filename] = content

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "add-dino-data",
		Data:      ensureValidData(content),
	}

	fileLogger.Printf("Sending create event for SteamID %s, Data: %s", steamID, truncateBody(eventData.Data))
	sendEventWithRetry(eventData)
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
		fileLogger.Printf("Error stating file %s: %v", filename, err)
		return
	}

	content, err := readFileContent(filename)
	if err != nil {
		fileLogger.Printf("Error reading modified file %s: %v", filename, err)
		return
	}

	// Обновляем кэш
	fileCache[filename] = content

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "change-dino-data",
		Data:      ensureValidData(content),
	}

	fileLogger.Printf("Sending change event for SteamID %s, Data: %s", steamID, truncateBody(eventData.Data))
	sendEventWithRetry(eventData)

	// Обновляем время модификации
	if info, err := os.Stat(filename); err == nil {
		fileStates[filename] = info.ModTime()
	}
}

func handleFileRemove(filename, steamID string, fileStates map[string]time.Time) {
	// Для удаленных файлов используем кэшированное содержимое
	content := getCachedContent(filename)

	eventData := EventData{
		SteamID64: steamID,
		Type:      "player",
		Event:     "delete-dino-data",
		Data:      ensureValidData(content),
	}

	fileLogger.Printf("Sending delete event for SteamID %s, Data: %s", steamID, truncateBody(eventData.Data))
	sendEventWithRetry(eventData)

	// Удаляем из кэша и состояний
	delete(fileCache, filename)
	delete(fileStates, filename)
}

func checkForDeletedFiles(fileStates map[string]time.Time) {
	for filename := range fileStates {
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			// Файл был удален вне событий watcher
			steamID := getSteamIDFromFilename(filename)
			if steamID != "" {
				fileLogger.Printf("Detected deleted file: %s", filepath.Base(filename))
				handleFileRemove(filename, steamID, fileStates)
			}
		}
	}
}

func getSteamIDFromFilename(filename string) string {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	if len(ext) >= len(base) {
		return base
	}
	return base[:len(base)-len(ext)]
}

func readFileContent(filename string) (string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// Получаем кэшированное содержимое файла
func getCachedContent(filename string) string {
	if content, exists := fileCache[filename]; exists {
		return content
	}
	return "" // Возвращаем пустую строку вместо null
}

// Гарантируем, что данные всегда будут валидными (не null)
func ensureValidData(data string) string {
	// Если данные пустые, возвращаем пустой JSON объект
	if strings.TrimSpace(data) == "" {
		return "{}"
	}

	// Пробуем распарсить как JSON чтобы проверить валидность
	var js json.RawMessage
	if err := json.Unmarshal([]byte(data), &js); err != nil {
		// Если не валидный JSON, логируем ошибку и возвращаем как JSON строку
		fileLogger.Printf("Data is not valid JSON, wrapping as string. Error: %v, Data: %s", err, truncateBody(data))
		// Экранируем и возвращаем как JSON строку
		escapedData := strings.ReplaceAll(data, `"`, `\"`)
		return `"` + escapedData + `"`
	}

	// Если валидный JSON, возвращаем как есть
	return data
}

func sendEventWithRetry(eventData EventData) {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		apiResponse := sendEvent(eventData)

		if apiResponse.Success {
			return // Успешно отправлено
		}

		// Если получили HTML вместо JSON, прерываем попытки
		if apiResponse.IsHTML {
			fileLogger.Printf("API returned HTML page (likely authentication required), stopping retries for SteamID %s", eventData.SteamID64)
			return
		}

		if attempt < maxRetries {
			fileLogger.Printf("Attempt %d failed for SteamID %s, retrying in %v...", attempt, eventData.SteamID64, retryDelay)
			time.Sleep(retryDelay)
		}
	}

	fileLogger.Printf("All %d attempts failed for SteamID %s", maxRetries, eventData.SteamID64)
}

func sendEvent(eventData EventData) ApiResponse {
	jsonData, err := json.Marshal(eventData)
	if err != nil {
		fileLogger.Printf("Error marshaling JSON: %v", err)
		return ApiResponse{
			Timestamp: time.Now().Format(time.RFC3339),
			EventType: eventData.Event,
			SteamID:   eventData.SteamID64,
			Success:   false,
			Error:     fmt.Sprintf("JSON marshal error: %v", err),
		}
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		fileLogger.Printf("Error creating request: %v", err)
		return ApiResponse{
			Timestamp: time.Now().Format(time.RFC3339),
			EventType: eventData.Event,
			SteamID:   eventData.SteamID64,
			Success:   false,
			Error:     fmt.Sprintf("Request creation error: %v", err),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "FileWatcher/1.0")
	// Добавляем заголовки для предотвращения кэширования
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	startTime := time.Now()
	resp, err := httpClient.Do(req)
	responseTime := time.Since(startTime)

	apiResponse := ApiResponse{
		Timestamp: time.Now().Format(time.RFC3339),
		EventType: eventData.Event,
		SteamID:   eventData.SteamID64,
	}

	if err != nil {
		apiResponse.Success = false
		apiResponse.Error = err.Error()
		logApiResponse(apiResponse, responseTime)
		return apiResponse
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Проверяем, является ли ответ HTML
	isHTML := strings.Contains(bodyStr, "<!DOCTYPE html>") ||
		strings.Contains(bodyStr, "<html") ||
		strings.Contains(bodyStr, "Steam") ||
		strings.Contains(bodyStr, "Sign In")

	apiResponse.StatusCode = resp.StatusCode
	apiResponse.Body = truncateBody(bodyStr)
	apiResponse.Success = resp.StatusCode >= 200 && resp.StatusCode < 300 && !isHTML
	apiResponse.IsHTML = isHTML

	if isHTML {
		apiResponse.Error = "Server returned HTML page instead of JSON (likely authentication required or wrong endpoint)"
	}

	// Логируем результат отправки
	logApiResponse(apiResponse, responseTime)

	if apiResponse.Success {
		fileLogger.Printf("Successfully sent event %s for SteamID %s (Response time: %v, Status: %d)",
			eventData.Event, eventData.SteamID64, responseTime, resp.StatusCode)
		log.Printf("Successfully sent event %s for SteamID %s", eventData.Event, eventData.SteamID64)
	} else {
		if apiResponse.IsHTML {
			fileLogger.Printf("API returned HTML page for SteamID %s: %d - Response contains Steam login page (Response time: %v)",
				eventData.SteamID64, resp.StatusCode, responseTime)
			log.Printf("API returned Steam login page for SteamID %s - check API endpoint and authentication", eventData.SteamID64)
		} else {
			fileLogger.Printf("Error response from server for SteamID %s: %d - %s (Response time: %v)",
				eventData.SteamID64, resp.StatusCode, truncateBody(bodyStr), responseTime)
			log.Printf("Error response from server: %d - %s", resp.StatusCode, truncateBody(bodyStr))
		}
	}

	return apiResponse
}

func truncateBody(body string) string {
	if len(body) > 500 {
		return body[:500] + "... [truncated]"
	}
	return body
}

func logApiResponse(response ApiResponse, responseTime time.Duration) {
	// Форматируем ответ для лога
	status := "SUCCESS"
	if !response.Success {
		status = "ERROR"
		if response.IsHTML {
			status = "HTML_RESPONSE"
		}
	}

	logEntry := fmt.Sprintf(
		"API_RESPONSE | Status: %s | Time: %s | Event: %s | SteamID: %s | HTTP: %d | ResponseTime: %v | Error: %s | Body: %s",
		status,
		response.Timestamp,
		response.EventType,
		response.SteamID,
		response.StatusCode,
		responseTime,
		response.Error,
		response.Body,
	)

	fileLogger.Println(logEntry)

	// Также выводим в консоль для удобства мониторинга
	if response.Success {
		log.Printf("API Success - Event: %s, SteamID: %s, Status: %d, Time: %v",
			response.EventType, response.SteamID, response.StatusCode, responseTime)
	} else if response.IsHTML {
		log.Printf("API HTML Response - Event: %s, SteamID: %s, Status: %d - Server returned Steam login page",
			response.EventType, response.SteamID, response.StatusCode)
	} else {
		log.Printf("API Error - Event: %s, SteamID: %s, Status: %d, Error: %s",
			response.EventType, response.SteamID, response.StatusCode, response.Error)
	}
}
