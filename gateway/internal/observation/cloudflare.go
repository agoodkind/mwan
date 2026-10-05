package observation

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/mwan/internal/observation/contract"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/load_balancers"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

// PoolOrigin preserves the maintained SDK origin health fields.
type PoolOrigin = contract.PoolOrigin

// PoolRegion preserves the maintained SDK regional health fields.
type PoolRegion = contract.PoolRegion

// PoolHealth preserves the maintained SDK pool health fields.
type PoolHealth = contract.PoolHealth

func (executor *Executor) cloudflarePool(ctx context.Context, spec CheckSpec, result Result) Result {
	if executor.config.CloudflareAccountID == "" || executor.config.CloudflareTokenFile == "" {
		result.Availability, result.Reason = AvailabilityMissing, "cloudflare account or protected token file is unavailable"
		return result
	}
	if !filepath.IsAbs(executor.config.CloudflareTokenFile) {
		result.Reason = "cloudflare credential file path must be absolute"
		return result
	}
	file, err := os.Open(executor.config.CloudflareTokenFile)
	if err != nil {
		result.Reason = "cloudflare credential file cannot be opened"
		return result
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		result.Reason = "cloudflare credential file requires private regular-file permissions"
		return result
	}
	tokenBytes := make([]byte, info.Size())
	if _, err := io.ReadFull(file, tokenBytes); err != nil {
		result.Reason = "cloudflare credential file cannot be read"
		return result
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		result.Reason = "cloudflare credential file is empty"
		return result
	}
	service := load_balancers.NewPoolHealthService(option.WithEnvironmentProduction(), option.WithAPIToken(token))
	reply, err := service.Get(ctx, spec.CloudflarePoolID, load_balancers.PoolHealthGetParams{AccountID: cloudflare.F(executor.config.CloudflareAccountID)})
	if err != nil {
		result.Reason = "cloudflare pool health read failed"
		return result
	}
	if reply == nil || reply.PoolID != spec.CloudflarePoolID {
		result.Reason = "cloudflare response pool identity does not match"
		return result
	}
	health, valid := decodePoolHealth(reply)
	result.CloudflarePool = &health
	if !valid {
		result.Availability, result.Reason = AvailabilityMissing, "cloudflare response omits required region or origin health"
		return result
	}
	if !poolOriginsPresent(spec, health) {
		result.Availability, result.Reason = AvailabilityMissing, "cloudflare response omits an expected origin"
		return result
	}
	result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
	if !poolHealthy(spec, health) {
		result.Reason = "cloudflare region or expected origin is unhealthy"
		return result
	}
	result.Outcome, result.Reason = OutcomePass, "cloudflare reports healthy regions and expected origins"
	return result
}

func decodePoolHealth(reply *load_balancers.PoolHealthGetResponse) (PoolHealth, bool) {
	health := PoolHealth{ID: reply.PoolID, Regions: nil}
	// The API uses dynamic region and hostname keys; the SDK retains them as raw fields.
	for name, field := range reply.POPHealth.JSON.ExtraFields {
		var region load_balancers.PoolHealthGetResponsePOPHealth
		if err := json.Unmarshal([]byte(field.Raw()), &region); err != nil || region.JSON.Healthy.IsNull() || region.JSON.Healthy.IsInvalid() {
			return health, false
		}
		observed := PoolRegion{Name: name, Healthy: region.Healthy, Origins: nil}
		for _, origin := range region.Origins {
			for hostname, value := range origin.JSON.ExtraFields {
				var status load_balancers.PoolHealthGetResponsePOPHealthOriginsIP
				if err := json.Unmarshal([]byte(value.Raw()), &status); err != nil || status.JSON.Healthy.IsNull() || status.JSON.Healthy.IsInvalid() {
					return health, false
				}
				observed.Origins = append(observed.Origins, PoolOrigin{Name: hostname, Health: status})
			}
		}
		if len(observed.Origins) == 0 {
			return health, false
		}
		slices.SortFunc(observed.Origins, func(left, right PoolOrigin) int { return strings.Compare(left.Name, right.Name) })
		health.Regions = append(health.Regions, observed)
	}
	slices.SortFunc(health.Regions, func(left, right PoolRegion) int { return strings.Compare(left.Name, right.Name) })
	return health, len(health.Regions) > 0
}

func poolHealthy(spec CheckSpec, health PoolHealth) bool {
	if health.ID != spec.CloudflarePoolID || len(health.Regions) == 0 {
		return false
	}
	for _, region := range health.Regions {
		if !region.Healthy {
			return false
		}
		for _, expected := range spec.CloudflareExpectedOrigins {
			matched := false
			for _, origin := range region.Origins {
				if origin.Name == expected && origin.Health.Healthy {
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func poolOriginsPresent(spec CheckSpec, health PoolHealth) bool {
	for _, region := range health.Regions {
		for _, expected := range spec.CloudflareExpectedOrigins {
			found := false
			for _, origin := range region.Origins {
				found = found || origin.Name == expected
			}
			if !found {
				return false
			}
		}
	}
	return true
}
