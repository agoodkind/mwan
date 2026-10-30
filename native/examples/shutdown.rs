use mwan_native::abi::*;
use std::error::Error;
use std::ffi::{c_char, c_int};
use std::fs;
use std::io::{self, Write};
use std::mem::size_of;
use std::net::IpAddr;
use std::process::ExitCode;
use std::ptr;
use std::str::{FromStr, SplitWhitespace};
use std::thread;
use std::time::{Duration, Instant};

const SDK_LOG_DEBUG: u32 = 8;
const PACKET_BUDGET: u32 = 64;
const QUEUE_PAUSE: Duration = Duration::from_millis(100);
const DIRECTION_MASKS: [u32; 2] = [1, 2];
type CheckResult<T> = Result<T, Box<dyn Error>>;

// These declarations use the existing public C ABI and its generated Rust types.
unsafe extern "C" {
    fn mwan_native_open(
        config: *const MwanNativeConfig,
        owner: *mut *mut MwanNative,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_close(
        owner: *mut *mut MwanNative,
        mode: u32,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_pair_install(
        owner: *mut MwanNative,
        spec: *const MwanNativePairSpec,
        result: *mut MwanNativePairResult,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_pair_query(
        owner: *mut MwanNative,
        handle: MwanNativeHandle,
        stats: *mut MwanNativePairStats,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_pair_remove(
        owner: *mut MwanNative,
        handle: MwanNativeHandle,
        result: *mut MwanNativePairResult,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_pair_resume_observed(
        owner: *mut MwanNative,
        handle: MwanNativeHandle,
        spec: *const MwanNativePairSpec,
        guards: *const MwanNativePacketGuard,
        result: *mut MwanNativePairResult,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_poll(
        owner: *mut MwanNative,
        budget: u32,
        stats: *mut MwanNativeWorkerStats,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn mwan_native_retirement_query(
        owner: *mut MwanNative,
        stats: *mut MwanNativeRetirementStats,
        error: *mut MwanNativeError,
    ) -> c_int;
    fn rte_log_set_global_level(level: u32);
    fn rte_log_set_level_pattern(pattern: *const c_char, level: u32) -> c_int;
    fn rte_log_get_stream() -> *mut libc::FILE;
    fn rte_log_dump(stream: *mut libc::FILE);
}

fn require(condition: bool, message: &str) -> CheckResult<()> {
    if condition {
        Ok(())
    } else {
        Err(io::Error::other(message).into())
    }
}

fn native_result(result: c_int, error: &MwanNativeError) -> CheckResult<()> {
    let bytes = error.message.map(|value| value.to_ne_bytes()[0]);
    let message = String::from_utf8_lossy(&bytes);
    require(
        result == 0,
        &format!(
            "Native failure rc={result} code={} operation={} object={}/{} direction={} message={}",
            error.code,
            error.operation,
            error.object_kind,
            error.object_id,
            error.direction,
            message.trim_end_matches('\0')
        ),
    )
}

struct Tokens<'a>(SplitWhitespace<'a>);
impl<'a> Tokens<'a> {
    fn word(&mut self) -> CheckResult<&'a str> {
        self.0
            .next()
            .ok_or_else(|| io::Error::other("Fixture ended before a required field").into())
    }
    fn number<T: FromStr>(&mut self) -> CheckResult<T>
    where
        T::Err: Error + 'static,
    {
        Ok(self.word()?.parse()?)
    }
    fn marker(&mut self, marker: &str) -> CheckResult<()> {
        require(
            self.word()? == marker,
            "Fixture has an incorrect record marker",
        )?;
        require(
            self.number::<u32>()? == MWAN_NATIVE_ABI_VERSION,
            "Fixture ABI does not match the native library",
        )
    }
    fn end(&mut self) -> CheckResult<()> {
        require(
            self.0.next().is_none(),
            "Fixture contains unexpected trailing fields",
        )
    }
}

fn mac(value: &str) -> CheckResult<[u8; 6]> {
    let fields: Vec<&str> = value.split(':').collect();
    require(fields.len() == 6, "MAC address requires six octets")?;
    let mut address = [0; 6];
    for (octet, field) in address.iter_mut().zip(fields) {
        *octet = u8::from_str_radix(field, 16)?;
    }
    Ok(address)
}
fn pci(value: &str) -> CheckResult<MwanNativePci> {
    let fields: Vec<&str> = value.split([':', '.']).collect();
    require(
        fields.len() == 4,
        "PCI address requires domain, bus, device and function",
    )?;
    Ok(MwanNativePci {
        domain: u16::from_str_radix(fields[0], 16)?,
        bus: u8::from_str_radix(fields[1], 16)?,
        device: u8::from_str_radix(fields[2], 16)?,
        function: u8::from_str_radix(fields[3], 16)?,
        ..MwanNativePci::default()
    })
}
fn address(value: &str, family: u8) -> CheckResult<[u8; 16]> {
    let mut bytes = [0; 16];
    match value.parse::<IpAddr>()? {
        IpAddr::V4(value) if u32::from(family) == MWAN_NATIVE_FAMILY_IPV4 => {
            bytes[..4].copy_from_slice(&value.octets())
        }
        IpAddr::V6(value) if u32::from(family) == MWAN_NATIVE_FAMILY_IPV6 => bytes = value.octets(),
        _ => {
            return Err(io::Error::other("Fixture address family does not match its tuple").into());
        }
    }
    Ok(bytes)
}
fn text<const LENGTH: usize>(target: &mut [c_char; LENGTH], value: &str) -> CheckResult<()> {
    require(
        value.len() < LENGTH && !value.contains('\0'),
        "Fixture text exceeds its ABI field",
    )?;
    for (target, byte) in target.iter_mut().zip(value.bytes()) {
        *target = byte as c_char;
    }
    Ok(())
}
fn configuration(path: &str) -> CheckResult<MwanNativeConfig> {
    let source = fs::read_to_string(path)?;
    let mut input = Tokens(source.split_whitespace());
    input.marker("MWAN_NATIVE_CONFIG")?;
    let mut config = MwanNativeConfig {
        abi_version: MWAN_NATIVE_ABI_VERSION,
        record_size: size_of::<MwanNativeConfig>() as u32,
        cpu: input.number()?,
        memory_mb: input.number()?,
        memory_mode: input.number()?,
        pair_capacity: input.number()?,
        mbuf_count: input.number()?,
        drain_timeout_ms: input.number()?,
        rx_descriptors: input.number()?,
        tx_descriptors: input.number()?,
        mtu: input.number()?,
        ..MwanNativeConfig::default()
    };
    text(&mut config.file_prefix, input.word()?)?;
    let directory = input.word()?;
    if directory != "-" {
        text(&mut config.hugepage_directory, directory)?;
    }
    config.controller_count = input.number()?;
    config.receiver_count = input.number()?;
    config.data_count = input.number()?;
    config.origin_count = input.number()?;
    require(
        config.controller_count <= MWAN_NATIVE_MAX_CONTROLLERS
            && config.receiver_count <= MWAN_NATIVE_MAX_RECEIVERS
            && config.data_count <= MWAN_NATIVE_MAX_DATA
            && config.origin_count <= MWAN_NATIVE_MAX_ORIGINS,
        "Fixture graph exceeds ABI capacities",
    )?;
    for controller in config
        .controllers
        .iter_mut()
        .take(config.controller_count as usize)
    {
        controller.pci = pci(input.word()?)?;
        controller.physical_receiver = input.number()?;
    }
    for receiver in config
        .receivers
        .iter_mut()
        .take(config.receiver_count as usize)
    {
        receiver.controller = input.number()?;
        receiver.vf = input.number()?;
        receiver.mac = mac(input.word()?)?;
    }
    for data in config.data.iter_mut().take(config.data_count as usize) {
        data.pci = pci(input.word()?)?;
        data.controller = input.number()?;
        data.vf = input.number()?;
        data.mac = mac(input.word()?)?;
    }
    for origin in config.origins.iter_mut().take(config.origin_count as usize) {
        origin.controller = input.number()?;
        origin.kind = input.number()?;
        origin.receiver = input.number()?;
        origin.data = input.number()?;
        origin.return_mode = input.number()?;
        origin.source_vf = input.number()?;
    }
    input.end()?;
    Ok(config)
}
fn pair(path: &str) -> CheckResult<MwanNativePairSpec> {
    let source = fs::read_to_string(path)?;
    let mut input = Tokens(source.split_whitespace());
    input.marker("MWAN_NATIVE_PAIR")?;
    let mut pair = MwanNativePairSpec {
        requested_direction_mask: input.number()?,
        cookie: input.number()?,
        policy_generation: input.number()?,
        ..MwanNativePairSpec::default()
    };
    require(
        matches!(pair.requested_direction_mask, 1 | 2),
        "Shutdown reuse requires one direction",
    )?;
    for (index, direction) in pair.directions.iter_mut().enumerate() {
        if pair.requested_direction_mask & DIRECTION_MASKS[index] == 0 {
            continue;
        }
        direction.origin_id = input.number()?;
        direction.egress_data_id = input.number()?;
        let family = input.number()?;
        let protocol = input.number()?;
        direction.original.family = family;
        direction.original.protocol = protocol;
        direction.original.source = address(input.word()?, family)?;
        direction.original.destination = address(input.word()?, family)?;
        direction.original.source_port = input.number()?;
        direction.original.destination_port = input.number()?;
        direction.translated.family = family;
        direction.translated.protocol = protocol;
        direction.translated.source = address(input.word()?, family)?;
        direction.translated.destination = address(input.word()?, family)?;
        direction.translated.source_port = input.number()?;
        direction.translated.destination_port = input.number()?;
        direction.original_source_mac = mac(input.word()?)?;
        direction.original_destination_mac = mac(input.word()?)?;
        direction.output_source_mac = mac(input.word()?)?;
        direction.output_destination_mac = mac(input.word()?)?;
        direction.output_mtu = input.number()?;
    }
    input.end()?;
    Ok(pair)
}
struct Case {
    config: MwanNativeConfig,
    pairs: [MwanNativePairSpec; 2],
    cycles: u64,
    seconds: u64,
    minimum_forwarded: u64,
    minimum_returned: u64,
}
impl Case {
    fn read(arguments: &[String]) -> CheckResult<Self> {
        let config = configuration(&arguments[2])?;
        let first = pair(&arguments[3])?;
        let second = pair(&arguments[4])?;
        let reused = pair(&arguments[5])?;
        let direction = first.requested_direction_mask.trailing_zeros() as usize;
        let original = &first.directions[direction];
        let replacement = &reused.directions[direction];
        require(
            config.pair_capacity == 1
                && first.requested_direction_mask == reused.requested_direction_mask
                && first.requested_direction_mask == second.requested_direction_mask
                && first.cookie != reused.cookie
                && original.original.destination != replacement.original.destination
                && original.translated == replacement.translated
                && original.origin_id == replacement.origin_id
                && original.original_source_mac == replacement.original_source_mac
                && original.original_destination_mac == replacement.original_destination_mac,
            "Shutdown reuse requires one slot and a shared translated tuple for different original destinations",
        )?;
        let case = Self {
            config,
            pairs: [first, reused],
            cycles: arguments[6].parse()?,
            seconds: arguments[7].parse()?,
            minimum_forwarded: arguments[8].parse()?,
            minimum_returned: arguments[9].parse()?,
        };
        require(
            (3..=16).contains(&case.cycles)
                && (1..=30).contains(&case.seconds)
                && case.minimum_forwarded > 0
                && case.minimum_returned > 0,
            "Shutdown reuse requires 3 to 16 cycles, 1 to 30 seconds, and positive packet minima",
        )?;
        Ok(case)
    }
}
struct Owner(*mut MwanNative);
impl Owner {
    fn open(&mut self, config: &MwanNativeConfig) -> CheckResult<()> {
        let mut error = MwanNativeError::default();
        // SAFETY: Complete separate ABI records and the owner remain on this OS thread.
        native_result(
            unsafe { mwan_native_open(config, &mut self.0, &mut error) },
            &error,
        )
    }
    fn install(&self, pair: &MwanNativePairSpec) -> CheckResult<MwanNativePairResult> {
        let mut result = MwanNativePairResult::default();
        let mut error = MwanNativeError::default();
        native_result(
            // SAFETY: The live owner and separate complete records belong to this thread.
            unsafe { mwan_native_pair_install(self.0, pair, &mut result, &mut error) },
            &error,
        )?;
        require(
            result.state == MWAN_NATIVE_PAIR_INSTALLED
                && result.installed_mask == pair.requested_direction_mask
                && result.restoration_mask == pair.requested_direction_mask
                && result.cleanup_failed_mask == 0,
            "Installed pair does not own the requested rule and restoration record",
        )?;
        Ok(result)
    }
    fn poll(&self) -> CheckResult<MwanNativeWorkerStats> {
        let mut stats = MwanNativeWorkerStats::default();
        let mut error = MwanNativeError::default();
        native_result(
            // SAFETY: The owner and both writable output records remain on the owner thread.
            unsafe { mwan_native_poll(self.0, PACKET_BUDGET, &mut stats, &mut error) },
            &error,
        )?;
        require(
            stats.quarantined_packets == 0
                && stats.malformed == 0
                && stats.lookup_misses == 0
                && stats.transmit_dropped == 0,
            "Worker reported an unrecovered packet, malformed packet, lookup miss or transmit drop",
        )?;
        Ok(stats)
    }
    fn phase(&self, seconds: u64) -> CheckResult<MwanNativeWorkerStats> {
        let end = Instant::now() + Duration::from_secs(seconds);
        loop {
            let stats = self.poll()?;
            if Instant::now() >= end {
                return Ok(stats);
            }
        }
    }
    fn counters(&self, handle: MwanNativeHandle, minimum: u64) -> CheckResult<()> {
        let mut stats = MwanNativePairStats::default();
        let mut error = MwanNativeError::default();
        native_result(
            // SAFETY: The live handle belongs to this owner; outputs remain separate and writable.
            unsafe { mwan_native_pair_query(self.0, handle, &mut stats, &mut error) },
            &error,
        )?;
        require(
            stats.result.handle == handle && stats.result.state == MWAN_NATIVE_PAIR_INSTALLED,
            "Counter query changed the installed generation",
        )?;
        let mut packets = 0;
        for (index, counter) in stats.directions.iter().enumerate() {
            if stats.result.requested_direction_mask & DIRECTION_MASKS[index] == 0 {
                continue;
            }
            require(
                counter.packets_valid != 0 && counter.bytes_valid != 0 && counter.error == 0,
                "Hardware counter sample is unavailable",
            )?;
            packets += counter.packets;
            println!(
                "RECYCLE_COUNTER generation={} direction={index} valid={}/{} error={} packets={} bytes={}",
                handle.generation,
                counter.packets_valid,
                counter.bytes_valid,
                counter.error,
                counter.packets,
                counter.bytes
            );
        }
        require(
            packets >= minimum,
            "Hardware counters did not meet the packet minimum",
        )
    }
    fn empty_storage(&self) -> CheckResult<()> {
        let mut stats = MwanNativeRetirementStats::default();
        let mut error = MwanNativeError::default();
        native_result(
            // SAFETY: The retained owner and separate outputs remain on its original thread.
            unsafe { mwan_native_retirement_query(self.0, &mut stats, &mut error) },
            &error,
        )?;
        println!(
            "RECYCLE_STORAGE retained={} draining={} retained_directions={}",
            stats.retained_pairs, stats.draining_pairs, stats.retained_directions
        );
        require(
            stats.active_pair_capacity == 1
                && stats.retained_pair_capacity == 1
                && stats.retained_pairs == 0
                && stats.draining_pairs == 0
                && stats.retained_directions == 0,
            "Disable did not reclaim restoration storage",
        )
    }
    fn stale(&self, handle: MwanNativeHandle, pair: &MwanNativePairSpec) -> CheckResult<()> {
        let mut stats = MwanNativePairStats::default();
        let mut result = MwanNativePairResult::default();
        let mut error = MwanNativeError::default();
        let guards = [MwanNativePacketGuard::default(); MWAN_NATIVE_DIRECTION_COUNT as usize];
        // SAFETY: The live owner receives an obsolete scalar handle and complete separate records.
        let (query, remove, resume) = unsafe {
            (
                mwan_native_pair_query(self.0, handle, &mut stats, &mut error),
                mwan_native_pair_remove(self.0, handle, &mut result, &mut error),
                mwan_native_pair_resume_observed(
                    self.0,
                    handle,
                    pair,
                    guards.as_ptr(),
                    &mut result,
                    &mut error,
                ),
            )
        };
        println!(
            "RECYCLE_STALE generation={} query_rc={query} remove_rc={remove} resume_rc={resume}",
            handle.generation
        );
        require(
            query == -libc::ESTALE && remove == -libc::ESTALE && resume == -libc::ESTALE,
            "Old generation remained valid after replacement installation",
        )
    }
    fn close(&mut self, mode: u32, cycle: u64) -> CheckResult<()> {
        let identity = self.0;
        let mut error = MwanNativeError::default();
        // SAFETY: This owner slot and separate error record remain on the original EAL thread.
        let result = unsafe { mwan_native_close(&mut self.0, mode, &mut error) };
        let mode_label = if mode == MWAN_NATIVE_DISABLE {
            "disable"
        } else {
            "final"
        };
        println!(
            "SHUTDOWN_API cycle={cycle} mode={mode_label} rc={result} owner_retained={} owner_released={}",
            u8::from(self.0 == identity),
            u8::from(self.0.is_null())
        );
        io::stdout().flush()?;
        native_result(result, &error)?;
        if mode == MWAN_NATIVE_DISABLE {
            require(self.0 == identity, "Disable replaced the EAL owner")
        } else {
            require(self.0.is_null(), "Final shutdown retained the EAL owner")
        }
    }
}
fn diagnostics() -> CheckResult<()> {
    // SAFETY: EAL initialized its logger. The constant is NUL-terminated and the SDK owns its stream.
    let result = unsafe {
        rte_log_set_global_level(SDK_LOG_DEBUG);
        let result = rte_log_set_level_pattern(c"pmd.net.bnxt.driver".as_ptr(), SDK_LOG_DEBUG);
        rte_log_dump(rte_log_get_stream());
        result
    };
    require(result == 0, "Cannot enable BNXT shutdown diagnostics")
}
fn worker(
    cycle: u64,
    phase: &str,
    before: &MwanNativeWorkerStats,
    after: &MwanNativeWorkerStats,
) -> CheckResult<()> {
    println!(
        "RECYCLE_WORKER cycle={cycle} phase={phase} received={} forwarded={} returned={} phase_forwarded={} phase_returned={} pending={} quarantined={} transmit_dropped={}",
        after.received,
        after.forwarded,
        after.returned_to_kernel,
        after.forwarded.saturating_sub(before.forwarded),
        after
            .returned_to_kernel
            .saturating_sub(before.returned_to_kernel),
        after.pending_packets,
        after.quarantined_packets,
        after.transmit_dropped
    );
    require(
        after.pending_packets == 0
            && after.forwarded >= before.forwarded
            && after.returned_to_kernel >= before.returned_to_kernel,
        "Worker retained pending packets or counters decreased",
    )
}
fn exercise(owner: &mut Owner, case: &Case) -> CheckResult<()> {
    owner.open(&case.config)?;
    diagnostics()?;
    let identity = owner.0;
    let mut selected = 0;
    let mut current = owner.install(&case.pairs[selected])?;
    let mut stats = owner.poll()?;
    let names = ["FIRST", "REUSED"];
    println!(
        "RECYCLE_CONTRACT external_unique_packet_ids=required boundary_proven=0 source=external_only"
    );
    for cycle in 1..=case.cycles {
        println!(
            "READY_RECYCLE_CURRENT cycle={cycle} pair={} seconds={}",
            names[selected], case.seconds
        );
        io::stdout().flush()?;
        let before = stats;
        stats = owner.phase(case.seconds)?;
        worker(cycle, "current", &before, &stats)?;
        owner.counters(current.handle, case.minimum_forwarded)?;
        require(
            stats.forwarded - before.forwarded >= case.minimum_forwarded,
            "Current phase did not forward enough fresh packets",
        )?;
        println!(
            "QUEUE_RECYCLE_OLD cycle={cycle} pair={} rx_pause_ns={} purpose=queue_external_old_packets_not_fence",
            names[selected],
            QUEUE_PAUSE.as_nanos()
        );
        io::stdout().flush()?;
        thread::sleep(QUEUE_PAUSE);
        let old = current.handle;
        owner.close(MWAN_NATIVE_DISABLE, cycle)?;
        owner.empty_storage()?;
        let before = stats;
        stats = owner.poll()?;
        worker(cycle, "shutdown_drain", &before, &stats)?;
        require(
            stats.returned_to_kernel - before.returned_to_kernel >= case.minimum_returned,
            "Disable did not restore enough queued packets",
        )?;
        owner.open(&case.config)?;
        require(owner.0 == identity, "Enable replaced the EAL owner")?;
        let replacement = 1 - selected;
        current = owner.install(&case.pairs[replacement])?;
        require(
            current.handle.instance == old.instance && current.handle.generation > old.generation,
            "Replacement did not advance the generation on the retained owner",
        )?;
        owner.stale(old, &case.pairs[selected])?;
        println!(
            "READY_RECYCLE_REUSED cycle={cycle} pair={} seconds={} compare_external_unique_ids=required",
            names[replacement], case.seconds
        );
        io::stdout().flush()?;
        let before = stats;
        stats = owner.phase(case.seconds)?;
        worker(cycle, "replacement", &before, &stats)?;
        owner.counters(current.handle, case.minimum_forwarded)?;
        require(
            stats.forwarded - before.forwarded >= case.minimum_forwarded,
            "Replacement phase did not forward enough fresh packets",
        )?;
        selected = replacement;
    }
    println!(
        "QUEUE_SHUTDOWN_FINAL pair={} rx_pause_ns={}",
        names[selected],
        QUEUE_PAUSE.as_nanos()
    );
    io::stdout().flush()?;
    thread::sleep(QUEUE_PAUSE);
    Ok(())
}
fn run(case: &Case) -> CheckResult<()> {
    let mut owner = Owner(ptr::null_mut());
    let result = exercise(&mut owner, case);
    let cleanup = owner.close(MWAN_NATIVE_FINAL_SHUTDOWN, case.cycles);
    if let Err(error) = &result {
        eprintln!("Shutdown case failed: {error}");
    }
    cleanup?;
    result?;
    println!(
        "RECYCLE_API_COMPLETE cycles={} successful_installs={} owner_released=1 boundary_proven=0",
        case.cycles,
        case.cycles + 1
    );
    Ok(())
}
fn main() -> ExitCode {
    let arguments: Vec<String> = std::env::args().collect();
    if arguments.len() != 10
        || !matches!(
            arguments[1].as_str(),
            "--shutdown-reuse" | "--check-fixtures"
        )
    {
        eprintln!(
            "Usage: shutdown --shutdown-reuse|--check-fixtures CONFIG FIRST_PAIR SECOND_PAIR REUSED_PAIR CYCLES PHASE_SECONDS MIN_FORWARDED MIN_RETURNED"
        );
        return ExitCode::FAILURE;
    }
    let result = Case::read(&arguments).and_then(|case| {
        if arguments[1] == "--check-fixtures" {
            Ok(())
        } else {
            run(&case)
        }
    });
    match result {
        Ok(()) if arguments[1] == "--check-fixtures" => ExitCode::SUCCESS,
        Ok(()) => ExitCode::from(2),
        Err(error) => {
            eprintln!("{error}");
            ExitCode::FAILURE
        }
    }
}
