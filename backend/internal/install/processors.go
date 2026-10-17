package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The OTel Collector processors a person can add to an install, in a small common-case catalog (filter, tail sampling, transform), each
// compiling to the processor body the chart's extraProcessors/extraProcessorNames escape hatch accepts. This only ever produces what a
// person could have typed into that escape hatch by hand, with validation in front of it. A raw body on an entry replaces the typed
// config, for what a catalog this small cannot express.
//
// One rule that is not obvious from the chart alone: tail_sampling only implements the traces processor interface, so referencing it from
// a metrics or logs pipeline makes the collector refuse to start. It is routed to the traces-only list; filter and transform implement
// every signal's interface and go in the shared one.

// ProcessorKind is which processor an entry is.
type ProcessorKind string

const (
	Filter       ProcessorKind = "filter"
	TailSampling ProcessorKind = "tailSampling"
	Transform    ProcessorKind = "transform"
)

// Signal is the kind of record a filter or transform acts on: metric, log or trace.
type Signal string

// FilterCondition drops a record when it holds. Conditions of one filter are ORed together.
type FilterCondition struct {
	// Field is the attribute's name; Level says whose: the record's own (the default: a data point's, a log's or a span's) or the
	// resource's (k8s.namespace.name, service.name and the like). The collector needs it spelled out.
	Field string `json:"field"`
	// Op is "eq", "neq" or "matches" (a regular expression).
	Op    string `json:"op"`
	Value string `json:"value"`
	Level string `json:"level"`
}

// FilterConfig drops the records of one signal that match a condition.
type FilterConfig struct {
	Signal     Signal            `json:"signal"`
	Conditions []FilterCondition `json:"conditions"`
}

// SamplingPolicy is one rule of tail sampling.
type SamplingPolicy struct {
	Name string `json:"name"`
	// Type is "probabilistic", "statusCode" or "latency".
	Type                 string  `json:"type"`
	ProbabilisticPercent float64 `json:"probabilisticPercent"`
	// StatusCodes are comma-separated, for the statusCode type.
	StatusCodes        string  `json:"statusCodes"`
	LatencyThresholdMs float64 `json:"latencyThresholdMs"`
}

// TailSamplingConfig keeps a subset of traces by policy once a trace is complete.
type TailSamplingConfig struct {
	DecisionWaitSeconds float64          `json:"decisionWaitSeconds"`
	Policies            []SamplingPolicy `json:"policies"`
}

// TransformStatement sets or deletes an attribute.
type TransformStatement struct {
	// Action is "set" or "delete".
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// TransformConfig changes the attributes of the records of one signal.
type TransformConfig struct {
	Signal     Signal               `json:"signal"`
	Statements []TransformStatement `json:"statements"`
}

// Processor is one extra processor of an install.
type Processor struct {
	// ID identifies an entry that has no name yet.
	ID   string        `json:"id"`
	Kind ProcessorKind `json:"kind"`
	// Name is a short slug identifying this processor ("drop_debug"), combined with its kind's OTel type into the key it has in the
	// collector's configuration. It must be unique among an install's processors.
	Name string `json:"name"`
	// Raw is a processor body as JSON, used in place of the typed config when it is not empty.
	Raw          string             `json:"raw"`
	Filter       FilterConfig       `json:"-"`
	TailSampling TailSamplingConfig `json:"-"`
	Transform    TransformConfig    `json:"-"`
}

// UnmarshalJSON reads the entry's config as the kind says it is.
func (p *Processor) UnmarshalJSON(data []byte) error {
	type plain Processor
	var raw struct {
		plain
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = Processor(raw.plain)
	if len(raw.Config) == 0 || string(raw.Config) == "null" {
		return nil
	}
	var into any
	switch p.Kind {
	case Filter:
		into = &p.Filter
	case TailSampling:
		into = &p.TailSampling
	case Transform:
		into = &p.Transform
	default:
		return fmt.Errorf("install: %q is not a processor kind", p.Kind)
	}
	return json.Unmarshal(raw.Config, into)
}

var baseType = map[ProcessorKind]string{Filter: "filter", TailSampling: "tail_sampling", Transform: "transform"}

var notSlug = regexp.MustCompile(`[^a-z0-9_]+`)

func slug(s string) string {
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_"), "_")
}

// Key is the key this processor has in the collector configuration's processors map, and the name referenced in whichever pipelines it
// is routed to. It falls back to the entry's id when it has no name yet, so an unnamed draft still renders to something valid instead of
// colliding with another unnamed one.
func (p Processor) Key() string {
	s := slug(p.Name)
	if s == "" {
		s = p.ID
	}
	return baseType[p.Kind] + "/" + s
}

// Target is which list of extra processors the entry belongs in: the traces-only one for tail sampling, the shared one otherwise.
func (p Processor) Target() string {
	if p.Kind == TailSampling {
		return "extraTracesProcessorNames"
	}
	return "extraProcessorNames"
}

// An object is a JSON object that keeps the order its fields were added in, as the commands always wrote them.
type (
	field struct {
		k string
		v any
	}
	object []field
)

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonText(f.k) + ":" + jsonText(f.v))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o object) get(k string) (any, bool) {
	for _, f := range o {
		if f.k == k {
			return f.v, true
		}
	}
	return nil, false
}

func (o *object) set(k string, v any) {
	for i := range *o {
		if (*o)[i].k == k {
			(*o)[i].v = v
			return
		}
	}
	*o = append(*o, field{k, v})
}

// ordered reads a JSON document keeping the order of every object's fields.
func ordered(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	v, err := orderedValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("unexpected data after the document")
	}
	return v, nil
}

func orderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if d == '[' {
		out := []any{}
		for dec.More() {
			v, err := orderedValue(dec)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		_, err := dec.Token()
		return out, err
	}
	out := object{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		v, err := orderedValue(dec)
		if err != nil {
			return nil, err
		}
		out.set(k.(string), v) // a repeated field keeps its first place and its last value, as JSON.parse does
	}
	_, err = dec.Token()
	return out, err
}

var (
	conditionField = map[Signal]string{"metric": "metric_conditions", "log": "log_conditions", "trace": "trace_conditions"}
	statementField = map[Signal]string{"metric": "metric_statements", "log": "log_statements", "trace": "trace_statements"}
	// The OTTL context a record's own attributes live in, per signal: the collector's filter processor refuses a bare attributes[...]
	// ("unable to infer context"), so every path starts with one.
	recordContext = map[Signal]string{"metric": "datapoint", "log": "log", "trace": "span"}
	signalOrder   = []Signal{"metric", "log", "trace"}
)

func compileCondition(c FilterCondition, s Signal) string {
	ctx := recordContext[s]
	if c.Level == "resource" {
		ctx = "resource"
	}
	attr := ctx + ".attributes[" + jsonText(c.Field) + "]"
	switch c.Op {
	case "eq":
		return attr + " == " + jsonText(c.Value)
	case "neq":
		return attr + " != " + jsonText(c.Value)
	}
	return "IsMatch(" + attr + ", " + jsonText(c.Value) + ")"
}

func compileStatement(s TransformStatement) string {
	if s.Action == "set" {
		return "set(attributes[" + jsonText(s.Key) + "], " + jsonText(s.Value) + ")"
	}
	return "delete_key(attributes, " + jsonText(s.Key) + ")"
}

func number(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// body is the processor's configuration as the collector reads it: the raw override as it was typed, or the typed config compiled.
func (p Processor) body() any {
	if strings.TrimSpace(p.Raw) != "" {
		v, err := ordered(p.Raw)
		if err != nil {
			return object{} // Problems says so; this stays a best effort and never fails mid-render
		}
		return v
	}
	switch p.Kind {
	case Filter:
		conds := []string{}
		for _, c := range p.Filter.Conditions {
			conds = append(conds, compileCondition(c, p.Filter.Signal))
		}
		return object{{"error_mode", "ignore"}, {conditionField[p.Filter.Signal], conds}}
	case TailSampling:
		policies := []any{}
		for _, pol := range p.TailSampling.Policies {
			switch pol.Type {
			case "probabilistic":
				policies = append(policies, object{{"name", pol.Name}, {"type", "probabilistic"}, {"probabilistic", object{{"sampling_percentage", json.Number(number(pol.ProbabilisticPercent))}}}})
			case "statusCode":
				codes := []string{}
				for _, c := range strings.Split(pol.StatusCodes, ",") {
					if c = strings.TrimSpace(c); c != "" {
						codes = append(codes, c)
					}
				}
				policies = append(policies, object{{"name", pol.Name}, {"type", "status_code"}, {"status_code", object{{"status_codes", codes}}}})
			default:
				policies = append(policies, object{{"name", pol.Name}, {"type", "latency"}, {"latency", object{{"threshold_ms", json.Number(number(pol.LatencyThresholdMs))}}}})
			}
		}
		return object{{"decision_wait", number(p.TailSampling.DecisionWaitSeconds) + "s"}, {"policies", policies}}
	}
	stmts := []string{}
	for _, s := range p.Transform.Statements {
		stmts = append(stmts, compileStatement(s))
	}
	return object{{statementField[p.Transform.Signal], []any{object{{"context", recordContext[p.Transform.Signal]}, {"statements", stmts}}}}}
}

// Bodies are the whole `telemetry.processors.extraProcessors` map: every entry's body, keyed by its Key, whichever list it is routed to.
// For an upgrade a map in the command is MERGED with the one installed before, key by key: a filter that moves from metrics to logs would
// keep its old metric_conditions beside the new log_conditions. So with upgrade the fields a body does not use are stated as an empty
// list, which replaces them (not null: a null deletes the key from the merge only when there is an earlier one to delete, and is
// otherwise left in the values, where the collector refuses a null that should be a list). The chart's own default has none of these, so
// on a fresh install they are no-ops. A raw body is the person's own and is stated as typed.
func Bodies(ps []Processor, upgrade bool) string {
	out := object{}
	for _, p := range ps {
		body := p.body()
		if o, ok := body.(object); ok && upgrade && strings.TrimSpace(p.Raw) == "" {
			var fields map[Signal]string
			switch p.Kind {
			case Filter:
				fields = conditionField
			case Transform:
				fields = statementField
			}
			for _, s := range signalOrder {
				if f, ok := fields[s]; ok {
					if _, has := o.get(f); !has {
						o = append(o, field{f, []string{}})
					}
				}
			}
			body = o
		}
		out.set(p.Key(), body)
	}
	return jsonText(out)
}

func validRegex(v string) bool {
	_, err := regexp.Compile(v)
	return err == nil
}

// ProcessorProblems says what is wrong with the processor list, in words a person can act on; none when it is fine. A regular expression
// is checked as the collector reads it (RE2), which has no lookahead or backreference.
func ProcessorProblems(ps []Processor) []string {
	var out []string
	count := map[string]int{}
	var keys []string
	for _, p := range ps {
		key := p.Key()
		if count[key] == 0 {
			keys = append(keys, key)
		}
		count[key]++
		who := p.Name
		if who == "" {
			who = p.ID
		}
		if strings.TrimSpace(p.Name) == "" {
			out = append(out, "Every processor needs a name.")
		}
		switch {
		case strings.TrimSpace(p.Raw) != "":
			v, err := ordered(p.Raw)
			if err != nil {
				out = append(out, fmt.Sprintf("\"%s\"'s raw override is not valid JSON.", who))
			} else if _, ok := v.(object); !ok {
				out = append(out, fmt.Sprintf("\"%s\"'s raw override must be a JSON object ({ ... }).", who))
			}
		case p.Kind == Filter && len(p.Filter.Conditions) == 0:
			out = append(out, fmt.Sprintf("\"%s\" (filter) has no conditions - it would drop nothing.", who))
		case p.Kind == Filter && anyCondition(p.Filter.Conditions, func(c FilterCondition) bool { return strings.TrimSpace(c.Field) == "" }):
			out = append(out, fmt.Sprintf("\"%s\" (filter) has a condition with no attribute name.", who))
		case p.Kind == Filter && anyCondition(p.Filter.Conditions, func(c FilterCondition) bool { return c.Op == "matches" && !validRegex(c.Value) }):
			out = append(out, fmt.Sprintf("\"%s\" (filter) has a regular expression that is not valid.", who))
		case p.Kind == TailSampling && len(p.TailSampling.Policies) == 0:
			out = append(out, fmt.Sprintf("\"%s\" (tail sampling) has no policies - it would sample nothing.", who))
		case p.Kind == Transform && len(p.Transform.Statements) == 0:
			out = append(out, fmt.Sprintf("\"%s\" (transform) has no statements - it would do nothing.", who))
		case p.Kind == Transform && anyStatement(p.Transform.Statements, func(s TransformStatement) bool { return strings.TrimSpace(s.Key) == "" }):
			out = append(out, fmt.Sprintf("\"%s\" (transform) has a statement with no attribute name.", who))
		}
	}
	for _, k := range keys {
		if count[k] > 1 {
			out = append(out, fmt.Sprintf("More than one processor renders to \"%s\" - give each a distinct name.", k))
		}
	}
	return out
}

func anyCondition(cs []FilterCondition, f func(FilterCondition) bool) bool {
	for _, c := range cs {
		if f(c) {
			return true
		}
	}
	return false
}

func anyStatement(ss []TransformStatement, f func(TransformStatement) bool) bool {
	for _, s := range ss {
		if f(s) {
			return true
		}
	}
	return false
}
