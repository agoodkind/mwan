use crate::abi::*;
use crate::dpdk;
use crate::packet;
use std::cell::UnsafeCell;
use std::collections::{HashMap, VecDeque};
use std::ffi::{CStr, CString, c_char};
use std::mem::{self, size_of};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::ptr::{self, NonNull};
use std::time::Duration;

const BURST: usize = 32;
const MINIMUM_MTU: u16 = 68;
const MAXIMUM_MTU: u16 = 9600;
const MAXIMUM_PAIRS: u32 = 65_536;
const MAXIMUM_MBUFS: u32 = 1_048_576;
const ETHERNET_HEADER_LENGTH: u16 = 14;
const VLAN_HEADERS_LENGTH: u16 = 8;
const DRAIN_QUIET_NS: u64 = 1_000_000;
const DRAIN_SLEEP_US: u64 = 50;
const NS_PER_MS: u64 = 1_000_000;
const MAXIMUM_PORTS: usize = 28;
const DIRECTION_MASKS: [u32; 2] = [1, 2];

type NativeResult<T> = Result<T, Box<MwanNativeError>>;

#[derive(Clone, Copy, Default, PartialEq, Eq)]
enum PairState {
    #[default]
    Absent,
    Installed,
    Draining,
    Faulted,
}

impl PairState {
    fn value(self) -> u32 {
        match self {
            Self::Absent => MWAN_NATIVE_PAIR_ABSENT,
            Self::Installed => MWAN_NATIVE_PAIR_INSTALLED,
            Self::Draining => MWAN_NATIVE_PAIR_DRAINING,
            Self::Faulted => MWAN_NATIVE_PAIR_FAULTED,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum PortState {
    Discovered,
    Configured,
    Started,
    Stopped,
    Closed,
}

#[derive(Clone, Copy)]
struct Port {
    id: u16,
    state: PortState,
}

#[derive(Clone, Copy, Default)]
struct ControllerPort {
    id: Option<u16>,
    miss: bool,
}

#[derive(Clone, Copy, Default)]
struct DataPort {
    id: Option<u16>,
    representor: Option<u16>,
    origin: Option<usize>,
}

#[derive(Default)]
struct Leg {
    flow: Option<NonNull<dpdk::rte_flow>>,
    inverse: bool,
    delete_failed: bool,
}

#[derive(Default)]
struct Pair {
    generation: u64,
    state: PairState,
    spec: MwanNativePairSpec,
    guards: Option<[MwanNativePacketGuard; 2]>,
    legs: [Leg; 2],
}

#[derive(Clone, Copy, Hash, PartialEq, Eq)]
struct InverseKey {
    origin: u32,
    tuple: MwanNativeTuple,
}

#[derive(Clone, Copy)]
struct LegId {
    slot: usize,
    generation: u64,
    direction: usize,
}

#[derive(Clone, Copy)]
struct PacketLeg {
    spec: MwanNativeDirection,
    guard: Option<MwanNativePacketGuard>,
    handle: Option<MwanNativeHandle>,
    installed: bool,
}

/// This value owns one received mbuf until a successful transmit transfers it.
struct PacketBuffer {
    pointer: NonNull<dpdk::rte_mbuf>,
    contiguous: bool,
}

impl PacketBuffer {
    fn received(pointer: *mut dpdk::rte_mbuf) -> Option<Self> {
        NonNull::new(pointer).map(|pointer| Self {
            pointer,
            contiguous: false,
        })
    }

    fn ethernet(&self) -> Option<[u8; ETHERNET_HEADER_LENGTH as usize]> {
        let mut bytes = [0; ETHERNET_HEADER_LENGTH as usize];
        // SAFETY: RX transferred this live mbuf to the owner. The scratch buffer
        // has the requested size, and the SDK bounds its read by packet length.
        let source = unsafe {
            dpdk::mwn_mbuf_read(
                self.pointer.as_ptr(),
                0,
                u32::from(ETHERNET_HEADER_LENGTH),
                bytes.as_mut_ptr().cast(),
            )
        };
        if source.is_null() {
            return None;
        }
        if source != bytes.as_ptr().cast() {
            // SAFETY: The SDK returned a readable Ethernet header in the mbuf.
            // Its storage cannot overlap this separate stack scratch buffer.
            unsafe {
                ptr::copy_nonoverlapping(source.cast::<u8>(), bytes.as_mut_ptr(), bytes.len());
            }
        }
        Some(bytes)
    }

    fn linearize(&mut self) -> Result<(), i32> {
        // SAFETY: This owner has exclusive ownership of the received mbuf.
        let unique = unsafe {
            dpdk::mwn_mbuf_refcount(self.pointer.as_ptr()) == 1
                && dpdk::mwn_mbuf_is_direct(self.pointer.as_ptr())
                && !dpdk::mwn_mbuf_is_external(self.pointer.as_ptr())
        };
        if !unique {
            return Err(libc::ENOTSUP);
        }
        // SAFETY: The direct mbuf has one reference, so linearization can copy
        // into its data buffer and release its remaining chain segments.
        if unsafe { dpdk::mwn_mbuf_linearize(self.pointer.as_ptr()) } != 0 {
            return Err(libc::ENOBUFS);
        }
        self.contiguous = true;
        Ok(())
    }

    fn frame(&mut self) -> Result<&mut [u8], i32> {
        if !self.contiguous {
            return Err(libc::ENOBUFS);
        }
        // SAFETY: This exclusive live mbuf remains owned here. Each helper
        // reads one initialized SDK field without borrowing the data buffer.
        let (length, offset, data_length, buffer_length, buffer) = unsafe {
            (
                dpdk::mwn_mbuf_length(self.pointer.as_ptr()) as usize,
                usize::from(dpdk::mwn_mbuf_data_offset(self.pointer.as_ptr())),
                usize::from(dpdk::mwn_mbuf_data_length(self.pointer.as_ptr())),
                usize::from(dpdk::mwn_mbuf_buffer_length(self.pointer.as_ptr())),
                dpdk::mwn_mbuf_buffer(self.pointer.as_ptr()),
            )
        };
        let end = offset.checked_add(length).ok_or(libc::EOVERFLOW)?;
        if buffer.is_null() || length > data_length || end > buffer_length {
            return Err(libc::EBADMSG);
        }
        // SAFETY: The unique direct mbuf is contiguous and its checked data
        // range fits the live SDK allocation. This mutable borrow ends before TX.
        let data = unsafe { dpdk::mwn_mbuf_data(self.pointer.as_ptr()) };
        if data.is_null() {
            return Err(libc::EBADMSG);
        }
        // SAFETY: The checks above establish a nonnull, exclusive allocation
        // containing exactly this bounded range for the lifetime of this borrow.
        Ok(unsafe { std::slice::from_raw_parts_mut(data, length) })
    }

    fn clear_offloads(&mut self) {
        // SAFETY: The owner exclusively owns the mbuf and its metadata.
        unsafe { dpdk::mwn_mbuf_clear_tx_offloads(self.pointer.as_ptr()) };
    }

    fn transfer(self) {
        // TX now owns this mbuf. Dropping PacketBuffer would free it twice.
        mem::forget(self);
    }
}

impl Drop for PacketBuffer {
    fn drop(&mut self) {
        // SAFETY: Only an untransferred PacketBuffer can run this destructor.
        // Every buffer destructor runs on the EAL owner thread.
        unsafe { dpdk::mwn_mbuf_free(self.pointer.as_ptr()) };
    }
}

struct Received {
    origin: usize,
    packet: PacketBuffer,
}

struct Pending {
    packet: PacketBuffer,
    port: u16,
    kernel: bool,
    prepared: Option<PreparedForward>,
}

#[derive(Clone, Copy)]
struct PreparedForward {
    handle: MwanNativeHandle,
    view: packet::ParsedPacket,
    direction: MwanNativeDirection,
    return_port: u16,
}

struct Owner {
    thread: libc::pthread_t,
    state: UnsafeCell<OwnerState>,
}

struct OwnerState {
    config: MwanNativeConfig,
    instance: u64,
    controllers: [ControllerPort; MWAN_NATIVE_MAX_CONTROLLERS as usize],
    receivers: [Option<u16>; MWAN_NATIVE_MAX_RECEIVERS as usize],
    data: [DataPort; MWAN_NATIVE_MAX_DATA as usize],
    ports: Vec<Port>,
    pool: Option<NonNull<dpdk::rte_mempool>>,
    pairs: Vec<Pair>,
    retained: HashMap<MwanNativeHandle, Pair>,
    inverses: HashMap<InverseKey, LegId>,
    pending: VecDeque<Pending>,
    received: VecDeque<Received>,
    quarantined: Option<Received>,
    next_rx: usize,
    stats: MwanNativeWorkerStats,
    terminal_failure: Option<MwanNativeError>,
    eal_ready: bool,
    active: bool,
    quiescing: bool,
    faulted: bool,
}

fn fault(
    code: i32,
    operation: u32,
    kind: u32,
    object: u32,
    direction: u32,
    message: &str,
) -> Box<MwanNativeError> {
    let code = code
        .checked_abs()
        .filter(|value| *value != 0)
        .unwrap_or(libc::EIO);
    let mut error = MwanNativeError {
        code,
        operation,
        object_kind: kind,
        object_id: object,
        direction,
        ..MwanNativeError::default()
    };
    write_text(&mut error.message, message.as_bytes());
    Box::new(error)
}

fn stale_handle(handle: MwanNativeHandle) -> Box<MwanNativeError> {
    fault(
        libc::ESTALE,
        MWAN_NATIVE_OP_CONFIG,
        MWAN_NATIVE_OBJECT_PAIR,
        handle.slot,
        0,
        "Stale pair handle",
    )
}

fn write_text<const LENGTH: usize>(destination: &mut [c_char; LENGTH], source: &[u8]) {
    destination.fill(0);
    for (target, value) in destination
        .iter_mut()
        .take(LENGTH.saturating_sub(1))
        .zip(source)
    {
        *target = *value as c_char;
    }
}

fn sdk_errno() -> i32 {
    // SAFETY: The shim reads this thread's SDK errno without taking ownership.
    unsafe { dpdk::mwn_errno() }
}

fn flow_fault(
    code: i32,
    operation: u32,
    kind: u32,
    object: u32,
    direction: u32,
    detail: &dpdk::rte_flow_error,
) -> Box<MwanNativeError> {
    let mut error = fault(
        code,
        operation,
        kind,
        object,
        direction,
        "DPDK flow operation failed",
    );
    error.flow_error_type = detail.type_;
    if !detail.message.is_null() {
        // SAFETY: The DPDK error contract supplies a live NUL-terminated string
        // until this SDK call's diagnostic has been copied.
        let text = unsafe { CStr::from_ptr(detail.message) };
        write_text(&mut error.message, text.to_bytes());
    }
    error
}

fn monotonic_ns() -> u64 {
    let mut value = libc::timespec {
        tv_sec: 0,
        tv_nsec: 0,
    };
    // SAFETY: The clock writes one valid timespec into this stack allocation.
    if unsafe { libc::clock_gettime(libc::CLOCK_MONOTONIC, &mut value) } != 0 {
        return 0;
    }
    u64::try_from(value.tv_sec)
        .unwrap_or(0)
        .wrapping_mul(1_000_000_000)
        .wrapping_add(u64::try_from(value.tv_nsec).unwrap_or(0))
}

fn pci_valid(value: &MwanNativePci) -> bool {
    value.device < 32 && value.function < 8 && value.reserved == [0; 3]
}

fn pci_name(value: &MwanNativePci) -> String {
    format!(
        "{:04x}:{:02x}:{:02x}.{:x}",
        value.domain, value.bus, value.device, value.function
    )
}

fn valid_mac(value: &[u8; 6]) -> bool {
    value[0] & 1 == 0 && value.iter().any(|byte| *byte != 0)
}

fn c_bytes(bytes: &[c_char]) -> Option<Vec<u8>> {
    let end = bytes.iter().position(|value| *value == 0)?;
    Some(
        bytes[..end]
            .iter()
            .map(|value| value.to_ne_bytes()[0])
            .collect(),
    )
}

fn validate_config(config: &MwanNativeConfig) -> NativeResult<()> {
    let invalid = || {
        fault(
            libc::EINVAL,
            MWAN_NATIVE_OP_CONFIG,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Invalid native configuration or ABI",
        )
    };
    let prefix = c_bytes(&config.file_prefix).ok_or_else(invalid)?;
    let directory = c_bytes(&config.hugepage_directory).ok_or_else(invalid)?;
    if config.abi_version != MWAN_NATIVE_ABI_VERSION
        || config.record_size as usize != size_of::<MwanNativeConfig>()
        || config.controller_count > MWAN_NATIVE_MAX_CONTROLLERS
        || config.receiver_count > MWAN_NATIVE_MAX_RECEIVERS
        || config.data_count == 0
        || config.data_count > MWAN_NATIVE_MAX_DATA
        || config.origin_count > MWAN_NATIVE_MAX_ORIGINS
        || config.pair_capacity == 0
        || config.pair_capacity > MAXIMUM_PAIRS
        || config.memory_mb == 0
        || config.drain_timeout_ms == 0
        || config.rx_descriptors == 0
        || config.tx_descriptors == 0
        || config.mbuf_count < u32::from(config.rx_descriptors)
        || config.mbuf_count > MAXIMUM_MBUFS
        || !(MINIMUM_MTU..=MAXIMUM_MTU).contains(&config.mtu)
        || config.reserved != 0
        || config.memory_mode > MWAN_NATIVE_MEMORY_ANONYMOUS
        || prefix.is_empty()
    {
        return Err(invalid());
    }
    if !prefix
        .iter()
        .all(|byte| byte.is_ascii_alphanumeric() || matches!(*byte, b'_' | b'-'))
    {
        return Err(fault(
            libc::EINVAL,
            MWAN_NATIVE_OP_CONFIG,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Invalid EAL file prefix",
        ));
    }
    if !directory.is_empty() && directory.first() != Some(&b'/') {
        return Err(fault(
            libc::EINVAL,
            MWAN_NATIVE_OP_CONFIG,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Hugepage directory must be absolute",
        ));
    }
    let controllers = &config.controllers[..config.controller_count as usize];
    let receivers = &config.receivers[..config.receiver_count as usize];
    let data = &config.data[..config.data_count as usize];
    let origins = &config.origins[..config.origin_count as usize];
    for (index, controller) in controllers.iter().enumerate() {
        if !pci_valid(&controller.pci)
            || controller.reserved != 0
            || controller.physical_receiver >= config.receiver_count
            || receivers[controller.physical_receiver as usize].controller != index as u32
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_CONTROLLER,
                index as u32,
                0,
                "Invalid controller or physical miss receiver",
            ));
        }
        if controllers[..index]
            .iter()
            .any(|other| other.pci == controller.pci)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_CONTROLLER,
                index as u32,
                0,
                "Controller PCI function appears more than once",
            ));
        }
    }
    for (index, receiver) in receivers.iter().enumerate() {
        if receiver.controller >= config.controller_count
            || receiver.reserved != 0
            || receiver.padding != [0; 2]
            || !valid_mac(&receiver.mac)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_RECEIVER,
                index as u32,
                0,
                "Invalid kernel receiver",
            ));
        }
        if receivers[..index].iter().any(|other| {
            receiver.controller == other.controller
                && (receiver.vf == other.vf || receiver.mac == other.mac)
        }) {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_RECEIVER,
                index as u32,
                0,
                "Kernel receiver VF or MAC appears more than once on a controller",
            ));
        }
    }
    let mut port_count = controllers.len() + receivers.len() + data.len();
    for (index, device) in data.iter().enumerate() {
        if !pci_valid(&device.pci)
            || device.reserved != 0
            || device.padding != [0; 2]
            || !valid_mac(&device.mac)
            || (device.controller != MWAN_NATIVE_NO_ID
                && device.controller >= config.controller_count)
            || (device.controller == MWAN_NATIVE_NO_ID && device.vf != 0)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "Invalid DATA device",
            ));
        }
        if controllers
            .iter()
            .any(|controller| controller.pci == device.pci)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "DATA and controller PCI functions overlap",
            ));
        }
        if data[..index].iter().any(|other| {
            device.pci == other.pci
                || (device.controller != MWAN_NATIVE_NO_ID
                    && device.controller == other.controller
                    && (device.vf == other.vf || device.mac == other.mac))
        }) {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "DATA device or represented VF appears more than once",
            ));
        }
        if device.controller != MWAN_NATIVE_NO_ID {
            port_count += 1;
            if receivers.iter().any(|receiver| {
                device.controller == receiver.controller
                    && (device.vf == receiver.vf || device.mac == receiver.mac)
            }) {
                return Err(fault(
                    libc::EINVAL,
                    MWAN_NATIVE_OP_CONFIG,
                    MWAN_NATIVE_OBJECT_DATA,
                    index as u32,
                    0,
                    "DATA and kernel receiver identities overlap",
                ));
            }
        }
    }
    // SAFETY: This helper returns the SDK's compile-time ethdev bound.
    if port_count > usize::from(unsafe { dpdk::mwn_max_ethports() }) {
        return Err(fault(
            libc::ENOSPC,
            MWAN_NATIVE_OP_CONFIG,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Graph exceeds the SDK ethdev limit",
        ));
    }
    for (index, origin) in origins.iter().enumerate() {
        if origin.controller >= config.controller_count
            || origin.receiver >= config.receiver_count
            || origin.data >= config.data_count
            || origin.reserved != 0
            || receivers[origin.receiver as usize].controller != origin.controller
            || data[origin.data as usize].controller != origin.controller
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_ORIGIN,
                index as u32,
                0,
                "Origin references do not identify one controller",
            ));
        }
        if origin.kind != MWAN_NATIVE_ORIGIN_PHYSICAL
            || origin.return_mode != MWAN_NATIVE_RETURN_ORIGINAL
        {
            return Err(fault(
                libc::ENOTSUP,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_ORIGIN,
                index as u32,
                0,
                "Only physical origins with original-frame return are supported",
            ));
        }
        if origin.source_vf != 0 {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_ORIGIN,
                index as u32,
                0,
                "Physical origin must not select a source VF",
            ));
        }
        if origins[..index]
            .iter()
            .any(|other| other.data == origin.data)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_ORIGIN,
                index as u32,
                0,
                "Origins require distinct DATA RX bindings",
            ));
        }
    }
    Ok(())
}

impl OwnerState {
    fn new(config: MwanNativeConfig) -> NativeResult<Self> {
        let allocation = || {
            fault(
                libc::ENOMEM,
                MWAN_NATIVE_OP_EAL,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Packet or pair storage allocation failed",
            )
        };
        let mut pairs = Vec::new();
        pairs
            .try_reserve_exact(config.pair_capacity as usize)
            .map_err(|_| allocation())?;
        pairs.resize_with(config.pair_capacity as usize, Pair::default);
        let mut inverses = HashMap::new();
        inverses
            .try_reserve(config.pair_capacity as usize * 4)
            .map_err(|_| allocation())?;
        let mut retained = HashMap::new();
        retained
            .try_reserve(config.pair_capacity as usize)
            .map_err(|_| allocation())?;
        let mut pending = VecDeque::new();
        pending
            .try_reserve_exact(BURST.min(config.mbuf_count as usize))
            .map_err(|_| allocation())?;
        let mut received = VecDeque::new();
        received
            .try_reserve_exact(BURST)
            .map_err(|_| allocation())?;
        let mut ports = Vec::new();
        ports
            .try_reserve_exact(MAXIMUM_PORTS)
            .map_err(|_| allocation())?;
        let mut state = Self {
            config,
            instance: 0,
            controllers: [ControllerPort::default(); MWAN_NATIVE_MAX_CONTROLLERS as usize],
            receivers: [None; MWAN_NATIVE_MAX_RECEIVERS as usize],
            data: [DataPort::default(); MWAN_NATIVE_MAX_DATA as usize],
            ports,
            pool: None,
            pairs,
            retained,
            inverses,
            pending,
            received,
            quarantined: None,
            next_rx: 0,
            stats: MwanNativeWorkerStats::default(),
            terminal_failure: None,
            eal_ready: false,
            active: false,
            quiescing: false,
            faulted: false,
        };
        for index in 0..state.config.origin_count as usize {
            state.data[state.config.origins[index].data as usize].origin = Some(index);
        }
        Ok(state)
    }

    fn initialize(&mut self) -> NativeResult<()> {
        self.start_eal()?;
        // SAFETY: EAL now owns this thread's random state.
        self.instance = unsafe { dpdk::rte_rand() }.max(1);
        let name = CString::new(format!("mwn_pool_{:x}", self.instance)).map_err(|_| {
            fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_EAL,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Invalid packet pool name",
            )
        })?;
        // SAFETY: The SDK returns its compile-time mbuf headroom.
        let headroom = unsafe { dpdk::mwn_mbuf_headroom() };
        let room = self.config.mtu + headroom + ETHERNET_HEADER_LENGTH + VLAN_HEADERS_LENGTH;
        // SAFETY: EAL is ready on this thread. Name remains valid during this
        // call, and room includes the configured MTU and all accepted headers.
        let pool = unsafe {
            dpdk::rte_pktmbuf_pool_create(name.as_ptr(), self.config.mbuf_count, 0, 0, room, -1)
        };
        self.pool = NonNull::new(pool);
        if self.pool.is_none() {
            return Err(fault(
                libc::ENOMEM,
                MWAN_NATIVE_OP_EAL,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Packet or pair storage allocation failed",
            ));
        }
        self.discover()?;
        for id in self.ordered_ports(false)? {
            self.configure_port(id)?;
        }
        self.activate()
    }

    fn start_eal(&mut self) -> NativeResult<()> {
        let prefix = c_bytes(&self.config.file_prefix).ok_or_else(|| {
            fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Invalid EAL file prefix",
            )
        })?;
        let prefix = String::from_utf8(prefix).map_err(|_| {
            fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Invalid EAL file prefix",
            )
        })?;
        let mut args = vec![
            b"mwan-native".to_vec(),
            format!("--lcores=0@{}", self.config.cpu).into_bytes(),
            b"--main-lcore=0".to_vec(),
            b"-m".to_vec(),
            self.config.memory_mb.to_string().into_bytes(),
            format!("--file-prefix={prefix}").into_bytes(),
            b"--no-telemetry".to_vec(),
        ];
        if self.config.memory_mode == MWAN_NATIVE_MEMORY_ANONYMOUS {
            args.push(b"--no-huge".to_vec());
            args.push(b"--no-shconf".to_vec());
        } else {
            let directory = c_bytes(&self.config.hugepage_directory).ok_or_else(|| {
                fault(
                    libc::EINVAL,
                    MWAN_NATIVE_OP_CONFIG,
                    MWAN_NATIVE_OBJECT_NONE,
                    0,
                    0,
                    "Invalid hugepage directory",
                )
            })?;
            if !directory.is_empty() {
                let mut argument = b"--huge-dir=".to_vec();
                argument.extend_from_slice(&directory);
                args.push(argument);
            }
        }
        for index in 0..self.config.controller_count as usize {
            let kernels: Vec<String> = self.config.receivers[..self.config.receiver_count as usize]
                .iter()
                .filter(|receiver| receiver.controller == index as u32)
                .map(|receiver| receiver.vf.to_string())
                .collect();
            let mut representors = kernels.clone();
            representors.extend(
                self.config.data[..self.config.data_count as usize]
                    .iter()
                    .filter(|data| data.controller == index as u32)
                    .map(|data| data.vf.to_string()),
            );
            let kernels = kernels.join(",");
            let representors = representors.join(",");
            if kernels.len() >= 128 || representors.len() >= 128 {
                return Err(fault(
                    libc::E2BIG,
                    MWAN_NATIVE_OP_CONFIG,
                    MWAN_NATIVE_OBJECT_CONTROLLER,
                    index as u32,
                    0,
                    "Representor argument exceeds its bound",
                ));
            }
            args.push(b"-a".to_vec());
            args.push(format!("{},representor=[{representors}],kernel-owned-representors=[{kernels}],app-id=0", pci_name(&self.config.controllers[index].pci)).into_bytes());
        }
        for data in &self.config.data[..self.config.data_count as usize] {
            args.push(b"-a".to_vec());
            args.push(format!("{},app-id=255", pci_name(&data.pci)).into_bytes());
        }
        let mut strings: Vec<Vec<c_char>> = args
            .into_iter()
            .map(|mut argument| {
                argument.push(0);
                argument.into_iter().map(|byte| byte as c_char).collect()
            })
            .collect();
        let mut pointers: Vec<*mut c_char> = strings
            .iter_mut()
            .map(|argument| argument.as_mut_ptr())
            .collect();
        let count = pointers.len() as i32;
        pointers.push(ptr::null_mut());
        // SAFETY: Every argv entry owns a writable NUL-terminated allocation,
        // all allocations remain live through EAL parsing, and argc is bounded.
        if unsafe { dpdk::rte_eal_init(count, pointers.as_mut_ptr()) } < 0 {
            return Err(fault(
                sdk_errno(),
                MWAN_NATIVE_OP_EAL,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "EAL initialization failed; inspect DPDK diagnostics",
            ));
        }
        self.eal_ready = true;
        Ok(())
    }

    fn named_port(&mut self, name: String, kind: u32, index: usize) -> NativeResult<u16> {
        let text = CString::new(name.as_bytes()).map_err(|_| {
            fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_PORT,
                kind,
                index as u32,
                0,
                "Invalid port name",
            )
        })?;
        let mut id = 0;
        // SAFETY: The live CString and output port slot match the SDK contract.
        let result = unsafe { dpdk::rte_eth_dev_get_port_by_name(text.as_ptr(), &mut id) };
        if result != 0 {
            return Err(fault(
                result,
                MWAN_NATIVE_OP_PORT,
                kind,
                index as u32,
                0,
                &name,
            ));
        }
        if !self.ports.iter().any(|port| port.id == id) {
            self.ports.push(Port {
                id,
                state: PortState::Discovered,
            });
        }
        let mut info = dpdk::rte_eth_dev_info::default();
        // SAFETY: The SDK identified this port and info is a writable SDK record.
        let result = unsafe { dpdk::rte_eth_dev_info_get(id, &mut info) };
        // SAFETY: The initialized information record remains live and unmodified.
        let driver_name = unsafe { dpdk::mwn_eth_driver_name(&info) };
        if result != 0 || driver_name.is_null() {
            return Err(fault(
                libc::ENOTSUP,
                MWAN_NATIVE_OP_PORT,
                kind,
                index as u32,
                0,
                "Graph requires the BNXT PMD",
            ));
        }
        // SAFETY: Successful device information supplies a live driver-name C string.
        if unsafe { CStr::from_ptr(driver_name) }.to_bytes() != b"net_bnxt" {
            return Err(fault(
                libc::ENOTSUP,
                MWAN_NATIVE_OP_PORT,
                kind,
                index as u32,
                0,
                "Graph requires the BNXT PMD",
            ));
        }
        Ok(id)
    }

    fn representor(
        &mut self,
        controller: usize,
        vf: u16,
        kind: u32,
        index: usize,
    ) -> NativeResult<u16> {
        self.named_port(
            format!(
                "net_{}_representor_{vf}",
                pci_name(&self.config.controllers[controller].pci)
            ),
            kind,
            index,
        )
    }

    fn discover(&mut self) -> NativeResult<()> {
        for index in 0..self.config.controller_count as usize {
            let id = self.named_port(
                pci_name(&self.config.controllers[index].pci),
                MWAN_NATIVE_OBJECT_CONTROLLER,
                index,
            )?;
            self.controllers[index].id = Some(id);
        }
        for index in 0..self.config.receiver_count as usize {
            let receiver = self.config.receivers[index];
            self.receivers[index] = Some(self.representor(
                receiver.controller as usize,
                receiver.vf,
                MWAN_NATIVE_OBJECT_RECEIVER,
                index,
            )?);
        }
        for index in 0..self.config.data_count as usize {
            let data = self.config.data[index];
            let id = self.named_port(pci_name(&data.pci), MWAN_NATIVE_OBJECT_DATA, index)?;
            self.data[index].id = Some(id);
            let mut mac = dpdk::rte_ether_addr::default();
            // SAFETY: The discovered DATA port writes one SDK Ethernet address.
            let result = unsafe { dpdk::rte_eth_macaddr_get(id, &mut mac) };
            if result != 0 || mac.addr_bytes != data.mac {
                return Err(fault(
                    libc::EINVAL,
                    MWAN_NATIVE_OP_PORT,
                    MWAN_NATIVE_OBJECT_DATA,
                    index as u32,
                    0,
                    "DATA VF MAC does not match its configured identity",
                ));
            }
            if data.controller != MWAN_NATIVE_NO_ID {
                self.data[index].representor = Some(self.representor(
                    data.controller as usize,
                    data.vf,
                    MWAN_NATIVE_OBJECT_DATA,
                    index,
                )?);
            }
        }
        for index in 0..self.config.data_count as usize {
            self.check_data_identity(index)?;
        }
        for index in 0..self.config.receiver_count as usize {
            let port = self.receiver_port(index)?;
            // SAFETY: Complete graph discovery and PCI verification precede role changes.
            let result = unsafe {
                dpdk::rte_pmd_bnxt_rep_role_set(port, dpdk::RTE_PMD_BNXT_REP_ROLE_KERNEL)
            };
            if result != 0 {
                return Err(fault(
                    result,
                    MWAN_NATIVE_OP_PORT,
                    MWAN_NATIVE_OBJECT_RECEIVER,
                    index as u32,
                    0,
                    "Kernel receiver role configuration failed",
                ));
            }
        }
        for index in 0..self.config.data_count as usize {
            if let Some(port) = self.data[index].representor {
                // SAFETY: The verified representor belongs to this owned graph.
                let result = unsafe {
                    dpdk::rte_pmd_bnxt_rep_role_set(port, dpdk::RTE_PMD_BNXT_REP_ROLE_DIRECT_DATA)
                };
                if result != 0 {
                    return Err(fault(
                        result,
                        MWAN_NATIVE_OP_PORT,
                        MWAN_NATIVE_OBJECT_DATA,
                        index as u32,
                        0,
                        "DATA representor role configuration failed",
                    ));
                }
            }
        }
        Ok(())
    }

    fn check_data_identity(&self, index: usize) -> NativeResult<()> {
        let data = self.config.data[index];
        if data.controller == MWAN_NATIVE_NO_ID {
            return Ok(());
        }
        let unavailable = |error: std::io::Error| {
            fault(
                error.raw_os_error().unwrap_or(libc::EIO),
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "Cannot verify DATA VF identity; expose the controller PCI, physfn and PF virtfn links in sysfs",
            )
        };
        let controller = std::fs::canonicalize(format!(
            "/sys/bus/pci/devices/{}",
            pci_name(&self.config.controllers[data.controller as usize].pci)
        ))
        .map_err(unavailable)?;
        let parent = match std::fs::canonicalize(controller.join("physfn")) {
            Ok(parent) => parent,
            Err(error) if error.raw_os_error() == Some(libc::ENOENT) => controller,
            Err(error) => return Err(unavailable(error)),
        };
        let expected = std::fs::canonicalize(parent.join(format!("virtfn{}", data.vf)))
            .map_err(unavailable)?;
        let actual = std::fs::canonicalize(format!("/sys/bus/pci/devices/{}", pci_name(&data.pci)))
            .map_err(unavailable)?;
        if expected != actual {
            return Err(fault(
                libc::EXDEV,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "DATA PCI function does not match its controller's represented VF",
            ));
        }
        Ok(())
    }

    fn receiver_port(&self, index: usize) -> NativeResult<u16> {
        self.receivers[index].ok_or_else(|| {
            fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_RECEIVER,
                index as u32,
                0,
                "Kernel receiver port is unavailable",
            )
        })
    }

    fn data_port(&self, index: usize) -> NativeResult<u16> {
        self.data[index].id.ok_or_else(|| {
            fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_DATA,
                index as u32,
                0,
                "DATA port is unavailable",
            )
        })
    }

    fn controller_port(&self, index: usize) -> NativeResult<u16> {
        self.controllers[index].id.ok_or_else(|| {
            fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_CONTROLLER,
                index as u32,
                0,
                "Controller port is unavailable",
            )
        })
    }

    fn ordered_ports(&self, shutdown: bool) -> NativeResult<Vec<u16>> {
        let mut result = Vec::new();
        result.try_reserve_exact(MAXIMUM_PORTS).map_err(|_| {
            fault(
                libc::ENOMEM,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Port order allocation failed",
            )
        })?;
        if !shutdown {
            result.extend(
                self.controllers[..self.config.controller_count as usize]
                    .iter()
                    .filter_map(|port| port.id),
            );
        }
        result.extend(
            self.data[..self.config.data_count as usize]
                .iter()
                .filter_map(|port| port.id),
        );
        if shutdown {
            result.extend(
                self.data[..self.config.data_count as usize]
                    .iter()
                    .filter_map(|port| port.representor),
            );
        }
        result.extend(
            self.receivers[..self.config.receiver_count as usize]
                .iter()
                .copied()
                .flatten(),
        );
        if !shutdown {
            result.extend(
                self.data[..self.config.data_count as usize]
                    .iter()
                    .filter_map(|port| port.representor),
            );
        }
        if shutdown {
            result.extend(
                self.controllers[..self.config.controller_count as usize]
                    .iter()
                    .filter_map(|port| port.id),
            );
        }
        for port in &self.ports {
            if !result.contains(&port.id) {
                result.push(port.id);
            }
        }
        Ok(result)
    }

    fn configure_port(&mut self, id: u16) -> NativeResult<()> {
        let mut config = dpdk::rte_eth_conf::default();
        // SAFETY: The writable opaque record has the SDK's generated layout.
        unsafe { dpdk::mwn_eth_conf_set_mtu(&mut config, u32::from(self.config.mtu)) };
        // Representors do not advertise link status change interrupts.
        if self.data[..self.config.data_count as usize]
            .iter()
            .any(|port| port.id == Some(id))
        {
            // SAFETY: The mutable configuration is a live, aligned SDK record.
            unsafe { dpdk::mwn_eth_conf_enable_lsc(&mut config) };
        }
        let mut info = dpdk::rte_eth_dev_info::default();
        // SAFETY: Discovery identified this owned port and info is writable.
        let mut result = unsafe { dpdk::rte_eth_dev_info_get(id, &mut info) };
        if result != 0 {
            return Err(fault(
                result,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_PORT,
                u32::from(id),
                0,
                "Device information failed",
            ));
        }
        // SAFETY: The SDK record is initialized, and this owner configures its
        // inactive port with one RX and one TX queue.
        result = unsafe { dpdk::rte_eth_dev_configure(id, 1, 1, &config) };
        let mut rx = self.config.rx_descriptors;
        let mut tx = self.config.tx_descriptors;
        if result == 0 {
            // SAFETY: Both descriptor output pointers refer to live u16 values.
            result = unsafe { dpdk::rte_eth_dev_adjust_nb_rx_tx_desc(id, &mut rx, &mut tx) };
        }
        // SAFETY: The port remains owned and discovered.
        let socket = unsafe { dpdk::rte_eth_dev_socket_id(id) };
        let socket = if socket < 0 { u32::MAX } else { socket as u32 };
        let pool = self.pool.ok_or_else(|| {
            fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_PORT,
                u32::from(id),
                0,
                "Packet pool is unavailable",
            )
        })?;
        if result == 0 {
            // SAFETY: The live pool remains owned until all these ports close.
            // The default RX record belongs to the successful device-info result.
            result = unsafe {
                dpdk::rte_eth_rx_queue_setup(
                    id,
                    0,
                    rx,
                    socket,
                    dpdk::mwn_eth_default_rxconf(&info),
                    pool.as_ptr(),
                )
            };
        }
        if result == 0 {
            // SAFETY: The inactive owned port and initialized default TX record
            // remain live for this queue setup call.
            result = unsafe {
                dpdk::rte_eth_tx_queue_setup(id, 0, tx, socket, dpdk::mwn_eth_default_txconf(&info))
            };
        }
        if result != 0 {
            return Err(fault(
                result,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_PORT,
                u32::from(id),
                0,
                "Port queue configuration failed",
            ));
        }
        if let Some(port) = self.ports.iter_mut().find(|port| port.id == id) {
            port.state = PortState::Configured;
        }
        Ok(())
    }

    fn start_port(&mut self, id: u16) -> NativeResult<()> {
        let port = self
            .ports
            .iter_mut()
            .find(|port| port.id == id)
            .ok_or_else(|| {
                fault(
                    libc::ENODEV,
                    MWAN_NATIVE_OP_PORT,
                    MWAN_NATIVE_OBJECT_PORT,
                    u32::from(id),
                    0,
                    "Port ownership is unavailable",
                )
            })?;
        if port.state == PortState::Started {
            return Ok(());
        }
        // SAFETY: The configured port belongs to this EAL owner thread.
        let result = unsafe { dpdk::rte_eth_dev_start(id) };
        if result != 0 {
            return Err(fault(
                result,
                MWAN_NATIVE_OP_PORT,
                MWAN_NATIVE_OBJECT_PORT,
                u32::from(id),
                0,
                "Port start failed",
            ));
        }
        port.state = PortState::Started;
        Ok(())
    }

    fn miss(&mut self, index: usize, install: bool) -> NativeResult<()> {
        let port = self.controller_port(index)?;
        let receiver =
            self.receiver_port(self.config.controllers[index].physical_receiver as usize)?;
        let mut attributes = dpdk::rte_flow_group_attr::default();
        let mut destination = dpdk::rte_flow_action_port_id::default();
        // SAFETY: These initialized SDK records remain exclusively writable.
        unsafe {
            dpdk::mwn_flow_group_attr_set_transfer(&mut attributes, true);
            dpdk::mwn_flow_port_id_set(&mut destination, u32::from(receiver));
        }
        let mut actions = [dpdk::rte_flow_action::default(); 2];
        actions[0].type_ = if install {
            dpdk::RTE_FLOW_ACTION_TYPE_PORT_ID
        } else {
            dpdk::RTE_FLOW_ACTION_TYPE_END
        };
        if install {
            actions[0].conf = (&destination as *const dpdk::rte_flow_action_port_id).cast();
        }
        actions[1].type_ = dpdk::RTE_FLOW_ACTION_TYPE_END;
        let mut detail = dpdk::rte_flow_error::default();
        // SAFETY: Action payloads and the terminated action array remain live
        // during this SDK call on the owned controller.
        let result = unsafe {
            dpdk::rte_flow_group_set_miss_actions(
                port,
                0,
                &attributes,
                actions.as_ptr(),
                &mut detail,
            )
        };
        if result != 0 {
            return Err(flow_fault(
                result,
                MWAN_NATIVE_OP_MISS,
                MWAN_NATIVE_OBJECT_CONTROLLER,
                index as u32,
                0,
                &detail,
            ));
        }
        self.controllers[index].miss = install;
        Ok(())
    }

    fn activate(&mut self) -> NativeResult<()> {
        for port in self.ordered_ports(false)? {
            self.start_port(port)?;
        }
        for index in 0..self.config.controller_count as usize {
            self.miss(index, true)?;
        }
        self.quiescing = false;
        self.active = true;
        Ok(())
    }
}

fn tuple_valid(tuple: &MwanNativeTuple) -> bool {
    tuple.reserved == 0
        && matches!(
            u32::from(tuple.family),
            MWAN_NATIVE_FAMILY_IPV4 | MWAN_NATIVE_FAMILY_IPV6
        )
        && matches!(
            u32::from(tuple.protocol),
            MWAN_NATIVE_PROTOCOL_TCP | MWAN_NATIVE_PROTOCOL_UDP
        )
        && tuple.source_port != 0
        && tuple.destination_port != 0
        && tuple.source != [0; 16]
        && tuple.destination != [0; 16]
        && (u32::from(tuple.family) != MWAN_NATIVE_FAMILY_IPV4
            || (tuple.source[4..] == [0; 12] && tuple.destination[4..] == [0; 12]))
}

fn same_addresses(left: &MwanNativeTuple, right: &MwanNativeTuple) -> bool {
    left.family == right.family
        && left.protocol == right.protocol
        && left.source == right.source
        && left.destination == right.destination
}

fn ipv6_sum(address: &[u8; 16]) -> u16 {
    let mut sum: u32 = address
        .chunks_exact(2)
        .map(|word| u32::from(u16::from_be_bytes([word[0], word[1]])))
        .sum();
    while sum > u32::from(u16::MAX) {
        sum = (sum & u32::from(u16::MAX)) + (sum >> 16);
    }
    if sum == u32::from(u16::MAX) {
        0
    } else {
        sum as u16
    }
}

fn ipv4_word(address: &[u8; 16]) -> u32 {
    u32::from_ne_bytes([address[0], address[1], address[2], address[3]])
}

fn action<T>(kind: u32, value: &T) -> dpdk::rte_flow_action {
    dpdk::rte_flow_action {
        type_: kind,
        conf: (value as *const T).cast(),
    }
}

fn item<T>(kind: u32, value: &T, mask: &T) -> dpdk::rte_flow_item {
    dpdk::rte_flow_item {
        type_: kind,
        spec: (value as *const T).cast(),
        last: ptr::null(),
        mask: (mask as *const T).cast(),
    }
}

impl OwnerState {
    fn direction_valid(&self, direction: &MwanNativeDirection, index: usize) -> NativeResult<()> {
        if direction.origin_id >= self.config.origin_count
            || direction.egress_data_id >= self.config.data_count
            || direction.reserved != 0
            || !tuple_valid(&direction.original)
            || !tuple_valid(&direction.translated)
            || direction.original.family != direction.translated.family
            || direction.original.protocol != direction.translated.protocol
            || direction.output_mtu < MINIMUM_MTU
            || direction.output_mtu > self.config.mtu
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_ORIGIN,
                direction.origin_id,
                index as u32,
                "Invalid exact direction",
            ));
        }
        if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV6 {
            if direction.original.source_port != direction.translated.source_port
                || direction.original.destination_port != direction.translated.destination_port
                || ipv6_sum(&direction.original.source) != ipv6_sum(&direction.translated.source)
                || ipv6_sum(&direction.original.destination)
                    != ipv6_sum(&direction.translated.destination)
            {
                return Err(fault(
                    libc::ENOTSUP,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    direction.origin_id,
                    index as u32,
                    "IPv6 translation requires unchanged ports and independently neutral addresses",
                ));
            }
        }
        for address in [
            &direction.original_source_mac,
            &direction.original_destination_mac,
            &direction.output_source_mac,
            &direction.output_destination_mac,
        ] {
            if !valid_mac(address) {
                return Err(fault(
                    libc::EINVAL,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    direction.origin_id,
                    index as u32,
                    "Direction requires unicast MAC addresses",
                ));
            }
        }
        let origin = self.config.origins[direction.origin_id as usize];
        if direction.original_destination_mac != self.config.receivers[origin.receiver as usize].mac
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_ORIGIN,
                direction.origin_id,
                index as u32,
                "Original destination must identify the origin's kernel receiver",
            ));
        }
        Ok(())
    }

    fn collision(&self, first: &MwanNativeDirection, second: &MwanNativeDirection) -> bool {
        let left = self.config.origins[first.origin_id as usize];
        let right = self.config.origins[second.origin_id as usize];
        if left.controller == right.controller
            && first.original == second.original
            && first.original_source_mac == second.original_source_mac
            && first.original_destination_mac == second.original_destination_mac
        {
            return true;
        }
        if first.origin_id != second.origin_id {
            return false;
        }
        first.original == second.original
            || first.translated == second.translated
            || (same_addresses(&first.translated, &second.translated)
                && (!same_addresses(&first.original, &second.original)
                    || first.original_source_mac != second.original_source_mac
                    || first.original_destination_mac != second.original_destination_mac))
    }

    fn build_flow(
        &self,
        direction: &MwanNativeDirection,
        index: usize,
        validate: bool,
    ) -> NativeResult<Option<NonNull<dpdk::rte_flow>>> {
        let origin = self.config.origins[direction.origin_id as usize];
        let controller = self.controller_port(origin.controller as usize)?;
        let representor = self.data[origin.data as usize].representor.ok_or_else(|| {
            fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_ORIGIN,
                direction.origin_id,
                index as u32,
                "DATA representor is unavailable",
            )
        })?;
        let mut attributes = dpdk::rte_flow_attr::default();
        // SAFETY: The initialized SDK attributes remain exclusively writable.
        unsafe { dpdk::mwn_flow_attr_set_transfer(&mut attributes, true) };
        let mut ethernet = dpdk::rte_flow_item_eth::default();
        let mut ethernet_mask = dpdk::rte_flow_item_eth::default();
        let ether_type = if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV4 {
            0x0800_u16
        } else {
            0x86dd_u16
        }
        .to_be();
        // SAFETY: Each source contains six bytes, and both writable SDK
        // records retain their generated size and alignment throughout copying.
        unsafe {
            dpdk::mwn_flow_eth_set(
                &mut ethernet,
                direction.original_source_mac.as_ptr(),
                direction.original_destination_mac.as_ptr(),
                ether_type,
            );
            dpdk::mwn_flow_eth_set(
                &mut ethernet_mask,
                [u8::MAX; 6].as_ptr(),
                [u8::MAX; 6].as_ptr(),
                u16::MAX,
            );
        }
        let mut ip4 = dpdk::rte_flow_item_ipv4::default();
        let mut ip4_mask = dpdk::rte_flow_item_ipv4::default();
        let mut ip6 = dpdk::rte_flow_item_ipv6::default();
        let mut ip6_mask = dpdk::rte_flow_item_ipv6::default();
        let mut tcp = dpdk::rte_flow_item_tcp::default();
        let mut tcp_mask = dpdk::rte_flow_item_tcp::default();
        let mut udp = dpdk::rte_flow_item_udp::default();
        let mut udp_mask = dpdk::rte_flow_item_udp::default();
        let mut items = [dpdk::rte_flow_item::default(); 4];
        items[0] = item(dpdk::RTE_FLOW_ITEM_TYPE_ETH, &ethernet, &ethernet_mask);
        if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV4 {
            // SAFETY: The initialized SDK records remain writable. Rust has
            // already selected the exact network-order fields and mask values.
            unsafe {
                dpdk::mwn_flow_ipv4_set(
                    &mut ip4,
                    ipv4_word(&direction.original.source),
                    ipv4_word(&direction.original.destination),
                    direction.original.protocol,
                );
                dpdk::mwn_flow_ipv4_set(&mut ip4_mask, u32::MAX, u32::MAX, u8::MAX);
            }
            items[1] = item(dpdk::RTE_FLOW_ITEM_TYPE_IPV4, &ip4, &ip4_mask);
        } else {
            // SAFETY: Both address sources contain sixteen bytes. The records
            // remain exclusively writable during these exact field copies.
            unsafe {
                dpdk::mwn_flow_ipv6_set(
                    &mut ip6,
                    direction.original.source.as_ptr(),
                    direction.original.destination.as_ptr(),
                    direction.original.protocol,
                );
                dpdk::mwn_flow_ipv6_set(
                    &mut ip6_mask,
                    [u8::MAX; 16].as_ptr(),
                    [u8::MAX; 16].as_ptr(),
                    u8::MAX,
                );
            }
            items[1] = item(dpdk::RTE_FLOW_ITEM_TYPE_IPV6, &ip6, &ip6_mask);
        }
        if u32::from(direction.original.protocol) == MWAN_NATIVE_PROTOCOL_TCP {
            // SAFETY: The writable SDK records receive only these copied fields.
            unsafe {
                dpdk::mwn_flow_tcp_set(
                    &mut tcp,
                    direction.original.source_port.to_be(),
                    direction.original.destination_port.to_be(),
                );
                dpdk::mwn_flow_tcp_set(&mut tcp_mask, u16::MAX, u16::MAX);
            }
            items[2] = item(dpdk::RTE_FLOW_ITEM_TYPE_TCP, &tcp, &tcp_mask);
        } else {
            // SAFETY: The writable SDK records receive only these copied fields.
            unsafe {
                dpdk::mwn_flow_udp_set(
                    &mut udp,
                    direction.original.source_port.to_be(),
                    direction.original.destination_port.to_be(),
                );
                dpdk::mwn_flow_udp_set(&mut udp_mask, u16::MAX, u16::MAX);
            }
            items[2] = item(dpdk::RTE_FLOW_ITEM_TYPE_UDP, &udp, &udp_mask);
        }
        items[3].type_ = dpdk::RTE_FLOW_ITEM_TYPE_END;
        let mut actions = [dpdk::rte_flow_action::default(); 7];
        let mut count = 0;
        let mut source4 = dpdk::rte_flow_action_set_ipv4::default();
        let mut destination4 = dpdk::rte_flow_action_set_ipv4::default();
        let mut source6 = dpdk::rte_flow_action_set_ipv6::default();
        let mut destination6 = dpdk::rte_flow_action_set_ipv6::default();
        let mut source_port = dpdk::rte_flow_action_set_tp::default();
        let mut destination_port = dpdk::rte_flow_action_set_tp::default();
        if direction.original.source != direction.translated.source {
            if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV6 {
                source6.ipv6_addr.a = direction.translated.source;
                actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_IPV6_SRC, &source6);
            } else {
                source4.ipv4_addr = ipv4_word(&direction.translated.source);
                actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_IPV4_SRC, &source4);
            }
            count += 1;
        }
        if direction.original.destination != direction.translated.destination {
            if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV6 {
                destination6.ipv6_addr.a = direction.translated.destination;
                actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_IPV6_DST, &destination6);
            } else {
                destination4.ipv4_addr = ipv4_word(&direction.translated.destination);
                actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_IPV4_DST, &destination4);
            }
            count += 1;
        }
        if direction.original.source_port != direction.translated.source_port {
            source_port.port = direction.translated.source_port.to_be();
            actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_TP_SRC, &source_port);
            count += 1;
        }
        if direction.original.destination_port != direction.translated.destination_port {
            destination_port.port = direction.translated.destination_port.to_be();
            actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_SET_TP_DST, &destination_port);
            count += 1;
        }
        let counter = dpdk::rte_flow_action_count::default();
        let mut output = dpdk::rte_flow_action_port_id::default();
        // SAFETY: The initialized destination record remains exclusively writable.
        unsafe { dpdk::mwn_flow_port_id_set(&mut output, u32::from(representor)) };
        actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_COUNT, &counter);
        count += 1;
        actions[count] = action(dpdk::RTE_FLOW_ACTION_TYPE_PORT_ID, &output);
        count += 1;
        actions[count].type_ = dpdk::RTE_FLOW_ACTION_TYPE_END;
        let mut detail = dpdk::rte_flow_error::default();
        if validate {
            // SAFETY: Every item, mask and action payload remains live and
            // immovable through this synchronous validation call. Arrays end
            // with SDK END items, and the controller belongs to this owner.
            let result = unsafe {
                dpdk::rte_flow_validate(
                    controller,
                    &attributes,
                    items.as_ptr(),
                    actions.as_ptr(),
                    &mut detail,
                )
            };
            if result != 0 {
                return Err(flow_fault(
                    result,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    direction.origin_id,
                    index as u32,
                    &detail,
                ));
            }
            return Ok(None);
        }
        // SAFETY: The synchronous SDK call copies the same live, terminated
        // item and action records into an owned flow on this controller.
        let flow = unsafe {
            dpdk::rte_flow_create(
                controller,
                &attributes,
                items.as_ptr(),
                actions.as_ptr(),
                &mut detail,
            )
        };
        NonNull::new(flow).map(Some).ok_or_else(|| {
            flow_fault(
                sdk_errno(),
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_ORIGIN,
                direction.origin_id,
                index as u32,
                &detail,
            )
        })
    }

    fn pair_result(&self, slot: usize) -> MwanNativePairResult {
        self.result_for(&self.pairs[slot], slot)
    }

    fn result_for(&self, pair: &Pair, slot: usize) -> MwanNativePairResult {
        let mut result = MwanNativePairResult {
            handle: MwanNativeHandle {
                instance: self.instance,
                generation: pair.generation,
                slot: slot as u32,
                reserved: 0,
            },
            state: pair.state.value(),
            requested_direction_mask: pair.spec.requested_direction_mask,
            ..MwanNativePairResult::default()
        };
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if pair.legs[index].flow.is_some() {
                result.installed_mask |= mask;
            }
            if pair.legs[index].inverse {
                result.restoration_mask |= mask;
            }
            if pair.legs[index].delete_failed {
                result.cleanup_failed_mask |= mask;
            }
        }
        result
    }

    fn handle_pair(&self, handle: MwanNativeHandle) -> NativeResult<&Pair> {
        let slot = handle.slot as usize;
        if handle.reserved != 0
            || handle.instance != self.instance
            || slot >= self.pairs.len()
            || handle.generation == 0
        {
            return Err(stale_handle(handle));
        }
        if self.pairs[slot].generation == handle.generation {
            return Ok(&self.pairs[slot]);
        }
        self.retained
            .get(&handle)
            .ok_or_else(|| stale_handle(handle))
    }

    fn retain_pair(&mut self, slot: usize) -> NativeResult<()> {
        let pair = &self.pairs[slot];
        if pair.state != PairState::Draining
            || pair
                .legs
                .iter()
                .any(|leg| leg.flow.is_some() || leg.delete_failed)
        {
            return Err(fault(
                libc::EBUSY,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                slot as u32,
                0,
                "Pair hardware cleanup has not completed",
            ));
        }
        if self.retained.len() >= self.config.pair_capacity as usize {
            return Err(fault(
                libc::ENOSPC,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                slot as u32,
                0,
                "Retained inverse capacity exhausted; DATA ports must retire before reclamation",
            ));
        }
        let handle = self.pair_result(slot).handle;
        let generation = self.pairs[slot].generation;
        let pair = mem::replace(
            &mut self.pairs[slot],
            Pair {
                generation,
                ..Pair::default()
            },
        );
        self.retained.insert(handle, pair);
        Ok(())
    }

    fn retirement_stats(&self) -> MwanNativeRetirementStats {
        MwanNativeRetirementStats {
            active_pair_capacity: self.config.pair_capacity,
            retained_pair_capacity: self.config.pair_capacity,
            retained_pairs: self.retained.len() as u32,
            draining_pairs: self
                .pairs
                .iter()
                .filter(|pair| pair.state == PairState::Draining)
                .count() as u32,
            retained_directions: self
                .retained
                .values()
                .map(|pair| pair.legs.iter().filter(|leg| leg.inverse).count() as u64)
                .sum(),
        }
    }

    fn forget_pair(&mut self, slot: usize) {
        let pair = &mut self.pairs[slot];
        for index in 0..2 {
            if pair.legs[index].inverse {
                let spec = pair.spec.directions[index];
                self.inverses.remove(&InverseKey {
                    origin: spec.origin_id,
                    tuple: spec.translated,
                });
            }
            pair.legs[index].inverse = false;
        }
        pair.state = PairState::Absent;
    }

    fn delete_pair(&mut self, slot: usize) -> NativeResult<()> {
        if self.pairs[slot].state == PairState::Absent {
            return Ok(());
        }
        self.pairs[slot].state = PairState::Draining;
        let mut failure = self.restore_pending().err();
        for index in 0..2 {
            let spec = self.pairs[slot].spec.directions[index];
            if self.pairs[slot].legs[index].delete_failed {
                if failure.is_none() {
                    failure = Some(fault(
                        libc::EIO,
                        MWAN_NATIVE_OP_DESTROY,
                        MWAN_NATIVE_OBJECT_ORIGIN,
                        spec.origin_id,
                        index as u32,
                        "Consumed flow handle cannot be retried",
                    ));
                }
                continue;
            }
            let Some(flow) = self.pairs[slot].legs[index].flow else {
                continue;
            };
            let controller = match self
                .controller_port(self.config.origins[spec.origin_id as usize].controller as usize)
            {
                Ok(controller) => controller,
                Err(error) => {
                    if failure.is_none() {
                        failure = Some(error);
                    }
                    continue;
                }
            };
            let mut detail = dpdk::rte_flow_error::default();
            // SAFETY: This live handle belongs to this controller. The SDK
            // consumes it once; Rust clears it immediately after the call.
            // BNXT can consume its FID even when deletion reports an error.
            let result = unsafe { dpdk::rte_flow_destroy(controller, flow.as_ptr(), &mut detail) };
            self.pairs[slot].legs[index].flow = None;
            if result != 0 {
                self.pairs[slot].legs[index].delete_failed = true;
                if failure.is_none() {
                    failure = Some(flow_fault(
                        result,
                        MWAN_NATIVE_OP_DESTROY,
                        MWAN_NATIVE_OBJECT_ORIGIN,
                        spec.origin_id,
                        index as u32,
                        &detail,
                    ));
                }
            }
        }
        if let Some(error) = failure {
            self.pairs[slot].state = PairState::Faulted;
            self.faulted = true;
            return Err(error);
        }
        Ok(())
    }

    fn install_pair(
        &mut self,
        spec: MwanNativePairSpec,
        guards: Option<[MwanNativePacketGuard; 2]>,
        result: &mut MwanNativePairResult,
    ) -> NativeResult<()> {
        if !self.active
            || self.faulted
            || self.quiescing
            || spec.requested_direction_mask == 0
            || spec.requested_direction_mask > 3
            || spec.reserved != 0
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Worker cannot admit a pair",
            ));
        }
        *result = MwanNativePairResult::default();
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask != 0 {
                self.direction_valid(&spec.directions[index], index)?;
                if let Some(guards) = guards {
                    let guard = guards[index];
                    if guard.reserved != [0; 3]
                        || guard.flow_label > 0x000f_ffff
                        || (u32::from(spec.directions[index].original.family)
                            == MWAN_NATIVE_FAMILY_IPV4
                            && guard.flow_label != 0)
                    {
                        return Err(fault(
                            libc::EINVAL,
                            MWAN_NATIVE_OP_VALIDATE,
                            MWAN_NATIVE_OBJECT_ORIGIN,
                            spec.directions[index].origin_id,
                            index as u32,
                            "Invalid observed packet selector guard",
                        ));
                    }
                }
            }
        }
        let mut free = None;
        let mut draining = None;
        for (slot, pair) in self.pairs.iter().enumerate() {
            if pair.state == PairState::Absent {
                if free.is_none() && pair.generation != u64::MAX {
                    free = Some(slot);
                }
                continue;
            }
            if draining.is_none()
                && pair.state == PairState::Draining
                && pair.generation != u64::MAX
            {
                draining = Some(slot);
            }
            for (first, first_mask) in DIRECTION_MASKS.iter().enumerate() {
                for (second, second_mask) in DIRECTION_MASKS.iter().enumerate() {
                    if spec.requested_direction_mask & first_mask != 0
                        && pair.spec.requested_direction_mask & second_mask != 0
                        && self.collision(&spec.directions[first], &pair.spec.directions[second])
                    {
                        return Err(fault(
                            libc::EEXIST,
                            MWAN_NATIVE_OP_VALIDATE,
                            MWAN_NATIVE_OBJECT_ORIGIN,
                            spec.directions[first].origin_id,
                            first as u32,
                            "Direction conflicts with an existing packet or restoration key",
                        ));
                    }
                }
            }
        }
        for pair in self.retained.values() {
            for (first, first_mask) in DIRECTION_MASKS.iter().enumerate() {
                for (second, second_mask) in DIRECTION_MASKS.iter().enumerate() {
                    if spec.requested_direction_mask & first_mask != 0
                        && pair.spec.requested_direction_mask & second_mask != 0
                        && self.collision(&spec.directions[first], &pair.spec.directions[second])
                    {
                        return Err(fault(
                            libc::EEXIST,
                            MWAN_NATIVE_OP_VALIDATE,
                            MWAN_NATIVE_OBJECT_ORIGIN,
                            spec.directions[first].origin_id,
                            first as u32,
                            "Direction conflicts with a retained restoration mapping",
                        ));
                    }
                }
            }
        }
        let slot = free.or(draining).ok_or_else(|| {
            fault(
                libc::ENOSPC,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                MWAN_NATIVE_NO_ID,
                0,
                "Pair capacity exhausted",
            )
        })?;
        if spec.requested_direction_mask == 3
            && self.collision(&spec.directions[0], &spec.directions[1])
        {
            return Err(fault(
                libc::EEXIST,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_ORIGIN,
                spec.directions[0].origin_id,
                0,
                "Pair directions overlap",
            ));
        }
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask != 0 {
                self.build_flow(&spec.directions[index], index, true)?;
            }
        }
        if self.pairs[slot].state == PairState::Draining {
            self.retain_pair(slot)?;
        }
        let generation = self.pairs[slot].generation + 1;
        self.pairs[slot] = Pair {
            generation,
            spec,
            guards,
            ..Pair::default()
        };
        self.pairs[slot].state = PairState::Draining;
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask == 0 {
                continue;
            }
            let direction = spec.directions[index];
            let key = InverseKey {
                origin: direction.origin_id,
                tuple: direction.translated,
            };
            if self.inverses.contains_key(&key) {
                let original = fault(
                    libc::EEXIST,
                    MWAN_NATIVE_OP_CREATE,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    direction.origin_id,
                    index as u32,
                    "Packet lookup allocation failed",
                );
                let cleanup = self.delete_pair(slot);
                *result = self.pair_result(slot);
                return cleanup.and(Err(original));
            }
            self.inverses.insert(
                key,
                LegId {
                    slot,
                    generation,
                    direction: index,
                },
            );
            self.pairs[slot].legs[index].inverse = true;
        }
        self.activate_pair(slot, result)
    }

    fn resume_pair(
        &mut self,
        handle: MwanNativeHandle,
        spec: MwanNativePairSpec,
        guards: [MwanNativePacketGuard; 2],
        result: &mut MwanNativePairResult,
    ) -> NativeResult<()> {
        *result = MwanNativePairResult::default();
        let pair = self.handle_pair(handle)?;
        let slot = handle.slot as usize;
        *result = self.result_for(pair, slot);
        if self.pairs[slot].generation != handle.generation {
            return Err(stale_handle(handle));
        }
        if !self.active
            || self.faulted
            || self.quiescing
            || pair.state != PairState::Draining
            || pair
                .legs
                .iter()
                .any(|leg| leg.flow.is_some() || leg.delete_failed)
        {
            return Err(fault(
                libc::EBUSY,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                handle.slot,
                0,
                "Pair resumption requires complete hardware cleanup and an active owner",
            ));
        }
        let mut expected = pair.spec;
        expected.policy_generation = spec.policy_generation;
        if spec.policy_generation < pair.spec.policy_generation
            || spec != expected
            || pair.guards != Some(guards)
        {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_PAIR,
                handle.slot,
                0,
                "Pair resumption requires identical connection, direction and selector records",
            ));
        }
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask == 0 {
                continue;
            }
            let direction = spec.directions[index];
            let key = InverseKey {
                origin: direction.origin_id,
                tuple: direction.translated,
            };
            let inverse_owned = pair.legs[index].inverse
                && self.inverses.get(&key).is_some_and(|leg| {
                    leg.slot == slot
                        && leg.generation == handle.generation
                        && leg.direction == index
                });
            if !inverse_owned {
                return Err(fault(
                    libc::ESTALE,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_PAIR,
                    handle.slot,
                    index as u32,
                    "Pair inverse ownership changed before resumption",
                ));
            }
            self.build_flow(&direction, index, true)?;
        }
        self.activate_pair(slot, result)?;
        self.pairs[slot].spec.policy_generation = spec.policy_generation;
        Ok(())
    }

    fn activate_pair(
        &mut self,
        slot: usize,
        result: &mut MwanNativePairResult,
    ) -> NativeResult<()> {
        let spec = self.pairs[slot].spec;
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask == 0 {
                continue;
            }
            match self.build_flow(&spec.directions[index], index, false) {
                Ok(flow) => self.pairs[slot].legs[index].flow = flow,
                Err(original) => {
                    let cleanup = self.delete_pair(slot);
                    *result = self.pair_result(slot);
                    return cleanup.and(Err(original));
                }
            }
        }
        self.pairs[slot].state = PairState::Installed;
        *result = self.pair_result(slot);
        Ok(())
    }

    fn query_pair(
        &self,
        handle: MwanNativeHandle,
        stats: &mut MwanNativePairStats,
    ) -> NativeResult<()> {
        *stats = MwanNativePairStats::default();
        let pair = self.handle_pair(handle)?;
        stats.result = self.result_for(pair, handle.slot as usize);
        let mut failure = None;
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            let out = &mut stats.directions[index];
            if pair.spec.requested_direction_mask & mask == 0 {
                out.error = -libc::ENOTSUP;
                continue;
            }
            let spec = pair.spec.directions[index];
            let mut count = dpdk::rte_flow_query_count::default();
            let action = dpdk::rte_flow_action {
                type_: dpdk::RTE_FLOW_ACTION_TYPE_COUNT,
                conf: ptr::null(),
            };
            let mut detail = dpdk::rte_flow_error::default();
            let result = match (pair.state, pair.legs[index].flow) {
                (PairState::Installed, Some(flow)) => {
                    let controller = self.controller_port(
                        self.config.origins[spec.origin_id as usize].controller as usize,
                    )?;
                    // SAFETY: The live flow and controller match, and the SDK
                    // receives initialized action, count and diagnostic records.
                    unsafe {
                        dpdk::rte_flow_query(
                            controller,
                            flow.as_ptr(),
                            &action,
                            (&mut count as *mut dpdk::rte_flow_query_count).cast(),
                            &mut detail,
                        )
                    }
                }
                _ => -libc::ENOENT,
            };
            let valid = if result == 0 {
                // SAFETY: The successful query initialized this live count
                // record. These helpers read only its validity bits.
                unsafe {
                    dpdk::mwn_flow_count_hits_valid(&count)
                        && dpdk::mwn_flow_count_bytes_valid(&count)
                }
            } else {
                false
            };
            let result = if result == 0 && !valid {
                -libc::EAGAIN
            } else {
                result
            };
            out.error = result;
            if result != 0 {
                if failure.is_none() {
                    failure = Some(flow_fault(
                        result,
                        MWAN_NATIVE_OP_QUERY,
                        MWAN_NATIVE_OBJECT_ORIGIN,
                        spec.origin_id,
                        index as u32,
                        &detail,
                    ));
                }
                continue;
            }
            // SAFETY: Both fields have valid bits and this SDK record remains live.
            unsafe {
                out.packets = dpdk::mwn_flow_count_hits(&count);
                out.bytes = dpdk::mwn_flow_count_bytes(&count);
            }
            out.packets_valid = 1;
            out.bytes_valid = 1;
            out.observed_monotonic_ns = monotonic_ns();
        }
        if let Some(error) = failure {
            Err(error)
        } else {
            Ok(())
        }
    }
}

impl OwnerState {
    fn update_pending_stats(&mut self) {
        self.stats.pending_packets = (self.pending.len() + self.received.len()) as u64;
    }

    fn restore_pending(&mut self) -> NativeResult<()> {
        for index in 0..self.pending.len() {
            let Some(prepared) = self.pending[index].prepared else {
                continue;
            };
            let installed = prepared.handle.instance == self.instance
                && self
                    .pairs
                    .get(prepared.handle.slot as usize)
                    .is_some_and(|pair| {
                        pair.generation == prepared.handle.generation
                            && pair.state == PairState::Installed
                    });
            if installed && !self.quiescing && !self.faulted {
                continue;
            }
            let restored = match self.pending[index].packet.frame() {
                Ok(frame) => packet::restore_forwarded(frame, &prepared.view, &prepared.direction)
                    .map_err(|error| error.code),
                Err(code) => Err(code),
            };
            if let Err(code) = restored {
                let mut error = fault(
                    code,
                    MWAN_NATIVE_OP_PACKET,
                    MWAN_NATIVE_OBJECT_PAIR,
                    prepared.handle.slot,
                    0,
                    "Pending packet restoration failed; buffer and owner retained",
                );
                error.packet = packet::diagnostic(&prepared.view);
                error.packet.decode_status = -code;
                self.quiescing = true;
                self.faulted = true;
                if self.terminal_failure.is_none() {
                    self.terminal_failure = Some(*error);
                }
                self.update_pending_stats();
                return Err(error);
            }
            let pending = &mut self.pending[index];
            pending.port = prepared.return_port;
            pending.kernel = true;
            pending.prepared = None;
            self.stats.unsupported = self.stats.unsupported.wrapping_add(1);
        }
        Ok(())
    }

    fn flush_pending(&mut self) -> NativeResult<()> {
        self.restore_pending()?;
        while let Some(front) = self.pending.front() {
            let port = front.port;
            let mut pointers = [ptr::null_mut(); BURST];
            let mut count = 0;
            for pending in self.pending.iter().take(BURST) {
                if pending.port != port {
                    break;
                }
                pointers[count] = pending.packet.pointer.as_ptr();
                count += 1;
            }
            // SAFETY: Every pointer identifies a distinct, live, owned mbuf.
            // TX consumes exactly the accepted prefix, which Rust immediately
            // removes without allocating storage or borrowing packet bytes.
            let sent = usize::from(unsafe {
                dpdk::mwn_tx_burst(port, 0, pointers.as_mut_ptr(), count as u16)
            });
            for _ in 0..sent.min(count) {
                if let Some(pending) = self.pending.pop_front() {
                    pending.packet.transfer();
                    if pending.kernel {
                        self.stats.returned_to_kernel =
                            self.stats.returned_to_kernel.wrapping_add(1);
                    } else {
                        self.stats.forwarded = self.stats.forwarded.wrapping_add(1);
                    }
                }
            }
            if sent > count {
                self.faulted = true;
                let error = fault(
                    libc::EIO,
                    MWAN_NATIVE_OP_PACKET,
                    MWAN_NATIVE_OBJECT_PORT,
                    u32::from(port),
                    0,
                    "SDK returned an invalid transmit count",
                );
                self.terminal_failure = Some(*error);
                self.update_pending_stats();
                return Err(error);
            }
            if sent != count {
                break;
            }
        }
        self.update_pending_stats();
        Ok(())
    }

    fn packet_leg(&self, origin: usize, view: &packet::ParsedPacket) -> Option<PacketLeg> {
        if view.ports {
            let leg = self
                .inverses
                .get(&InverseKey {
                    origin: origin as u32,
                    tuple: view.tuple,
                })
                .copied()?;
            let handle = MwanNativeHandle {
                instance: self.instance,
                generation: leg.generation,
                slot: leg.slot as u32,
                reserved: 0,
            };
            let pair = self.handle_pair(handle).ok()?;
            return Some(PacketLeg {
                spec: pair.spec.directions[leg.direction],
                guard: pair.guards.map(|guards| guards[leg.direction]),
                handle: Some(handle),
                installed: pair.state == PairState::Installed,
            });
        }
        if !view.fragment {
            return None;
        }
        for pair in self.pairs.iter().chain(self.retained.values()) {
            if pair.state == PairState::Absent {
                continue;
            }
            for index in 0..2 {
                let direction = pair.spec.directions[index];
                if pair.legs[index].inverse
                    && direction.origin_id == origin as u32
                    && same_addresses(&direction.translated, &view.tuple)
                {
                    return Some(PacketLeg {
                        spec: direction,
                        guard: pair.guards.map(|guards| guards[index]),
                        handle: None,
                        installed: pair.state == PairState::Installed,
                    });
                }
            }
        }
        None
    }

    fn quarantine_front(
        &mut self,
        code: i32,
        diagnostic: MwanNativePacketDiagnostic,
    ) -> Box<MwanNativeError> {
        let origin = self
            .received
            .front()
            .map(|received| received.origin as u32)
            .unwrap_or(MWAN_NATIVE_NO_ID);
        if self.quarantined.is_none() {
            self.quarantined = self.received.pop_front();
            if self.quarantined.is_some() {
                self.stats.quarantined_packets = self.stats.quarantined_packets.wrapping_add(1);
            }
        }
        self.faulted = true;
        self.update_pending_stats();
        let mut error = fault(
            code,
            MWAN_NATIVE_OP_PACKET,
            MWAN_NATIVE_OBJECT_ORIGIN,
            origin,
            0,
            "Packet lacks a verified inverse; retained for recovery and admissions stopped",
        );
        error.packet = diagnostic;
        error
    }

    fn process_front(
        &mut self,
        links: &mut [Option<bool>; MWAN_NATIVE_MAX_DATA as usize],
    ) -> NativeResult<()> {
        let Some(received) = self.received.front_mut() else {
            return Ok(());
        };
        if received
            .packet
            .ethernet()
            .is_some_and(|header| header[0] & 1 != 0)
        {
            self.stats.discarded_group_frames = self.stats.discarded_group_frames.wrapping_add(1);
            drop(self.received.pop_front());
            return Ok(());
        }
        let decoded = match received.packet.linearize() {
            Ok(()) => match received.packet.frame() {
                Ok(frame) => packet::decode(frame).map_err(|error| (error.code, error.diagnostic)),
                Err(code) => Err((
                    code,
                    MwanNativePacketDiagnostic {
                        decode_status: -code,
                        ..MwanNativePacketDiagnostic::default()
                    },
                )),
            },
            Err(code) => Err((
                code,
                MwanNativePacketDiagnostic {
                    decode_status: -code,
                    ..MwanNativePacketDiagnostic::default()
                },
            )),
        };
        let view = match decoded {
            Ok(view) => view,
            Err((code, diagnostic)) => {
                self.stats.malformed = self.stats.malformed.wrapping_add(1);
                return Err(self.quarantine_front(code, diagnostic));
            }
        };
        if view.malformed {
            self.stats.malformed = self.stats.malformed.wrapping_add(1);
        }
        let origin = self
            .received
            .front()
            .map(|received| received.origin)
            .ok_or_else(|| {
                fault(
                    libc::EIO,
                    MWAN_NATIVE_OP_PACKET,
                    MWAN_NATIVE_OBJECT_NONE,
                    0,
                    0,
                    "Received packet ownership is unavailable",
                )
            })?;
        let mut diagnostic = packet::diagnostic(&view);
        let leg = self.packet_leg(origin, &view);
        if let Some(leg) = leg {
            let spec = leg.spec;
            if view.source_mac != spec.original_source_mac {
                diagnostic.failure_flags |= MWAN_NATIVE_PACKET_SOURCE_MAC_MISMATCH;
            }
            if view.destination_mac != spec.original_destination_mac {
                diagnostic.failure_flags |= MWAN_NATIVE_PACKET_DESTINATION_MAC_MISMATCH;
            }
        } else {
            diagnostic.failure_flags |= MWAN_NATIVE_PACKET_KEY_MISSING;
        }
        if diagnostic.failure_flags != 0 {
            self.stats.lookup_misses = self.stats.lookup_misses.wrapping_add(1);
            return Err(self.quarantine_front(libc::ENOENT, diagnostic));
        }
        let leg = leg.ok_or_else(|| {
            fault(
                libc::ENOENT,
                MWAN_NATIVE_OP_PACKET,
                MWAN_NATIVE_OBJECT_ORIGIN,
                origin as u32,
                0,
                "Packet inverse is unavailable",
            )
        })?;
        let spec = leg.spec;
        let mut kernel = self.quiescing || self.faulted || !leg.installed;
        if let Some(guard) = leg.guard {
            kernel |=
                guard.traffic_class != view.traffic_class || guard.flow_label != view.flow_label;
        }
        if !kernel {
            let eligibility = match self
                .received
                .front_mut()
                .map(|received| received.packet.frame())
            {
                Some(Ok(frame)) => packet::eligible(frame, &view, &spec),
                Some(Err(code)) => return Err(self.quarantine_front(code, diagnostic)),
                None => {
                    return Err(fault(
                        libc::EIO,
                        MWAN_NATIVE_OP_PACKET,
                        MWAN_NATIVE_OBJECT_NONE,
                        0,
                        0,
                        "Received packet ownership is unavailable",
                    ));
                }
            };
            kernel = !eligibility;
        }
        let mut output = self.data_port(spec.egress_data_id as usize)?;
        if !kernel {
            let status = &mut links[spec.egress_data_id as usize];
            let up = match *status {
                Some(up) => up,
                None => {
                    let mut link = dpdk::rte_eth_link::default();
                    // SAFETY: This output belongs to the discovered owned
                    // graph. The initialized record accepts the SDK link state.
                    let result = unsafe { dpdk::rte_eth_link_get_nowait(output, &mut link) };
                    let up = if result == 0 {
                        // SAFETY: Successful SDK output initialized this live
                        // record; the helper reads only its link-status bit.
                        unsafe { dpdk::mwn_eth_link_status(&link) }
                    } else {
                        false
                    };
                    *status = Some(up);
                    up
                }
            };
            kernel = !up;
        }
        if self.pending.len() >= BURST.min(self.config.mbuf_count as usize) {
            return Err(self.quarantine_front(libc::ENOBUFS, diagnostic));
        }
        let return_port = self.data_port(self.config.origins[origin].data as usize)?;
        if kernel {
            output = return_port;
            self.stats.unsupported = self.stats.unsupported.wrapping_add(1);
        }
        let prepared = if kernel {
            None
        } else {
            let Some(handle) = leg.handle else {
                return Err(self.quarantine_front(libc::ESTALE, diagnostic));
            };
            Some(PreparedForward {
                handle,
                view,
                direction: spec,
                return_port,
            })
        };
        let edited = match self
            .received
            .front_mut()
            .map(|received| received.packet.frame())
        {
            Some(Ok(frame)) => {
                if kernel {
                    packet::restore(frame, &view, &spec)
                } else {
                    packet::forward(frame, &view, &spec)
                }
            }
            Some(Err(code)) => return Err(self.quarantine_front(code, diagnostic)),
            None => {
                return Err(fault(
                    libc::EIO,
                    MWAN_NATIVE_OP_PACKET,
                    MWAN_NATIVE_OBJECT_NONE,
                    0,
                    0,
                    "Received packet ownership is unavailable",
                ));
            }
        };
        if let Err(error) = edited {
            return Err(self.quarantine_front(error.code, error.diagnostic));
        }
        if let Some(received) = self.received.front_mut() {
            received.packet.clear_offloads();
        }
        let received = self.received.pop_front().ok_or_else(|| {
            fault(
                libc::EIO,
                MWAN_NATIVE_OP_PACKET,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Received packet ownership is unavailable",
            )
        })?;
        self.pending.push_back(Pending {
            packet: received.packet,
            port: output,
            kernel,
            prepared,
        });
        self.update_pending_stats();
        Ok(())
    }

    fn poll(&mut self, mut budget: u32) -> NativeResult<()> {
        if self.quarantined.is_some() {
            return Err(fault(
                libc::EIO,
                MWAN_NATIVE_OP_PACKET,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Unrecoverable packet retained; controller recovery is required",
            ));
        }
        self.flush_pending()?;
        if !self.pending.is_empty() {
            return Ok(());
        }
        let mut links = [None; MWAN_NATIVE_MAX_DATA as usize];
        for _ in 0..self.config.data_count {
            if budget == 0 {
                break;
            }
            let data_id = self.next_rx % self.config.data_count as usize;
            self.next_rx = self.next_rx.wrapping_add(1);
            let data = self.data[data_id];
            let (Some(origin), Some(port)) = (data.origin, data.id) else {
                continue;
            };
            if !self
                .ports
                .iter()
                .any(|entry| entry.id == port && entry.state == PortState::Started)
            {
                continue;
            }
            let requested = (budget as usize)
                .min(BURST)
                .min(self.config.mbuf_count as usize);
            let mut pointers = [ptr::null_mut(); BURST];
            // SAFETY: The started DATA RX queue belongs to this owner. The
            // array has requested writable slots. RX transfers each returned
            // mbuf, which Rust records before packet processing.
            let returned = usize::from(unsafe {
                dpdk::mwn_rx_burst(port, 0, pointers.as_mut_ptr(), requested as u16)
            });
            let count = returned.min(requested);
            budget -= count as u32;
            self.stats.received = self.stats.received.wrapping_add(count as u64);
            let mut invalid = returned > requested;
            for pointer in pointers.into_iter().take(count) {
                if let Some(packet) = PacketBuffer::received(pointer) {
                    self.received.push_back(Received { origin, packet });
                } else {
                    invalid = true;
                }
            }
            self.update_pending_stats();
            if invalid {
                self.faulted = true;
                let error = fault(
                    libc::EIO,
                    MWAN_NATIVE_OP_PACKET,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    origin as u32,
                    0,
                    "SDK returned an invalid receive batch; packets retained",
                );
                self.terminal_failure = Some(*error);
                return Err(error);
            }
            while !self.received.is_empty() {
                self.process_front(&mut links)?;
            }
            self.flush_pending()?;
            if !self.pending.is_empty() {
                break;
            }
        }
        Ok(())
    }

    fn drain(&mut self) -> NativeResult<()> {
        let deadline =
            monotonic_ns().saturating_add(u64::from(self.config.drain_timeout_ms) * NS_PER_MS);
        let mut quiet_since = None;
        loop {
            let before = self.stats.received;
            self.poll(BURST as u32 * self.config.data_count)?;
            let now = monotonic_ns();
            if before == self.stats.received && self.pending.is_empty() && self.received.is_empty()
            {
                let start = quiet_since.get_or_insert(now);
                if now.saturating_sub(*start) >= DRAIN_QUIET_NS {
                    return Ok(());
                }
            } else {
                quiet_since = None;
            }
            std::thread::sleep(Duration::from_micros(DRAIN_SLEEP_US));
            if monotonic_ns() >= deadline {
                break;
            }
        }
        Err(fault(
            libc::ETIMEDOUT,
            MWAN_NATIVE_OP_DRAIN,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Packet drain did not become quiet; restoration records retained",
        ))
    }

    fn close(&mut self, mode: u32) -> NativeResult<()> {
        if !self.eal_ready {
            if let Some(error) = self.terminal_failure {
                return Err(Box::new(error));
            }
            if self.pool.is_some()
                || !self.ports.is_empty()
                || !self.inverses.is_empty()
                || !self.retained.is_empty()
                || !self.pending.is_empty()
                || !self.received.is_empty()
                || self.quarantined.is_some()
                || self.pairs.iter().any(|pair| {
                    pair.legs
                        .iter()
                        .any(|leg| leg.flow.is_some() || leg.delete_failed)
                })
            {
                return Err(fault(
                    libc::EIO,
                    MWAN_NATIVE_OP_DRAIN,
                    MWAN_NATIVE_OBJECT_NONE,
                    0,
                    0,
                    "SDK resources remain without successful EAL initialization",
                ));
            }
            // Failed EAL initialization does not grant ownership of EAL.
            // Calling its one-time cleanup here would invalidate later owners.
            return Ok(());
        }
        self.quiescing = true;
        let mut failure = self.terminal_failure.map(Box::new);
        for slot in 0..self.pairs.len() {
            if let Err(error) = self.delete_pair(slot)
                && failure.is_none()
            {
                failure = Some(error);
            }
        }
        if let Some(error) = failure {
            return Err(error);
        }
        if self.pool.is_some() {
            self.drain()?;
        }
        for index in 0..self.config.controller_count as usize {
            if self.controllers[index].miss
                && let Err(error) = self.miss(index, false)
            {
                self.faulted = true;
                return Err(error);
            }
        }
        let ports = self.ordered_ports(true)?;
        for id in &ports {
            let Some(port) = self.ports.iter_mut().find(|port| port.id == *id) else {
                continue;
            };
            if port.state != PortState::Started {
                continue;
            }
            // SAFETY: This owner stops its own live port before releasing any
            // packet pool or inverse records required by queued buffers.
            let result = unsafe { dpdk::rte_eth_dev_stop(*id) };
            if result != 0 {
                self.faulted = true;
                return Err(fault(
                    result,
                    MWAN_NATIVE_OP_PORT,
                    MWAN_NATIVE_OBJECT_PORT,
                    u32::from(*id),
                    0,
                    "Port stop failed",
                ));
            }
            port.state = PortState::Stopped;
        }
        self.active = false;
        for slot in 0..self.pairs.len() {
            self.forget_pair(slot);
        }
        self.retained.clear();
        self.inverses.clear();
        if mode == MWAN_NATIVE_DISABLE {
            return Ok(());
        }
        for id in ports {
            // SAFETY: The SDK owns its ethdev validity table for this EAL thread.
            if unsafe { dpdk::rte_eth_dev_is_valid_port(id) } == 0 {
                continue;
            }
            // SAFETY: Owned rules have retired and all started queues stopped.
            let result = unsafe { dpdk::rte_eth_dev_close(id) };
            if result != 0 {
                let error = fault(
                    result,
                    MWAN_NATIVE_OP_PORT,
                    MWAN_NATIVE_OBJECT_PORT,
                    u32::from(id),
                    0,
                    "Port close failed; process recovery is required",
                );
                self.terminal_failure = Some(*error);
                self.faulted = true;
                return Err(error);
            }
            if let Some(port) = self.ports.iter_mut().find(|port| port.id == id) {
                port.state = PortState::Closed;
            }
        }
        if let Some(pool) = self.pool.take() {
            // SAFETY: Queues are closed and Rust retains neither pending nor
            // quarantined mbufs. The pool has a single owning SDK allocation.
            unsafe { dpdk::rte_mempool_free(pool.as_ptr()) };
        }
        // SAFETY: Every discovered owned port closed before final EAL cleanup.
        let result = unsafe { dpdk::rte_eal_cleanup() };
        self.eal_ready = false;
        if result != 0 {
            let error = fault(
                result,
                MWAN_NATIVE_OP_EAL,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Final EAL cleanup failed; process recovery is required",
            );
            self.terminal_failure = Some(*error);
            self.faulted = true;
            return Err(error);
        }
        Ok(())
    }
}

unsafe fn owner_state<'a>(pointer: *mut MwanNative) -> NativeResult<&'a mut OwnerState> {
    let pointer = NonNull::new(pointer.cast::<Owner>()).ok_or_else(|| {
        fault(
            libc::ENODEV,
            MWAN_NATIVE_OP_EAL,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "EAL owner is unavailable",
        )
    })?;
    // SAFETY: The ABI caller supplies a live owner from this library. The
    // outer shared record never mutates its thread identity; UnsafeCell
    // separates the state that only the verified owner thread may borrow.
    let owner = unsafe { pointer.as_ref() };
    // SAFETY: Both pthread identities belong to live OS threads.
    if unsafe { libc::pthread_equal(owner.thread, libc::pthread_self()) } == 0 {
        return Err(fault(
            libc::EPERM,
            MWAN_NATIVE_OP_EAL,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "Operation requires the EAL owner thread",
        ));
    }
    // SAFETY: Every ABI operation requires this same OS thread and exclusive
    // nonreentrant use. The caller's live owner bounds the returned borrow.
    let state = unsafe { &mut *owner.state.get() };
    Ok(state)
}

unsafe fn state_for<'a>(pointer: *mut MwanNative) -> NativeResult<&'a mut OwnerState> {
    // SAFETY: The caller supplies a live exclusive owner. The shared identity
    // check rejects other OS threads before creating a mutable state borrow.
    let state = unsafe { owner_state(pointer) }?;
    if let Some(error) = state.terminal_failure {
        return Err(Box::new(error));
    }
    if !state.eal_ready {
        return Err(fault(
            libc::ENODEV,
            MWAN_NATIVE_OP_EAL,
            MWAN_NATIVE_OBJECT_NONE,
            0,
            0,
            "EAL owner is unavailable",
        ));
    }
    Ok(state)
}

fn write_failure(destination: *mut MwanNativeError, error: &MwanNativeError) -> i32 {
    if !destination.is_null() {
        // SAFETY: Each ABI entry point requires a writable error record that
        // remains separate from its live owner and input records.
        unsafe { destination.write(*error) };
    }
    -error.code
}

fn panic_failure(pointer: *mut MwanNative, operation: u32) -> MwanNativeError {
    let mut error = MwanNativeError {
        code: libc::EFAULT,
        operation,
        ..MwanNativeError::default()
    };
    write_text(
        &mut error.message,
        b"Native operation panicked; owner and resources retained",
    );
    if let Some(pointer) = NonNull::new(pointer.cast::<Owner>()) {
        // SAFETY: A published owner remains allocated on any failed operation.
        // The outer immutable identity can be read before borrowing its state.
        let owner = unsafe { pointer.as_ref() };
        // SAFETY: Comparing the immutable identity does not mutate owner state.
        if unsafe { libc::pthread_equal(owner.thread, libc::pthread_self()) } != 0 {
            // SAFETY: Unwinding ended the operation's mutable state borrow.
            // Only this verified thread can now record the terminal fault.
            let state = unsafe { &mut *owner.state.get() };
            state.faulted = true;
            state.quiescing = true;
            if state.quarantined.is_none() {
                state.quarantined = state.received.pop_front();
                if state.quarantined.is_some() {
                    state.stats.quarantined_packets =
                        state.stats.quarantined_packets.wrapping_add(1);
                }
            }
            state.update_pending_stats();
            state.terminal_failure = Some(error);
        }
    }
    error
}

fn boundary(
    owner: *mut MwanNative,
    error: *mut MwanNativeError,
    operation: u32,
    action: impl FnOnce() -> NativeResult<()>,
) -> i32 {
    if error.is_null() {
        return -libc::EINVAL;
    }
    // SAFETY: All operational ABI callers provide a writable error record.
    unsafe { error.write(MwanNativeError::default()) };
    match catch_unwind(AssertUnwindSafe(action)) {
        Ok(Ok(())) => 0,
        Ok(Err(failure)) => write_failure(error, &failure),
        Err(_) => write_failure(error, &panic_failure(owner, operation)),
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn mwan_native_abi_version() -> u32 {
    MWAN_NATIVE_ABI_VERSION
}

/// # Safety
/// A nonnull output must identify a writable build-information record.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_build_info(info: *mut MwanNativeBuildInfo) -> i32 {
    if info.is_null() {
        return -libc::EINVAL;
    }
    match catch_unwind(AssertUnwindSafe(|| {
        let mut output = MwanNativeBuildInfo {
            abi_version: MWAN_NATIVE_ABI_VERSION,
            config_size: size_of::<MwanNativeConfig>() as u32,
            pair_spec_size: size_of::<MwanNativePairSpec>() as u32,
            direction_size: size_of::<MwanNativeDirection>() as u32,
            controller_size: size_of::<MwanNativeController>() as u32,
            receiver_size: size_of::<MwanNativeReceiver>() as u32,
            data_size: size_of::<MwanNativeData>() as u32,
            origin_size: size_of::<MwanNativeOrigin>() as u32,
            error_size: size_of::<MwanNativeError>() as u32,
            worker_stats_size: size_of::<MwanNativeWorkerStats>() as u32,
            pair_stats_size: size_of::<MwanNativePairStats>() as u32,
            ..MwanNativeBuildInfo::default()
        };
        // SAFETY: The SDK returns a static NUL-terminated version string.
        let version = unsafe { CStr::from_ptr(dpdk::mwn_dpdk_version()) };
        write_text(&mut output.dpdk_version, version.to_bytes());
        write_text(
            &mut output.source_sha256,
            env!("MWAN_DPDK_SOURCE_SHA256").as_bytes(),
        );
        write_text(
            &mut output.patch_sha256,
            env!("MWAN_DPDK_PATCH_SHA256").as_bytes(),
        );
        // SAFETY: The caller supplied a writable, correctly sized output.
        unsafe { info.write(output) };
    })) {
        Ok(()) => 0,
        Err(_) => -libc::EFAULT,
    }
}

/// # Safety
/// Inputs and outputs must identify separate valid ABI records. An existing
/// owner must come from this library and remain live for this exclusive call.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_open(
    config: *const MwanNativeConfig,
    result: *mut *mut MwanNative,
    error: *mut MwanNativeError,
) -> i32 {
    if result.is_null() || error.is_null() {
        return -libc::EINVAL;
    }
    // SAFETY: The caller supplies a writable error record and a live owner slot.
    unsafe { error.write(MwanNativeError::default()) };
    let outcome = catch_unwind(AssertUnwindSafe(|| {
        if config.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_NONE,
                0,
                0,
                "Invalid native configuration or ABI",
            ));
        }
        // SAFETY: The input is a complete caller-owned ABI configuration.
        let config = unsafe { config.read() };
        validate_config(&config)?;
        // SAFETY: The caller's owner slot remains live throughout this call.
        let current = unsafe { result.read() };
        if !current.is_null() {
            // SAFETY: The slot can contain only a live owner from this library.
            let state = unsafe { state_for(current) }?;
            if state.faulted || state.config != config {
                return Err(fault(
                    libc::EBUSY,
                    MWAN_NATIVE_OP_CONFIG,
                    MWAN_NATIVE_OBJECT_NONE,
                    0,
                    0,
                    "Reopen requires the identical healthy EAL configuration",
                ));
            }
            if !state.active {
                state.activate()?;
            }
            return Ok(());
        }
        let state = OwnerState::new(config)?;
        // SAFETY: pthread_self returns this calling OS thread's identity.
        let thread = unsafe { libc::pthread_self() };
        let pointer = Box::into_raw(Box::new(Owner {
            thread,
            state: UnsafeCell::new(state),
        }));
        // SAFETY: Publishing the allocation before SDK initialization preserves
        // ownership if EAL, partial initialization or Rust unwinding fails.
        unsafe { result.write(pointer.cast()) };
        // SAFETY: This fresh owner is exclusively borrowed on its recorded thread.
        let state = unsafe { &mut *(*pointer).state.get() };
        if let Err(failure) = state.initialize() {
            state.faulted = true;
            return Err(failure);
        }
        Ok(())
    }));
    match outcome {
        Ok(Ok(())) => 0,
        Ok(Err(failure)) => write_failure(error, &failure),
        Err(_) => {
            // SAFETY: The owner slot remains live, including after partial init.
            let owner = unsafe { result.read() };
            write_failure(error, &panic_failure(owner, MWAN_NATIVE_OP_EAL))
        }
    }
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Input,
/// result and error pointers must identify separate valid ABI records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_pair_install(
    owner: *mut MwanNative,
    spec: *const MwanNativePairSpec,
    result: *mut MwanNativePairResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_CREATE, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if spec.is_null() || result.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Worker cannot admit a pair",
            ));
        }
        // SAFETY: The input record remains readable; the owner copies it.
        let input = unsafe { spec.read() };
        let mut output = MwanNativePairResult::default();
        let outcome = state.install_pair(input, None, &mut output);
        // SAFETY: The separate result slot remains writable even on failure,
        // including when cleanup retains inverse records or consumed handles.
        unsafe { result.write(output) };
        outcome
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Input,
/// guard array, result and error pointers must identify separate valid records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_pair_install_observed(
    owner: *mut MwanNative,
    spec: *const MwanNativePairSpec,
    guards: *const MwanNativePacketGuard,
    result: *mut MwanNativePairResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_CREATE, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if spec.is_null() || guards.is_null() || result.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Observed pair, selector guards and result are required",
            ));
        }
        // SAFETY: The caller supplies a complete pair and two complete guard
        // records. Copies end every input borrow before hardware mutation.
        let (input, guards) = unsafe {
            (
                spec.read(),
                guards.cast::<[MwanNativePacketGuard; 2]>().read(),
            )
        };
        let mut output = MwanNativePairResult::default();
        let outcome = state.install_pair(input, Some(guards), &mut output);
        // SAFETY: The separate result remains writable after partial failure.
        unsafe { result.write(output) };
        outcome
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Input,
/// guard array, result and error pointers must identify separate valid records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_pair_resume_observed(
    owner: *mut MwanNative,
    handle: MwanNativeHandle,
    spec: *const MwanNativePairSpec,
    guards: *const MwanNativePacketGuard,
    result: *mut MwanNativePairResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_CREATE, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if spec.is_null() || guards.is_null() || result.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_PAIR,
                handle.slot,
                0,
                "Resumed pair, selector guards and result are required",
            ));
        }
        // SAFETY: The caller supplies a complete pair and two complete guard
        // records. Copies end every input borrow before hardware mutation.
        let (input, guards) = unsafe {
            (
                spec.read(),
                guards.cast::<[MwanNativePacketGuard; 2]>().read(),
            )
        };
        let mut output = MwanNativePairResult::default();
        let outcome = state.resume_pair(handle, input, guards, &mut output);
        // SAFETY: The separate result remains writable after partial failure.
        unsafe { result.write(output) };
        outcome
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Output
/// and error pointers must identify separate writable ABI records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_pair_query(
    owner: *mut MwanNative,
    handle: MwanNativeHandle,
    stats: *mut MwanNativePairStats,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_QUERY, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if stats.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_QUERY,
                MWAN_NATIVE_OBJECT_PAIR,
                handle.slot,
                0,
                "Counter result is required",
            ));
        }
        let mut output = MwanNativePairStats::default();
        let outcome = state.query_pair(handle, &mut output);
        // SAFETY: The separate ABI output remains writable for this call.
        unsafe { stats.write(output) };
        outcome
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Output
/// and error pointers must identify separate writable ABI records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_pair_remove(
    owner: *mut MwanNative,
    handle: MwanNativeHandle,
    result: *mut MwanNativePairResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_DESTROY, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if result.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_DESTROY,
                MWAN_NATIVE_OBJECT_PAIR,
                handle.slot,
                0,
                "Pair result is required",
            ));
        }
        state.handle_pair(handle)?;
        let slot = handle.slot as usize;
        let outcome = if state.pairs[slot].generation == handle.generation {
            state.delete_pair(slot)
        } else {
            Ok(())
        };
        let output = state.result_for(state.handle_pair(handle)?, slot);
        // SAFETY: The separate result remains writable after failed deletion.
        unsafe { result.write(output) };
        outcome
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Output
/// and error pointers must identify separate writable ABI records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_retirement_query(
    owner: *mut MwanNative,
    stats: *mut MwanNativeRetirementStats,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_QUERY, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if stats.is_null() {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_QUERY,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Retirement statistics are required",
            ));
        }
        // SAFETY: The caller supplies the separate writable output record.
        unsafe { stats.write(state.retirement_stats()) };
        Ok(())
    })
}

/// # Safety
/// The owner must remain live for this exclusive owner-thread call. Output
/// and error pointers must identify separate writable ABI records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_poll(
    owner: *mut MwanNative,
    budget: u32,
    stats: *mut MwanNativeWorkerStats,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_PACKET, || {
        // SAFETY: The ABI caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        if stats.is_null() || budget == 0 {
            return Err(fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_PACKET,
                MWAN_NATIVE_OBJECT_ORIGIN,
                0,
                0,
                "Packet budget and statistics are required",
            ));
        }
        let outcome = if state.active {
            state.poll(budget)
        } else {
            Ok(())
        };
        // SAFETY: The separate output remains writable after a polling failure.
        unsafe { stats.write(state.stats) };
        outcome
    })
}

/// # Safety
/// The slot must identify a live owner from this library or contain null.
/// The owner thread must have exclusive access to the slot and error record.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_close(
    pointer: *mut *mut MwanNative,
    mode: u32,
    error: *mut MwanNativeError,
) -> i32 {
    if pointer.is_null() || error.is_null() || mode > MWAN_NATIVE_FINAL_SHUTDOWN {
        return -libc::EINVAL;
    }
    // SAFETY: The caller supplies a readable and writable owner slot.
    let owner = unsafe { pointer.read() };
    if owner.is_null() {
        return 0;
    }
    boundary(owner, error, MWAN_NATIVE_OP_DRAIN, || {
        // SAFETY: The slot contains this library's live exclusive owner.
        let state = unsafe { owner_state(owner) }?;
        state.close(mode)?;
        if mode == MWAN_NATIVE_FINAL_SHUTDOWN {
            // SAFETY: Successful final cleanup retired every SDK resource and
            // emptied all owned packet queues. The state borrow ended above.
            unsafe { drop(Box::from_raw(owner.cast::<Owner>())) };
            // SAFETY: The caller's owner slot remains writable after cleanup.
            unsafe { pointer.write(ptr::null_mut()) };
        }
        Ok(())
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    const CHILD_MODE: &str = "MWAN_NATIVE_PRE_EAL_CLEANUP_CHILD";
    const TEST_NAME: &str = "controller::tests::pre_eal_failure_can_close_and_initialize_again";

    #[test]
    fn pre_eal_failure_can_close_and_initialize_again() {
        if std::env::var_os(CHILD_MODE).is_none() {
            let executable = std::env::current_exe().expect("locate the native test executable");
            let output = Command::new(executable)
                .args(["--exact", TEST_NAME, "--nocapture", "--test-threads=1"])
                .env(CHILD_MODE, "1")
                .output()
                .expect("start the isolated EAL cleanup regression");
            assert!(
                output.status.success(),
                "isolated EAL cleanup regression failed: {}\n{}",
                String::from_utf8_lossy(&output.stdout),
                String::from_utf8_lossy(&output.stderr),
            );
            return;
        }

        let mut configuration = input_configuration();
        let device = format!(
            "/sys/bus/pci/devices/{}",
            pci_name(&configuration.data[0].pci)
        );
        let missing = std::fs::metadata(&device)
            .expect_err("the EAL cleanup regression requires an unavailable DATA PCI function");
        assert_eq!(missing.kind(), std::io::ErrorKind::NotFound);
        let mut owner = ptr::null_mut();
        let mut error = MwanNativeError::default();
        // SAFETY: All records are complete, separate ABI allocations. The
        // invalid lcore exercises SDK argument rejection before port probing.
        let result = unsafe { mwan_native_open(&configuration, &mut owner, &mut error) };
        assert_eq!(result, -libc::EINVAL, "{:?}", error);
        assert_eq!(error.operation, MWAN_NATIVE_OP_EAL);
        assert!(
            !owner.is_null(),
            "failed initialization lost its Rust owner"
        );

        let address = owner as usize;
        let (result, retained) = std::thread::spawn(move || {
            let mut foreign_owner = address as *mut MwanNative;
            let mut error = MwanNativeError::default();
            // SAFETY: The owner remains live and the original thread waits.
            // This call must reject its OS thread before borrowing state.
            let result = unsafe {
                mwan_native_close(&mut foreign_owner, MWAN_NATIVE_FINAL_SHUTDOWN, &mut error)
            };
            (result, foreign_owner as usize)
        })
        .join()
        .expect("join the rejected cleanup thread");
        assert_eq!(result, -libc::EPERM);
        assert_eq!(retained, address);

        // SAFETY: This is the original owner thread, with a live exclusive
        // owner slot and a separate writable error record.
        let result =
            unsafe { mwan_native_close(&mut owner, MWAN_NATIVE_FINAL_SHUTDOWN, &mut error) };
        assert_eq!(result, 0, "{:?}", error);
        assert!(
            owner.is_null(),
            "final close retained an uninitialized owner"
        );

        configuration.cpu = allowed_cpu();
        // SAFETY: The previous owner released its Rust records without
        // consuming EAL cleanup. The same ABI inputs remain complete and valid.
        let result = unsafe { mwan_native_open(&configuration, &mut owner, &mut error) };
        assert!(
            result < 0,
            "the deliberately unavailable DATA PCI function opened"
        );
        assert_eq!(
            error.operation, MWAN_NATIVE_OP_PORT,
            "EAL did not initialize after the earlier failure: {:?}",
            error
        );
        assert!(!owner.is_null());
        // SAFETY: EAL initialized on this thread. The retained owner records
        // every acquired SDK resource for ordered final cleanup.
        let result =
            unsafe { mwan_native_close(&mut owner, MWAN_NATIVE_FINAL_SHUTDOWN, &mut error) };
        assert_eq!(
            result, 0,
            "earlier failure consumed SDK cleanup: {:?}",
            error
        );
        assert!(owner.is_null());
    }

    fn input_configuration() -> MwanNativeConfig {
        let mut configuration = MwanNativeConfig {
            abi_version: MWAN_NATIVE_ABI_VERSION,
            record_size: size_of::<MwanNativeConfig>() as u32,
            cpu: u32::MAX,
            memory_mb: 64,
            memory_mode: MWAN_NATIVE_MEMORY_ANONYMOUS,
            data_count: 1,
            pair_capacity: 1,
            mbuf_count: 255,
            drain_timeout_ms: 1000,
            rx_descriptors: 64,
            tx_descriptors: 64,
            mtu: 1500,
            ..MwanNativeConfig::default()
        };
        configuration.data[0] = MwanNativeData {
            pci: MwanNativePci {
                domain: u16::MAX,
                bus: u8::MAX,
                device: 31,
                function: 7,
                ..MwanNativePci::default()
            },
            controller: MWAN_NATIVE_NO_ID,
            mac: [2, 0, 0, 0, 0, 1],
            ..MwanNativeData::default()
        };
        write_text(
            &mut configuration.file_prefix,
            format!("mwan_eal_failure_{}", std::process::id()).as_bytes(),
        );
        configuration
    }

    fn allowed_cpu() -> u32 {
        // SAFETY: cpu_set_t contains an integer bit mask, and an all-zero mask
        // is a valid initialized value before the kernel fills its active bytes.
        let mut affinity = unsafe { mem::zeroed::<libc::cpu_set_t>() };
        // SAFETY: The kernel receives a writable mask of the supplied size.
        let result =
            unsafe { libc::sched_getaffinity(0, size_of::<libc::cpu_set_t>(), &mut affinity) };
        assert_eq!(result, 0, "read the test thread's CPU affinity");
        for cpu in 0..libc::CPU_SETSIZE as usize {
            // SAFETY: The CPU index is within the initialized mask's capacity.
            if unsafe { libc::CPU_ISSET(cpu, &affinity) } {
                return cpu as u32;
            }
        }
        panic!("the EAL cleanup regression requires an allowed CPU");
    }
}
