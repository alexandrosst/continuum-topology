package telemetry

// The answers this package's routes give that were maps at the handler. The fields are in the alphabetical order of their JSON names,
// which is the order a map was encoded in: the bytes on the wire are the ones they were before.

// mintedGatewayTokenDoc answers the call that mints a gateway token: the secret, shown once and never kept, and when it was made and expires.
type mintedGatewayTokenDoc struct {
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	Token     string `json:"token"`
}

// gatewayTokenDoc answers the call that asks whether a gateway token is out. Without one only Active (false) is there; with one it also
// says when it was made, when it expires and whether that has passed. Never the secret.
type gatewayTokenDoc struct {
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt,omitempty"`
	Expired   *bool  `json:"expired,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}
