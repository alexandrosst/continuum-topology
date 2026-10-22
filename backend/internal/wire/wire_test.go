package wire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type Mode string

type leaf struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

type base struct {
	Created time.Time `json:"created"`
}

type secret struct{}

func (secret) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

type thing struct {
	base
	Name     string          `json:"name"`
	Count    int             `json:"count"`
	Ratio    float64         `json:"ratio,omitempty"`
	On       bool            `json:"on"`
	Mode     Mode            `json:"mode"`
	Seen     *time.Time      `json:"seen"`
	Leaf     leaf            `json:"leaf"`
	Maybe    *leaf           `json:"maybe"`
	Leaves   []leaf          `json:"leaves"`
	Extra    []leaf          `json:"extra,omitempty"`
	ByName   map[string]leaf `json:"byName"`
	Tags     []string        `json:"tags"`
	Rows     [][]int         `json:"rows"`
	Free     any             `json:"free"`
	Raw      json.RawMessage `json:"raw"`
	Opaque   secret          `json:"opaque"`
	Data     []byte          `json:"data"`
	Big      int64           `json:"big,string"`
	Skipped  string          `json:"-"`
	hidden   string
	Untagged string
	Dotted   string `json:"a.b"`
	Next     *thing `json:"next,omitempty"`
}

func TestEveryKindIsWrittenAsEncodingJSONWritesIt(t *testing.T) {
	got, err := Generate([]Root{{Name: "Thing", Value: thing{}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"export interface Thing {":       "",
		"  created: string":              "an embedded struct's fields are written into the struct that embeds it",
		"  name: string":                 "",
		"  count: number":                "",
		"  ratio?: number":               "omitempty makes it optional",
		"  on: boolean":                  "",
		"  mode: string":                 "a named string is a string",
		"  seen: string | null":          "a nil pointer is null",
		"  leaf: Leaf":                   "",
		"  maybe: Leaf | null":           "",
		"  leaves: Leaf[]":               "a nil slice is written [], not null",
		"  extra?: Leaf[]":               "",
		"  byName: Record<string, Leaf>": "",
		"  tags: string[]":               "",
		"  rows: number[][]":             "",
		"  free: unknown":                "an interface is unknown, and may be null",
		"  raw: unknown":                 "json.RawMessage is a document",
		"  opaque: unknown":              "a type that marshals itself cannot be read",
		"  data: string":                 "bytes are base64",
		"  big: string":                  ",string writes a number as a string",
		"  Untagged: string":             "an untagged field is written under its Go name",
		"  'a.b': string":                "a key that is not an identifier is quoted",
		"  next?: Thing":                 "a recursive type refers to itself",
		"export interface Leaf {":        "",
		"  note?: string":                "",
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(got, "\n") {
		lines[l] = true
	}
	for line, why := range want {
		if !lines[line] {
			t.Errorf("the generated file has no %q (%s):\n%s", line, why, got)
		}
	}
	for _, not := range []string{"Skipped", "hidden"} {
		if strings.Contains(got, not) {
			t.Errorf("%s is not encoded and must not be written:\n%s", not, got)
		}
	}
	if !strings.HasPrefix(got, Header) {
		t.Error("the file does not open with its header")
	}
}

type a1 struct{ X string }
type a2 struct{ Y string }

func TestNamesThatCollideAreQualifiedByTheirPackageAndExplicitOnesWin(t *testing.T) {
	_, err := Generate([]Root{{Name: "Same", Value: a1{}}, {Name: "Same", Value: a2{}}})
	if err == nil {
		t.Fatal("two types called Same must be refused")
	}
	if _, err := Generate([]Root{{Name: "One", Value: a1{}}, {Name: "Two", Value: a1{}}}); err == nil {
		t.Fatal("one type given two names must be refused")
	}
	if _, err := Generate([]Root{{Value: 3}}); err == nil {
		t.Fatal("a root that is not a struct must be refused")
	}
}

func TestOutputDoesNotDependOnTheOrderOfTheRoots(t *testing.T) {
	x, _ := Generate([]Root{{Value: a1{}}, {Value: a2{}}})
	y, _ := Generate([]Root{{Value: a2{}}, {Value: a1{}}})
	if x != y {
		t.Fatalf("the order of the roots changed the file:\n%s\n---\n%s", x, y)
	}
}

type Level string

func (Level) WireWords() []string { return []string{"low", "mid", "high"} }

type gauge struct {
	Now   Level   `json:"now"`
	Limit *Level  `json:"limit"`
	Past  []Level `json:"past"`
	Opt   Level   `json:"opt,omitempty"`
	Raw   string  `json:"raw"`
}

type bareLevel string

func (bareLevel) WireWords() []string { return nil }

type scope string

// An empty word is a word: "" is what a bearer operator's CA scope reads, and the union says so.
func (scope) WireWords() []string { return []string{"operator", "org", ""} }

type holdsScope struct {
	S scope `json:"s"`
}

type twice string

func (twice) WireWords() []string { return []string{"a", "a"} }

type quote string

func (quote) WireWords() []string { return []string{"it's"} }

type holds struct {
	B bareLevel `json:"b"`
}
type holdsTwice struct {
	T twice `json:"t"`
}
type holdsQuote struct {
	Q quote `json:"q"`
}

func TestAStringTypeThatListsItsWordsIsTheUnionOfThem(t *testing.T) {
	got, err := Generate([]Root{{Name: "Gauge", Value: gauge{}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"export type Level = 'low' | 'mid' | 'high'",
		"  now: Level",
		"  limit: Level | null",
		"  past: Level[]",
		"  opt?: Level",
		"  raw: string",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("the generated file has no %q:\n%s", line, got)
		}
	}
}

func TestAWordListThatCannotBeAUnionIsRefused(t *testing.T) {
	for name, v := range map[string]any{"empty": holds{}, "twice": holdsTwice{}, "quote": holdsQuote{}} {
		if _, err := Generate([]Root{{Value: v}}); err == nil {
			t.Errorf("%s: a list of words that cannot be written as a union must be refused", name)
		}
	}
}

// Shade has the name of the fixture's type in testdata/words, which RequireWordsComplete finds by name.
type Shade string

func (Shade) WireWords() []string { return []string{"red", "blue"} }

func TestEveryConstantOfAWordsTypeIsListedAndEveryWordHasOne(t *testing.T) {
	problems, err := wordsProblems("testdata/words", []Words{Shade("")})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("want one problem for the constant that is not listed and one for the word without a constant, got %q", problems)
	}
	if all := strings.Join(problems, "\n"); !strings.Contains(all, `"blue", which no constant`) || !strings.Contains(all, `"green" is not among`) {
		t.Errorf("the problems name the wrong words: %q", problems)
	}
}

func TestTheEmptyStringCanBeAWord(t *testing.T) {
	got, err := Generate([]Root{{Value: holdsScope{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "export type Scope = 'operator' | 'org' | ''\n") || !strings.Contains(got, "  s: Scope\n") {
		t.Errorf("the empty word is missing:\n%s", got)
	}
}
