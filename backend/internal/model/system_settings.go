package model

import "encoding/json"

// A single row makes a multi-field change and its revision atomic.
type SystemSettings struct {
	ID        uint            `gorm:"primaryKey"`
	Revision  uint64          `gorm:"not null"`
	Overrides json.RawMessage `gorm:"type:jsonb;not null"`
}
