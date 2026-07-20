package scheduler

import (
	"encoding/json"
	"fmt"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type TaskModel struct {
	ID              string `gorm:"primaryKey"`
	Type            string `gorm:"index"`
	State           string `gorm:"index"`
	Priority        int    `gorm:"index"`
	ConfigJSON      []byte
	Input           []byte
	Result          []byte
	Error           string
	Retry           int
	MaxRetry        int
	CreatedAt       int64
	StartedAt       *int64
	EndedAt         *int64
	MetadataJSON    []byte
	Dependencies    []byte // JSON array of strings
	IsAgent         bool   `gorm:"index"`
	AgentConfigJSON []byte
}

func initDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.AutoMigrate(&TaskModel{}); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return db, nil
}

func taskToModel(t *Task) (*TaskModel, error) {
	configJSON, _ := json.Marshal(t.Config)
	metadataJSON, _ := json.Marshal(t.Metadata)
	depsJSON, _ := json.Marshal(t.Dependencies)

	var acJSON []byte
	if t.AgentConfig != nil {
		acJSON, _ = json.Marshal(t.AgentConfig)
	}

	m := &TaskModel{
		ID:              t.ID,
		Type:            t.Type,
		State:           string(t.State),
		Priority:        int(t.Priority),
		ConfigJSON:      configJSON,
		Input:           t.Input,
		Result:          t.Result,
		Error:           t.Error,
		Retry:           t.Retry,
		MaxRetry:        t.MaxRetry,
		CreatedAt:       t.CreatedAt.UnixNano(),
		MetadataJSON:    metadataJSON,
		Dependencies:    depsJSON,
		IsAgent:         t.IsAgent,
		AgentConfigJSON: acJSON,
	}

	if t.StartedAt != nil {
		ts := t.StartedAt.UnixNano()
		m.StartedAt = &ts
	}
	if t.EndedAt != nil {
		te := t.EndedAt.UnixNano()
		m.EndedAt = &te
	}

	return m, nil
}
