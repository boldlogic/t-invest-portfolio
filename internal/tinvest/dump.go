package tinvest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// DumpPath — файл одной сессии. Каталог в .gitignore: там ответы счёта.
const DumpPath = "logs/api-dump.jsonl"

var (
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[^"\\\s]+`)
	tokenPattern  = regexp.MustCompile(`t\.[A-Za-z0-9_-]{20,}`)
	protoJSON     = protojson.MarshalOptions{EmitUnpopulated: false}
)

// Dump дописывает один JSON-объект на вызов. Токен в строку не кладётся.
type Dump struct {
	file *os.File
	mu   sync.Mutex
}

// OpenDump создаёт или очищает файл сессии.
func OpenDump(path string) (*Dump, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("каталог лога: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("файл лога: %w", err)
	}
	return &Dump{file: file}, nil
}

// Close сбрасывает файл.
func (d *Dump) Close() error {
	if d == nil || d.file == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Close()
}

// UseDump включает запись ответов. nil отключает её.
func (c *Client) UseDump(dump *Dump) {
	if c == nil {
		return
	}
	c.dump = dump
}

// NoteSession пишет адрес контура без токена.
func (c *Client) NoteSession(endpoint, caFile string) {
	request := map[string]any{"endpoint": endpoint}
	if caFile != "" {
		request["ca_file"] = caFile
	}
	c.note("session", time.Now(), request, nil, nil)
}

// Note пишет одно наблюдение в дамп, если он включён.
func (c *Client) Note(method string, request, response any, callErr error) {
	c.note(method, time.Now(), request, response, callErr)
}

func (c *Client) note(method string, started time.Time, request, response any, callErr error) {
	if c == nil || c.dump == nil {
		return
	}
	c.dump.write(method, started, request, response, callErr)
}

func (d *Dump) write(method string, started time.Time, request, response any, callErr error) {
	if d == nil || d.file == nil {
		return
	}
	line := map[string]any{
		"at":         time.Now().UTC().Format(time.RFC3339Nano),
		"method":     method,
		"elapsed_ms": time.Since(started).Milliseconds(),
	}
	if request != nil {
		line["request"] = request
	}
	if response != nil {
		line["response"] = rawBody(response)
	}
	if callErr != nil {
		line["error"] = callErr.Error()
	}
	body, err := json.Marshal(line)
	if err != nil {
		body = []byte(fmt.Sprintf(`{"method":%q,"error":"не удалось записать ответ: %s"}`, method, err.Error()))
	}
	body = scrub(body)
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = d.file.Write(append(body, '\n'))
	_ = d.file.Sync()
}

func rawBody(response any) any {
	switch value := response.(type) {
	case proto.Message:
		return messageJSON(value)
	case []proto.Message:
		out := make([]json.RawMessage, 0, len(value))
		for _, item := range value {
			out = append(out, messageJSON(item))
		}
		return out
	default:
		return response
	}
}

func messageJSON(message proto.Message) json.RawMessage {
	if message == nil {
		return []byte("null")
	}
	body, err := protoJSON.Marshal(message)
	if err != nil {
		encoded, _ := json.Marshal(message.ProtoReflect().Descriptor().Name())
		return encoded
	}
	return body
}

func scrub(body []byte) []byte {
	body = bearerPattern.ReplaceAll(body, []byte("Bearer [скрыто]"))
	return tokenPattern.ReplaceAll(body, []byte("t.[скрыто]"))
}
