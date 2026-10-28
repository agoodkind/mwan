//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"container/list"
	"fmt"
	"time"

	"github.com/ti-mo/conntrack"
)

type observationKey struct {
	tuple        conntrack.Tuple
	ifindex      int
	flowLabel    uint32
	trafficClass uint8
}

type observationAttempt struct {
	key observationKey
	at  time.Time
}

type observationCache struct {
	entries  map[observationKey]*list.Element
	order    list.List
	capacity int
}

func (cache *observationCache) admit(observation packetObservation, now time.Time, interval time.Duration) (bool, error) {
	if cache.capacity <= 0 {
		return false, fmt.Errorf("native observation cache capacity must be positive")
	}
	if cache.entries == nil {
		return false, fmt.Errorf("native observation cache lacks its entry index")
	}
	if len(cache.entries) > cache.capacity {
		return false, fmt.Errorf("native observation cache exceeds its configured capacity")
	}
	key := observationKey{tuple: observation.Tuple, ifindex: observation.ifindex, flowLabel: observation.FlowLabel, trafficClass: observation.TrafficClass}
	if element, exists := cache.entries[key]; exists {
		previous, ok := element.Value.(observationAttempt)
		if !ok {
			return false, fmt.Errorf("native observation cache contains an invalid attempt: %T", element.Value)
		}
		if now.Sub(previous.at) < interval {
			return false, nil
		}
		element.Value = observationAttempt{key: key, at: now}
		cache.order.MoveToFront(element)
		return true, nil
	}
	if len(cache.entries) == cache.capacity {
		oldest := cache.order.Back()
		if oldest == nil {
			return false, fmt.Errorf("native observation cache lacks its oldest attempt")
		}
		previous, ok := oldest.Value.(observationAttempt)
		if !ok {
			return false, fmt.Errorf("native observation cache contains an invalid oldest attempt: %T", oldest.Value)
		}
		delete(cache.entries, previous.key)
		cache.order.Remove(oldest)
	}
	cache.entries[key] = cache.order.PushFront(observationAttempt{key: key, at: now})
	return true, nil
}
