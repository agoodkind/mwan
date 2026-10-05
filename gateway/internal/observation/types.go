package observation

import (
	"slices"
	"time"

	"goodkind.io/mwan/internal/observation/contract"
)

// Operation selects the production probe or provider observation.
type Operation = contract.Operation

// Dimension separates application and provider acceptance evidence.
type Dimension = contract.Dimension

// Family requires independent IPv4 and IPv6 observations.
type Family = contract.Family

// Router identifies the expected forwarding owner.
type Router = contract.Router

// PublicIPPolicy optionally requires the current provider assignment.
type PublicIPPolicy = contract.PublicIPPolicy

// EndpointKind selects local, virtual machine, or container identity checks.
type EndpointKind = contract.EndpointKind

// Endpoint requires the observer's independent machine identity.
type Endpoint = contract.Endpoint

// CheckSpec defines the path, reply, identity, and freshness acceptance contract.
type CheckSpec = contract.CheckSpec

// Availability distinguishes completed replies from unavailable evidence.
type Availability = contract.Availability

// Outcome separates target failures from unknown observations.
type Outcome = contract.Outcome

// Path records the source and next hop used by an actual probe.
type Path = contract.Path

// DistributionSample correlates socket identity with provider ingress capture.
type DistributionSample = contract.DistributionSample

// Result retains completed failures and their independent observer evidence.
type Result = contract.Result

const (
	// OperationHTTP requires configured HTTP status and body semantics.
	OperationHTTP = contract.OperationHTTP
	// OperationDNS requires addresses from the configured resolver.
	OperationDNS = contract.OperationDNS
	// OperationPing requires an ICMP echo reply.
	OperationPing = contract.OperationPing
	// OperationPublicIP records the externally reported source address.
	OperationPublicIP = contract.OperationPublicIP
	// OperationDistribution requires independent provider distribution observations.
	OperationDistribution = contract.OperationDistribution
	// OperationCloudflarePool reads provider pool health.
	OperationCloudflarePool = contract.OperationCloudflarePool
	// OperationSSHBanner requires the advertised SSH server version, without authentication.
	OperationSSHBanner = contract.OperationSSHBanner
	// DimensionDownstreamApplication checks a downstream client application reply.
	DimensionDownstreamApplication = contract.DimensionDownstreamApplication
	// DimensionInboundApplication checks an inbound application reply.
	DimensionInboundApplication = contract.DimensionInboundApplication
	// DimensionConnectionDistribution checks provider selection across new requests.
	DimensionConnectionDistribution = contract.DimensionConnectionDistribution
	// DimensionInboundPool checks an external load balancer pool.
	DimensionInboundPool = contract.DimensionInboundPool
	// DimensionProviderEgress checks a configured provider path.
	DimensionProviderEgress = contract.DimensionProviderEgress
	// DimensionPingPath checks an ICMP source and selected kernel route.
	DimensionPingPath = contract.DimensionPingPath
	// DimensionPublicIP checks the externally reported address independently of balancing.
	DimensionPublicIP = contract.DimensionPublicIP
	// FamilyIPv4 requires IPv4 execution and results.
	FamilyIPv4 = contract.FamilyIPv4
	// FamilyIPv6 requires IPv6 execution and results.
	FamilyIPv6 = contract.FamilyIPv6
	// RouterPrimary identifies the configured primary next hop.
	RouterPrimary = contract.RouterPrimary
	// RouterBackup identifies the configured backup next hop.
	RouterBackup = contract.RouterBackup
	// PublicIPFamilyOnly accepts the reported family without provider-prefix attribution.
	PublicIPFamilyOnly = contract.PublicIPFamilyOnly
	// PublicIPCurrentAssignment requires a current valid provider assignment.
	PublicIPCurrentAssignment = contract.PublicIPCurrentAssignment
	// EndpointLocal executes on the verified local machine.
	EndpointLocal = contract.EndpointLocal
	// EndpointQEMU executes through the existing guest-agent transport.
	EndpointQEMU = contract.EndpointQEMU
	// EndpointLXC executes inside the configured container with fixed arguments.
	EndpointLXC = contract.EndpointLXC
	// AvailabilityComplete indicates that the target probe produced a verdict.
	AvailabilityComplete = contract.AvailabilityComplete
	// AvailabilityMissing indicates that required evidence is unavailable.
	AvailabilityMissing = contract.AvailabilityMissing
	// AvailabilityStale indicates that an observation has expired.
	AvailabilityStale = contract.AvailabilityStale
	// AvailabilityError indicates an observer or execution failure.
	AvailabilityError = contract.AvailabilityError
	// OutcomePass indicates the required response and path were observed.
	OutcomePass = contract.OutcomePass
	// OutcomeFail indicates an observed target or response failure.
	OutcomeFail = contract.OutcomeFail
	// OutcomeUnknown indicates that available evidence cannot determine health.
	OutcomeUnknown = contract.OutcomeUnknown
)

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
	if spec.Operation == OperationDistribution {
		return requiredDistributionPassed(spec, result, now)
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
