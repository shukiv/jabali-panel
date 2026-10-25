// Package models — Snuffleupagus state + incident + rule-override rows.
// M41, ADR-0088.
package models

import (
	"encoding/json"
	"net"
	"regexp"
	"time"
)

type SnuffleupagusMode string

const (
	SnuffleupagusModeOff        SnuffleupagusMode = "off"
	SnuffleupagusModeSimulation SnuffleupagusMode = "simulation"
	SnuffleupagusModeEnforce    SnuffleupagusMode = "enforce"
)

type SnuffleupagusState struct {
	ID                int8              `gorm:"primaryKey;default:1"     json:"-"`
	Mode              SnuffleupagusMode `gorm:"type:enum('off','simulation','enforce');not null;default:'off'" json:"mode"`
	LastAppliedAt     *time.Time        `gorm:"type:datetime(6)"          json:"last_applied_at,omitempty"`
	LastAppliedSha256 *string           `gorm:"type:char(64)"             json:"last_applied_sha256,omitempty"`
}

func (SnuffleupagusState) TableName() string { return "snuffleupagus_state" }

type SnuffleupagusRuleOverride struct {
	RuleName string `gorm:"primaryKey;type:varchar(128)" json:"rule_name"`
	// No gorm default tag: with one, GORM writes the default in place of
	// false, so disabling a rule saved enabled=1 (GH #2262). The column
	// keeps its DEFAULT 1.
	Enabled     bool      `gorm:"type:tinyint(1);not null" json:"enabled"`
	Reason      *string   `gorm:"type:varchar(512)"           json:"reason,omitempty"`
	SetByUserID *string   `gorm:"type:char(26)"               json:"set_by_user_id,omitempty"`
	SetAt       time.Time `gorm:"type:datetime(6);not null;default:current_timestamp(6)" json:"set_at"`
}

func (SnuffleupagusRuleOverride) TableName() string { return "snuffleupagus_rule_overrides" }

type SnuffleupagusAction string

const (
	SnuffleupagusActionLog            SnuffleupagusAction = "log"
	SnuffleupagusActionBlock          SnuffleupagusAction = "block"
	SnuffleupagusActionSimulatedBlock SnuffleupagusAction = "simulated_block"
)

type SnuffleupagusIncident struct {
	ID         int64               `gorm:"primaryKey;autoIncrement" json:"id"`
	Ts         time.Time           `gorm:"type:datetime(6);not null;index:ix_snuf_inc_ts" json:"ts"`
	RuleName   string              `gorm:"type:varchar(128);not null;index:ix_snuf_inc_rule" json:"rule_name"`
	Action     SnuffleupagusAction `gorm:"type:enum('log','block','simulated_block');not null" json:"action"`
	SourceIP   []byte              `gorm:"type:varbinary(16)" json:"-"`
	RequestURI *string             `gorm:"type:varchar(2048)" json:"request_uri,omitempty"`
	PhpVersion *string             `gorm:"type:varchar(8);column:php_version" json:"php_version,omitempty"`
	DomainID   *string             `gorm:"type:char(26);column:domain_id;index:ix_snuf_inc_domain" json:"domain_id,omitempty"`
	Raw        *string             `gorm:"type:text" json:"raw,omitempty"`
}

func (SnuffleupagusIncident) TableName() string { return "snuffleupagus_incidents" }

// SnuffleupagusSourceIP packs a client address for the varbinary(16)
// source_ip column: 16 bytes, IPv4 mapped. As text an IPv6 address does
// not fit, and the insert fails (GH #2262). Nil when s is not an address.
func SnuffleupagusSourceIP(s string) []byte {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	return ip.To16()
}

// SourceIPString renders SourceIP. Rows written before GH #2262 hold the
// address as text, later ones as 16 bytes.
func (in SnuffleupagusIncident) SourceIPString() string {
	if len(in.SourceIP) == 0 {
		return ""
	}
	if ip := net.ParseIP(string(in.SourceIP)); ip != nil {
		return ip.String()
	}
	if len(in.SourceIP) == net.IPv6len || len(in.SourceIP) == net.IPv4len {
		return net.IP(in.SourceIP).String()
	}
	return ""
}

// snufScriptDomainRe matches the script path Snuffleupagus names at the end
// of a log line: "... in /home/<user>/domains/<domain>/... on line <n>".
var snufScriptDomainRe = regexp.MustCompile(` in /home/[^/ ]+/domains/([A-Za-z0-9.-]+)/[^ ]* on line `)

// Domain is the site whose script the rule stopped, from the last script
// path in the log line. Empty for CLI code and paths outside a site.
func (in SnuffleupagusIncident) Domain() string {
	if in.Raw == nil {
		return ""
	}
	m := snufScriptDomainRe.FindAllStringSubmatch(*in.Raw, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// MarshalJSON adds source_ip as text and the domain, for the admin API and
// the CLI.
func (in SnuffleupagusIncident) MarshalJSON() ([]byte, error) {
	type plain SnuffleupagusIncident
	return json.Marshal(struct {
		plain
		SourceIP string `json:"source_ip,omitempty"`
		Domain   string `json:"domain,omitempty"`
	}{plain(in), in.SourceIPString(), in.Domain()})
}
