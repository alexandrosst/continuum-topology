package workspace

import "continuum/internal/model"

// The records a person declares and no agent observes. The workspace document is kept and replayed as JSON without
// knowing these shapes, on purpose: a document written by a newer client keeps every field it has. They are written
// down here once so that the TypeScript the page uses for them is generated and not copied, and the page's own
// declarations are held to them at compile time (src/lib/typesAgree.ts).

// SiteKind is what a site is.
type SiteKind string

const (
	SiteCloudRegion SiteKind = "cloud-region"
	SiteDataCenter  SiteKind = "data-center"
	SiteEdge        SiteKind = "edge-site"
)

// WireWords is the list of SiteKind's words, as the generated types give it.
func (SiteKind) WireWords() []string {
	return []string{string(SiteCloudRegion), string(SiteDataCenter), string(SiteEdge)}
}

// TrustZone is how far a site is trusted.
type TrustZone string

const (
	TrustPublic     TrustZone = "public"
	TrustPrivate    TrustZone = "private"
	TrustRestricted TrustZone = "restricted"
)

// WireWords is the list of TrustZone's words, as the generated types give it.
func (TrustZone) WireWords() []string {
	return []string{string(TrustPublic), string(TrustPrivate), string(TrustRestricted)}
}

// DeviceKind is what a device is.
type DeviceKind string

const (
	DeviceSensor   DeviceKind = "sensor"
	DeviceActuator DeviceKind = "actuator"
	DeviceCamera   DeviceKind = "camera"
	DevicePLC      DeviceKind = "plc"
	DeviceGateway  DeviceKind = "gateway"
	DeviceTag      DeviceKind = "tag"
	DeviceVehicle  DeviceKind = "vehicle"
	DeviceOther    DeviceKind = "other"
)

// WireWords is the list of DeviceKind's words, as the generated types give it.
func (DeviceKind) WireWords() []string {
	return []string{string(DeviceSensor), string(DeviceActuator), string(DeviceCamera), string(DevicePLC), string(DeviceGateway), string(DeviceTag), string(DeviceVehicle), string(DeviceOther)}
}

// SiteLinkSource is whether a link between sites was measured or declared.
type SiteLinkSource string

const (
	SiteLinkMeasured SiteLinkSource = "measured"
	SiteLinkDeclared SiteLinkSource = "declared"
)

// WireWords is the list of SiteLinkSource's words, as the generated types give it.
func (SiteLinkSource) WireWords() []string {
	return []string{string(SiteLinkMeasured), string(SiteLinkDeclared)}
}

// Site is a place on the map. Clusters and devices point at a site; sites are linked with measured RTT and loss.
type Site struct {
	ID      string   `json:"id"`
	OrgID   string   `json:"orgId"`
	Name    string   `json:"name"`
	Kind    SiteKind `json:"kind"`
	Lat     float64  `json:"lat"`
	Lng     float64  `json:"lng"`
	Country string   `json:"country"` // ISO 3166-1 alpha-2, e.g. "GR"
	City    string   `json:"city,omitempty"`
	// DataResidency is the jurisdiction the data must stay in (e.g. "EU"), for placement decisions.
	DataResidency string    `json:"dataResidency,omitempty"`
	TrustZone     TrustZone `json:"trustZone,omitempty"`
}

// SiteLink is a network path between two sites, measured by the agents or declared by a person.
type SiteLink struct {
	ID         string         `json:"id"`
	OrgID      string         `json:"orgId"`
	A          string         `json:"a"`
	B          string         `json:"b"`
	RTTMs      *float64       `json:"rttMs,omitempty"`
	LossPct    *float64       `json:"lossPct,omitempty"`
	Mbps       *float64       `json:"mbps,omitempty"`
	MeasuredAt string         `json:"measuredAt,omitempty"`
	Source     SiteLinkSource `json:"source"`
}

// Device is a thing that belongs to an application and is not on Kubernetes: sensors, cameras, PLCs, actuators. It is
// usually a fleet of identical units at one site, so a record has a Count and not a row for each unit.
type Device struct {
	model.Provenance
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Kind  DeviceKind `json:"kind"`
	Count int        `json:"count"`
	// ApplicationID and SiteID say which application it belongs to and where it is.
	ApplicationID string `json:"applicationId,omitempty"`
	SiteID        string `json:"siteId,omitempty"`
	// GatewayNodeID is the node it is physically attached to (a USB or CSI camera on a Jetson, a serial PLC on a gateway).
	GatewayNodeID string             `json:"gatewayNodeId,omitempty"`
	Protocol      string             `json:"protocol"` // MQTT, OPC UA, Modbus, RTSP...
	Connectivity  model.Connectivity `json:"connectivity"`
	HardwareModel string             `json:"hardwareModel,omitempty"`
	Firmware      string             `json:"firmware,omitempty"`
	Status        model.Status       `json:"status"`
	Labels        map[string]string  `json:"labels"`
}
