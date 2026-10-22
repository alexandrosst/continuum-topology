package operatorsapi

import "continuum/internal/store"

// The answers this package's routes give that were maps at the handler. The fields are in the alphabetical order of their JSON names,
// which is the order a map was encoded in: the bytes on the wire are the ones they were before. A key that a map had only sometimes is an
// optional field (omitempty; omitzero where an empty list must stay different from no list).

// operatorInstalledDoc answers creating an operator and installing it again: the operator, the install command, what to apply in the
// source clusters, and the Secrets the install points at. Token and SecretCommand are there for a bearer operator only (an mTLS one has
// nothing to show: Operator.ReceiverAuth says why); the heartbeat fields when a heartbeat secret was minted; RestartCommand on a reissue.
type operatorInstalledDoc struct {
	ExportSecretCommand      string           `json:"exportSecretCommand,omitempty"`
	ExportTarget             *exportTargetDoc `json:"exportTarget,omitempty"`
	HeartbeatCaSecretCommand string           `json:"heartbeatCaSecretCommand,omitempty"`
	HeartbeatIntervalSeconds int              `json:"heartbeatIntervalSeconds,omitempty"`
	HeartbeatSecretCommand   string           `json:"heartbeatSecretCommand,omitempty"`
	HeartbeatToken           string           `json:"heartbeatToken,omitempty"`
	HeartbeatURL             string           `json:"heartbeatUrl,omitempty"`
	HeartbeatWarning         string           `json:"heartbeatWarning,omitempty"`
	Install                  string           `json:"install"`
	Operator                 Doc              `json:"operator"`
	Reminders                []string         `json:"reminders"`
	RestartCommand           string           `json:"restartCommand,omitempty"`
	SecretCommand            *string          `json:"secretCommand,omitempty"`
	TLSSecretCommand         string           `json:"tlsSecretCommand,omitempty"`
	Token                    *string          `json:"token,omitempty"`
}

// exportTargetDoc says where a regional operator that exports to another operator sends: its name, the endpoint, whether other clusters can
// reach it, and what to doubt about it.
type exportTargetDoc struct {
	Endpoint                   string   `json:"endpoint"`
	Name                       string   `json:"name"`
	OperatorID                 string   `json:"operatorId"`
	ReachableFromOtherClusters bool     `json:"reachableFromOtherClusters"`
	Warnings                   []string `json:"warnings,omitempty"`
}

// operatorScopeChangedDoc answers PUT .../operators/{id}/scope: the operator as it is now, and what to apply in the clusters it gained.
type operatorScopeChangedDoc struct {
	ExportSecretCommand string           `json:"exportSecretCommand,omitempty"`
	ExportTarget        *exportTargetDoc `json:"exportTarget,omitempty"`
	Operator            Doc              `json:"operator"`
	Reminders           []string         `json:"reminders"`
}

// operatorCertificatesDoc answers GET .../operators/{id}/certificates: the ledger, newest first. Never a certificate or a key.
type operatorCertificatesDoc struct {
	Certificates []issuedCertDoc `json:"certificates"`
}

// operatorUninstallDoc answers revoking or deleting an operator: the command that removes the running receiver, or none when it cannot be
// written (which the operator's id, a name this server made, makes unlikely).
type operatorUninstallDoc struct {
	Uninstall string `json:"uninstall,omitempty"`
}

// heartbeatEnabledDoc answers switching an operator's heartbeat on (or rotating its secret): the secret, shown once, with the commands that
// use it. HeartbeatRestartCommand is there when the secret was rotated (a running pod does not see a new value until it restarts).
type heartbeatEnabledDoc struct {
	HeartbeatCaSecretCommand string `json:"heartbeatCaSecretCommand,omitempty"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
	HeartbeatRestartCommand  string `json:"heartbeatRestartCommand,omitempty"`
	HeartbeatSecretCommand   string `json:"heartbeatSecretCommand"`
	HeartbeatToken           string `json:"heartbeatToken"`
	HeartbeatUpgradeCommand  string `json:"heartbeatUpgradeCommand"`
	HeartbeatURL             string `json:"heartbeatUrl"`
	HeartbeatWarning         string `json:"heartbeatWarning,omitempty"`
	Operator                 Doc    `json:"operator"`
	Rotated                  bool   `json:"rotated"`
}

// intentCommandDoc answers the call that writes the command for a telemetry intent: the parts (InstallFragment, SecretCommands), or with the
// page's telemetry the whole block (Command). Problems says what is wrong with that telemetry, and is absent when none was asked about;
// the other keys depend on where the intent sends.
type intentCommandDoc struct {
	Command         string                        `json:"command,omitempty"`
	Endpoint        *string                       `json:"endpoint,omitempty"`
	InstallFragment string                        `json:"installFragment"`
	Namespace       string                        `json:"namespace"`
	Operators       map[string]store.ReceiverAuth `json:"operators,omitempty"`
	Problems        []string                      `json:"problems,omitzero"`
	ReceiverAuth    *store.ReceiverAuth           `json:"receiverAuth,omitempty"`
	Release         string                        `json:"release"`
	SecretCommands  []string                      `json:"secretCommands"`
	Warnings        []string                      `json:"warnings,omitempty"`
}
