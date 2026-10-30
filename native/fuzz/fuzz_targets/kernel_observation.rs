#![no_main]

use libfuzzer_sys::fuzz_target;
use mwan_native::{
    abi::{
        MWAN_NATIVE_OBSERVATION_FRAME_LIMIT, MwanNativeFrame, MwanNativeKernelFrame,
        MwanNativeKernelMetadata, MwanNativeKernelObservation,
    },
    mwan_native_observe_kernel_batch, packet,
};

const METADATA_LENGTH: usize = 32;

fn word(input: &[u8], offset: usize) -> u16 {
    u16::from_le_bytes(input[offset..offset + 2].try_into().expect("metadata word"))
}

fn double_word(input: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(
        input[offset..offset + 4]
            .try_into()
            .expect("metadata double word"),
    )
}

fuzz_target!(|input: &[u8]| {
    if input.len() <= METADATA_LENGTH
        || input.len() > METADATA_LENGTH + MWAN_NATIVE_OBSERVATION_FRAME_LIMIT as usize
    {
        return;
    }
    // The corpus uses little-endian scalar fields in public metadata order,
    // followed by Ethernet bytes. Rust never casts bytes to an ABI record.
    let metadata = MwanNativeKernelMetadata {
        auxiliary_status: double_word(input, 0),
        packet_length: double_word(input, 4),
        captured_length: double_word(input, 8),
        mac_offset: word(input, 12),
        network_offset: word(input, 14),
        vnet_flags: input[16],
        gso_type: input[17],
        header_length: word(input, 18),
        gso_size: word(input, 20),
        checksum_start: word(input, 22),
        checksum_offset: word(input, 24),
        reserved16: word(input, 26),
        reserved32: double_word(input, 28),
    };
    let bytes = &input[METADATA_LENGTH..];
    let original = bytes.to_vec();
    let frame = MwanNativeFrame {
        data: bytes.as_ptr(),
        length: bytes.len() as u32,
        reserved: 0,
    };
    let ordinary_metadata = MwanNativeKernelMetadata {
        auxiliary_status: 1,
        packet_length: frame.length,
        captured_length: frame.length,
        network_offset: 14,
        ..Default::default()
    };
    let frames = [
        MwanNativeKernelFrame {
            frame,
            metadata: ordinary_metadata,
        },
        MwanNativeKernelFrame { frame, metadata },
    ];
    let original_frames = frames;
    let sentinel = MwanNativeKernelObservation {
        effective_network_length: 77,
        reserved: 99,
        ..Default::default()
    };
    let mut outputs = [sentinel; 2];
    // SAFETY: Both arrays contain two aligned live records. Every frame
    // borrows immutable bytes from input, separate from the writable outputs.
    let status =
        unsafe { mwan_native_observe_kernel_batch(frames.as_ptr(), outputs.as_mut_ptr(), 2) };
    assert_eq!(bytes, original);
    assert_eq!(frames, original_frames);
    if metadata.reserved16 != 0 || metadata.reserved32 != 0 {
        assert!(status < 0);
        assert_eq!(outputs, [sentinel; 2]);
        return;
    }
    assert_eq!(status, 0);
    let wire = packet::observe(bytes);
    assert_eq!(outputs[0].observation.eligible, wire.eligible);
    for output in outputs {
        assert_eq!(output.reserved, 0);
        assert!(output.observation.eligible <= 1);
        if output.observation.eligible == 0 {
            assert_eq!(output.effective_network_length, 0);
        } else {
            assert_eq!(output.observation.decode_status, 0);
            assert!(output.effective_network_length > 0);
            assert!(output.effective_network_length <= output.observation.network_length);
            assert!(output.effective_network_length <= u32::from(u16::MAX));
            assert!(output.observation.network_length as usize <= bytes.len());
        }
    }
    if wire.eligible != 0 {
        assert_eq!(outputs[0].observation, wire);
        assert_eq!(outputs[0].effective_network_length, wire.network_length);
    }
});
