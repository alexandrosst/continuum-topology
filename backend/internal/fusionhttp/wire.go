package fusionhttp

import (
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/wire"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: fusionapi.AppTopology{}},
		{Value: fusionapi.Fused{}},
		{Value: fusionapi.Overview{}},
		{Value: fusionapi.Trace{}},
		{Value: fusionapi.TraceSummary{}},
		{Value: logsDoc{}},
		{Value: metricNamesDoc{}},
		{Value: metricSeriesDoc{}},
		{Value: metricRangeDoc{}},
		{Value: statusDoc{}},
		{Value: applicationsDoc{}},
		{Value: servicesDoc{}},
		{Value: tracesDoc{}},
		{Value: traceBatchDoc{}},
	}
}
