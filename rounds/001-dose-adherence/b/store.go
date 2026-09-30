package gomedicationcourse

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Ledger 是服务的完整持久化状态。所有变更先改内存账本再落盘，
// 事件（Events）只追加，是重建一切投影的事实来源。
type Ledger struct {
	Courses     map[string]*Course              `json:"courses"`
	Versions    map[string]map[int]*PlanVersion `json:"versions"`
	Events      []Event                         `json:"events"`
	Records     map[string]AdministrationRecord `json:"records"`
	Corrections map[string]CorrectionRecord     `json:"corrections"`
	Refs        map[string]string               `json:"refs"`
	EventSeq    int64                           `json:"event_seq"`
}

// Store 持久化接口。
type Store interface {
	Load() (*Ledger, error)
	Save(*Ledger) error
}

// MemoryStore 进程内持久化（测试用）。
type MemoryStore struct {
	mu     sync.Mutex
	ledger *Ledger
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{ledger: newLedger()}
}

func (s *MemoryStore) Load() (*Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ledger, nil
}

func (s *MemoryStore) Save(l *Ledger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledger = l
	return nil
}

// FileStore 以单个 JSON 文件持久化账本，写入采用临时文件 + rename 原子替换。
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Load() (*Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
	return newLedger(), nil
		}
		return nil, err
	}
	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("load ledger %s: %w", s.path, err)
	}
	l.ensureMaps()
	return &l, nil
}

func (s *FileStore) Save(l *Ledger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newLedger() *Ledger {
	return &Ledger{
		Courses:     map[string]*Course{},
		Versions:    map[string]map[int]*PlanVersion{},
		Records:     map[string]AdministrationRecord{},
		Corrections: map[string]CorrectionRecord{},
		Refs:        map[string]string{},
	}
}

func (l *Ledger) ensureMaps() {
	if l.Courses == nil {
		l.Courses = map[string]*Course{}
	}
	if l.Versions == nil {
		l.Versions = map[string]map[int]*PlanVersion{}
	}
	if l.Records == nil {
		l.Records = map[string]AdministrationRecord{}
	}
	if l.Corrections == nil {
		l.Corrections = map[string]CorrectionRecord{}
	}
	if l.Refs == nil {
		l.Refs = map[string]string{}
	}
}
