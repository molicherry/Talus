package model

import (
	"strings"
	"time"
)

const (
	DefaultUsageLeaseDuration      = time.Minute
	DefaultUsageOrdinaryRetention  = 30 * 24 * time.Hour
	DefaultUsageSensitiveRetention = 90 * 24 * time.Hour
	UsageDeleteActionSuffix        = ".delete"
)

var usageSensitiveActions = [...]string{"credential.reveal", "api_key.reveal", "service.credentials", "server.host_key.trust"}

// UsagePolicy is shared by capture retries and database retention. Nonpositive
// values select defaults, including RETENTION_DAYS=0; they never disable expiry.
type UsagePolicy struct {
	LeaseDuration, OrdinaryRetention, SensitiveRetention time.Duration
}

func (p UsagePolicy) WithDefaults() UsagePolicy {
	if p.LeaseDuration <= 0 {
		p.LeaseDuration = DefaultUsageLeaseDuration
	}
	if p.OrdinaryRetention <= 0 {
		p.OrdinaryRetention = DefaultUsageOrdinaryRetention
	}
	if p.SensitiveRetention <= 0 {
		p.SensitiveRetention = DefaultUsageSensitiveRetention
	}
	return p
}

// UsageSensitiveActions returns a copy for SQL predicates. Deletion actions
// additionally use the shared suffix, including future resource types.
func UsageSensitiveActions() []string {
	return append([]string(nil), usageSensitiveActions[:]...)
}

func UsageActionSensitive(action string) bool {
	if strings.HasSuffix(action, UsageDeleteActionSuffix) {
		return true
	}
	for _, sensitive := range usageSensitiveActions {
		if action == sensitive {
			return true
		}
	}
	return false
}

func (p UsagePolicy) Retention(action string) time.Duration {
	p = p.WithDefaults()
	if UsageActionSensitive(action) {
		return p.SensitiveRetention
	}
	return p.OrdinaryRetention
}
