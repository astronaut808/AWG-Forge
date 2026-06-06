package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	DefaultMaxSize  = int64(5 * 1024 * 1024)
	DefaultMaxFiles = 3
	DefaultTail     = 100
)

type Event struct {
	Time      time.Time      `json:"time"`
	Level     string         `json:"level"`
	Event     string         `json:"event"`
	Message   string         `json:"message,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
	Error     string         `json:"error,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

type Logger interface {
	Log(ctx context.Context, event Event)
}

type noopLogger struct{}

func (noopLogger) Log(context.Context, Event) {}

type fileLogger struct {
	path     string
	maxSize  int64
	maxFiles int
	mu       sync.Mutex
}

type ReadOptions struct {
	Tail  int
	Level string
	Event string
}

func New(cfg config.Config) Logger {
	if !cfg.AuditLogEnabled || strings.TrimSpace(cfg.AuditLogPath) == "" {
		return noopLogger{}
	}
	maxSize := cfg.AuditLogMaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	maxFiles := cfg.AuditLogMaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	return &fileLogger{path: cfg.AuditLogPath, maxSize: maxSize, maxFiles: maxFiles}
}

func (l *fileLogger) Log(ctx context.Context, event Event) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	event = Sanitize(event)
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if event.Level == "" {
		event.Level = "info"
	}
	if event.Event == "" {
		event.Event = "event"
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return
	}
	if err := l.rotateIfNeeded(int64(len(line))); err != nil {
		return
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.Write(line)
}

func (l *fileLogger) rotateIfNeeded(incoming int64) error {
	info, err := os.Stat(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size()+incoming <= l.maxSize {
		return nil
	}
	if l.maxFiles <= 1 {
		return os.Truncate(l.path, 0)
	}
	for n := l.maxFiles - 1; n >= 1; n-- {
		from := l.path + "." + strconv.Itoa(n)
		to := l.path + "." + strconv.Itoa(n+1)
		if n == l.maxFiles-1 {
			_ = os.Remove(to)
		}
		if _, err := os.Stat(from); err == nil {
			_ = os.Rename(from, to)
		}
	}
	return os.Rename(l.path, l.path+".1")
}

func Sanitize(event Event) Event {
	event.Level = normalizeLevel(event.Level)
	event.Event = strings.TrimSpace(event.Event)
	event.Message = scrubString(event.Message)
	event.Error = scrubString(event.Error)
	if len(event.Fields) == 0 {
		event.Fields = nil
		return event
	}
	clean := make(map[string]any, len(event.Fields))
	for key, value := range event.Fields {
		clean[key] = scrubValue(key, value)
	}
	event.Fields = clean
	return event
}

func Error(err error) string {
	if err == nil {
		return ""
	}
	return scrubString(err.Error())
}

func ReadFile(path string, opts ReadOptions) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return Read(file, opts)
}

func Read(reader io.Reader, opts ReadOptions) ([]Event, error) {
	tail := opts.Tail
	if tail <= 0 {
		tail = DefaultTail
	}
	level := normalizeLevel(opts.Level)
	eventName := strings.TrimSpace(opts.Event)
	var events []Event
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		event = Sanitize(event)
		if level != "" && event.Level != level {
			continue
		}
		if eventName != "" && event.Event != eventName {
			continue
		}
		events = append(events, event)
		if len(events) > tail {
			events = events[len(events)-tail:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func FormatText(events []Event) string {
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "%s %-5s %s", event.Time.Format(time.RFC3339), strings.ToUpper(event.Level), event.Event)
		if event.Message != "" {
			fmt.Fprintf(&b, ": %s", event.Message)
		}
		if event.Error != "" {
			fmt.Fprintf(&b, " error=%s", event.Error)
		}
		if len(event.Fields) > 0 {
			keys := make([]string, 0, len(event.Fields))
			for key := range event.Fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&b, " %s=%v", key, event.Fields[key])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func normalizeLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "", "info", "warn", "error":
		return level
	default:
		return "info"
	}
}

func scrubValue(key string, value any) any {
	if isSensitiveKey(key) {
		return "<redacted>"
	}
	switch v := value.(type) {
	case string:
		return scrubString(v)
	case error:
		return scrubString(v.Error())
	case map[string]any:
		out := make(map[string]any, len(v))
		for nestedKey, nestedValue := range v {
			out[nestedKey] = scrubValue(nestedKey, nestedValue)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = scrubString(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = scrubValue(key, item)
		}
		return out
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(key)
	sensitive := []string{
		"private", "preshared", "password", "secret", "session", "token",
		"config", "conf", "key", "import", "backup_password", "ciphertext",
	}
	for _, needle := range sensitive {
		if strings.Contains(key, needle) {
			return true
		}
	}
	return false
}

func scrubString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		value = value[:500] + "..."
	}
	if strings.HasPrefix(value, "vpn://") {
		return "<redacted>"
	}
	return value
}
