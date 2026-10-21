package decisionloop

// The bodies this package's routes read. They were anonymous structs at the handlers; naming them puts them in the TypeScript written
// from the Go types (internal/wire), so the page sends what the server reads.

// recordDecisionsRequest is the body of POST .../decisions: decisions made elsewhere, for the log.
type recordDecisionsRequest struct {
	Entries []decisionEntryDoc `json:"entries"`
}

// submitProposalRequest is the body of POST .../proposals. TTLSeconds is optional; it is refused above MaxProposalTTL.
type submitProposalRequest struct {
	Advisor      string             `json:"advisor"`
	ModelVersion int64              `json:"modelVersion"`
	Change       AccessChange       `json:"change"`
	Evidence     []EvidenceFact     `json:"evidence"`
	Predicted    map[string]float64 `json:"predicted"`
	TTLSeconds   int                `json:"ttlSeconds"`
}

// decideProposalRequest is the body of POST .../proposals/{id}/accept and .../reject: the note is kept with the decision.
type decideProposalRequest struct {
	Note string `json:"note"`
}
