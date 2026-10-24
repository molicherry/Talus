package service

import (
	"context"
	"fmt"

	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/repository"
)

type residentSettings struct {
	URL       string
	Interval  int
	AllowHTTP bool
}
type residentSettingsKey struct{}

func (m *ResidentMonitor) SetRuntimeConfig(r *config.Runtime) { m.runtimeConfig = r }
func (m *ResidentMonitor) settings() residentSettings {
	if m.runtimeConfig != nil {
		c := m.runtimeConfig.Current()
		return residentSettings{c.AgentHubURL, c.MonitorInterval, c.AgentAllowHTTP}
	}
	return residentSettings{m.hubURL, m.interval, m.allowHTTP}
}
func (m *ResidentMonitor) installSettings(ctx context.Context) residentSettings {
	if c, ok := ctx.Value(residentSettingsKey{}).(residentSettings); ok {
		return c
	}
	return m.settings()
}
func (c residentSettings) hash(tokenHash string) string {
	return repository.TokenHash(fmt.Sprintf("%s:%s:%d:%t", c.URL, tokenHash, c.Interval, c.AllowHTTP))
}
