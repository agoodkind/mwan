use super::*;
use crate::abi::{MwanNativeDirection, MwanNativeKernelMetadata};

const IPV4: &[u8] = include_bytes!("../tests/malformed/tcp4-input.pcap");
const IPV6: &[u8] = include_bytes!("../tests/packet/tcp-source-input.pcap");

fn first_frame(capture: &[u8]) -> &[u8] {
    let length = u32::from_le_bytes(capture[32..36].try_into().expect("PCAP length"));
    &capture[40..40 + length as usize]
}

fn metadata(frame: &[u8]) -> MwanNativeKernelMetadata {
    MwanNativeKernelMetadata {
        auxiliary_status: 1,
        packet_length: frame.len() as u32,
        captured_length: frame.len() as u32,
        network_offset: 14,
        ..MwanNativeKernelMetadata::default()
    }
}

fn descriptor(frame: &[u8], metadata: MwanNativeKernelMetadata) -> MwanNativeKernelFrame {
    MwanNativeKernelFrame {
        frame: MwanNativeFrame {
            data: frame.as_ptr(),
            length: frame.len() as u32,
            reserved: 0,
        },
        metadata,
    }
}

fn observe(frame: &[u8], metadata: MwanNativeKernelMetadata) -> MwanNativeKernelObservation {
    let before = frame.to_vec();
    let input = descriptor(frame, metadata);
    let original = input;
    let mut output = MwanNativeKernelObservation::default();
    // SAFETY: The live input and output records do not overlap. The descriptor
    // borrows the complete immutable buffer for the synchronous call.
    let status = unsafe { mwan_native_observe_kernel_batch(&input, &mut output, 1) };
    assert_eq!(status, 0);
    assert_eq!(input, original);
    assert_eq!(frame, before);
    assert_eq!(output.reserved, 0);
    output
}

fn repair_ipv4(frame: &mut [u8]) {
    if frame[14] >> 4 != 4 {
        return;
    }
    frame[24..26].fill(0);
    let mut sum: u32 = frame[14..34]
        .chunks_exact(2)
        .map(|word| u32::from(u16::from_be_bytes([word[0], word[1]])))
        .sum();
    while sum > u32::from(u16::MAX) {
        sum = (sum & u32::from(u16::MAX)) + (sum >> 16);
    }
    frame[24..26].copy_from_slice(&(!(sum as u16)).to_be_bytes());
}

fn aggregate(capture: &[u8], network_length: usize) -> (Vec<u8>, MwanNativeKernelMetadata) {
    let mut frame = first_frame(capture).to_vec();
    let view = packet::decode(&frame).expect("captured TCP headers");
    frame.resize(14 + network_length, 0x5a);
    if view.tuple.family == 4 {
        frame[16..18].copy_from_slice(&(network_length as u16).to_be_bytes());
        repair_ipv4(&mut frame);
    } else {
        frame[18..20].copy_from_slice(&((network_length - 40) as u16).to_be_bytes());
    }
    frame[view.transport_offset + 13] = 24;
    frame[view.transport_offset + 16..view.transport_offset + 18].fill(0);
    let transport_header = usize::from(frame[view.transport_offset + 12] >> 4) * 4;
    let mut metadata = metadata(&frame);
    metadata.auxiliary_status = 265;
    metadata.vnet_flags = 1;
    metadata.gso_type = if view.tuple.family == 4 { 1 } else { 4 };
    metadata.header_length = (view.transport_offset + transport_header + 8) as u16;
    metadata.gso_size = 1448;
    metadata.checksum_start = view.transport_offset as u16;
    metadata.checksum_offset = 16;
    (frame, metadata)
}

#[test]
fn kernel_batch_observes_tcp_aggregates_without_weakening_wire_validation() {
    for capture in [IPV4, IPV6] {
        let base = packet::observe(first_frame(capture));
        for network_length in [21772, 23220, 65212] {
            let (mut frame, metadata) = aggregate(capture, network_length);
            let parsed = packet::decode(&frame).expect("aggregate headers");
            let direction = MwanNativeDirection {
                output_mtu: u16::MAX,
                ..Default::default()
            };
            if packet::eligible(&frame, &parsed, &direction) {
                frame[parsed.transport_offset + 17] ^= 1;
            }
            let result = observe(&frame, metadata);
            let expected = MwanNativeObservation {
                network_length: network_length as u32,
                tcp_flags: 24,
                ..base
            };
            assert_eq!(result.observation, expected);
            let tcp_header = usize::from(frame[parsed.transport_offset + 12] >> 4) * 4;
            assert_eq!(
                result.effective_network_length as usize,
                parsed.ip_header_length + tcp_header + 1448
            );
            assert!(!packet::eligible(&frame, &parsed, &direction));
            let input = descriptor(&frame, metadata).frame;
            let mut old = MwanNativeObservation::default();
            // SAFETY: The descriptor and writable output are separate live
            // records, and frame remains readable throughout the call.
            assert_eq!(unsafe { mwan_native_observe_batch(&input, &mut old, 1) }, 0);
            assert_eq!(old.eligible, 0);
            for vnet_flags in [0, 2] {
                let valid = MwanNativeKernelMetadata {
                    auxiliary_status: 385,
                    vnet_flags,
                    checksum_start: 0,
                    checksum_offset: 0,
                    ..metadata
                };
                assert_eq!(observe(&frame, valid), result);
                let ecn = MwanNativeKernelMetadata {
                    gso_type: valid.gso_type | 128,
                    ..valid
                };
                assert_eq!(observe(&frame, ecn), result);
            }
        }
    }
}

#[test]
fn kernel_batch_preserves_ordinary_tcp_udp_checksums_and_padding() {
    for capture in [
        IPV4,
        IPV6,
        include_bytes!("../tests/packet/ipv4-udp-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/udp6-input.pcap").as_slice(),
    ] {
        let frame = first_frame(capture);
        let wire = packet::observe(frame);
        assert_eq!(wire.eligible, 1);
        let observed = observe(frame, metadata(frame));
        assert_eq!(observed.observation, wire);
        assert_eq!(observed.effective_network_length, wire.network_length);
        let mut padded = frame.to_vec();
        padded.extend_from_slice(&[0; 8]);
        assert_eq!(observe(&padded, metadata(&padded)), observed);
        let view = packet::decode(frame).expect("ordinary headers");
        let mut damaged = frame.to_vec();
        *damaged.last_mut().expect("transport payload") ^= 1;
        let omitted_udp_checksum = wire.tuple.family == 4
            && wire.tuple.protocol == 17
            && frame[view.transport_offset + 6..view.transport_offset + 8] == [0, 0];
        assert_eq!(
            observe(&damaged, metadata(&damaged)).observation.eligible,
            u8::from(omitted_udp_checksum)
        );

        let field = if wire.tuple.protocol == 6 { 16 } else { 6 };
        let mut pending = frame.to_vec();
        pending[view.transport_offset + field..view.transport_offset + field + 2].fill(0);
        let deferred = MwanNativeKernelMetadata {
            auxiliary_status: 9,
            vnet_flags: 1,
            checksum_start: view.transport_offset as u16,
            checksum_offset: field as u16,
            ..metadata(&pending)
        };
        assert_eq!(observe(&pending, deferred).observation, wire);
        if wire.tuple.protocol == 17 {
            let zero = observe(&pending, metadata(&pending));
            assert_eq!(zero.observation.eligible, u8::from(wire.tuple.family == 4));
            let valid = MwanNativeKernelMetadata {
                auxiliary_status: 129,
                ..metadata(&pending)
            };
            assert_eq!(
                observe(&pending, valid).observation.eligible,
                u8::from(wire.tuple.family == 4)
            );
        }
    }
}

#[test]
fn kernel_batch_derives_tcp_options_and_bounds_short_segments() {
    for capture in [IPV4, IPV6] {
        let (mut frame, mut meta) = aggregate(capture, 400);
        let view = packet::decode(&frame).expect("TCP aggregate headers");
        // Twelve option bytes change the parsed header without changing the
        // independent skb_headlen hint into a transport header length.
        frame[view.transport_offset + 12] = 0x80;
        frame[view.transport_offset + 20..view.transport_offset + 32].fill(1);
        meta.header_length = (view.transport_offset + 32 + 8) as u16;
        assert_eq!(observe(&frame, meta).effective_network_length, 400);
        meta.gso_size = 100;
        assert_eq!(
            observe(&frame, meta).effective_network_length as usize,
            view.ip_header_length + 32 + 100
        );
        for flags in [16, 24, 80, 144, 208] {
            frame[view.transport_offset + 13] = flags;
            let result = observe(&frame, meta);
            assert_eq!(result.observation.eligible, 1);
            assert_eq!(result.observation.tcp_flags, flags);
        }
        meta.header_length = (view.transport_offset + 20) as u16;
        assert_eq!(observe(&frame, meta).effective_network_length, 0);

        let empty_length = view.ip_header_length + 32;
        let (mut empty, mut empty_meta) = aggregate(capture, empty_length);
        empty[view.transport_offset + 12] = 0x80;
        empty_meta.header_length = empty.len() as u16;
        assert_eq!(observe(&empty, empty_meta).effective_network_length, 0);

        let ordinary = first_frame(capture);
        let software = MwanNativeKernelMetadata {
            auxiliary_status: 257,
            gso_type: meta.gso_type,
            gso_size: 1448,
            header_length: ordinary.len() as u16,
            ..metadata(ordinary)
        };
        assert_eq!(
            observe(ordinary, software).observation,
            packet::observe(ordinary)
        );
        let mut corrupt = ordinary.to_vec();
        *corrupt.last_mut().expect("TCP payload") ^= 1;
        assert_eq!(observe(&corrupt, software).effective_network_length, 0);
    }
}

#[test]
fn kernel_batch_rejects_udp_segmentation_and_inconsistent_lengths() {
    for capture in [
        include_bytes!("../tests/packet/ipv4-udp-input.pcap").as_slice(),
        include_bytes!("../tests/malformed/udp6-input.pcap").as_slice(),
    ] {
        let frame = first_frame(capture);
        let view = packet::decode(frame).expect("UDP headers");
        for gso_type in [1, 3, 4, 5] {
            let meta = MwanNativeKernelMetadata {
                auxiliary_status: 265,
                vnet_flags: 1,
                gso_type,
                gso_size: 1200,
                header_length: frame.len() as u16,
                checksum_start: view.transport_offset as u16,
                checksum_offset: 6,
                ..metadata(frame)
            };
            assert_eq!(observe(frame, meta).effective_network_length, 0);
        }
        let mut changed = frame.to_vec();
        changed[view.transport_offset + 5] ^= 1;
        let valid = MwanNativeKernelMetadata {
            auxiliary_status: 129,
            ..metadata(&changed)
        };
        assert_eq!(observe(&changed, valid).effective_network_length, 0);
    }
}

#[test]
fn kernel_batch_rejects_inconsistent_metadata() {
    for capture in [IPV4, IPV6] {
        let (frame, good) = aggregate(capture, 21772);
        let changes: &[fn(&mut MwanNativeKernelMetadata)] = &[
            |m| m.auxiliary_status &= !1,
            |m| m.auxiliary_status |= 16,
            |m| m.auxiliary_status |= 64,
            |m| m.auxiliary_status |= 1 << 31,
            |m| m.auxiliary_status |= 128,
            |m| m.auxiliary_status &= !8,
            |m| m.auxiliary_status &= !256,
            |m| m.packet_length += 1,
            |m| m.captured_length -= 1,
            |m| m.mac_offset = 1,
            |m| m.network_offset = 18,
            |m| m.vnet_flags = 0,
            |m| m.vnet_flags = 3,
            |m| m.vnet_flags |= 4,
            |m| m.gso_type = if m.gso_type == 1 { 4 } else { 1 },
            |m| m.gso_type = 3,
            |m| m.gso_type |= 64,
            |m| m.gso_type = 0,
            |m| m.gso_size = 0,
            |m| m.gso_size = u16::MAX,
            |m| m.header_length = m.checksum_start + 19,
            |m| m.header_length = m.packet_length as u16 + 1,
            |m| m.checksum_start += 1,
            |m| m.checksum_offset = 6,
            |m| m.checksum_start = u16::MAX,
            |m| m.checksum_offset = u16::MAX,
        ];
        for (index, change) in changes.iter().enumerate() {
            let mut invalid = good;
            change(&mut invalid);
            let result = observe(&frame, invalid);
            assert_eq!(result.observation.eligible, 0, "metadata case {index}");
            assert_eq!(result.effective_network_length, 0);
            assert!(result.observation.decode_status < 0);
        }
        let ordinary = first_frame(capture);
        for invalid in [
            MwanNativeKernelMetadata {
                header_length: 1,
                ..metadata(ordinary)
            },
            MwanNativeKernelMetadata {
                gso_size: 1,
                ..metadata(ordinary)
            },
            MwanNativeKernelMetadata {
                auxiliary_status: 257,
                ..metadata(ordinary)
            },
            MwanNativeKernelMetadata {
                vnet_flags: 2,
                ..metadata(ordinary)
            },
            MwanNativeKernelMetadata {
                checksum_start: 34,
                ..metadata(ordinary)
            },
            MwanNativeKernelMetadata {
                checksum_offset: 16,
                ..metadata(ordinary)
            },
        ] {
            assert_eq!(observe(ordinary, invalid).effective_network_length, 0);
        }
    }
}

#[test]
fn kernel_batch_rejects_unsupported_headers_and_truncation() {
    for capture in [IPV4, IPV6] {
        let (frame, good) = aggregate(capture, 21772);
        let view = packet::decode(&frame).expect("aggregate headers");
        let transport = view.transport_offset;
        let mut changes = vec![(transport + 12, 0x40), (transport + 12, 0x51)];
        for flags in [0, 2, 3, 17, 18, 20, 48] {
            changes.push((transport + 13, flags));
        }
        if view.tuple.family == 4 {
            changes.extend([(14, 0x46), (20, 0x20), (22, 1), (24, frame[24] ^ 1)]);
        } else {
            changes.extend([(20, 0), (20, 43), (20, 44), (21, 1)]);
        }
        for (offset, byte) in changes {
            let mut changed = frame.clone();
            changed[offset] = byte;
            if offset != 24 {
                repair_ipv4(&mut changed);
            }
            let result = observe(&changed, good);
            assert_eq!(
                result.observation.eligible, 0,
                "header offset {offset}, byte {byte}"
            );
            assert_eq!(result.effective_network_length, 0);
        }
        for length in [1, 13, 20, transport, transport + 19, frame.len() - 1] {
            let truncated = &frame[..length];
            let adjusted = MwanNativeKernelMetadata {
                packet_length: length as u32,
                captured_length: length as u32,
                ..good
            };
            assert_eq!(observe(truncated, adjusted).effective_network_length, 0);
        }
        let mut tagged = frame[..12].to_vec();
        tagged.extend_from_slice(&[0x81, 0, 0, 1]);
        tagged.extend_from_slice(&frame[12..]);
        let adjusted = MwanNativeKernelMetadata {
            packet_length: tagged.len() as u32,
            captured_length: tagged.len() as u32,
            ..good
        };
        assert_eq!(observe(&tagged, adjusted).effective_network_length, 0);
    }
}

#[test]
fn kernel_batch_checks_all_descriptors_before_writing() {
    let (frame, good) = aggregate(IPV4, 21772);
    let valid = descriptor(&frame, good);
    let changes: &[fn(&mut MwanNativeKernelFrame)] = &[
        |f| f.frame.data = std::ptr::null(),
        |f| f.frame.length = 0,
        |f| f.frame.reserved = 1,
        |f| f.metadata.reserved16 = 1,
        |f| f.metadata.reserved32 = 1,
    ];
    let sentinel = MwanNativeKernelObservation {
        effective_network_length: 123,
        reserved: 456,
        ..Default::default()
    };
    for change in changes {
        let mut frames = [valid; 2];
        change(&mut frames[1]);
        let mut outputs = [sentinel; 2];
        // SAFETY: Both arrays have two live separate records. The descriptors
        // reference the complete buffer unless null, which validation rejects.
        let status =
            unsafe { mwan_native_observe_kernel_batch(frames.as_ptr(), outputs.as_mut_ptr(), 2) };
        assert_eq!(status, -libc::EINVAL);
        assert_eq!(outputs, [sentinel; 2]);
    }
    let oversized = vec![0; MWAN_NATIVE_OBSERVATION_FRAME_LIMIT as usize + 1];
    let input = descriptor(&oversized, metadata(&oversized));
    let mut output = sentinel;
    // SAFETY: The descriptor references all allocated oversized bytes, and
    // output is a separate writable record.
    let status = unsafe { mwan_native_observe_kernel_batch(&input, &mut output, 1) };
    assert_eq!(status, -libc::EINVAL);
    assert_eq!(output, sentinel);
    for count in [0, 257] {
        let inputs = vec![valid; 257];
        let mut outputs = vec![sentinel; 257];
        // SAFETY: Arrays provide 257 live records and separate writable outputs.
        let status = unsafe {
            mwan_native_observe_kernel_batch(inputs.as_ptr(), outputs.as_mut_ptr(), count)
        };
        assert_eq!(status, -libc::EINVAL);
        assert!(outputs.iter().all(|value| *value == sentinel));
    }
    let inputs = vec![valid; 256];
    let mut outputs = vec![sentinel; 256];
    // SAFETY: Separate arrays provide exactly 256 initialized descriptors and
    // writable outputs. Every descriptor borrows the complete frame.
    let status =
        unsafe { mwan_native_observe_kernel_batch(inputs.as_ptr(), outputs.as_mut_ptr(), 256) };
    assert_eq!(status, 0);
    assert!(
        outputs
            .iter()
            .all(|value| value.observation.eligible == 1 && value.reserved == 0)
    );
    let (maximum, metadata) = aggregate(IPV4, 65536 - 14);
    assert_eq!(observe(&maximum, metadata).observation.eligible, 1);
}
