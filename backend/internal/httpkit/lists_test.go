package httpkit

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

type leaf struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type holder struct {
	Rows    []leaf              `json:"rows"`
	ByName  map[string]leaf     `json:"byName"`
	Matrix  [][]int             `json:"matrix"`
	Bytes   []byte              `json:"bytes"`
	Maybe   *leaf               `json:"maybe"`
	MaybeL  *[]string           `json:"maybeList"`
	Free    any                 `json:"free"`
	At      time.Time           `json:"at"`
	Raw     json.RawMessage     `json:"raw"`
	Omitted []string            `json:"omitted,omitempty"`
	Zeroed  []string            `json:"zeroed,omitzero"`
	Hidden  []string            `json:"-"`
	Nested  struct{ In []int }  `json:"nested"`
	Embeds  map[string][]string `json:"embeds"`
	private []string
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestANilListIsAnEmptyOneAndWhatIsNotAListStaysNull(t *testing.T) {
	in := holder{Free: map[string][]int(nil), Embeds: map[string][]string{"a": nil}, Rows: []leaf{{Name: "x"}}}
	in.Nested.In = nil
	in.private = nil
	got := encode(t, ListsAsLists(in))
	want := `{"rows":[{"name":"x","tags":[]}],"byName":{},"matrix":[],"bytes":"","maybe":null,"maybeList":null,"free":{},"at":"0001-01-01T00:00:00Z","raw":null,` +
		`"nested":{"In":[]},"embeds":{"a":[]}}`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestOmitzeroKeepsANilListOutOfTheAnswer(t *testing.T) {
	got := encode(t, ListsAsLists(struct {
		Z []string `json:"z,omitzero"`
		E []string `json:"e,omitempty"`
		K []string `json:"k"`
	}{}))
	if got != `{"k":[]}` {
		t.Errorf("got %s", got)
	}
}

func TestWhatHoldsNoNilListIsWrittenAsItWas(t *testing.T) {
	in := &holder{Rows: []leaf{{Name: "x", Tags: []string{"a"}}}, ByName: map[string]leaf{"k": {Tags: []string{}}}, Matrix: [][]int{{1}}, Bytes: []byte("hi"),
		Maybe: &leaf{Tags: []string{"t"}}, Free: []any{1, "two"}, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Raw: json.RawMessage(`{"a":1}`), Embeds: map[string][]string{}}
	in.Nested.In = []int{}
	if a, b := encode(t, in), encode(t, ListsAsLists(in)); a != b {
		t.Errorf("\n was %s\n now %s", a, b)
	}
}

func TestTheValueGivenIsNotChanged(t *testing.T) {
	in := &holder{Rows: []leaf{{Name: "x"}}, ByName: map[string]leaf{"k": {}}}
	ListsAsLists(in)
	if in.Rows[0].Tags != nil || in.ByName["k"].Tags != nil || in.Matrix != nil || in.Bytes != nil {
		t.Errorf("the original was filled in: %+v", in)
	}
}

type onPointer struct{ N int }

func (*onPointer) MarshalJSON() ([]byte, error) { return []byte(`"pointer"`), nil }

func TestAMarshalerOnThePointerIsCalledAsItWasBefore(t *testing.T) {
	// a value in a struct that is not addressable is written by its fields; copying must not make it addressable
	type doc struct {
		A onPointer `json:"a"`
		L []int     `json:"l"`
	}
	if a, b := encode(t, doc{}), encode(t, ListsAsLists(doc{})); a == b {
		t.Fatalf("the nil list should differ: %s", a)
	}
	got := encode(t, ListsAsLists(doc{}))
	if got != `{"a":{"N":0},"l":[]}` {
		t.Errorf("got %s", got)
	}
}

func TestWriteJSONSendsEmptyListsAndTheStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 201, map[string]any{"rows": []int(nil), "n": 1})
	if rec.Code != 201 || rec.Body.String() != `{"n":1,"rows":[]}`+"\n" || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = httptest.NewRecorder()
	WriteJSON(rec, 200, []string{"a"})
	if rec.Body.String() != `["a"]`+"\n" {
		t.Errorf("%q", rec.Body.String())
	}
}

func TestWhatCannotBeWrittenIsA500NotHalfAnAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 200, map[string]any{"f": func() {}})
	if rec.Code != 500 || rec.Body.String() != `{"error":"the answer could not be written"}`+"\n" {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
}
