package operatorsapi

import (
	"encoding/json"
	"testing"

	"continuum/internal/store"
	"continuum/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// sameBytes fails the test unless got is written exactly as was is: was being the map a handler wrote before the answer had a type.
func sameBytes(t *testing.T, name string, got, was any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(name, err)
	}
	w, err := json.Marshal(was)
	if err != nil {
		t.Fatal(name, err)
	}
	if string(g) != string(w) {
		t.Errorf("%s:\n got %s\nwant %s", name, g, w)
	}
}

// The answers that were maps at the handlers are structs now. Their keys came and went with the operator, so each way a map could come out
// is held to the map it replaced: the bytes on the wire are the ones they were.
func TestAnswersThatWereMapsAreWrittenAsTheyWere(t *testing.T) {
	op := Doc{ID: "op1", Name: "regional"}
	rem := []string{"helm upgrade a"}
	empty := ""
	tok, sec := "tok", "kubectl create secret"
	target := &exportTargetDoc{OperatorID: "c", Name: "central", Endpoint: "c:4317", ReachableFromOtherClusters: true, Warnings: []string{"w"}}
	asMap := func(d *exportTargetDoc) map[string]any {
		m := map[string]any{"operatorId": d.OperatorID, "name": d.Name, "endpoint": d.Endpoint, "reachableFromOtherClusters": d.ReachableFromOtherClusters}
		if len(d.Warnings) > 0 {
			m["warnings"] = d.Warnings
		}
		return m
	}

	// creating an operator: a bearer one with a heartbeat, an export target and a certificate
	sameBytes(t, "installed, everything", operatorInstalledDoc{Operator: op, Install: "helm install", Reminders: rem, Token: &tok, SecretCommand: &sec,
		HeartbeatToken: "hb", HeartbeatSecretCommand: "hbcmd", HeartbeatURL: "https://x/hb", HeartbeatIntervalSeconds: 60, HeartbeatWarning: "plain http",
		HeartbeatCaSecretCommand: "ca", TLSSecretCommand: "tls", RestartCommand: "restart", ExportSecretCommand: "export", ExportTarget: target},
		map[string]any{"operator": op, "install": "helm install", "reminders": rem, "token": tok, "secretCommand": sec, "heartbeatToken": "hb",
			"heartbeatSecretCommand": "hbcmd", "heartbeatUrl": "https://x/hb", "heartbeatIntervalSeconds": 60, "heartbeatWarning": "plain http",
			"heartbeatCaSecretCommand": "ca", "tlsSecretCommand": "tls", "restartCommand": "restart", "exportSecretCommand": "export", "exportTarget": asMap(target)})
	// an mTLS one with nothing else: no token, and a bearer one whose command is empty keeps the empty key
	sameBytes(t, "installed, mTLS", operatorInstalledDoc{Operator: op, Install: "helm install", Reminders: rem},
		map[string]any{"operator": op, "install": "helm install", "reminders": rem})
	sameBytes(t, "installed, empty token", operatorInstalledDoc{Operator: op, Install: "i", Reminders: nil, Token: &empty, SecretCommand: &empty},
		map[string]any{"operator": op, "install": "i", "reminders": []string(nil), "token": "", "secretCommand": ""})
	sameBytes(t, "export target without warnings", target2(), asMap(target2()))

	// changing a scope
	sameBytes(t, "scope", operatorScopeChangedDoc{Operator: op, Reminders: rem}, map[string]any{"operator": op, "reminders": rem})
	sameBytes(t, "scope with an export", operatorScopeChangedDoc{Operator: op, Reminders: rem, ExportSecretCommand: "export", ExportTarget: target},
		map[string]any{"operator": op, "reminders": rem, "exportSecretCommand": "export", "exportTarget": asMap(target)})

	sameBytes(t, "certificates", operatorCertificatesDoc{Certificates: []issuedCertDoc{{Serial: "1"}}}, map[string]any{"certificates": []issuedCertDoc{{Serial: "1"}}})
	sameBytes(t, "uninstall", operatorUninstallDoc{Uninstall: "helm uninstall"}, map[string]any{"uninstall": "helm uninstall"})
	sameBytes(t, "uninstall, no command", operatorUninstallDoc{}, map[string]any{})

	// the heartbeat
	sameBytes(t, "heartbeat", heartbeatEnabledDoc{Operator: op, Rotated: false, HeartbeatToken: "t", HeartbeatSecretCommand: "s", HeartbeatUpgradeCommand: "u",
		HeartbeatURL: "https://x", HeartbeatIntervalSeconds: 60},
		map[string]any{"operator": op, "rotated": false, "heartbeatToken": "t", "heartbeatSecretCommand": "s", "heartbeatUpgradeCommand": "u", "heartbeatUrl": "https://x",
			"heartbeatIntervalSeconds": 60})
	sameBytes(t, "heartbeat, rotated, with warning and CA", heartbeatEnabledDoc{Operator: op, Rotated: true, HeartbeatToken: "t", HeartbeatSecretCommand: "s",
		HeartbeatUpgradeCommand: "u", HeartbeatURL: "http://x", HeartbeatIntervalSeconds: 60, HeartbeatRestartCommand: "r", HeartbeatWarning: "w", HeartbeatCaSecretCommand: "ca"},
		map[string]any{"operator": op, "rotated": true, "heartbeatToken": "t", "heartbeatSecretCommand": "s", "heartbeatUpgradeCommand": "u", "heartbeatUrl": "http://x",
			"heartbeatIntervalSeconds": 60, "heartbeatRestartCommand": "r", "heartbeatWarning": "w", "heartbeatCaSecretCommand": "ca"})

	// the command for a telemetry intent, by the way it went
	ep, ra := "c:4317", store.ReceiverAuthMTLS
	sameBytes(t, "intent, parts", intentCommandDoc{Namespace: "ns", Release: "rel", InstallFragment: "--set a=b", SecretCommands: []string{}},
		map[string]any{"namespace": "ns", "release": "rel", "installFragment": "--set a=b", "secretCommands": []string{}})
	sameBytes(t, "intent, problems found", intentCommandDoc{Namespace: "ns", Release: "rel", InstallFragment: "", SecretCommands: []string{}, Problems: []string{"bad"}},
		map[string]any{"namespace": "ns", "release": "rel", "installFragment": "", "secretCommands": []string{}, "problems": []string{"bad"}})
	sameBytes(t, "intent, whole block, no problems", intentCommandDoc{Namespace: "ns", Release: "rel", InstallFragment: "f", SecretCommands: []string{"s"}, Command: "block", Problems: []string{}},
		map[string]any{"namespace": "ns", "release": "rel", "installFragment": "f", "secretCommands": []string{"s"}, "command": "block", "problems": []string{}})
	sameBytes(t, "intent, one operator", intentCommandDoc{Namespace: "ns", Release: "rel", InstallFragment: "f", SecretCommands: []string{"s"}, ReceiverAuth: &ra, Endpoint: &ep,
		Operators: map[string]store.ReceiverAuth{"op1": store.ReceiverAuthMTLS}, Warnings: []string{"w"}},
		map[string]any{"namespace": "ns", "release": "rel", "installFragment": "f", "secretCommands": []string{"s"}, "receiverAuth": "mtls", "endpoint": "c:4317",
			"operators": map[string]string{"op1": "mtls"}, "warnings": []string{"w"}})
}

func target2() *exportTargetDoc {
	return &exportTargetDoc{OperatorID: "c", Name: "central", Endpoint: "c:4317"}
}

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfAnOperatorAreItsConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", AddressStatus(""), Exposure(""), DestinationRole(""), CertKind(""), CertValidity(""))
}
