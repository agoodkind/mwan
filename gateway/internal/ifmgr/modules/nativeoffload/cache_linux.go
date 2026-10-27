//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"container/list"
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

func (cache *observationCache) admit(observation packetObservation, now time.Time, interval time.Duration) bool {
	key := observationKey{tuple: observation.Tuple, ifindex: observation.ifindex, flowLabel: observation.FlowLabel, trafficClass: observation.TrafficClass}
	if element, exists := cache.entries[key]; exists {
		previous := element.Value.(observationAttempt)
		if now.Sub(previous.at) < interval {
			return false
		}
		element.Value = observationAttempt{key: key, at: now}
		cache.order.MoveToFront(element)
		return true
	}
	if len(cache.entries) == cache.capacity {
		oldest := cache.order.Back()
		delete(cache.entries, oldest.Value.(observationAttempt).key)
		cache.order.Remove(oldest)
	}
	cache.entries[key] = cache.order.PushFront(observationAttempt{key: key, at: now})
	return true
}
