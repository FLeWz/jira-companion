package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Ref struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Entry struct {
	ID      string `json:"id"` // one task can have several entries
	Key     string `json:"key"`
	Summary string `json:"summary"`
	Minutes int    `json:"minutes"`
	Comment string `json:"comment,omitempty"`
	Logged  bool   `json:"logged"`
}

type Remember struct {
	Project  *Ref `json:"project,omitempty"`
	Parent   *Ref `json:"parent,omitempty"`
	Assignee *Ref `json:"assignee,omitempty"`
	Reporter *Ref `json:"reporter,omitempty"`
	Workday  int  `json:"workday,omitempty"` // last work day length, prefilled each new day
}

type State struct {
	Date       string   `json:"date"`
	WorkdayMin int      `json:"workdayMin"`
	Entries    []Entry  `json:"entries"`
	Loaded     bool     `json:"loaded"` // active tasks fetched for this day
	Remember   Remember `json:"remember"`
}

// Store is a mutex-guarded JSON file. Callers use Lock/Unlock around S.
type Store struct {
	sync.Mutex
	path string
	S    State
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.S); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Rollover resets the day part of the state when the date changed. Lock held.
func (s *Store) Rollover() {
	for i := range s.S.Entries {
		if s.S.Entries[i].ID == "" {
			s.S.Entries[i].ID = NewID()
		}
	}
	today := time.Now().Format("2006-01-02")
	if s.S.Date != today {
		s.resetDay()
		s.S.Date = today
	}
}

// resetDay clears the day, keeps remembered values. Lock held.
func (s *Store) resetDay() {
	s.S.Entries = nil
	s.S.Loaded = false
	s.S.WorkdayMin = s.S.Remember.Workday
}

// Save writes atomically. Lock held.
func (s *Store) Save() error {
	b, err := json.MarshalIndent(s.S, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// NewID returns a short random id for day entries.
func NewID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
