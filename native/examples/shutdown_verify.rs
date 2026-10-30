use mwan_native::abi::{MWAN_NATIVE_ABI_VERSION, MwanNativeDirection, MwanNativeTuple};
use mwan_native::packet;
use std::collections::{BTreeMap, BTreeSet};
use std::error::Error;
use std::fs;
use std::io::{self, Write};
use std::net::Ipv4Addr;
use std::process::{Command, ExitCode, Stdio};

const ETHERNET: usize = 14;
const IPV4: usize = 20;
const UDP: usize = 8;
const UDP_OFFSET: usize = ETHERNET + IPV4;
const PAYLOAD_OFFSET: usize = UDP_OFFSET + UDP;
const ID_OFFSET: usize = PAYLOAD_OFFSET + 8;
type VerifyResult<T> = Result<T, Box<dyn Error>>;

fn require(condition: bool, message: &str) -> VerifyResult<()> {
    if condition {
        Ok(())
    } else {
        Err(io::Error::other(message).into())
    }
}
fn bytes<const LENGTH: usize>(source: &[u8], offset: usize) -> VerifyResult<[u8; LENGTH]> {
    source
        .get(offset..offset + LENGTH)
        .and_then(|value| value.try_into().ok())
        .ok_or_else(|| io::Error::other(format!("Incomplete record at byte {offset}")).into())
}
fn word(source: &[u8], offset: usize) -> VerifyResult<u16> {
    Ok(u16::from_be_bytes(bytes(source, offset)?))
}
fn sha256(source: &[u8]) -> VerifyResult<String> {
    let mut child = Command::new("sha256sum")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()?;
    child
        .stdin
        .take()
        .ok_or_else(|| io::Error::other("SHA256 input is unavailable"))?
        .write_all(source)?;
    let output = child.wait_with_output()?;
    require(output.status.success(), "sha256sum failed")?;
    let text = String::from_utf8(output.stdout)?;
    let hash = text
        .split_whitespace()
        .next()
        .ok_or_else(|| io::Error::other("SHA256 output is empty"))?;
    require(
        hash.len() == 64 && hash.bytes().all(|value| value.is_ascii_hexdigit()),
        "SHA256 output is invalid",
    )?;
    Ok(hash.to_owned())
}
fn read(path: &str) -> VerifyResult<Vec<u8>> {
    let source = fs::read(path)?;
    println!("FILE_SHA256 {} {}", sha256(&source)?, path);
    Ok(source)
}
fn pcap(path: &str) -> VerifyResult<Vec<Vec<u8>>> {
    let source = read(path)?;
    let little = match bytes::<4>(&source, 0)? {
        [0xd4, 0xc3, 0xb2, 0xa1] | [0x4d, 0x3c, 0xb2, 0xa1] => true,
        [0xa1, 0xb2, 0xc3, 0xd4] | [0xa1, 0xb2, 0x3c, 0x4d] => false,
        _ => return Err(io::Error::other(format!("{path}: classic PCAP is required")).into()),
    };
    let number = |offset| -> VerifyResult<u32> {
        let value = bytes(&source, offset)?;
        Ok(if little {
            u32::from_le_bytes(value)
        } else {
            u32::from_be_bytes(value)
        })
    };
    let version = bytes::<4>(&source, 4)?;
    require(
        version == if little { [2, 0, 4, 0] } else { [0, 2, 0, 4] },
        "PCAP version must be 2.4",
    )?;
    let snaplen = number(16)?;
    require(
        snaplen > 0 && number(20)? == 1,
        "PCAP must contain Ethernet without FCS metadata",
    )?;
    let mut offset = 24;
    let mut packets = Vec::new();
    while offset < source.len() {
        let captured = number(offset + 8)?;
        let original = number(offset + 12)?;
        require(
            captured == original && captured > 0 && captured <= snaplen,
            &format!("{path}: incomplete or invalid capture at {offset}"),
        )?;
        offset += 16;
        let end = offset
            .checked_add(captured as usize)
            .ok_or_else(|| io::Error::other("PCAP record length overflow"))?;
        let frame = source.get(offset..end).ok_or_else(|| {
            io::Error::other(format!("{path}: truncated PCAP record at {offset}"))
        })?;
        packets.push(frame.to_vec());
        offset = end;
    }
    require(
        offset == source.len(),
        "PCAP contains a partial trailing record",
    )?;
    Ok(packets)
}
fn checksum_sum(source: &[u8]) -> u32 {
    let mut sum = 0_u32;
    let mut chunks = source.chunks_exact(2);
    for word in &mut chunks {
        sum += u32::from(u16::from_be_bytes([word[0], word[1]]));
    }
    if let Some(byte) = chunks.remainder().first() {
        sum += u32::from(*byte) * 256;
    }
    sum
}
fn fold(mut sum: u32) -> u16 {
    while sum > u32::from(u16::MAX) {
        sum = (sum & u32::from(u16::MAX)) + sum / 65536;
    }
    sum as u16
}
fn transport_sum(frame: &[u8]) -> u32 {
    checksum_sum(&frame[ETHERNET + 12..ETHERNET + 20])
        + 17
        + (frame.len() - UDP_OFFSET) as u32
        + checksum_sum(&frame[UDP_OFFSET..])
}
fn packet_id(frame: &[u8]) -> VerifyResult<u32> {
    require(
        frame.len() >= ID_OFFSET + 4,
        "Frame does not contain the fixture payload ID",
    )?;
    require(
        word(frame, 12)? == 0x0800 && frame[ETHERNET] == 0x45 && frame[ETHERNET + 9] == 17,
        "Verifier requires untagged IPv4 UDP fixtures without options",
    )?;
    require(
        word(frame, ETHERNET + 6)? & 0x3fff == 0,
        "Fragmented fixture packet is unsupported",
    )?;
    require(
        usize::from(word(frame, ETHERNET + 2)?) + ETHERNET == frame.len(),
        "IPv4 length does not match the complete Ethernet record",
    )?;
    require(
        usize::from(word(frame, UDP_OFFSET + 4)?) == frame.len() - UDP_OFFSET,
        "UDP length does not match the IPv4 payload",
    )?;
    require(
        fold(checksum_sum(&frame[ETHERNET..UDP_OFFSET])) == u16::MAX,
        "IPv4 checksum is invalid",
    )?;
    require(
        word(frame, UDP_OFFSET + 6)? != 0 && fold(transport_sum(frame)) == u16::MAX,
        "UDP checksum is absent or invalid",
    )?;
    let decoded = packet::decode(frame).map_err(|error| {
        io::Error::other(format!("Production decoder rejected frame: {}", error.code))
    })?;
    require(
        !decoded.malformed(),
        "Production decoder detected malformed headers",
    )?;
    let id = u32::from_be_bytes(bytes(frame, ID_OFFSET)?);
    require(id != 0, "Fixture payload ID must be positive")?;
    Ok(id)
}
fn mac(value: &str) -> VerifyResult<[u8; 6]> {
    let parts: Vec<&str> = value.split(':').collect();
    require(parts.len() == 6, "Fixture MAC requires six octets")?;
    let mut result = [0; 6];
    for (byte, part) in result.iter_mut().zip(parts) {
        *byte = u8::from_str_radix(part, 16)?;
    }
    Ok(result)
}
fn address(value: &str) -> VerifyResult<[u8; 16]> {
    let mut result = [0; 16];
    result[..4].copy_from_slice(&value.parse::<Ipv4Addr>()?.octets());
    Ok(result)
}
fn direction(path: &str) -> VerifyResult<MwanNativeDirection> {
    let source = String::from_utf8(read(path)?)?;
    let fields: Vec<&str> = source.split_whitespace().collect();
    require(
        fields.len() == 22,
        "Fixture pair requires exactly one direction",
    )?;
    require(
        fields[0] == "MWAN_NATIVE_PAIR"
            && fields[1].parse::<u32>()? == MWAN_NATIVE_ABI_VERSION
            && matches!(fields[2], "1" | "2")
            && fields[7] == "4"
            && fields[8] == "17",
        "Verifier requires the current single-direction IPv4 UDP pair format",
    )?;
    Ok(MwanNativeDirection {
        origin_id: fields[5].parse()?,
        egress_data_id: fields[6].parse()?,
        original: MwanNativeTuple {
            family: 4,
            protocol: 17,
            source: address(fields[9])?,
            destination: address(fields[10])?,
            source_port: fields[11].parse()?,
            destination_port: fields[12].parse()?,
            ..MwanNativeTuple::default()
        },
        translated: MwanNativeTuple {
            family: 4,
            protocol: 17,
            source: address(fields[13])?,
            destination: address(fields[14])?,
            source_port: fields[15].parse()?,
            destination_port: fields[16].parse()?,
            ..MwanNativeTuple::default()
        },
        original_source_mac: mac(fields[17])?,
        original_destination_mac: mac(fields[18])?,
        output_source_mac: mac(fields[19])?,
        output_destination_mac: mac(fields[20])?,
        output_mtu: fields[21].parse()?,
        ..MwanNativeDirection::default()
    })
}
fn translated(original: &[u8], mapping: &MwanNativeDirection) -> VerifyResult<Vec<u8>> {
    let mut frame = original.to_vec();
    require(
        frame[ETHERNET + 8] > 1,
        "Fixture TTL cannot support forwarding",
    )?;
    require(
        frame.len() - ETHERNET <= usize::from(mapping.output_mtu),
        "Fixture exceeds the declared output MTU",
    )?;
    frame[..6].copy_from_slice(&mapping.output_destination_mac);
    frame[6..12].copy_from_slice(&mapping.output_source_mac);
    frame[ETHERNET + 8] -= 1;
    frame[ETHERNET + 12..ETHERNET + 16].copy_from_slice(&mapping.translated.source[..4]);
    frame[ETHERNET + 16..ETHERNET + 20].copy_from_slice(&mapping.translated.destination[..4]);
    frame[UDP_OFFSET..UDP_OFFSET + 2]
        .copy_from_slice(&mapping.translated.source_port.to_be_bytes());
    frame[UDP_OFFSET + 2..UDP_OFFSET + 4]
        .copy_from_slice(&mapping.translated.destination_port.to_be_bytes());
    frame[ETHERNET + 10..ETHERNET + 12].fill(0);
    let ip_checksum = !fold(checksum_sum(&frame[ETHERNET..UDP_OFFSET]));
    frame[ETHERNET + 10..ETHERNET + 12].copy_from_slice(&ip_checksum.to_be_bytes());
    frame[UDP_OFFSET + 6..UDP_OFFSET + 8].fill(0);
    let udp_checksum = !fold(transport_sum(&frame));
    let udp_checksum = if udp_checksum == 0 {
        u16::MAX
    } else {
        udp_checksum
    };
    frame[UDP_OFFSET + 6..UDP_OFFSET + 8].copy_from_slice(&udp_checksum.to_be_bytes());
    packet_id(&frame)?;
    Ok(frame)
}
struct Expected {
    original: Vec<u8>,
    translated: Vec<u8>,
}
fn inputs(
    paths: &[String],
    mappings: &[MwanNativeDirection],
) -> VerifyResult<BTreeMap<u32, Expected>> {
    let mut expected = BTreeMap::new();
    for path in paths {
        let frames = pcap(path)?;
        require(
            !frames.is_empty(),
            &format!("{path}: frozen input is empty"),
        )?;
        for original in frames {
            let id = packet_id(&original)?;
            let parsed = packet::decode(&original)
                .map_err(|error| io::Error::other(format!("Input decoder error {}", error.code)))?;
            let candidates: Vec<&MwanNativeDirection> = mappings
                .iter()
                .filter(|mapping| {
                    mapping.original == parsed.tuple()
                        && mapping.original_source_mac == parsed.source_mac()
                        && mapping.original_destination_mac == parsed.destination_mac()
                })
                .collect();
            require(
                candidates.len() == 1,
                &format!("{path}: ID {id} does not identify exactly one original mapping"),
            )?;
            let rewritten = translated(&original, candidates[0])?;
            require(
                expected
                    .insert(
                        id,
                        Expected {
                            original,
                            translated: rewritten,
                        },
                    )
                    .is_none(),
                &format!("Frozen input files repeat ID {id}"),
            )?;
        }
    }
    Ok(expected)
}
fn capture(
    path: &str,
    expected: &BTreeMap<u32, Expected>,
    receiver: bool,
) -> VerifyResult<BTreeSet<u32>> {
    let mut ids = BTreeSet::new();
    for frame in pcap(path)? {
        let id = packet_id(&frame)?;
        let input = expected.get(&id).ok_or_else(|| {
            io::Error::other(format!("{path}: ID {id} is absent from frozen inputs"))
        })?;
        let wanted = if receiver {
            &input.translated
        } else {
            &input.original
        };
        require(
            &frame == wanted,
            &format!("{path}: ID {id} differs from its exact expected frame"),
        )?;
        require(
            ids.insert(id),
            &format!("{path}: ID {id} occurs more than once"),
        )?;
    }
    Ok(ids)
}
fn run(arguments: &[String]) -> VerifyResult<()> {
    let mappings = [direction(&arguments[1])?, direction(&arguments[2])?];
    require(
        mappings[0].original.destination != mappings[1].original.destination
            && mappings[0].translated == mappings[1].translated,
        "Fixture mappings must reuse one translated tuple for different original destinations",
    )?;
    let expected = inputs(&arguments[6..], &mappings)?;
    let sender = capture(&arguments[3], &expected, false)?;
    let kernel = capture(&arguments[4], &expected, false)?;
    let receiver = capture(&arguments[5], &expected, true)?;
    require(!sender.is_empty(), "Sender capture is empty")?;
    for id in &sender {
        require(
            kernel.contains(id) != receiver.contains(id),
            &format!("Sender ID {id} does not occur exactly once at one output"),
        )?;
    }
    for id in kernel.union(&receiver) {
        require(
            sender.contains(id),
            &format!("Output ID {id} does not occur at the sender"),
        )?;
    }
    println!("CAPTURE_IDS role=sender ids={sender:?}");
    println!("CAPTURE_IDS role=kernel ids={kernel:?}");
    println!("CAPTURE_IDS role=receiver ids={receiver:?}");
    println!(
        "CAPTURE_PROOF frozen={} sent={} kernel={} receiver={} unsent={} exact_bytes=1 checksums=1 unique_ids=1 complete_records=1",
        expected.len(),
        sender.len(),
        kernel.len(),
        receiver.len(),
        expected.len() - sender.len()
    );
    Ok(())
}
fn main() -> ExitCode {
    let arguments: Vec<String> = std::env::args().collect();
    if arguments.len() < 7 {
        eprintln!(
            "Usage: shutdown_verify FIRST_PAIR REUSED_PAIR SENDER_PCAP KERNEL_PCAP RECEIVER_PCAP INPUT_PCAP..."
        );
        return ExitCode::FAILURE;
    }
    match run(&arguments) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("Capture verification failed: {error}");
            ExitCode::FAILURE
        }
    }
}
