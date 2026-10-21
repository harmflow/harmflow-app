package app

import (
	"fmt"
	"regexp"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Settings are the preferences the dashboard changes (FS-010 FR-009, FS-011 FR-006, FS-012).
type Settings struct {
	SchemaVersion       string            `json:"schemaVersion"`
	Language            string            `json:"language"`
	TrayEnabled         bool              `json:"trayEnabled"`
	NotificationLevel   string            `json:"notificationLevel"`
	LogRetentionDays    int               `json:"logRetentionDays"`
	Theme               string            `json:"theme"`
	PathVariables       map[string]string `json:"pathVariables"`
	OnboardingCompleted bool              `json:"onboardingCompleted"`
}

type AutostartView struct {
	Enabled   bool   `json:"enabled"`
	Mechanism string `json:"mechanism"`
	PlistPath string `json:"plistPath,omitempty"`
}

type SettingsView struct {
	Settings
	Autostart AutostartView `json:"autostart"`
}

type SettingsPatch struct {
	Language            *string           `json:"language"`
	TrayEnabled         *bool             `json:"trayEnabled"`
	NotificationLevel   *string           `json:"notificationLevel"`
	LogRetentionDays    *int              `json:"logRetentionDays"`
	Autostart           *bool             `json:"autostart"`
	PathVariables       map[string]string `json:"pathVariables"`
	OnboardingCompleted *bool             `json:"onboardingCompleted"`
}

func defaultSettings() Settings {
	return Settings{SchemaVersion: "1", Language: "system", TrayEnabled: true, NotificationLevel: "all",
		LogRetentionDays: 30, Theme: "harmflow-dark", PathVariables: map[string]string{}}
}

func (a *App) loadSettings() error {
	s := defaultSettings()
	if _, err := a.d.State.Load("settings", &s); err != nil {
		return err
	}
	if s.PathVariables == nil {
		s.PathVariables = map[string]string{}
	}
	if s.LogRetentionDays <= 0 {
		s.LogRetentionDays = 30
	}
	a.mu.Lock()
	a.settings = s
	a.mu.Unlock()
	return nil
}

func (a *App) GetSettings() SettingsView {
	a.mu.Lock()
	s := a.settings
	vars := map[string]string{}
	for k, v := range s.PathVariables {
		vars[k] = v
	}
	s.PathVariables = vars
	a.mu.Unlock()
	v := SettingsView{Settings: s, Autostart: AutostartView{Mechanism: "none"}}
	if a.d.Service != nil {
		v.Autostart = AutostartView{Enabled: a.d.Service.Enabled(), Mechanism: a.d.Service.Mechanism(),
			PlistPath: a.d.Service.Location()}
	}
	return v
}

var varName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// UpdateSettings applies a patch. Autostart is opt-in: it changes only when the user asks (D-008).
func (a *App) UpdateSettings(p SettingsPatch) (SettingsView, error) {
	if p.Language != nil && !containsString([]string{"pt-BR", "en-US", "es-ES", "system"}, *p.Language) {
		return SettingsView{}, newError(CodeValidationFailed,
			domain.T("settings_language_invalid", "language must be pt-BR, en-US, es-ES, or system"))
	}
	if p.NotificationLevel != nil && !containsString([]string{"all", "failures_and_attention", "none"}, *p.NotificationLevel) {
		return SettingsView{}, newError(CodeValidationFailed, domain.T("settings_notification_level_invalid",
			"notificationLevel must be all, failures_and_attention, or none"))
	}
	if p.LogRetentionDays != nil && (*p.LogRetentionDays < 1 || *p.LogRetentionDays > 3650) {
		return SettingsView{}, newError(CodeValidationFailed,
			domain.T("settings_log_retention_invalid", "logRetentionDays must be between 1 and 3650"))
	}
	for k := range p.PathVariables {
		if !varName.MatchString(k) {
			return SettingsView{}, newError(CodeValidationFailed, domain.T("settings_path_variable_invalid",
				`path variable "{{name}}" must be uppercase letters, digits, and _`, "name", k))
		}
	}
	if p.Autostart != nil && a.d.Service != nil {
		var err error
		if *p.Autostart {
			err = a.d.Service.Enable(a.d.DaemonExecutable, a.d.DaemonArgs, a.d.DaemonEnv)
		} else {
			err = a.d.Service.Disable()
		}
		if err != nil {
			return SettingsView{}, fmt.Errorf("autostart: %w", err)
		}
	}
	a.mu.Lock()
	s := &a.settings
	if p.Language != nil {
		s.Language = *p.Language
	}
	if p.TrayEnabled != nil {
		s.TrayEnabled = *p.TrayEnabled
	}
	if p.NotificationLevel != nil {
		s.NotificationLevel = *p.NotificationLevel
	}
	if p.LogRetentionDays != nil {
		s.LogRetentionDays = *p.LogRetentionDays
	}
	if p.PathVariables != nil {
		s.PathVariables = map[string]string{}
		for k, v := range p.PathVariables {
			s.PathVariables[k] = v
		}
	}
	if p.OnboardingCompleted != nil {
		s.OnboardingCompleted = *p.OnboardingCompleted
	}
	saved := *s
	a.mu.Unlock()
	if err := a.d.State.Save("settings", saved); err != nil {
		return SettingsView{}, err
	}
	return a.GetSettings(), nil
}
