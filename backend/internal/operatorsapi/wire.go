package operatorsapi

import (
	"continuum/internal/destination"
	"continuum/internal/wire"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: AddressProbe{}},
		{Name: "OperatorDestination", Value: destination.Doc{}},
		{Name: "OperatorDestinationEntry", Value: operatorDestinationDoc{}},
		{Name: "RegionalOperator", Value: Doc{}},
		{Value: SendersDoc{}},
		{Value: issuedCertDoc{}},
		{Value: operatorInstalledDoc{}},
		{Value: exportTargetDoc{}},
		{Value: operatorScopeChangedDoc{}},
		{Value: operatorCertificatesDoc{}},
		{Value: operatorUninstallDoc{}},
		{Value: heartbeatEnabledDoc{}},
		{Value: intentCommandDoc{}},
		{Value: createOperatorRequest{}},
		{Value: setOperatorAddressRequest{}},
		{Value: updateOperatorScopeRequest{}},
		{Value: revokeOperatorRequest{}},
		{Value: intentCommandRequest{}},
	}
}
