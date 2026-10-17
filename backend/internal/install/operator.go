package install

// OperatorProcessors are the Helm values that set the regional operator chart's extra processors, in the order a person added them: the
// bodies in one value and each list of names whole (a list replaces what an earlier command set, where an indexed name would leave the tail
// of a longer one in place). Only the lists that have something in them are stated. The chart has no `telemetry` root, so the path is
// `processors.*`, otherwise the same as an agent's.
//
// Processor tuning is chart-values-only and is never stored on the operator, so it cannot be part of the command the server prints for the
// operator; it is added to that command. Nothing to add is no values; processors with a problem are an error that says what is wrong.
func OperatorProcessors(ps []Processor) ([]Value, error) {
	if len(ps) == 0 {
		return nil, nil
	}
	if p := ProcessorProblems(ps); len(p) > 0 {
		return nil, &ProblemsError{Problems: p}
	}
	v := []Value{SetJSON("processors.extraProcessors", Bodies(ps, false))}
	byTarget := map[string][]string{}
	for _, p := range ps {
		byTarget[p.Target()] = append(byTarget[p.Target()], p.Key())
	}
	for _, target := range []string{"extraProcessorNames", "extraTracesProcessorNames"} {
		if keys := byTarget[target]; len(keys) > 0 {
			v = append(v, Strings("processors."+target, keys))
		}
	}
	return v, nil
}
