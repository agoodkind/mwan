use super::*;
use std::net::Ipv6Addr;

const SOURCE_MAC: [u8; 6] = [0x80, 0x69, 0x1a, 0xdc, 0x1d, 0xb2];
const RECEIVER_MAC: [u8; 6] = [2, 0x57, 0x50, 4, 0, 0];
const OUTPUT_MAC: [u8; 6] = [2, 0x57, 0x50, 4, 3, 1];
const CLIENT_MAC: [u8; 6] = [0x24, 0x5e, 0xbe, 0x8e, 0x6b, 0xa5];

struct Corpus {
    input: &'static [u8],
    output: &'static [u8],
    kernel: &'static [u8],
    source: Option<&'static str>,
    destination: Option<&'static str>,
    mtu: u16,
}

#[test]
fn frozen_malformed_packets_return_exact_bytes_and_allow_the_next_packet() {
    let cases = [
        Corpus {
            input: include_bytes!("../tests/malformed/tcp4-input.pcap"),
            output: include_bytes!("../tests/malformed/tcp4-expected-output.pcap"),
            kernel: include_bytes!("../tests/malformed/tcp4-expected-kernel.pcap"),
            source: None,
            destination: None,
            mtu: 1500,
        },
        Corpus {
            input: include_bytes!("../tests/malformed/tcp6-input.pcap"),
            output: include_bytes!("../tests/malformed/tcp6-expected-output.pcap"),
            kernel: include_bytes!("../tests/malformed/tcp6-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: None,
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/malformed/udp6-input.pcap"),
            output: include_bytes!("../tests/malformed/udp6-expected-output.pcap"),
            kernel: include_bytes!("../tests/malformed/udp6-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: None,
            mtu: 128,
        },
    ];
    for case in cases {
        let input = frames(case.input);
        let direction = direction(input.first().expect("first packet"), &case);
        let mut output = Vec::new();
        let mut kernel = Vec::new();
        let mut malformed = 0;
        for original in &input {
            let mut received = hardware_translation(original, &direction);
            let parsed = decode(&received).expect("complete translated headers");
            malformed += usize::from(parsed.malformed);
            if eligible(&received, &parsed, &direction) {
                forward(&mut received, &parsed, &direction).expect("forward packet");
                output.push(received);
            } else {
                restore(&mut received, &parsed, &direction).expect("restore packet");
                kernel.push(received);
            }
        }
        assert_eq!(malformed, 1);
        assert_eq!(output, owned_frames(case.output));
        assert_eq!(kernel, owned_frames(case.kernel));
        assert_eq!(output.len(), 2);
        assert_eq!(kernel.len(), 2);
    }
}

#[test]
fn frozen_npt_packets_preserve_transport_and_exception_bytes() {
    let cases = [
        Corpus {
            input: include_bytes!("../tests/packet/tcp-source-input.pcap"),
            output: include_bytes!("../tests/packet/tcp-source-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/tcp-source-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: None,
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/tcp-destination-input.pcap"),
            output: include_bytes!("../tests/packet/tcp-destination-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/tcp-destination-expected-kernel.pcap"),
            source: None,
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/tcp-both-input.pcap"),
            output: include_bytes!("../tests/packet/tcp-both-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/tcp-both-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/udp-source-input.pcap"),
            output: include_bytes!("../tests/packet/udp-source-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/udp-source-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: None,
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/udp-destination-input.pcap"),
            output: include_bytes!("../tests/packet/udp-destination-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/udp-destination-expected-kernel.pcap"),
            source: None,
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/udp-both-input.pcap"),
            output: include_bytes!("../tests/packet/udp-both-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/udp-both-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
    ];
    for case in cases {
        let input = frames(case.input);
        let direction = direction(input.first().expect("first packet"), &case);
        let mut output = Vec::new();
        let mut kernel = Vec::new();
        for original in input {
            let mut received = hardware_translation(original, &direction);
            let parsed = decode(&received).expect("NPT packet headers");
            if eligible(&received, &parsed, &direction) {
                forward(&mut received, &parsed, &direction).expect("forward NPT packet");
                output.push(received);
            } else {
                restore(&mut received, &parsed, &direction).expect("restore NPT exception");
                kernel.push(received);
            }
        }
        assert_eq!(output, owned_frames(case.output));
        assert_eq!(kernel, owned_frames(case.kernel));
    }
}

#[test]
fn extension_and_fragment_packets_return_the_frozen_originals() {
    let cases = [
        Corpus {
            input: include_bytes!("../tests/packet/tcp-both-probes-input.pcap"),
            output: include_bytes!("../tests/packet/tcp-both-probes-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/tcp-both-probes-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/udp-both-probes-input.pcap"),
            output: include_bytes!("../tests/packet/udp-both-probes-expected-output.pcap"),
            kernel: include_bytes!("../tests/packet/udp-both-probes-expected-kernel.pcap"),
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
    ];
    for case in cases {
        let input = frames(case.input);
        let direction = direction(input.first().expect("first packet"), &case);
        let mut output = Vec::new();
        let mut kernel = Vec::new();
        for original in input {
            let mut received = hardware_translation(original, &direction);
            let parsed = decode(&received).expect("extension or fragment headers");
            if eligible(&received, &parsed, &direction) {
                forward(&mut received, &parsed, &direction).expect("forward sentinel");
                output.push(received);
            } else {
                restore(&mut received, &parsed, &direction).expect("restore extended packet");
                kernel.push(received);
            }
        }
        assert_eq!(output, owned_frames(case.output));
        assert_eq!(kernel, owned_frames(case.kernel));
    }
}

#[test]
fn ipv4_udp_wire_zero_preserves_the_frozen_frames() {
    let case = Corpus {
        input: include_bytes!("../tests/packet/ipv4-udp-input.pcap"),
        output: include_bytes!("../tests/packet/ipv4-udp-expected-output.pcap"),
        kernel: include_bytes!("../tests/packet/ipv4-udp-expected-kernel.pcap"),
        source: None,
        destination: None,
        mtu: 1500,
    };
    let input = frames(case.input);
    let mut direction = direction(input.first().expect("first packet"), &case);
    direction.translated = direction.original;
    let mut output = Vec::new();
    let mut kernel = Vec::new();
    for original in input {
        let mut received = original.to_vec();
        let parsed = decode(&received).expect("IPv4 UDP headers");
        assert_eq!(read_u16(&received, parsed.transport_offset + 6), Some(0));
        if eligible(&received, &parsed, &direction) {
            forward(&mut received, &parsed, &direction).expect("forward UDP zero");
            output.push(received);
        } else {
            restore(&mut received, &parsed, &direction).expect("restore UDP zero");
            kernel.push(received);
        }
    }
    assert_eq!(output, owned_frames(case.output));
    assert_eq!(kernel, owned_frames(case.kernel));
}

#[test]
fn every_header_boundary_rejects_or_restores_without_extending_the_frame() {
    for corpus in [
        include_bytes!("../tests/malformed/tcp4-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/tcp6-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/udp6-input.pcap").as_slice(),
    ] {
        let packets = frames(corpus);
        let original = packets.first().expect("valid frame");
        for length in 0..original.len() {
            let truncated = original.get(..length).expect("bounded truncation");
            match decode(truncated) {
                Ok(parsed) => {
                    assert!(parsed.malformed);
                    let direction = identity_direction(&parsed);
                    assert!(!eligible(truncated, &parsed, &direction));
                    let mut restored = truncated.to_vec();
                    restore(&mut restored, &parsed, &direction).expect("complete bounded headers");
                    assert_eq!(restored, truncated);
                }
                Err(error) => {
                    assert_eq!(error.code, libc::EBADMSG);
                    assert_eq!(error.diagnostic.decode_status, -libc::EBADMSG);
                    if length >= ETHERNET_HEADER_LENGTH {
                        assert_ne!(
                            error.diagnostic.validity & MWAN_NATIVE_PACKET_ETHERNET_VALID,
                            0
                        );
                    }
                }
            }
        }
    }
}

#[test]
fn inverse_nat_preserves_corrupted_header_and_tcp_checksums() {
    let input = frames(include_bytes!("../tests/malformed/tcp4-input.pcap"));
    let case = Corpus {
        input: &[],
        output: &[],
        kernel: &[],
        source: None,
        destination: None,
        mtu: 1500,
    };
    let mut original = input.first().expect("IPv4 TCP frame").to_vec();
    let direction = direction(&original, &case);
    original
        .get_mut(24..26)
        .expect("IPv4 checksum")
        .copy_from_slice(&0x1234_u16.to_be_bytes());
    original
        .get_mut(50..52)
        .expect("TCP checksum")
        .copy_from_slice(&0x5678_u16.to_be_bytes());
    let mut received = hardware_translation(&original, &direction);
    let parsed = decode(&received).expect("bad checksums retain headers");
    assert!(!eligible(&received, &parsed, &direction));
    restore(&mut received, &parsed, &direction).expect("restore corrupted checksums");
    assert_eq!(received, original);
}

#[test]
fn invalid_tcp_header_lengths_and_reserved_bits_remain_exceptions() {
    let packets = frames(include_bytes!("../tests/packet/tcp-source-input.pcap"));
    let valid = packets.first().expect("valid TCP packet");
    for field in [0x40, 0x4f, 0x50, 0x51, 0xf0] {
        let mut frame = valid.to_vec();
        *frame.get_mut(66).expect("TCP data offset") = field;
        recompute_tcp6_checksum(&mut frame);
        let parsed = decode(&frame).expect("TCP base headers");
        assert_eq!(
            eligible(&frame, &parsed, &identity_direction(&parsed)),
            field == 0x50
        );
    }
}

#[test]
fn vlan_and_extension_bounds_preserve_partial_diagnostics() {
    let packets = frames(include_bytes!("../tests/malformed/udp6-input.pcap"));
    let valid = packets.first().expect("valid UDP packet");
    let mut tagged = valid.to_vec();
    for count in 1..=3 {
        let mut next = Vec::new();
        next.extend_from_slice(tagged.get(..12).expect("Ethernet addresses"));
        next.extend_from_slice(&VLAN_ETHERTYPE.to_be_bytes());
        next.extend_from_slice(&[0, 1]);
        next.extend_from_slice(tagged.get(12..).expect("EtherType and network bytes"));
        tagged = next;
        if count <= MAX_VLAN_HEADERS {
            let parsed = decode(&tagged).expect("supported VLAN count");
            assert!(parsed.extended);
            assert!(!eligible(&tagged, &parsed, &identity_direction(&parsed)));
        } else {
            let error = decode(&tagged).expect_err("third VLAN exceeds parser bound");
            assert_eq!(error.code, libc::EBADMSG);
            assert_eq!(error.diagnostic.ether_type, VLAN_ETHERTYPE);
        }
    }
    for count in [MAX_IPV6_EXTENSION_HEADERS, MAX_IPV6_EXTENSION_HEADERS + 1] {
        let frame = extension_chain(valid, count);
        if count == MAX_IPV6_EXTENSION_HEADERS {
            let parsed = decode(&frame).expect("bounded extension chain");
            assert!(parsed.extended);
            assert!(!eligible(&frame, &parsed, &identity_direction(&parsed)));
        } else {
            assert_eq!(
                decode(&frame).expect_err("excess extension chain").code,
                libc::EPROTONOSUPPORT
            );
        }
    }
    let mut frame = extension_chain(valid, 1);
    *frame.get_mut(55).expect("extension length") = u8::MAX;
    let error = decode(&frame).expect_err("extension cannot exceed captured network bytes");
    assert_eq!(error.code, libc::EBADMSG);
    assert_ne!(error.diagnostic.validity & MWAN_NATIVE_PACKET_IP_VALID, 0);
    frame.get_mut(18..20).expect("IPv6 payload length").fill(0);
    assert_eq!(
        decode(&frame).expect_err("underdeclared extension").code,
        libc::EBADMSG
    );
}

#[test]
fn editing_rejects_a_different_frame_length_before_mutation() {
    let input = frames(include_bytes!("../tests/malformed/tcp4-input.pcap"));
    let original = input.first().expect("valid TCP packet");
    let parsed = decode(original).expect("valid headers");
    let direction = identity_direction(&parsed);
    let mut frame = original
        .get(..original.len() - 1)
        .expect("truncated copy")
        .to_vec();
    let before = frame.clone();
    assert!(restore(&mut frame, &parsed, &direction).is_err());
    assert_eq!(frame, before);
    assert!(forward(&mut frame, &parsed, &direction).is_err());
    assert_eq!(frame, before);
    assert!(restore_forwarded(&mut frame, &parsed, &direction).is_err());
    assert_eq!(frame, before);
}

#[test]
fn retiring_prepared_packets_restores_exact_original_frames() {
    let cases = [
        Corpus {
            input: include_bytes!("../tests/malformed/tcp4-input.pcap"),
            output: &[],
            kernel: &[],
            source: None,
            destination: None,
            mtu: 1500,
        },
        Corpus {
            input: include_bytes!("../tests/packet/tcp-both-input.pcap"),
            output: &[],
            kernel: &[],
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/udp-both-input.pcap"),
            output: &[],
            kernel: &[],
            source: Some("2001:db8:7702:0:fffe::11"),
            destination: Some("2001:db8:9901:0:fffe::12"),
            mtu: 128,
        },
        Corpus {
            input: include_bytes!("../tests/packet/ipv4-udp-input.pcap"),
            output: &[],
            kernel: &[],
            source: None,
            destination: None,
            mtu: 1500,
        },
    ];
    for case in cases {
        let mut restored = 0;
        for original in frames(case.input) {
            let mut direction = direction(original, &case);
            let mut received = if u32::from(direction.original.family) == MWAN_NATIVE_FAMILY_IPV4
                && u32::from(direction.original.protocol) == MWAN_NATIVE_PROTOCOL_UDP
            {
                direction.translated = direction.original;
                original.to_vec()
            } else {
                hardware_translation(original, &direction)
            };
            let parsed = decode(&received).expect("captured translated headers");
            if !eligible(&received, &parsed, &direction) {
                continue;
            }
            forward(&mut received, &parsed, &direction).expect("prepare owned TX frame");
            assert_ne!(received, original);
            let ttl_field = if u32::from(parsed.tuple.family) == MWAN_NATIVE_FAMILY_IPV4 {
                8
            } else {
                7
            };
            for offset in [
                0,
                ETHERNET_ADDRESS_LENGTH,
                parsed.network_offset + ttl_field,
            ] {
                let mut invalid = received.clone();
                *invalid.get_mut(offset).expect("prepared header field") ^= 1;
                let before = invalid.clone();
                assert!(restore_forwarded(&mut invalid, &parsed, &direction).is_err());
                assert_eq!(invalid, before);
            }
            restore_forwarded(&mut received, &parsed, &direction)
                .expect("restore retired TX frame");
            assert_eq!(received, original);
            let before = received.clone();
            assert!(restore_forwarded(&mut received, &parsed, &direction).is_err());
            assert_eq!(received, before);
            restored += 1;
        }
        assert!(
            restored > 0,
            "captured input must exercise prepared forwarding"
        );
    }
}

#[test]
fn retiring_prepared_ipv4_preserves_wire_zero_header_checksums() {
    let input = frames(include_bytes!("../tests/malformed/tcp4-input.pcap"));
    let mut original = input.first().expect("captured IPv4 TCP frame").to_vec();
    let parsed = decode(&original).expect("captured IPv4 headers");
    let checksum_offset = parsed.network_offset + 10;
    let identifier_offset = parsed.network_offset + 4;
    original[checksum_offset..checksum_offset + 2].fill(0);
    original[identifier_offset..identifier_offset + 2].fill(0);
    let header = read_range(&original, parsed.network_offset, parsed.ip_header_length)
        .expect("complete IPv4 header");
    let identifier = !fold_checksum(sum_words(header));
    original[identifier_offset..identifier_offset + 2].copy_from_slice(&identifier.to_be_bytes());
    for checksum in [0_u16, u16::MAX] {
        original[checksum_offset..checksum_offset + 2].copy_from_slice(&checksum.to_be_bytes());
        let parsed = decode(&original).expect("wire-zero IPv4 headers");
        let direction = identity_direction(&parsed);
        assert!(eligible(&original, &parsed, &direction));
        let mut prepared = original.clone();
        forward(&mut prepared, &parsed, &direction).expect("prepare wire-zero TX frame");
        restore_forwarded(&mut prepared, &parsed, &direction).expect("restore wire-zero TX frame");
        assert_eq!(prepared, original);
    }
}

fn frames(bytes: &[u8]) -> Vec<&[u8]> {
    let little_endian = match bytes.get(..4).expect("PCAP magic") {
        [0xd4, 0xc3, 0xb2, 0xa1] | [0x4d, 0x3c, 0xb2, 0xa1] => true,
        [0xa1, 0xb2, 0xc3, 0xd4] | [0xa1, 0xb2, 0x3c, 0x4d] => false,
        _ => panic!("fixture must use classic PCAP"),
    };
    let mut offset = 24_usize;
    let mut packets = Vec::new();
    while offset < bytes.len() {
        let length_bytes = read_array::<4>(bytes, offset + 8).expect("PCAP captured length");
        let length = if little_endian {
            u32::from_le_bytes(length_bytes)
        } else {
            u32::from_be_bytes(length_bytes)
        };
        offset = offset.checked_add(16).expect("PCAP record offset");
        let end = offset
            .checked_add(usize::try_from(length).expect("PCAP length"))
            .expect("PCAP frame end");
        packets.push(
            bytes
                .get(offset..end)
                .expect("complete captured PCAP frame"),
        );
        offset = end;
    }
    packets
}

fn owned_frames(bytes: &[u8]) -> Vec<Vec<u8>> {
    frames(bytes).into_iter().map(<[u8]>::to_vec).collect()
}

fn direction(frame: &[u8], case: &Corpus) -> MwanNativeDirection {
    let parsed = decode(frame).expect("fixture headers");
    let mut direction = identity_direction(&parsed);
    direction.output_mtu = case.mtu;
    direction.original_source_mac = SOURCE_MAC;
    direction.original_destination_mac = RECEIVER_MAC;
    direction.output_source_mac = OUTPUT_MAC;
    direction.output_destination_mac = CLIENT_MAC;
    if u32::from(parsed.tuple.family) == MWAN_NATIVE_FAMILY_IPV4 {
        direction
            .translated
            .source
            .get_mut(..4)
            .expect("IPv4 source")
            .copy_from_slice(&[198, 18, 0, 21]);
        direction.translated.source_port = 25401;
    } else {
        if let Some(address) = case.source {
            direction.translated.source = address
                .parse::<Ipv6Addr>()
                .expect("translated source")
                .octets();
        }
        if let Some(address) = case.destination {
            direction.translated.destination = address
                .parse::<Ipv6Addr>()
                .expect("translated destination")
                .octets();
        }
    }
    direction
}

fn identity_direction(parsed: &ParsedPacket) -> MwanNativeDirection {
    MwanNativeDirection {
        original: parsed.tuple,
        translated: parsed.tuple,
        original_source_mac: parsed.source_mac,
        original_destination_mac: parsed.destination_mac,
        output_source_mac: parsed.source_mac,
        output_destination_mac: parsed.destination_mac,
        output_mtu: u16::MAX,
        ..MwanNativeDirection::default()
    }
}

fn hardware_translation(original: &[u8], direction: &MwanNativeDirection) -> Vec<u8> {
    let parsed = decode(original).expect("hardware input headers");
    let mut translated = original.to_vec();
    if u32::from(parsed.tuple.family) == MWAN_NATIVE_FAMILY_IPV6 {
        translated
            .get_mut(22..38)
            .expect("IPv6 source")
            .copy_from_slice(&direction.translated.source);
        translated
            .get_mut(38..54)
            .expect("IPv6 destination")
            .copy_from_slice(&direction.translated.destination);
        return translated;
    }
    let ip_before = original.get(26..34).expect("IPv4 addresses");
    let mut ip_after = Vec::new();
    ip_after.extend_from_slice(
        direction
            .translated
            .source
            .get(..4)
            .expect("translated IPv4 source"),
    );
    ip_after.extend_from_slice(
        direction
            .translated
            .destination
            .get(..4)
            .expect("translated IPv4 destination"),
    );
    let mut tcp_after = Vec::new();
    tcp_after.extend_from_slice(&ip_after);
    tcp_after.extend_from_slice(&direction.translated.source_port.to_be_bytes());
    tcp_after.extend_from_slice(&direction.translated.destination_port.to_be_bytes());
    let mut tcp_before = ip_before.to_vec();
    tcp_before.extend_from_slice(original.get(34..38).expect("original TCP ports"));
    let ip_checksum = reference_delta(
        read_u16(original, 24).expect("IPv4 checksum"),
        ip_before,
        &ip_after,
    );
    let tcp_checksum = reference_delta(
        read_u16(original, 50).expect("TCP checksum"),
        &tcp_before,
        &tcp_after,
    );
    translated
        .get_mut(24..26)
        .expect("translated IPv4 checksum")
        .copy_from_slice(&ip_checksum.to_be_bytes());
    translated
        .get_mut(26..34)
        .expect("translated IPv4 addresses")
        .copy_from_slice(&ip_after);
    translated
        .get_mut(34..38)
        .expect("translated TCP ports")
        .copy_from_slice(tcp_after.get(8..12).expect("translated ports"));
    translated
        .get_mut(50..52)
        .expect("translated TCP checksum")
        .copy_from_slice(&tcp_checksum.to_be_bytes());
    translated
}

fn reference_delta(checksum: u16, before: &[u8], after: &[u8]) -> u16 {
    let mut sum = u32::from(checksum);
    for (old, new) in before.chunks_exact(2).zip(after.chunks_exact(2)) {
        let old: [u8; 2] = old.try_into().expect("old checksum word");
        let new: [u8; 2] = new.try_into().expect("new checksum word");
        sum += u32::from(u16::from_be_bytes(old));
        sum += u32::from(!u16::from_be_bytes(new));
        sum = (sum & 0xffff) + (sum >> 16);
    }
    while sum > 0xffff {
        sum = (sum & 0xffff) + (sum >> 16);
    }
    sum as u16
}

fn recompute_tcp6_checksum(frame: &mut [u8]) {
    frame.get_mut(70..72).expect("TCP checksum").fill(0);
    let mut pseudo = frame.get(22..54).expect("IPv6 addresses").to_vec();
    let length = u32::try_from(frame.len() - 54).expect("transport length");
    pseudo.extend_from_slice(&length.to_be_bytes());
    pseudo.extend_from_slice(&[0, 0, 0, 6]);
    pseudo.extend_from_slice(frame.get(54..).expect("TCP segment"));
    let checksum = !fold_checksum(sum_words(&pseudo));
    frame
        .get_mut(70..72)
        .expect("TCP checksum")
        .copy_from_slice(&checksum.to_be_bytes());
}

fn extension_chain(original: &[u8], count: usize) -> Vec<u8> {
    let mut frame = original
        .get(..54)
        .expect("Ethernet and IPv6 headers")
        .to_vec();
    *frame.get_mut(20).expect("IPv6 next header") = libc::IPPROTO_HOPOPTS as u8;
    let payload_length = original.len() - 54 + count * 8;
    frame
        .get_mut(18..20)
        .expect("IPv6 payload length")
        .copy_from_slice(
            &u16::try_from(payload_length)
                .expect("bounded payload")
                .to_be_bytes(),
        );
    for index in 0..count {
        let next = if index + 1 == count { 17 } else { 0 };
        frame.extend_from_slice(&[next, 0, 0, 0, 0, 0, 0, 0]);
    }
    frame.extend_from_slice(original.get(54..).expect("UDP segment"));
    frame
}

#[test]
fn shared_ipv6_restoration_preserves_complete_frames_for_each_profile() {
    for corpus in [
        include_bytes!("../tests/packet/tcp-both-probes-input.pcap").as_slice(),
        include_bytes!("../tests/packet/udp-both-probes-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/tcp6-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/udp6-input.pcap").as_slice(),
    ] {
        for field in [
            MWAN_NATIVE_IPV6_RESTORE_SOURCE,
            MWAN_NATIVE_IPV6_RESTORE_DESTINATION,
        ] {
            for frame in frames(corpus) {
                let mut original = frame.to_vec();
                original.extend_from_slice(&[0xa5; 17]);
                let headers = decode(&original).expect("complete captured IPv6 headers");
                original[headers.network_offset..headers.network_offset + 4]
                    .copy_from_slice(&[0x6b, 0x37, 0x12, 0x34]);
                if headers.ports {
                    let checksum_field =
                        if u32::from(headers.tuple.protocol) == MWAN_NATIVE_PROTOCOL_TCP {
                            16
                        } else {
                            6
                        };
                    original[headers.transport_offset + checksum_field] ^= 0x5a;
                }
                let view = decode(&original).expect("modified captured IPv6 headers");
                if !view.ports && !view.fragment {
                    continue;
                }
                let mut direction = identity_direction(&view);
                let (relative, address) = if field == MWAN_NATIVE_IPV6_RESTORE_SOURCE {
                    (8, &mut direction.translated.source)
                } else {
                    (24, &mut direction.translated.destination)
                };
                // Swap complete words to preserve the address checksum sum.
                address.swap(4, 6);
                address.swap(5, 7);
                let mut received = original.clone();
                let offset = view.network_offset + relative;
                received[offset..offset + 16].copy_from_slice(address);
                let translated = decode(&received).expect("translated captured IPv6 headers");
                restore_ipv6_field(&mut received, &translated, field, &direction)
                    .expect("restore selected address without changing other bytes");
                assert_eq!(received, original);
            }
        }
    }
}

#[test]
fn shared_ipv6_restoration_rejects_stale_bytes_without_partial_edits() {
    let input = frames(include_bytes!("../tests/packet/tcp-source-input.pcap"));
    let original = input.first().expect("captured TCP IPv6 packet");
    let parsed = decode(original).expect("captured IPv6 headers");
    let direction = identity_direction(&parsed);
    for offset in [0, ETHERNET_ADDRESS_LENGTH, parsed.network_offset + 8] {
        let mut changed = original.to_vec();
        changed[offset] ^= 2;
        let before = changed.clone();
        assert!(
            restore_ipv6_field(
                &mut changed,
                &parsed,
                MWAN_NATIVE_IPV6_RESTORE_SOURCE,
                &direction
            )
            .is_err()
        );
        assert_eq!(changed, before);
    }
}
