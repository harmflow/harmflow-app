package app

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Settings are the preferences the dashboard changes.
type Settings struct {
	SchemaVersion     string            `json:"schemaVersion"`
	Language          string            `json:"language"`
	TrayEnabled       bool              `json:"trayEnabled"`
	NotificationLevel string            `json:"notificationLevel"`
	LogRetentionDays  int               `json:"logRetentionDays"`
	Theme             string            `json:"theme"`
	PathVariables     map[string]string `json:"pathVariables"`
	// AllowedFolders are where, beside the home folder and the mounted volumes, a path that lets a harness write may
	// lead.
	AllowedFolders      []string `json:"allowedFolders"`
	OnboardingCompleted bool     `json:"onboardingCompleted"`
}

// AutostartView is whether the daemon starts with the user's session, by which mechanism, none when the daemon has no
// service manager, and where its definition lives.
type AutostartView struct {
	Enabled   bool   `json:"enabled"`
	Mechanism string `json:"mechanism"`
	PlistPath string `json:"plistPath,omitempty"`
}

// SettingsView is the settings as the API returns them, with the state of autostart.
type SettingsView struct {
	Settings
	Autostart AutostartView `json:"autostart"`
}

// SettingsPatch is a change to the settings: a nil field leaves its setting as it is, and a path-variable map or a
// folder list that is set replaces the whole one. Autostart enables or disables the start with the user's session.
type SettingsPatch struct {
	Language            *string           `json:"language"`
	TrayEnabled         *bool             `json:"trayEnabled"`
	NotificationLevel   *string           `json:"notificationLevel"`
	LogRetentionDays    *int              `json:"logRetentionDays"`
	Autostart           *bool             `json:"autostart"`
	PathVariables       map[string]string `json:"pathVariables"`
	AllowedFolders      []string          `json:"allowedFolders"`
	OnboardingCompleted *bool             `json:"onboardingCompleted"`
}

func defaultSettings() Settings {
	return Settings{SchemaVersion: "1", Language: "system", TrayEnabled: true, NotificationLevel: "all",
		LogRetentionDays: 30, Theme: "harmflow-dark", PathVariables: map[string]string{}, AllowedFolders: []string{}}
}

func (a *App) loadSettings() error {
	s := defaultSettings()
	if _, err := a.d.State.Load("settings", &s); err != nil {
		return err
	}
	if s.PathVariables == nil {
		s.PathVariables = map[string]string{}
	}
	if s.AllowedFolders == nil {
		s.AllowedFolders = []string{}
	}
	if s.LogRetentionDays <= 0 {
		s.LogRetentionDays = 30
	}
	a.mu.Lock()
	a.settings = s
	a.mu.Unlock()
	return nil
}

// GetSettings returns a copy of the settings, with the state of autostart.
func (a *App) GetSettings() SettingsView {
	a.mu.Lock()
	s := a.settings
	vars := map[string]string{}
	for k, v := range s.PathVariables {
		vars[k] = v
	}
	s.PathVariables = vars
	s.AllowedFolders = append([]string{}, s.AllowedFolders...)
	a.mu.Unlock()
	v := SettingsView{Settings: s, Autostart: AutostartView{Mechanism: "none"}}
	if a.d.Service != nil {
		v.Autostart = AutostartView{Enabled: a.d.Service.Enabled(), Mechanism: a.d.Service.Mechanism(),
			PlistPath: a.d.Service.Location()}
	}
	return v
}

var varName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// UpdateSettings applies a patch. Autostart is opt-in: it changes only when the user asks.
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
	if err := a.checkPathSettings(p); err != nil {
		return SettingsView{}, err
	}
	if p.Autostart != nil && a.d.Service != nil {
		var err error
		switch {
		case *p.Autostart && a.d.DaemonExecutable == "":
			// The daemon could not find its own path at start; an agent with no program would never start it.
			err = errors.New("the daemon's executable is unknown")
		case *p.Autostart:
			err = a.d.Service.Enable(a.d.DaemonExecutable, a.d.DaemonArgs, a.d.DaemonEnv)
		default:
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
	if p.AllowedFolders != nil {
		s.AllowedFolders = append([]string{}, p.AllowedFolders...)
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

// checkPathSettings refuses an allowed folder in a place no harness may write, and a change to the path variables or the
// allowed folders that would put a saved task's path outside the rule, naming the task.
func (a *App) checkPathSettings(p SettingsPatch) error {
	if p.PathVariables == nil && p.AllowedFolders == nil {
		return nil
	}
	for i, f := range p.AllowedFolders {
		real, err := a.allowedFolder(f)
		if err == nil {
			err = a.pathEnvFor(Settings{}).rule.Forbidden(real)
		}
		if err != nil {
			return &Error{Code: CodeValidationFailed, Text: domain.T("settings_allowed_folder_invalid",
				"allowed folder {{folder}} is refused: {{detail}}", "folder", f).Nest("detail", domain.TextOf(err)),
				Validation: fieldIssue(fmt.Sprintf("/allowedFolders/%d", i), domain.TextOf(err))}
		}
	}
	a.mu.Lock()
	current := a.settings
	after := current
	if p.PathVariables != nil {
		after.PathVariables = p.PathVariables
	}
	if p.AllowedFolders != nil {
		after.AllowedFolders = p.AllowedFolders
	}
	docs := map[string]domain.TaskDocument{}
	for id, e := range a.tasks {
		if e.invalid == nil && !e.deletePending {
			docs[id] = e.doc
		}
	}
	a.mu.Unlock()
	before, then := a.pathEnvFor(current), a.pathEnvFor(after)
	for _, id := range sortedKeys(docs) {
		broken := newPathIssues(a.taskPathIssues(docs[id], before), a.taskPathIssues(docs[id], then))
		if len(broken) == 0 {
			continue
		}
		is := broken[0]
		return &Error{Code: CodeValidationFailed, Text: domain.T("settings_change_breaks_task",
			"this change would put {{field}} of task {{task}} outside the allowed paths: {{detail}}",
			"field", is.path, "task", id).Nest("detail", is.text), Validation: fieldIssue(is.path, is.text)}
	}
	return nil
}

// fieldIssue is a ValidationResult with the one error at path, for a refusal that is not a task's.
func fieldIssue(path string, text domain.Text) *ValidationResult {
	return &ValidationResult{Errors: []schemaIssue{{Path: path, Text: text, Code: "semantic"}}, Warnings: []schemaIssue{}}
}
