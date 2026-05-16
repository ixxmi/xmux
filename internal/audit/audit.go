package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Record struct {
	Time      time.Time `json:"time"`
	SessionID string    `json:"session_id"`
	RequestID string    `json:"request_id"`
	User      string    `json:"user"`
	EdgeID    string    `json:"edge_id"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	Allowed   bool      `json:"allowed"`
	Reason    string    `json:"reason,omitempty"`
	ExitCode  int       `json:"exit_code"`
	Duration  string    `json:"duration"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
}

type Writer interface {
	Write(Record) error
}

type JSONLWriter struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func NewJSONLWriter(path string) (*JSONLWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLWriter{file: file, enc: json.NewEncoder(file)}, nil
}

func (w *JSONLWriter) Write(record Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(record)
}

func (w *JSONLWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}
