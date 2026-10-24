package main

import (
	"flag"
	"log/slog"
	"os"

	"ikhnos/internal/pki"
)

var requireIdentityURI = flag.Bool("require-identity-uri", os.Getenv("IKHNOS_REQUIRE_IDENTITY_URI") != "false",
	"refuse an agent or operator certificate that does not state, in its URI name, who it was issued to (spiffe://ikhnos/org/<org>/agent/<id>, and the operator ones). On by default: every certificate this server issues now states it. Set it false only while certificates issued before the URI existed are still in use (an agent's lasts a day, an operator's 30), and turn it on again once they have been renewed; env IKHNOS_REQUIRE_IDENTITY_URI=false")

// applyIdentityPolicy sets what the server holds certificates to, and says so: a server that refuses legacy certificates and one that
// accepts them look the same until a certificate is refused, so the choice is in the log from the first line.
func applyIdentityPolicy(log *slog.Logger, require bool) {
	pki.RequireIdentityURI = require
	if require {
		log.Info("certificates must state their holder: one issued before the identity URI existed is refused (--require-identity-uri=false accepts it while it is renewed)")
		return
	}
	log.Warn("certificates without an identity URI are accepted (--require-identity-uri=false): turn this on again once the agents and operators have renewed")
}
