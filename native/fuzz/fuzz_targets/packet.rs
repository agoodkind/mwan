#![no_main]

use libfuzzer_sys::fuzz_target;
use mwan_native::{
    abi::{MWAN_NATIVE_FAMILY_IPV4, MWAN_NATIVE_FAMILY_IPV6, MwanNativeDirection},
    packet::{decode, eligible, forward, restore},
};

const MAXIMUM_FRAME_LENGTH: usize = 14 + 8 + 40 + u16::MAX as usize;

fuzz_target!(|input: &[u8]| {
    if input.len() > MAXIMUM_FRAME_LENGTH {
        return;
    }
    let parsed = match decode(input) {
        Ok(packet) => packet,
        Err(error) => {
            assert!(error.code > 0);
            assert_eq!(error.diagnostic.decode_status, -error.code);
            return;
        }
    };
    let tuple = parsed.tuple();
    let mut direction = MwanNativeDirection {
        original: tuple,
        translated: tuple,
        original_source_mac: parsed.source_mac(),
        original_destination_mac: parsed.destination_mac(),
        output_source_mac: [2, 0, 0, 0, 0, 1],
        output_destination_mac: [2, 0, 0, 0, 0, 2],
        output_mtu: u16::MAX,
        ..MwanNativeDirection::default()
    };
    match u32::from(tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => {
            if let Some(first) = direction.original.source.first_mut() {
                *first ^= input.first().copied().unwrap_or(0);
            }
            direction.original.source_port = direction.original.source_port.wrapping_add(1);
        }
        MWAN_NATIVE_FAMILY_IPV6 => {
            direction.original.source.rotate_left(2);
            direction.original.destination.rotate_right(2);
        }
        _ => unreachable!("the decoder accepts only IPv4 and IPv6"),
    }
    let mut returned = input.to_vec();
    restore(&mut returned, &parsed, &direction)
        .expect("decoded headers permit bounded restoration");
    assert_eq!(returned.len(), input.len());
    if eligible(input, &parsed, &direction) {
        let mut forwarded = input.to_vec();
        forward(&mut forwarded, &parsed, &direction)
            .expect("eligible headers permit bounded forwarding");
        assert_eq!(forwarded.len(), input.len());
    }
});
