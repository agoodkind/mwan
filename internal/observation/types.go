package observation

import (
	"net/netip"
	"slices"
	"time"
)

// Operation selects the production probe or provider observation.
type Operation string

const (
	// OperationHTTP requires configured HTTP status and body semantics.
	OperationHTTP Operation = "http"
	// OperationDNS requires addresses from the configured resolver.
	OperationDNS Operation = "dns"
	// OperationPing requires an ICMP echo reply.
	OperationPing Operation = "ping"
	// OperationPublicIP records the externally reported source address.
	OperationPublicIP Operation = "public_ip"
	// OperationDistribution requires independent provider distribution observations.
	OperationDistribution Operation = "distribution"
	// OperationCloudflarePool reads provider pool health.
	OperationCloudflarePool Operation = "cloudflare_pool"
	// OperationSSHBanner requires the advertised SSH server version, without authentication.
	OperationSSHBanner Operation = "ssh_banner"
)

// Dimension identifies the independent health requirement being observed.
type Dimension string

const (
	// DimensionDownstreamApplication checks a downstream client application reply.
	DimensionDownstreamApplication Dimension = "downstream_application"
	// DimensionInboundApplication checks an inbound application reply.
	DimensionInboundApplication Dimension = "inbound_application"
	// DimensionConnectionDistribution checks provider selection across new requests.
	DimensionConnectionDistribution Dimension = "connection_distribution"
	// DimensionInboundPool checks an external load balancer pool.
	DimensionInboundPool Dimension = "inbound_pool"
	// DimensionProviderEgress checks a configured provider path.
	DimensionProviderEgress Dimension = "provider_egress"
	// DimensionPingPath checks an ICMP source and selected kernel route.
	DimensionPingPath Dimension = "ping_path"
	// DimensionPublicIP checks the externally reported address independently of balancing.
	DimensionPublicIP Dimension = "public_ip"
)

// Family restricts both the request and its acceptance to one IP version.
type Family string

const (
	// FamilyIPv4 requires IPv4 execution and results.
	FamilyIPv4 Family = "ipv4"
	// FamilyIPv6 requires IPv6 execution and results.
	FamilyIPv6 Family = "ipv6"
)

// Router classifies an observed next hop using current path metadata.
type Router string

const (
	// RouterPrimary identifies the configured primary next hop.
	RouterPrimary Router = "primary"
	// RouterBackup identifies the configured backup next hop.
	RouterBackup Router = "backup"
)

// PublicIPPolicy selects external family checking or current assignment checking.
type PublicIPPolicy string

const (
	// PublicIPFamilyOnly accepts the reported family without provider-prefix attribution.
	PublicIPFamilyOnly PublicIPPolicy = "family_only"
	// PublicIPCurrentAssignment requires a current valid provider assignment.
	PublicIPCurrentAssignment PublicIPPolicy = "current_assignment"
)

// EndpointKind selects local, QEMU, or LXC execution.
type EndpointKind string

const (
	// EndpointLocal executes on the verified local machine.
	EndpointLocal EndpointKind = "local"
	// EndpointQEMU executes through the existing guest-agent transport.
	EndpointQEMU EndpointKind = "qemu"
	// EndpointLXC executes inside the configured container with fixed arguments.
	EndpointLXC EndpointKind = "lxc"
)

// Endpoint requires the actual observer and hypervisor machine identities.
type Endpoint struct {
	Kind          EndpointKind `json:"kind"`
	VMID          int          `json:"vmid,omitempty"`
	MachineID     string       `json:"machine_id"`
	HostMachineID string       `json:"host_machine_id,omitempty"`
}

// CheckSpec defines a positive reply and the required observer and path.
type CheckSpec struct {
	ID                        string         `json:"id"`
	Dimension                 Dimension      `json:"dimension"`
	Operation                 Operation      `json:"operation"`
	Observer                  Endpoint       `json:"observer"`
	Family                    Family         `json:"family"`
	ConnectionID              string         `json:"connection_id,omitempty"`
	Router                    Router         `json:"router,omitempty"`
	Interface                 string         `json:"interface,omitempty"`
	Source                    netip.Addr     `json:"source,omitzero"`
	Target                    string         `json:"target"`
	TimeoutSeconds            int            `json:"timeout_seconds"`
	MaxAgeSeconds             int            `json:"max_age_seconds"`
	HTTPMethod                string         `json:"http_method,omitempty"`
	ExpectedHTTPStatus        []int          `json:"expected_http_status,omitempty"`
	ExpectedBody              string         `json:"expected_body,omitempty"`
	ExpectedNextHop           netip.Addr     `json:"expected_next_hop,omitzero"`
	DNSExpectedAddresses      []netip.Addr   `json:"dns_expected_addresses,omitempty"`
	DNSServer                 string         `json:"dns_server,omitempty"`
	ExpectedSSHVersion        string         `json:"expected_ssh_version,omitempty"`
	CloudflarePoolID          string         `json:"cloudflare_pool_id,omitempty"`
	CloudflareExpectedOrigins []string       `json:"cloudflare_expected_origins,omitempty"`
	DistributionSamples       int            `json:"distribution_samples,omitempty"`
	PublicIPPolicy            PublicIPPolicy `json:"public_ip_policy,omitempty"`
}

// Availability separates a performed observation from missing or unusable evidence.
type Availability string

const (
	// AvailabilityComplete indicates that the target probe produced a verdict.
	AvailabilityComplete Availability = "complete"
	// AvailabilityMissing indicates that required evidence is unavailable.
	AvailabilityMissing Availability = "missing"
	// AvailabilityStale indicates that an observation has expired.
	AvailabilityStale Availability = "stale"
	// AvailabilityError indicates an observer or execution failure.
	AvailabilityError Availability = "error"
)

// Outcome distinguishes a healthy reply from failure or an unknown verdict.
type Outcome string

const (
	// OutcomePass indicates the required response and path were observed.
	OutcomePass Outcome = "pass"
	// OutcomeFail indicates an observed target or response failure.
	OutcomeFail Outcome = "fail"
	// OutcomeUnknown indicates that available evidence cannot determine health.
	OutcomeUnknown Outcome = "unknown"
)

// Path records socket addresses and the subsequently queried kernel route.
type Path struct {
	Source       netip.Addr `json:"source,omitzero"`
	Destination  netip.Addr `json:"destination,omitzero"`
	NextHop      netip.Addr `json:"next_hop,omitzero"`
	Interface    string     `json:"interface,omitempty"`
	ConnectionID string     `json:"connection_id,omitempty"`
	Router       Router     `json:"router,omitempty"`
}

// DistributionSample records one request and its measured provider association.
type DistributionSample struct {
	At           time.Time  `json:"at"`
	PublicIP     netip.Addr `json:"public_ip"`
	ConnectionID string     `json:"connection_id"`
	HTTPStatus   int        `json:"http_status"`
}

// Result contains one observation with its endpoint, family, and reply evidence.
type Result struct {
	CheckID        string               `json:"check_id"`
	Dimension      Dimension            `json:"dimension"`
	Operation      Operation            `json:"operation"`
	Target         string               `json:"target"`
	PublicIPPolicy PublicIPPolicy       `json:"public_ip_policy,omitempty"`
	Family         Family               `json:"family"`
	Observer       Endpoint             `json:"observer"`
	ObservedAt     time.Time            `json:"observed_at"`
	Availability   Availability         `json:"availability"`
	Outcome        Outcome              `json:"outcome"`
	Reason         string               `json:"reason"`
	Path           Path                 `json:"path"`
	HTTPStatus     int                  `json:"http_status,omitempty"`
	ResponseBody   string               `json:"response_body,omitempty"`
	DNSAddresses   []netip.Addr         `json:"dns_addresses,omitempty"`
	PublicIP       netip.Addr           `json:"public_ip,omitzero"`
	SSHVersion     string               `json:"ssh_version,omitempty"`
	Distribution   []DistributionSample `json:"distribution,omitempty"`
	CloudflarePool *PoolHealth          `json:"cloudflare_pool,omitempty"`
}

// RequiredPassed rejects replies from another endpoint, family, target, or expired check.
func RequiredPassed(spec CheckSpec, result Result, now time.Time) bool {
	if result.CheckID != spec.ID || result.Dimension != spec.Dimension ||
		result.Operation != spec.Operation || result.Target != spec.Target || result.PublicIPPolicy != spec.PublicIPPolicy || result.Family != spec.Family || result.Observer != spec.Observer {
		return false
	}
	if result.Availability != AvailabilityComplete || result.Outcome != OutcomePass ||
		result.ObservedAt.IsZero() || spec.MaxAgeSeconds <= 0 {
		return false
	}
	age := now.Sub(result.ObservedAt)
	if age < 0 || age > time.Duration(spec.MaxAgeSeconds)*time.Second {
		return false
	}
	return requiredPathPassed(spec, result) && requiredReplyPassed(spec, result)
}

func requiredPathPassed(spec CheckSpec, result Result) bool {
	if spec.Router != "" && result.Path.Router != spec.Router {
		return false
	}
	if spec.ConnectionID != "" && result.Path.ConnectionID != spec.ConnectionID {
		return false
	}
	if spec.Source.IsValid() && result.Path.Source != spec.Source {
		return false
	}
	if spec.ExpectedNextHop.IsValid() && result.Path.NextHop != spec.ExpectedNextHop {
		return false
	}
	if spec.Interface != "" && result.Path.Interface != spec.Interface {
		return false
	}
	return true
}

func requiredReplyPassed(spec CheckSpec, result Result) bool {
	if spec.Operation == OperationCloudflarePool {
		return result.CloudflarePool != nil && poolOriginsPresent(spec, *result.CloudflarePool) && poolHealthy(spec, *result.CloudflarePool)
	}
	if spec.Operation == OperationHTTP || spec.Operation == OperationPublicIP {
		if !slices.Contains(spec.ExpectedHTTPStatus, result.HTTPStatus) || (spec.ExpectedBody != "" && result.ResponseBody != spec.ExpectedBody) {
			return false
		}
	}
	if spec.Operation == OperationSSHBanner && result.SSHVersion != spec.ExpectedSSHVersion {
		return false
	}
	if spec.Operation == OperationDNS {
		for _, address := range spec.DNSExpectedAddresses {
			if !slices.Contains(result.DNSAddresses, address) {
				return false
			}
		}
	}
	return true
}
