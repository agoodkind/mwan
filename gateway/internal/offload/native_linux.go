//go:build linux && cgo && nativeoffload

package offload

/*
#cgo pkg-config: mwan-native
#include <mwan_native.h>
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/interfaceintent"
)

// NativeControllerID references one controller in NativeConfig.
type NativeControllerID uint32

// NativeReceiverID references one kernel receiver in NativeConfig.
type NativeReceiverID uint32

// NativeDataID references one actual DATA device in NativeConfig.
type NativeDataID uint32

// NativeOriginID references one ingress binding in NativeConfig.
type NativeOriginID uint32

// NativeNoController identifies an output-only DATA device.
const NativeNoController NativeControllerID = C.MWAN_NATIVE_NO_ID

// NativeReturnMode selects the disposition of an exceptional packet.
type NativeReturnMode uint32

const (
	// NativeReturnOriginal restores the frame for its original kernel receiver.
	NativeReturnOriginal NativeReturnMode = C.MWAN_NATIVE_RETURN_ORIGINAL
	// NativeReturnPeerUnicast remains unsupported until source return passes hardware acceptance.
	NativeReturnPeerUnicast NativeReturnMode = C.MWAN_NATIVE_RETURN_PEER_UNICAST
)

// NativeIngressOrigin identifies the source port that hardware must classify.
type NativeIngressOrigin uint32

const (
	// NativePhysicalOrigin selects packets from the physical port.
	NativePhysicalOrigin NativeIngressOrigin = C.MWAN_NATIVE_ORIGIN_PHYSICAL
	// NativeVFOrigin requires VF source classification, which the current core rejects.
	NativeVFOrigin NativeIngressOrigin = C.MWAN_NATIVE_ORIGIN_NATIVE_VF
)

// NativeMemoryMode selects the EAL memory allocator.
type NativeMemoryMode uint32

const (
	// NativeHugepages uses the host's reserved hugepages.
	NativeHugepages NativeMemoryMode = C.MWAN_NATIVE_MEMORY_HUGEPAGES
	// NativeAnonymousMemory uses anonymous memory for development configurations.
	NativeAnonymousMemory NativeMemoryMode = C.MWAN_NATIVE_MEMORY_ANONYMOUS
)

// NativePCIAddress identifies one PCI function.
type NativePCIAddress = interfaceintent.PCIAddress

// NativeController owns one PCI function and its physical group-zero miss.
type NativeController struct {
	PCI              NativePCIAddress
	PhysicalReceiver NativeReceiverID
}

// NativeKernelReceiver identifies a kernel-owned VF on one controller.
type NativeKernelReceiver struct {
	Controller NativeControllerID
	VF         uint16
	MAC        [6]byte
}

// NativeDataDevice identifies one actual DATA function and its optional representor.
type NativeDataDevice struct {
	PCI        NativePCIAddress
	Controller NativeControllerID
	VF         uint16
	MAC        [6]byte
}

// NativeOrigin binds an ingress source to one receiver and one distinct DATA RX device.
type NativeOrigin struct {
	Controller NativeControllerID
	Kind       NativeIngressOrigin
	Receiver   NativeReceiverID
	Data       NativeDataID
	ReturnMode NativeReturnMode
	SourceVF   uint16
}

// NativeConfig specifies the ownership graph, EAL resources, and polling limits.
type NativeConfig struct {
	CPU                    uint32
	MemoryMiB              uint32
	MemoryMode             NativeMemoryMode
	Controllers            []NativeController
	Receivers              []NativeKernelReceiver
	Data                   []NativeDataDevice
	Origins                []NativeOrigin
	PairCapacity           uint32
	MbufCount              uint32
	DrainTimeout           time.Duration
	ConntrackUpdateTimeout time.Duration
	RXDescriptors          uint16
	TXDescriptors          uint16
	MTU                    uint16
	HugepageDirectory      string
	FilePrefix             string
	PollBudget             uint32
	IdleWait               time.Duration
}

// NativeDirection supplies an exact mapping and the caller's resolved output path.
type NativeDirection struct {
	Origin                 NativeOriginID
	EgressData             NativeDataID
	Original               conntrack.Tuple
	Translated             conntrack.Tuple
	OriginalSourceMAC      [6]byte
	OriginalDestinationMAC [6]byte
	OutputSourceMAC        [6]byte
	OutputDestinationMAC   [6]byte
	OutputMTU              uint16
}

// NativePairSpec groups the original and reply mappings under one policy generation.
// RequestedDirections uses bit zero for original and bit one for reply.
type NativePairSpec struct {
	Cookie              uint64
	PolicyGeneration    uint64
	RequestedDirections uint32
	Directions          [2]NativeDirection
}

// NativePacketGuard requires the observed route selectors before forwarding.
type NativePacketGuard struct {
	FlowLabel    uint32
	TrafficClass uint8
}

// NativeEvidenceGuard checks Go policy and namespace generations between batches.
// Check must read memory without network operations or calls to NativeRuntime.
type NativeEvidenceGuard struct {
	Check func() error
}

// NativeRetirementStats separates hardware capacity from retained inverse mappings.
type NativeRetirementStats struct {
	ActivePairCapacity   uint32
	RetainedPairCapacity uint32
	RetainedPairs        uint32
	DrainingPairs        uint32
	RetainedDirections   uint64
}

// NativePairHandle is valid only in the process and slot generation that created it.
type NativePairHandle struct {
	instance   uint64
	generation uint64
	slot       uint32
}

// LogValue keeps native identity visible without exporting writable fields.
func (handle NativePairHandle) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Uint64("instance", handle.instance),
		slog.Uint64("generation", handle.generation),
		slog.Uint64("slot", uint64(handle.slot)),
	)
}

// NativePairState reports hardware installation and inverse-record retention.
type NativePairState uint32

const (
	// NativePairAbsent permits the slot to accept another generation.
	NativePairAbsent NativePairState = C.MWAN_NATIVE_PAIR_ABSENT
	// NativePairInstalled permits eligible packets to use the resolved output.
	NativePairInstalled NativePairState = C.MWAN_NATIVE_PAIR_INSTALLED
	// NativePairDraining retains inverse records for packets after hardware removal.
	NativePairDraining NativePairState = C.MWAN_NATIVE_PAIR_DRAINING
	// NativePairFaulted requires recovery after a hardware deletion failure.
	NativePairFaulted NativePairState = C.MWAN_NATIVE_PAIR_FAULTED
)

// NativePairResult reports installed rules separately from retained inverse records.
type NativePairResult struct {
	Handle                  NativePairHandle
	State                   NativePairState
	RequestedDirections     uint32
	InstalledDirections     uint32
	CleanupFailedDirections uint32
	RestorationDirections   uint32
}

// NativePairStats reports each requested direction's hardware counters.
type NativePairStats struct {
	Result     NativePairResult
	Directions [2]NativeCounter
}

// NativeWorkerStats counts packet outcomes. Forwarded and ReturnedToKernel count
// packets that the PMD TX queue accepted; external captures verify delivery.
type NativeWorkerStats struct {
	Received             uint64
	Forwarded            uint64
	ReturnedToKernel     uint64
	Unsupported          uint64
	LookupMisses         uint64
	Malformed            uint64
	TransmitDropped      uint64
	Pending              uint64
	Quarantined          uint64
	DiscardedGroupFrames uint64
}

// NativeBuildInfo identifies the linked ABI and verified DPDK source artifacts.
type NativeBuildInfo struct {
	ABIVersion   uint32
	DPDKVersion  string
	SourceSHA256 string
	PatchSHA256  string
}

// NativeObjectKind identifies the graph object associated with an operation.
type NativeObjectKind uint32

const (
	// NativeObjectNone reports a process-wide operation.
	NativeObjectNone NativeObjectKind = C.MWAN_NATIVE_OBJECT_NONE
	// NativeObjectController reports a controller array index.
	NativeObjectController NativeObjectKind = C.MWAN_NATIVE_OBJECT_CONTROLLER
	// NativeObjectReceiver reports a kernel receiver array index.
	NativeObjectReceiver NativeObjectKind = C.MWAN_NATIVE_OBJECT_RECEIVER
	// NativeObjectData reports a DATA device array index.
	NativeObjectData NativeObjectKind = C.MWAN_NATIVE_OBJECT_DATA
	// NativeObjectOrigin reports an origin array index.
	NativeObjectOrigin NativeObjectKind = C.MWAN_NATIVE_OBJECT_ORIGIN
	// NativeObjectPort reports a discovered DPDK ethdev port ID.
	NativeObjectPort NativeObjectKind = C.MWAN_NATIVE_OBJECT_PORT
	// NativeObjectPair reports a logical pair slot.
	NativeObjectPair NativeObjectKind = C.MWAN_NATIVE_OBJECT_PAIR
)

// NativeError identifies the failed operation and preserves its errno.
type NativeError struct {
	Code          syscall.Errno
	Operation     uint32
	ObjectKind    NativeObjectKind
	ObjectID      uint32
	Direction     uint32
	FlowErrorType uint32
	Message       string
	Packet        NativePacketDiagnostic
}

// NativePacketDiagnostic records RX headers when the worker quarantines a packet.
// The tuple reflects any hardware rewrite. Validity fields distinguish absent headers.
type NativePacketDiagnostic struct {
	EthernetValid          bool
	IPValid                bool
	PortsValid             bool
	KeyMissing             bool
	SourceMACMismatch      bool
	DestinationMACMismatch bool
	DecodeStatus           int32
	EtherType              uint16
	Tuple                  conntrack.Tuple
	SourceMAC              [6]byte
	DestinationMAC         [6]byte
}

// Error formats the native operation, graph object, direction, and failure.
func (e *NativeError) Error() string {
	return fmt.Sprintf("native operation %d object %d/%d direction %d: %s: %s", e.Operation, e.ObjectKind, e.ObjectID, e.Direction, e.Message, e.Code)
}

// Unwrap permits [errors.Is] to compare the underlying errno.
func (e *NativeError) Unwrap() error { return e.Code }

type nativeOperation uint8

const (
	nativeEnable nativeOperation = iota
	nativeInstall
	nativeQuery
	nativeRemove
	nativeDisable
	nativeShutdown
	nativeRetirement
	nativeResume
	nativeVXLANOpen
	nativeVXLANInstall
	nativeVXLANQuery
	nativeVXLANRemove
	nativeVXLANClose
)

type nativeRequest struct {
	operation     nativeOperation
	config        C.struct_mwan_native_config
	pair          C.struct_mwan_native_pair_spec
	guards        [2]C.struct_mwan_native_packet_guard
	observed      bool
	evidence      *NativeEvidenceGuard
	deadline      time.Time
	handle        C.struct_mwan_native_handle
	vxlanEndpoint C.struct_mwan_native_vxlan_endpoint
	vxlanPlan     C.struct_mwan_native_vxlan_plan
	vxlanRule     C.struct_mwan_native_vxlan_result
	reply         chan nativeReply
}

type nativeReply struct {
	pair        NativePairResult
	stats       NativePairStats
	retirement  NativeRetirementStats
	vxlan       NativeVXLANResult
	vxlanStats  NativeVXLANStats
	err         error
	ownerExists bool
}

// NativeRuntime serializes all DPDK calls on one process-lifetime OS thread.
// Disable retains EAL. Close is final and must run only during process shutdown.
type NativeRuntime struct {
	requests      chan nativeRequest
	done          chan struct{}
	log           *slog.Logger
	pollBudget    uint32
	idleWait      time.Duration
	updater       *ConntrackUpdater
	updateTimeout time.Duration
	clock         clock.Clock
	shuttingDown  bool
	mu            sync.RWMutex
	stats         NativeWorkerStats
	pollError     error
	terminalError error
	evidence      map[*NativeEvidenceGuard]map[NativePairHandle]struct{}
	pairEvidence  map[NativePairHandle]*NativeEvidenceGuard
	vxlan         map[NativeVXLANContext]nativeVXLANOwnership
}

// ReadNativeBuild reads the linked library's version and verifies its ABI sizes.
func ReadNativeBuild() (NativeBuildInfo, error) {
	if C.mwan_native_abi_version() != C.MWAN_NATIVE_ABI_VERSION {
		return NativeBuildInfo{}, errors.New("native library ABI does not match the Go binding")
	}
	var info C.struct_mwan_native_build_info
	if result := C.mwan_native_build_info(&info); result != 0 {
		err := fmt.Errorf("read native build: %w", syscall.Errno(-result))
		slog.Error("native build metadata read failed", "err", err)
		return NativeBuildInfo{}, err
	}
	if info.abi_version != C.MWAN_NATIVE_ABI_VERSION ||
		info.config_size != C.sizeof_struct_mwan_native_config ||
		info.pair_spec_size != C.sizeof_struct_mwan_native_pair_spec ||
		info.direction_size != C.sizeof_struct_mwan_native_direction ||
		info.controller_size != C.sizeof_struct_mwan_native_controller ||
		info.receiver_size != C.sizeof_struct_mwan_native_receiver ||
		info.data_size != C.sizeof_struct_mwan_native_data ||
		info.origin_size != C.sizeof_struct_mwan_native_origin ||
		info.error_size != C.sizeof_struct_mwan_native_error ||
		info.worker_stats_size != C.sizeof_struct_mwan_native_worker_stats ||
		info.pair_stats_size != C.sizeof_struct_mwan_native_pair_stats {
		return NativeBuildInfo{}, errors.New("native library ABI does not match the Go binding")
	}
	return NativeBuildInfo{
		ABIVersion: uint32(info.abi_version), DPDKVersion: C.GoString(&info.dpdk_version[0]),
		SourceSHA256: C.GoString(&info.source_sha256[0]), PatchSHA256: C.GoString(&info.patch_sha256[0]),
	}, nil
}

// NewNativeRuntime creates the owner thread and initializes its configured ports.
// The conntrack socket uses the constructor caller's network namespace.
// An initialization error can return a runtime that still owns EAL resources.
// The caller must retain that runtime and report any final Close error.
// A nil runtime confirms that startup did not retain a native owner.
func NewNativeRuntime(config NativeConfig, log *slog.Logger) (*NativeRuntime, error) {
	if log == nil {
		return nil, errors.New("native runtime requires a logger")
	}
	if _, err := ReadNativeBuild(); err != nil {
		return nil, err
	}
	encoded, err := encodeNativeConfig(config)
	if err != nil {
		return nil, err
	}
	updater, err := NewConntrackUpdater(config.ConntrackUpdateTimeout)
	if err != nil {
		return nil, fmt.Errorf("open native conntrack updater: %w", err)
	}
	worker := new(NativeRuntime)
	worker.requests = make(chan nativeRequest)
	worker.done = make(chan struct{})
	worker.log = log
	worker.pollBudget = config.PollBudget
	worker.idleWait = config.IdleWait
	worker.updater, worker.updateTimeout = updater, config.ConntrackUpdateTimeout
	worker.clock = clock.Real{}
	worker.evidence = make(map[*NativeEvidenceGuard]map[NativePairHandle]struct{})
	worker.pairEvidence = make(map[NativePairHandle]*NativeEvidenceGuard)
	worker.vxlan = make(map[NativeVXLANContext]nativeVXLANOwnership)
	ready := make(chan nativeReply, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("native owner panicked; process restart required: %v", r)
				slog.Error("native owner stopped with live EAL resources", "err", err)
				worker.mu.Lock()
				worker.terminalError = err
				worker.mu.Unlock()
			}
			worker.closeUpdater()
			close(worker.done)
		}()
		runtime.LockOSThread()
		worker.run(encoded, ready)
		runtime.UnlockOSThread()
	}()
	var result nativeReply
	select {
	case result = <-ready:
	case <-worker.done:
		select {
		case result = <-ready:
		default:
			return worker, worker.closedError()
		}
	}
	if result.err != nil && !result.ownerExists {
		<-worker.done
		if cleanupError := worker.shutdownError(); cleanupError != nil {
			return worker, errors.Join(result.err, cleanupError)
		}
		return nil, result.err
	}
	return worker, result.err
}

// Enable restarts a disabled owner with its identical configuration.
func (worker *NativeRuntime) Enable(ctx context.Context, config NativeConfig) error {
	if config.PollBudget != worker.pollBudget || config.IdleWait != worker.idleWait || config.ConntrackUpdateTimeout != worker.updateTimeout {
		return errors.New("native reopen requires identical polling and conntrack update settings")
	}
	encoded, err := encodeNativeConfig(config)
	if err != nil {
		return err
	}
	var request nativeRequest
	request.operation, request.config = nativeEnable, encoded
	return worker.call(ctx, request).err
}

// Install consumes an existing kernel identity; provider selection stays with the caller.
func (worker *NativeRuntime) Install(ctx context.Context, flow conntrack.Flow, spec NativePairSpec) (NativePairResult, error) {
	return worker.install(ctx, flow, spec, nil, nil, nil)
}

// InstallObserved applies the packet guards captured with the connection.
func (worker *NativeRuntime) InstallObserved(ctx context.Context, flow conntrack.Flow, spec NativePairSpec, guards [2]NativePacketGuard) (NativePairResult, error) {
	return worker.install(ctx, flow, spec, &guards, nil, nil)
}

// InstallObservedWithEvidence rechecks forwarding evidence around rule installation.
func (worker *NativeRuntime) InstallObservedWithEvidence(ctx context.Context, flow conntrack.Flow, spec NativePairSpec, guards [2]NativePacketGuard, evidence *NativeEvidenceGuard) (NativePairResult, error) {
	return worker.install(ctx, flow, spec, &guards, nil, evidence)
}

// ResumeObserved preserves the suspended generation and its existing inverse.
func (worker *NativeRuntime) ResumeObserved(ctx context.Context, flow conntrack.Flow, handle NativePairHandle, spec NativePairSpec, guards [2]NativePacketGuard) (NativePairResult, error) {
	return worker.install(ctx, flow, spec, &guards, &handle, nil)
}

// ResumeObservedWithEvidence requires current evidence for the retained generation.
func (worker *NativeRuntime) ResumeObservedWithEvidence(ctx context.Context, flow conntrack.Flow, handle NativePairHandle, spec NativePairSpec, guards [2]NativePacketGuard, evidence *NativeEvidenceGuard) (NativePairResult, error) {
	return worker.install(ctx, flow, spec, &guards, &handle, evidence)
}

func (worker *NativeRuntime) install(ctx context.Context, flow conntrack.Flow, spec NativePairSpec, guards *[2]NativePacketGuard, resume *NativePairHandle, evidence *NativeEvidenceGuard) (NativePairResult, error) {
	if flow.ID == 0 || flow.Zone != 0 {
		return NativePairResult{}, errors.New("native admission requires an existing zone-zero connection")
	}
	encoded, err := encodeNativePair(spec)
	if err != nil {
		return NativePairResult{}, err
	}
	for i, direction := range spec.Directions {
		if spec.RequestedDirections&[2]uint32{1, 2}[i] != 0 &&
			(direction.Original.Proto.Protocol != flow.TupleOrig.Proto.Protocol || direction.Translated.Proto.Protocol != flow.TupleOrig.Proto.Protocol) {
			return NativePairResult{}, errors.New("native direction protocol does not match the connection")
		}
	}
	var request nativeRequest
	request.operation, request.pair = nativeInstall, encoded
	request.evidence = evidence
	if resume != nil {
		request.operation = nativeResume
		request.handle = encodeNativeHandle(*resume)
	}
	request.deadline, _ = ctx.Deadline()
	if err := worker.prepareInstall(ctx, flow, guards != nil); err != nil {
		return NativePairResult{}, err
	}
	if guards != nil {
		request.observed = true
		for index, guard := range *guards {
			request.guards[index].flow_label = C.uint32_t(guard.FlowLabel)
			request.guards[index].traffic_class = C.uint8_t(guard.TrafficClass)
		}
	}
	result := worker.call(ctx, request)
	return result.pair, result.err
}

func (worker *NativeRuntime) prepareInstall(ctx context.Context, flow conntrack.Flow, observed bool) error {
	lookupContext, cancel := context.WithTimeout(ctx, worker.updateTimeout)
	defer cancel()
	if observed {
		current, err := FreshConntrackFlow(lookupContext, flow)
		if err != nil {
			return err
		}
		if current.Mark != flow.Mark || current.Status != flow.Status ||
			current.ProtoInfo.TCP != nil && flow.ProtoInfo.TCP != nil && current.ProtoInfo.TCP.State != flow.ProtoInfo.TCP.State {
			failure := fmt.Errorf("observed connection policy changed: %w", syscall.ESTALE)
			worker.log.WarnContext(ctx, "prepare native admission failed", "id", flow.ID, "err", failure)
			return failure
		}
	}
	if flow.TupleOrig.Proto.Protocol == syscall.IPPROTO_TCP {
		if err := worker.updater.SetLiberal(lookupContext, flow); err != nil {
			return err
		}
	}
	return nil
}

// Retirement reports the separate active and retained mapping capacities.
func (worker *NativeRuntime) Retirement(ctx context.Context) (NativeRetirementStats, error) {
	var request nativeRequest
	request.operation = nativeRetirement
	result := worker.call(ctx, request)
	return result.retirement, result.err
}

// Query reads hardware counters without treating unavailable counters as zero.
func (worker *NativeRuntime) Query(ctx context.Context, handle NativePairHandle) (NativePairStats, error) {
	var request nativeRequest
	request.operation, request.handle = nativeQuery, encodeNativeHandle(handle)
	result := worker.call(ctx, request)
	return result.stats, result.err
}

// Remove deletes hardware rules and retains inverse records until Disable.
func (worker *NativeRuntime) Remove(ctx context.Context, handle NativePairHandle) (NativePairResult, error) {
	var request nativeRequest
	request.operation, request.handle = nativeRemove, encodeNativeHandle(handle)
	result := worker.call(ctx, request)
	return result.pair, result.err
}

// Disable removes owned rules and stops ports while retaining the EAL owner.
func (worker *NativeRuntime) Disable(ctx context.Context) error {
	var request nativeRequest
	request.operation = nativeDisable
	return worker.call(ctx, request).err
}

// Close performs final process shutdown and releases EAL.
func (worker *NativeRuntime) Close(ctx context.Context) error {
	select {
	case <-worker.done:
		return worker.shutdownError()
	default:
	}
	var request nativeRequest
	request.operation = nativeShutdown
	err := worker.call(ctx, request).err
	if errors.Is(err, net.ErrClosed) {
		return worker.shutdownError()
	}
	return err
}

// Stats reads the latest packet totals and polling failure.
func (worker *NativeRuntime) Stats() (NativeWorkerStats, error) {
	worker.mu.RLock()
	defer worker.mu.RUnlock()
	if worker.terminalError != nil {
		return worker.stats, worker.terminalError
	}
	return worker.stats, worker.pollError
}

func (worker *NativeRuntime) shutdownError() error {
	worker.mu.RLock()
	defer worker.mu.RUnlock()
	return worker.terminalError
}

func (worker *NativeRuntime) closedError() error {
	if err := worker.shutdownError(); err != nil {
		return err
	}
	return net.ErrClosed
}

func (worker *NativeRuntime) closeUpdater() {
	err := worker.updater.Close()
	if err != nil {
		worker.mu.Lock()
		if !errors.Is(worker.terminalError, err) {
			worker.terminalError = errors.Join(worker.terminalError, err)
		}
		worker.mu.Unlock()
	}
}

// Cancellation bounds queue admission. An accepted operation returns its actual
// result so cancellation cannot discard a newly installed hardware handle.
func (worker *NativeRuntime) call(ctx context.Context, request nativeRequest) nativeReply {
	var result nativeReply
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	request.reply = make(chan nativeReply, 1)
	select {
	case worker.requests <- request:
		select {
		case result = <-request.reply:
			return result
		case <-worker.done:
			select {
			case result = <-request.reply:
			default:
				result.err = worker.closedError()
			}
		}
	case <-worker.done:
		result.err = worker.closedError()
	case <-ctx.Done():
		result.err = ctx.Err()
	}
	return result
}

func nativeCallError(rc C.int, detail C.struct_mwan_native_error) error {
	if rc == 0 {
		return nil
	}
	code := syscall.Errno(detail.code)
	if code == 0 {
		code = syscall.Errno(-rc)
	}
	return &NativeError{
		Code: code, Operation: uint32(detail.operation), ObjectKind: NativeObjectKind(detail.object_kind), ObjectID: uint32(detail.object_id),
		Direction: uint32(detail.direction), FlowErrorType: uint32(detail.flow_error_type), Message: C.GoString(&detail.message[0]),
		Packet: decodeNativePacket(detail.packet),
	}
}

func decodeNativePacket(packet C.struct_mwan_native_packet_diagnostic) NativePacketDiagnostic {
	var result NativePacketDiagnostic
	result.EthernetValid = packet.validity&C.MWAN_NATIVE_PACKET_ETHERNET_VALID != 0
	result.IPValid = packet.validity&C.MWAN_NATIVE_PACKET_IP_VALID != 0
	result.PortsValid = packet.validity&C.MWAN_NATIVE_PACKET_PORTS_VALID != 0
	result.KeyMissing = packet.failure_flags&C.MWAN_NATIVE_PACKET_KEY_MISSING != 0
	result.SourceMACMismatch = packet.failure_flags&C.MWAN_NATIVE_PACKET_SOURCE_MAC_MISMATCH != 0
	result.DestinationMACMismatch = packet.failure_flags&C.MWAN_NATIVE_PACKET_DESTINATION_MAC_MISMATCH != 0
	result.DecodeStatus, result.EtherType = int32(packet.decode_status), uint16(packet.ether_type)
	for i := range result.SourceMAC {
		result.SourceMAC[i], result.DestinationMAC[i] = byte(packet.source_mac[i]), byte(packet.destination_mac[i])
	}
	if !result.IPValid {
		return result
	}
	var source, destination [16]byte
	for i := range source {
		source[i], destination[i] = byte(packet.tuple.source[i]), byte(packet.tuple.destination[i])
	}
	if packet.tuple.family == C.MWAN_NATIVE_FAMILY_IPV4 {
		result.Tuple.IP.SourceAddress = netip.AddrFrom4([4]byte(source[:4]))
		result.Tuple.IP.DestinationAddress = netip.AddrFrom4([4]byte(destination[:4]))
	} else {
		result.Tuple.IP.SourceAddress = netip.AddrFrom16(source)
		result.Tuple.IP.DestinationAddress = netip.AddrFrom16(destination)
	}
	result.Tuple.Proto.Protocol = uint8(packet.tuple.protocol)
	result.Tuple.Proto.SourcePort = uint16(packet.tuple.source_port)
	result.Tuple.Proto.DestinationPort = uint16(packet.tuple.destination_port)
	return result
}

func encodeNativePCI(address NativePCIAddress) C.struct_mwan_native_pci {
	var result C.struct_mwan_native_pci
	result.domain, result.bus = C.uint16_t(address.Domain), C.uint8_t(address.Bus)
	result.device, result.function = C.uint8_t(address.Device), C.uint8_t(address.Function)
	return result
}

func encodeNativeConfig(config NativeConfig) (C.struct_mwan_native_config, error) {
	var result C.struct_mwan_native_config
	if len(config.Controllers) > C.MWAN_NATIVE_MAX_CONTROLLERS || len(config.Receivers) > C.MWAN_NATIVE_MAX_RECEIVERS ||
		len(config.Data) == 0 || len(config.Data) > C.MWAN_NATIVE_MAX_DATA || len(config.Origins) > C.MWAN_NATIVE_MAX_ORIGINS ||
		config.PollBudget == 0 || config.IdleWait < 0 || config.DrainTimeout < time.Millisecond ||
		config.ConntrackUpdateTimeout <= 0 ||
		config.DrainTimeout/time.Millisecond > time.Duration(^uint32(0)) {
		return result, errors.New("invalid native graph count, poll budget, or timeout")
	}
	result.abi_version, result.record_size = C.MWAN_NATIVE_ABI_VERSION, C.sizeof_struct_mwan_native_config
	result.cpu, result.memory_mb = C.uint32_t(config.CPU), C.uint32_t(config.MemoryMiB)
	result.memory_mode = C.uint32_t(config.MemoryMode)
	result.controller_count, result.receiver_count = C.uint32_t(len(config.Controllers)), C.uint32_t(len(config.Receivers))
	result.data_count, result.origin_count = C.uint32_t(len(config.Data)), C.uint32_t(len(config.Origins))
	result.pair_capacity, result.mbuf_count = C.uint32_t(config.PairCapacity), C.uint32_t(config.MbufCount)
	result.drain_timeout_ms = C.uint32_t(config.DrainTimeout / time.Millisecond)
	result.rx_descriptors, result.tx_descriptors = C.uint16_t(config.RXDescriptors), C.uint16_t(config.TXDescriptors)
	result.mtu = C.uint16_t(config.MTU)
	if err := nativeText(result.hugepage_directory[:], config.HugepageDirectory); err != nil {
		return result, err
	}
	if err := nativeText(result.file_prefix[:], config.FilePrefix); err != nil {
		return result, err
	}
	for i, controller := range config.Controllers {
		result.controllers[i].pci = encodeNativePCI(controller.PCI)
		result.controllers[i].physical_receiver = C.uint32_t(controller.PhysicalReceiver)
	}
	for i, receiver := range config.Receivers {
		out := &result.receivers[i]
		out.controller, out.vf = C.uint32_t(receiver.Controller), C.uint16_t(receiver.VF)
		for n := range receiver.MAC {
			out.mac[n] = C.uint8_t(receiver.MAC[n])
		}
	}
	for i, data := range config.Data {
		out := &result.data[i]
		out.pci, out.controller, out.vf = encodeNativePCI(data.PCI), C.uint32_t(data.Controller), C.uint16_t(data.VF)
		for n := range data.MAC {
			out.mac[n] = C.uint8_t(data.MAC[n])
		}
	}
	for i, origin := range config.Origins {
		out := &result.origins[i]
		out.controller, out.kind = C.uint32_t(origin.Controller), C.uint32_t(origin.Kind)
		out.receiver, out.data = C.uint32_t(origin.Receiver), C.uint32_t(origin.Data)
		out.return_mode, out.source_vf = C.uint32_t(origin.ReturnMode), C.uint16_t(origin.SourceVF)
	}
	return result, nil
}

func nativeText(destination []C.char, value string) error {
	if len(value) >= len(destination) {
		return errors.New("native configuration string exceeds its ABI field")
	}
	for i := range len(value) {
		if value[i] == 0 {
			return errors.New("native configuration string contains a zero byte")
		}
		destination[i] = C.char(value[i])
	}
	return nil
}

func encodeNativeTuple(tuple conntrack.Tuple) (C.struct_mwan_native_tuple, error) {
	var result C.struct_mwan_native_tuple
	if tuple.Zone != 0 || tuple.Proto.ICMPv4 || tuple.Proto.ICMPv6 ||
		!tuple.IP.SourceAddress.IsValid() || !tuple.IP.DestinationAddress.IsValid() ||
		tuple.IP.SourceAddress.Is4In6() || tuple.IP.DestinationAddress.Is4In6() ||
		tuple.IP.SourceAddress.Zone() != "" || tuple.IP.DestinationAddress.Zone() != "" ||
		tuple.IP.SourceAddress.Is4() != tuple.IP.DestinationAddress.Is4() {
		return result, errors.New("native tuple requires one unambiguous address family and zone zero")
	}
	result.protocol = C.uint8_t(tuple.Proto.Protocol)
	result.source_port, result.destination_port = C.uint16_t(tuple.Proto.SourcePort), C.uint16_t(tuple.Proto.DestinationPort)
	if tuple.IP.SourceAddress.Is4() {
		result.family = C.MWAN_NATIVE_FAMILY_IPV4
		source, destination := tuple.IP.SourceAddress.As4(), tuple.IP.DestinationAddress.As4()
		for i := range source {
			result.source[i], result.destination[i] = C.uint8_t(source[i]), C.uint8_t(destination[i])
		}
	} else {
		result.family = C.MWAN_NATIVE_FAMILY_IPV6
		source, destination := tuple.IP.SourceAddress.As16(), tuple.IP.DestinationAddress.As16()
		for i := range source {
			result.source[i], result.destination[i] = C.uint8_t(source[i]), C.uint8_t(destination[i])
		}
	}
	return result, nil
}

func encodeNativePair(spec NativePairSpec) (C.struct_mwan_native_pair_spec, error) {
	var result C.struct_mwan_native_pair_spec
	if spec.RequestedDirections == 0 || spec.RequestedDirections > 3 {
		return result, errors.New("native pair requires direction mask 1, 2, or 3")
	}
	result.cookie, result.policy_generation = C.uint64_t(spec.Cookie), C.uint64_t(spec.PolicyGeneration)
	result.requested_direction_mask = C.uint32_t(spec.RequestedDirections)
	masks := [2]uint32{1, 2}
	for i, direction := range spec.Directions {
		if spec.RequestedDirections&masks[i] == 0 {
			continue
		}
		out := &result.directions[i]
		out.origin_id, out.egress_data_id = C.uint32_t(direction.Origin), C.uint32_t(direction.EgressData)
		var err error
		out.original, err = encodeNativeTuple(direction.Original)
		if err != nil {
			return result, err
		}
		out.translated, err = encodeNativeTuple(direction.Translated)
		if err != nil {
			return result, err
		}
		out.output_mtu = C.uint16_t(direction.OutputMTU)
		for n := range direction.OriginalSourceMAC {
			out.original_source_mac[n] = C.uint8_t(direction.OriginalSourceMAC[n])
			out.original_destination_mac[n] = C.uint8_t(direction.OriginalDestinationMAC[n])
			out.output_source_mac[n] = C.uint8_t(direction.OutputSourceMAC[n])
			out.output_destination_mac[n] = C.uint8_t(direction.OutputDestinationMAC[n])
		}
	}
	return result, nil
}

func encodeNativeHandle(handle NativePairHandle) C.struct_mwan_native_handle {
	var result C.struct_mwan_native_handle
	result.instance, result.generation, result.slot = C.uint64_t(handle.instance), C.uint64_t(handle.generation), C.uint32_t(handle.slot)
	return result
}

func decodeNativePairResult(result C.struct_mwan_native_pair_result) NativePairResult {
	return NativePairResult{
		Handle: NativePairHandle{instance: uint64(result.handle.instance), generation: uint64(result.handle.generation), slot: uint32(result.handle.slot)},
		State:  NativePairState(result.state), RequestedDirections: uint32(result.requested_direction_mask),
		InstalledDirections: uint32(result.installed_mask), CleanupFailedDirections: uint32(result.cleanup_failed_mask),
		RestorationDirections: uint32(result.restoration_mask),
	}
}

func decodeNativePairStats(stats C.struct_mwan_native_pair_stats) NativePairStats {
	var result NativePairStats
	result.Result = decodeNativePairResult(stats.result)
	for i, counter := range stats.directions {
		result.Directions[i] = decodeNativeCounter(counter)
	}
	return result
}

func decodeNativeCounter(counter C.struct_mwan_native_counter) NativeCounter {
	result := NativeCounter{
		Packets: uint64(counter.packets), Bytes: uint64(counter.bytes),
		ObservedMonotonicNS: uint64(counter.observed_monotonic_ns),
		PacketsValid:        counter.packets_valid != 0, BytesValid: counter.bytes_valid != 0,
		Error: nil,
	}
	if counter.error != 0 {
		result.Error = syscall.Errno(-counter.error)
	}
	return result
}

func decodeNativeWorkerStats(stats C.struct_mwan_native_worker_stats) NativeWorkerStats {
	return NativeWorkerStats{
		Received: uint64(stats.received), Forwarded: uint64(stats.forwarded),
		ReturnedToKernel: uint64(stats.returned_to_kernel), Unsupported: uint64(stats.unsupported),
		LookupMisses: uint64(stats.lookup_misses), Malformed: uint64(stats.malformed),
		TransmitDropped: uint64(stats.transmit_dropped), Pending: uint64(stats.pending_packets), Quarantined: uint64(stats.quarantined_packets),
		DiscardedGroupFrames: uint64(stats.discarded_group_frames),
	}
}
