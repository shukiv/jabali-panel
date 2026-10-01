package models

import (
	"strings"
	"time"
)

// PHPPoolIniOverride represents a php.ini directive override for a PHP pool.
// Stored one-row-per-override; rendered as php_admin_value or php_admin_flag
// in the pool config file. Only allowlisted directives are permitted.
type PHPPoolIniOverride struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	PoolID    string    `gorm:"type:char(26);not null" json:"pool_id"`
	Directive string    `gorm:"type:varchar(64);not null" json:"directive"`
	Value     string    `gorm:"type:varchar(255);not null" json:"value"`
	Kind      string    `gorm:"type:enum('value','flag');not null;default:'value'" json:"kind"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (PHPPoolIniOverride) TableName() string { return "php_pool_ini_overrides" }

// PHPIniBoolOn reads a php.ini boolean the way PHP does (zend_ini_parse_bool):
// "on", "yes" and "true" (any case) are on; anything else is on when its
// leading integer is non-zero ("1" on; "0", "off", "" off). A pool override
// on a boolean directive can hold any of these, as a flag or a value.
func PHPIniBoolOn(v string) bool {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "on", "yes", "true":
		return true
	}
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		if s[i] != '0' {
			return true
		}
	}
	return false
}
