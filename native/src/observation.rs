use crate::abi::{
    MWAN_NATIVE_OBSERVATION_BATCH_LIMIT, MWAN_NATIVE_OBSERVATION_FRAME_LIMIT, MwanNativeFrame,
    MwanNativeObservation,
};
use crate::packet;
use std::panic::{AssertUnwindSafe, catch_unwind};

/// # Safety
/// The caller provides separate arrays with count readable frame descriptors
/// and count writable observations. Every descriptor identifies readable frame
/// bytes for the duration of this nonreentrant call. Inputs cannot overlap outputs.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_observe_batch(
    frames: *const MwanNativeFrame,
    observations: *mut MwanNativeObservation,
    count: u32,
) -> i32 {
    if frames.is_null()
        || observations.is_null()
        || count == 0
        || count > MWAN_NATIVE_OBSERVATION_BATCH_LIMIT
    {
        return -libc::EINVAL;
    }
    match catch_unwind(AssertUnwindSafe(|| {
        // SAFETY: The caller supplies the complete bounded descriptor array.
        let frames = unsafe { std::slice::from_raw_parts(frames, count as usize) };
        if frames.iter().any(|frame| {
            frame.data.is_null()
                || frame.length == 0
                || frame.length > MWAN_NATIVE_OBSERVATION_FRAME_LIMIT
                || frame.reserved != 0
        }) {
            return -libc::EINVAL;
        }
        for (index, frame) in frames.iter().enumerate() {
            // SAFETY: Descriptor validation bounds this readable frame. The
            // immutable borrow ends before writing the separate output record.
            let bytes = unsafe { std::slice::from_raw_parts(frame.data, frame.length as usize) };
            let observation = packet::observe(bytes);
            // SAFETY: The caller supplies count separate writable observations,
            // and index comes from the equally sized descriptor array.
            unsafe { observations.add(index).write(observation) };
        }
        0
    })) {
        Ok(result) => result,
        Err(_) => -libc::EFAULT,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn public_batch_observes_captured_headers_and_rejects_bad_checksums() {
        let ipv4 = first_frame(include_bytes!("../tests/malformed/tcp4-input.pcap"));
        let ipv6 = first_frame(include_bytes!("../tests/packet/tcp-source-input.pcap"));
        let mut corrupt = ipv6.to_vec();
        *corrupt.last_mut().expect("captured TCP payload") ^= 1;
        let frames = [descriptor(ipv4), descriptor(ipv6), descriptor(&corrupt)];
        let mut observations = [MwanNativeObservation::default(); 3];
        // SAFETY: These complete arrays are separate. Each descriptor borrows
        // immutable captured bytes that remain live through the ABI operation.
        let result =
            unsafe { mwan_native_observe_batch(frames.as_ptr(), observations.as_mut_ptr(), 3) };
        assert_eq!(result, 0);
        let ipv4 = observations[0];
        assert_eq!(ipv4.decode_status, 0);
        assert_eq!(ipv4.eligible, 1);
        assert_eq!(ipv4.tuple.family, 4);
        assert_eq!(ipv4.tuple.protocol, 6);
        assert_eq!(&ipv4.tuple.source[..4], &[198, 18, 0, 11]);
        assert_eq!(&ipv4.tuple.destination[..4], &[198, 18, 0, 12]);
        assert_eq!(ipv4.tuple.source_port, 15401);
        assert_eq!(ipv4.tuple.destination_port, 15402);
        assert_eq!(ipv4.network_length, 168);
        assert_eq!(ipv4.tcp_flags, 16);
        assert_eq!(ipv4.ttl, 64);
        assert_eq!(ipv4.source_mac, [0x80, 0x69, 0x1a, 0xdc, 0x1d, 0xb2]);
        assert_eq!(ipv4.destination_mac, [2, 0x57, 0x50, 4, 0, 0]);
        let ipv6 = observations[1];
        assert_eq!(ipv6.decode_status, 0);
        assert_eq!(ipv6.eligible, 1);
        assert_eq!(ipv6.tuple.family, 6);
        assert_eq!(ipv6.network_length, 84);
        assert_eq!(ipv6.traffic_class, 46);
        assert_eq!(ipv6.flow_label, 251_247);
        assert_eq!(observations[2].decode_status, 0);
        assert_eq!(observations[2].eligible, 0);
        assert_eq!(observations[2].tuple, ipv6.tuple);
    }

    #[test]
    fn public_batch_rejects_truncation_tags_and_invalid_descriptors() {
        let ipv6 = first_frame(include_bytes!("../tests/packet/tcp-source-input.pcap"));
        let mut tagged = ipv6[..12].to_vec();
        tagged.extend_from_slice(&[0x81, 0, 0, 1]);
        tagged.extend_from_slice(&ipv6[12..]);
        let frames = [descriptor(&ipv6[..20]), descriptor(&tagged)];
        let mut observations = [MwanNativeObservation::default(); 2];
        // SAFETY: The bounded descriptors and outputs use separate arrays,
        // and the complete captured buffers remain live during parsing.
        let result =
            unsafe { mwan_native_observe_batch(frames.as_ptr(), observations.as_mut_ptr(), 2) };
        assert_eq!(result, 0);
        assert_eq!(observations[0].decode_status, -libc::EBADMSG);
        assert_eq!(observations[0].eligible, 0);
        assert_eq!(observations[1].decode_status, 0);
        assert_eq!(observations[1].eligible, 0);
        let invalid = MwanNativeFrame {
            reserved: 1,
            ..descriptor(ipv6)
        };
        observations[0].eligible = 9;
        // SAFETY: A reserved-field failure must reject before reading this
        // otherwise complete single descriptor or changing the output.
        let result = unsafe { mwan_native_observe_batch(&invalid, observations.as_mut_ptr(), 1) };
        assert_eq!(result, -libc::EINVAL);
        assert_eq!(observations[0].eligible, 9);
    }

    fn descriptor(bytes: &[u8]) -> MwanNativeFrame {
        MwanNativeFrame {
            data: bytes.as_ptr(),
            length: bytes.len() as u32,
            reserved: 0,
        }
    }

    fn first_frame(capture: &[u8]) -> &[u8] {
        assert_eq!(&capture[..4], &[0xd4, 0xc3, 0xb2, 0xa1]);
        let length = u32::from_le_bytes(capture[32..36].try_into().expect("PCAP length"));
        &capture[40..40 + length as usize]
    }
}
