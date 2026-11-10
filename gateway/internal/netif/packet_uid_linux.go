//go:build linux

package netif

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const packetUIDMapBytes = 65536

type packetUIDExtent struct {
	inside uint32
	parent uint32
	count  uint32
}

type packetUIDMap []packetUIDExtent

type packetRouteUIDMode uint8

const (
	packetRouteUIDAbsent packetRouteUIDMode = iota
	packetRouteUIDProjected
	packetRouteUIDIndependent
)

type packetRouteUID struct {
	mode  packetRouteUIDMode
	value uint32
	rules []netlink.Rule
}

func forwardingPacketUID(handle *netlink.Handle, family int) (packetRouteUID, error) {
	var uid packetRouteUID
	if family != unix.AF_INET6 {
		return uid, nil
	}
	var query packetQuery
	query.operation = packetQueryRoute
	value, certified, err := packetIPv6InputUID(query)
	if err != nil {
		return uid, err
	}
	if certified {
		uid.mode, uid.value = packetRouteUIDProjected, value
		return uid, nil
	}
	rules, err := handle.RuleList(unix.AF_INET6)
	if err != nil {
		return uid, query.failure("inspect UID-independent IPv6 policy", err)
	}
	if packetRulesUseUID(rules) {
		return uid, &EgressObservationRequiredError{Reason: EgressObservationUIDMapping, NexthopID: 0}
	}
	uid.mode, uid.rules = packetRouteUIDIndependent, rules
	return uid, nil
}

func packetRulesUseUID(rules []netlink.Rule) bool {
	for _, rule := range rules {
		if rule.UIDRange != nil {
			return true
		}
	}
	return false
}

func (uid packetRouteUID) verify(handle *netlink.Handle) error {
	if uid.mode != packetRouteUIDIndependent {
		return nil
	}
	var query packetQuery
	query.operation = packetQueryRoute
	rules, err := handle.RuleList(unix.AF_INET6)
	if err != nil {
		return query.failure("revalidate UID-independent IPv6 policy", err)
	}
	if packetRulesUseUID(rules) {
		return &EgressObservationRequiredError{Reason: EgressObservationUIDMapping, NexthopID: 0}
	}
	if !reflect.DeepEqual(uid.rules, rules) {
		return query.failure("IPv6 policy changed during forwarding lookup", unix.ESTALE)
	}
	return nil
}

// nsfs namespace inodes retain global kernel UID zero. fstat translates that
// owner into the caller's user namespace without using the caller's UID.
func packetIPv6InputUID(query packetQuery) (uid uint32, certified bool, resultErr error) {
	mapping, err := readPacketUIDMap(query)
	if err != nil {
		return 0, false, err
	}
	if len(mapping) == 1 && mapping[0].inside == 0 && mapping[0].parent == 0 && mapping[0].count == math.MaxUint32 {
		return 0, true, nil
	}
	descriptor, err := unix.Open("/proc/thread-self/ns/user", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, false, query.failure("open IPv6 input UID namespace", err)
	}
	defer func() {
		if err := unix.Close(descriptor); err != nil {
			resultErr = errors.Join(resultErr, query.failure("close IPv6 input UID namespace", err))
		}
	}()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(descriptor, &filesystem); err != nil {
		return 0, false, query.failure("inspect input UID namespace filesystem", err)
	}
	if filesystem.Type != unix.NSFS_MAGIC {
		return 0, false, errors.New("input UID namespace descriptor does not use nsfs")
	}
	kind, err := unix.IoctlRetInt(descriptor, unix.NS_GET_NSTYPE)
	if err != nil {
		return 0, false, query.failure("inspect input UID namespace type", err)
	}
	if kind != unix.CLONE_NEWUSER {
		return 0, false, errors.New("input UID namespace descriptor is not a user namespace")
	}
	var identity unix.Stat_t
	if err := unix.Fstat(descriptor, &identity); err != nil {
		return 0, false, query.failure("translate kernel input UID", err)
	}
	// Linux permits overflowuid values only in the 16-bit UID range. Reject
	// that entire range without depending on a mutable sysctl snapshot.
	if identity.Uid <= math.MaxUint16 || !mapping.contains(identity.Uid) {
		return 0, false, nil
	}
	return identity.Uid, true, nil
}

func readPacketUIDMap(query packetQuery) (mapping packetUIDMap, resultErr error) {
	file, err := os.Open("/proc/thread-self/uid_map")
	if err != nil {
		return nil, query.failure("open caller UID map", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, query.failure("close caller UID map", err))
		}
	}()
	data, err := io.ReadAll(io.LimitReader(file, packetUIDMapBytes+1))
	if err != nil {
		return nil, query.failure("read caller UID map", err)
	}
	if len(data) > packetUIDMapBytes {
		return nil, errors.New("caller UID map exceeds its kernel text bound")
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields)%3 != 0 {
		return nil, errors.New("caller UID map requires complete three-field extents")
	}
	for offset := 0; offset < len(fields); offset += 3 {
		var values [3]uint32
		for index := range values {
			value, err := strconv.ParseUint(fields[offset+index], 10, 32)
			if err != nil {
				return nil, query.failure("decode caller UID map", err)
			}
			values[index] = uint32(value)
		}
		extent := packetUIDExtent{inside: values[0], parent: values[1], count: values[2]}
		if extent.count == 0 || uint64(extent.inside)+uint64(extent.count) > math.MaxUint32 ||
			uint64(extent.parent)+uint64(extent.count) > math.MaxUint32 {
			return nil, fmt.Errorf("caller UID map has an invalid extent: %+v", extent)
		}
		mapping = append(mapping, extent)
	}
	return mapping, nil
}

func (mapping packetUIDMap) contains(uid uint32) bool {
	for _, extent := range mapping {
		if uid >= extent.inside && uint64(uid) < uint64(extent.inside)+uint64(extent.count) {
			return true
		}
	}
	return false
}
