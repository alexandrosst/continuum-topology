// Package observed is what the flows say. Agents report who talked to whom; this package keeps the table of those edges per agent
// (FlowTable), refuses or trims what no agent could honestly have sent (SanitizeFlowBatch, BoundFlowFacts), and reads the topology
// off the table of every cluster together (Topology): which workload depends on which, which addresses belong to which cluster
// (AddrIndex), what is outside.
//
// It is pure. It knows no agent, no hub and no store: the hub fills it and the modules that need to know where an address leads
// read it. That is why it sits under agents and connectivity rather than inside either.
package observed
