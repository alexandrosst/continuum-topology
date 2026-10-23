package settingsapi

import "ikhnos/internal/kernel"

// saveSettingsRequest is the body of PUT .../settings: the settings as the page holds them, and whether to clear the decider secret (a body
// that leaves the secret empty keeps the one that is set, so that a client that never saw it cannot blank it by accident).
type saveSettingsRequest struct {
	kernel.Settings
	ClearDeciderSecret bool `json:"clearDeciderSecret"`
}

// settingsConflictDoc answers PUT .../settings with 409: someone saved first, and these are the settings as they are now.
type settingsConflictDoc struct {
	SettingsDoc
	Error string `json:"error"`
}
