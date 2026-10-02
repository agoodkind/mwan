package observation

import "goodkind.io/mwan/internal/observation/contract"

// ProviderIngress identifies an independently captured provider path.
type ProviderIngress = contract.ProviderIngress

// DistributionPlan requires current provider eligibility and steering policy.
type DistributionPlan = contract.DistributionPlan

// DistributionCalibration records eligible providers before sample acceptance.
type DistributionCalibration = contract.DistributionCalibration

// CalibratedProvider records a provider's expected share under the current policy.
type CalibratedProvider = contract.CalibratedProvider

// TCPIngress records the packet tuple and kernel capture timestamp.
type TCPIngress = contract.TCPIngress

// CaptureReady requires capture startup before any distribution request.
type CaptureReady = contract.CaptureReady

// ProviderShare compares captured requests with the calibrated expectation.
type ProviderShare = contract.ProviderShare
