//! C layouts and constants come from the public ABI header.

include!(concat!(env!("OUT_DIR"), "/abi_bindings.rs"));

impl Default for MwanNativeFrame {
    fn default() -> Self {
        Self {
            data: std::ptr::null(),
            length: 0,
            reserved: 0,
        }
    }
}

impl Default for MwanNativeConfig {
    fn default() -> Self {
        Self {
            abi_version: 0,
            record_size: 0,
            cpu: 0,
            memory_mb: 0,
            memory_mode: 0,
            controller_count: 0,
            receiver_count: 0,
            data_count: 0,
            origin_count: 0,
            pair_capacity: 0,
            mbuf_count: 0,
            drain_timeout_ms: 0,
            rx_descriptors: 0,
            tx_descriptors: 0,
            mtu: 0,
            reserved: 0,
            hugepage_directory: [0; MWAN_NATIVE_PATH_SIZE as usize],
            file_prefix: [0; MWAN_NATIVE_PREFIX_SIZE as usize],
            controllers: [MwanNativeController::default(); MWAN_NATIVE_MAX_CONTROLLERS as usize],
            receivers: [MwanNativeReceiver::default(); MWAN_NATIVE_MAX_RECEIVERS as usize],
            data: [MwanNativeData::default(); MWAN_NATIVE_MAX_DATA as usize],
            origins: [MwanNativeOrigin::default(); MWAN_NATIVE_MAX_ORIGINS as usize],
        }
    }
}

impl Default for MwanNativeError {
    fn default() -> Self {
        Self {
            code: 0,
            operation: 0,
            object_id: 0,
            direction: 0,
            flow_error_type: 0,
            object_kind: 0,
            message: [0; MWAN_NATIVE_TEXT_SIZE as usize],
            packet: MwanNativePacketDiagnostic::default(),
        }
    }
}

impl Default for MwanNativeBuildInfo {
    fn default() -> Self {
        Self {
            abi_version: 0,
            config_size: 0,
            pair_spec_size: 0,
            direction_size: 0,
            controller_size: 0,
            receiver_size: 0,
            data_size: 0,
            origin_size: 0,
            error_size: 0,
            worker_stats_size: 0,
            pair_stats_size: 0,
            reserved: 0,
            dpdk_version: [0; 32],
            source_sha256: [0; 65],
            patch_sha256: [0; 65],
        }
    }
}
