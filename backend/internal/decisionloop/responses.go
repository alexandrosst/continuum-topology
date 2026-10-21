package decisionloop

import "encoding/json"

// The answers this package's routes give that were maps at the handler. The fields are in the alphabetical order of their JSON names,
// which is the order a map was encoded in: the bytes on the wire are the ones they were before.

// recordedDoc answers POST .../decisions: how many entries were kept.
type recordedDoc struct {
	Recorded int `json:"recorded"`
}

// decisionsDoc answers GET .../decisions.
type decisionsDoc struct {
	Decisions []decisionLogDoc `json:"decisions"`
}

// externalDecisionDoc answers POST .../decide: what the external decider is called, and what it answered, as it answered it.
type externalDecisionDoc struct {
	Decider string          `json:"decider"`
	Result  json.RawMessage `json:"result"`
}

// proposalsDoc answers GET .../proposals.
type proposalsDoc struct {
	Proposals []proposalDoc `json:"proposals"`
}

// advisorsDoc answers GET .../proposals/advisors.
type advisorsDoc struct {
	Advisors []advisorRecordDoc `json:"advisors"`
}
