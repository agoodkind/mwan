use mwan_native::abi::{MwanNativeDirection, MwanNativePacketGuard};
use mwan_native::packet;

const DSCP: u8 = 0x10;
const FLOW_LABEL: u32 = 0x12345;
const IPV6_VERSION_WORD: u32 = 0x6000_0000;

#[test]
fn packet_guards_accept_ecn_transitions_and_preserve_wire_bytes() {
    for corpus in [
        include_bytes!("malformed/tcp4-input.pcap").as_slice(),
        include_bytes!("packet/tcp-source-input.pcap").as_slice(),
    ] {
        for packet_ecn in 0..4 {
            let frame = selected_frame(corpus, DSCP | packet_ecn);
            let parsed = packet::decode(&frame).expect("complete TCP frame");
            let diagnostic = packet::diagnostic(&parsed);
            let original_observation = packet::observe(&frame);
            let direction = MwanNativeDirection {
                original: diagnostic.tuple,
                translated: diagnostic.tuple,
                original_source_mac: diagnostic.source_mac,
                original_destination_mac: diagnostic.destination_mac,
                output_source_mac: diagnostic.source_mac,
                output_destination_mac: diagnostic.destination_mac,
                output_mtu: 1500,
                ..MwanNativeDirection::default()
            };
            assert!(packet::eligible(&frame, &parsed, &direction));
            for guard_ecn in 0..4 {
                let mut guard = MwanNativePacketGuard {
                    traffic_class: DSCP | guard_ecn,
                    flow_label: original_observation.flow_label,
                    reserved: [0; 3],
                };
                assert!(packet::guard_matches(&parsed, &guard));
                if original_observation.flow_label != 0 {
                    guard.flow_label ^= 1;
                    assert!(!packet::guard_matches(&parsed, &guard));
                }
            }
            let guard = MwanNativePacketGuard {
                traffic_class: DSCP,
                flow_label: original_observation.flow_label,
                reserved: [0; 3],
            };
            for dscp_bit in [0x04, 0x08, 0x10, 0x20, 0x40, 0x80] {
                let different = selected_frame(corpus, (DSCP ^ dscp_bit) | packet_ecn);
                let changed = packet::decode(&different).expect("TCP frame with another DSCP");
                assert!(packet::eligible(&different, &changed, &direction));
                assert!(!packet::guard_matches(&changed, &guard));
            }
            let mut forwarded = frame.clone();
            packet::forward(&mut forwarded, &parsed, &direction)
                .expect("forward guarded TCP frame");
            let observation = packet::observe(&forwarded);
            assert_eq!(observation.traffic_class, DSCP | packet_ecn);
            assert_eq!(observation.flow_label, original_observation.flow_label);
            packet::restore_forwarded(&mut forwarded, &parsed, &direction)
                .expect("restore the guarded frame after retirement");
            assert_eq!(forwarded, frame);
        }
    }
}

fn selected_frame(corpus: &[u8], traffic_class: u8) -> Vec<u8> {
    let captured = u32::from_le_bytes(corpus[32..36].try_into().expect("PCAP captured length"));
    let end = 40 + usize::try_from(captured).expect("bounded packet length");
    let mut frame = corpus[40..end].to_vec();
    let network_offset = 14;
    if frame[network_offset] >> 4 == 4 {
        frame[network_offset + 1] = traffic_class;
        frame[network_offset + 10..network_offset + 12].fill(0);
        let header_length = usize::from(frame[network_offset] & 0x0f) * 4;
        let checksum = internet_checksum(&frame[network_offset..network_offset + header_length]);
        frame[network_offset + 10..network_offset + 12].copy_from_slice(&checksum.to_be_bytes());
    } else {
        let word = IPV6_VERSION_WORD | u32::from(traffic_class) << 20 | FLOW_LABEL;
        frame[network_offset..network_offset + 4].copy_from_slice(&word.to_be_bytes());
    }
    frame
}

fn internet_checksum(bytes: &[u8]) -> u16 {
    let mut sum = 0_u32;
    for word in bytes.chunks_exact(2) {
        sum += u32::from(u16::from_be_bytes([word[0], word[1]]));
    }
    while sum > u32::from(u16::MAX) {
        sum = (sum & u32::from(u16::MAX)) + (sum >> 16);
    }
    !u16::try_from(sum).expect("folded IPv4 header checksum")
}
