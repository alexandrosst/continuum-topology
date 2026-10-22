package connectivity

import (
	"time"

	"continuum/internal/kernel"
)

// Testing an address from the cluster that will dial it.
//
// Whether an operator's address works depends on where it is dialled from, and the only place that knows is the sending
// cluster. The agent there already times TCP connections to the addresses the server names (see paths.go), so a test is
// that mechanism with an address an administrator just chose: the server adds it to the agent's targets for a short
// while, the agent connects and closes (it sends and reads nothing), and the result is read back here. It shows that the
// address answers on its port from that cluster. It does not show that the operator is what answers: the certificate is
// checked when data is sent, and the operator's stable name is what it is checked against.

const (
	// AddressTestTTL is how long a test stays in the agent's targets and its result stays readable.
	AddressTestTTL = 10 * time.Minute
	// MaxAddressTests bounds the tests one agent holds at once, so a person cannot turn the agent into a port scanner.
	MaxAddressTests = 6
	// SourceAddressTest marks a target issued for a test, as opposed to an observed or a manual one.
	SourceAddressTest = "address-test"
)

// AddressTest is one address being tried from one agent. Kept on the agent's view and lost with it on a restart.
type AddressTest struct {
	ID      string
	Address string
	Host    string
	Port    int
	Created time.Time
	// Quiet marks a test the server started itself (see address_drift.go), not one an administrator asked for.
	Quiet bool

	Done            bool
	Samples, Failed int
	Min, P50, P95   float64
	At              time.Time
}

// TestState is where a test of an address stands: pending (no result yet), or what it found.
type TestState string

// The states of a test of an address.
const (
	TestPending     TestState = "pending"
	TestReachable   TestState = "reachable"
	TestUnreachable TestState = "unreachable"
)

// WireWords is the list of TestState's words, as the generated types give it.
func (TestState) WireWords() []string {
	return []string{string(TestPending), string(TestReachable), string(TestUnreachable)}
}

// AddressTestDoc is what the UI reads about one test.
type AddressTestDoc struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	// State is pending (no result yet), reachable (a connection was made) or unreachable (every attempt failed).
	State    TestState `json:"state"`
	Samples  int       `json:"samples,omitempty"`
	Failed   int       `json:"failed,omitempty"`
	RTTMinMs float64   `json:"rttMinMs,omitempty"`
	RTTP50Ms float64   `json:"rttP50Ms,omitempty"`
	RTTP95Ms float64   `json:"rttP95Ms,omitempty"`
	At       string    `json:"at,omitempty"`
	Created  string    `json:"created"`
}

func (t *AddressTest) Doc() AddressTestDoc {
	d := AddressTestDoc{ID: t.ID, Address: t.Address, State: TestPending, Created: kernel.RFC(t.Created)}
	if t.Done {
		d.Samples, d.Failed, d.RTTMinMs, d.RTTP50Ms, d.RTTP95Ms, d.At = t.Samples, t.Failed, t.Min, t.P50, t.P95, kernel.RFC(t.At)
		d.State = TestReachable
		if t.Failed >= t.Samples {
			d.State = TestUnreachable
		}
	}
	return d
}
