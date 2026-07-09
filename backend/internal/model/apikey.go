package model

import "time"

type APIKey struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	KeyHash   string    `gorm:"uniqueIndex;not null" json:"-"`
	KeyPrefix string    `gorm:"size:16;not null" json:"key_prefix"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

func (APIKey) TableName() string { return "api_keys" }
