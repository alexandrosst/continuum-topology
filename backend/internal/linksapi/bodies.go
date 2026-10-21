package linksapi

import "time"

// rotateCredentialsRequest is the body of the call that renews throughput-test credentials: either a Cluster (every pair it is in) or the
// two ends A and B of one pair.
type rotateCredentialsRequest struct {
	Cluster string `json:"cluster"`
	A       string `json:"a"`
	B       string `json:"b"`
}

// rotatedDoc answers it. Nothing secret is in the answer.
type rotatedDoc struct {
	Rotated bool `json:"rotated"`
}

// fusionLinksDoc answers GET /api/v1/fusion/links: the Sites screen's figures for a script, a dashboard or a decision engine.
type fusionLinksDoc struct {
	Pairs []fusionLinkPair `json:"pairs"`
	At    time.Time        `json:"at"`
	// Stale says At is older than the last attempt: FUSION could not be read just now and these are the previous figures.
	Stale bool `json:"stale,omitempty"`
}
