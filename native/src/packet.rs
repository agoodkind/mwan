// SPDX-License-Identifier: BSD-3-Clause

#![forbid(unsafe_code)]

use crate::abi::{
    MWAN_NATIVE_FAMILY_IPV4, MWAN_NATIVE_FAMILY_IPV6, MWAN_NATIVE_PACKET_ETHERNET_VALID,
    MWAN_NATIVE_PACKET_IP_VALID, MWAN_NATIVE_PACKET_PORTS_VALID, MWAN_NATIVE_PROTOCOL_TCP,
    MWAN_NATIVE_PROTOCOL_UDP, MwanNativeDirection, MwanNativeObservation,
    MwanNativePacketDiagnostic, MwanNativeTuple,
};
use std::fmt;

const ETHERNET_HEADER_LENGTH: usize = 14;
const ETHERNET_ADDRESS_LENGTH: usize = 6;
const VLAN_HEADER_LENGTH: usize = 4;
const MAX_VLAN_HEADERS: usize = 2;
const IPV4_HEADER_LENGTH: usize = 20;
const IPV6_HEADER_LENGTH: usize = 40;
const TCP_HEADER_LENGTH: usize = 20;
const UDP_HEADER_LENGTH: usize = 8;
const MAX_IPV6_EXTENSION_HEADERS: usize = 8;
const IPV6_EXTENSION_MINIMUM_LENGTH: usize = 8;
const IPV4_ETHERTYPE: u16 = 0x0800;
const IPV6_ETHERTYPE: u16 = 0x86dd;
const VLAN_ETHERTYPE: u16 = 0x8100;
const QINQ_ETHERTYPE: u16 = 0x88a8;
const IPV4_FRAGMENT_MASK: u16 = 0x3fff;
const IPV4_FRAGMENT_OFFSET_MASK: u16 = 0x1fff;
const IPV6_FRAGMENT_OFFSET_MASK: u16 = 0xfff8;
const TCP_ACK_FLAG: u8 = 0x10;
const TCP_EXCEPTION_FLAGS: u8 = 0x27;
const CHECKSUM_WORD_MASK: u64 = u16::MAX as u64;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ParsedPacket {
    pub(crate) tuple: MwanNativeTuple,
    pub(crate) network_offset: usize,
    pub(crate) ip_header_length: usize,
    pub(crate) transport_offset: usize,
    pub(crate) transport_length: usize,
    pub(crate) network_length: usize,
    pub(crate) ttl: u8,
    pub(crate) flags: u8,
    pub(crate) traffic_class: u8,
    pub(crate) flow_label: u32,
    pub(crate) fragment: bool,
    pub(crate) noninitial_fragment: bool,
    pub(crate) extended: bool,
    pub(crate) ports: bool,
    pub(crate) malformed: bool,
    pub(crate) ether_type: u16,
    pub(crate) source_mac: [u8; ETHERNET_ADDRESS_LENGTH],
    pub(crate) destination_mac: [u8; ETHERNET_ADDRESS_LENGTH],
    ipv4_checksum: u16,
    frame_length: usize,
    ethernet_valid: bool,
}

impl ParsedPacket {
    pub fn tuple(&self) -> MwanNativeTuple {
        self.tuple
    }

    pub fn source_mac(&self) -> [u8; ETHERNET_ADDRESS_LENGTH] {
        self.source_mac
    }

    pub fn destination_mac(&self) -> [u8; ETHERNET_ADDRESS_LENGTH] {
        self.destination_mac
    }

    pub fn malformed(&self) -> bool {
        self.malformed
    }

    fn error(&self, code: i32) -> DecodeError {
        let mut packet = diagnostic(self);
        packet.decode_status = -code;
        DecodeError {
            code,
            diagnostic: packet,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DecodeError {
    pub code: i32,
    pub diagnostic: MwanNativePacketDiagnostic,
}

impl fmt::Display for DecodeError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "packet decode or edit failed with errno {}",
            self.code
        )
    }
}

impl std::error::Error for DecodeError {}

pub fn diagnostic(view: &ParsedPacket) -> MwanNativePacketDiagnostic {
    let mut packet = MwanNativePacketDiagnostic::default();
    if view.ethernet_valid {
        packet.validity |= MWAN_NATIVE_PACKET_ETHERNET_VALID;
        packet.ether_type = view.ether_type;
        packet.source_mac = view.source_mac;
        packet.destination_mac = view.destination_mac;
    }
    if view.tuple.family != 0 {
        packet.validity |= MWAN_NATIVE_PACKET_IP_VALID;
        packet.tuple = view.tuple;
    }
    if view.ports {
        packet.validity |= MWAN_NATIVE_PACKET_PORTS_VALID;
    }
    packet
}

pub fn decode(frame: &[u8]) -> Result<ParsedPacket, DecodeError> {
    let mut view = ParsedPacket {
        frame_length: frame.len(),
        ..ParsedPacket::default()
    };
    decode_headers(frame, &mut view).map_err(|code| view.error(code))?;
    Ok(view)
}

pub fn observe(frame: &[u8]) -> MwanNativeObservation {
    let mut observation = MwanNativeObservation::default();
    let view = match decode(frame) {
        Ok(view) => view,
        Err(error) => {
            observation.tuple = error.diagnostic.tuple;
            observation.source_mac = error.diagnostic.source_mac;
            observation.destination_mac = error.diagnostic.destination_mac;
            observation.decode_status = -error.code;
            return observation;
        }
    };
    observation.tuple = view.tuple;
    observation.source_mac = view.source_mac;
    observation.destination_mac = view.destination_mac;
    observation.network_length = view.network_length as u32;
    observation.flow_label = view.flow_label;
    observation.traffic_class = view.traffic_class;
    observation.tcp_flags = view.flags;
    observation.ttl = view.ttl;
    let direction = MwanNativeDirection {
        output_mtu: u16::MAX,
        ..MwanNativeDirection::default()
    };
    observation.eligible = u8::from(eligible(frame, &view, &direction));
    observation
}

fn decode_headers(frame: &[u8], view: &mut ParsedPacket) -> Result<(), i32> {
    let ethernet = frame.get(..ETHERNET_HEADER_LENGTH).ok_or(libc::EBADMSG)?;
    view.destination_mac = read_array(ethernet, 0).ok_or(libc::EBADMSG)?;
    view.source_mac = read_array(ethernet, ETHERNET_ADDRESS_LENGTH).ok_or(libc::EBADMSG)?;
    view.ether_type = read_u16(ethernet, 12).ok_or(libc::EBADMSG)?;
    view.ethernet_valid = true;
    view.network_offset = ETHERNET_HEADER_LENGTH;
    let mut ether_type = view.ether_type;
    let mut vlan_count = 0;
    while matches!(ether_type, VLAN_ETHERTYPE | QINQ_ETHERTYPE) {
        if vlan_count == MAX_VLAN_HEADERS {
            return Err(libc::EBADMSG);
        }
        let vlan =
            read_range(frame, view.network_offset, VLAN_HEADER_LENGTH).ok_or(libc::EBADMSG)?;
        ether_type = read_u16(vlan, 2).ok_or(libc::EBADMSG)?;
        view.network_offset = view
            .network_offset
            .checked_add(VLAN_HEADER_LENGTH)
            .ok_or(libc::EBADMSG)?;
        view.extended = true;
        vlan_count += 1;
    }
    let network = frame.get(view.network_offset..).ok_or(libc::EBADMSG)?;
    match ether_type {
        IPV4_ETHERTYPE => decode_ipv4(network, view)?,
        IPV6_ETHERTYPE => decode_ipv6(network, view)?,
        _ => return Err(libc::EPROTONOSUPPORT),
    }
    view.transport_offset = view
        .network_offset
        .checked_add(view.ip_header_length)
        .ok_or(libc::EBADMSG)?;
    view.transport_length = view
        .network_length
        .min(network.len())
        .checked_sub(view.ip_header_length)
        .ok_or(libc::EBADMSG)?;
    let transport =
        read_range(frame, view.transport_offset, view.transport_length).ok_or(libc::EBADMSG)?;
    let minimum_transport_length = match u32::from(view.tuple.protocol) {
        MWAN_NATIVE_PROTOCOL_TCP => TCP_HEADER_LENGTH,
        MWAN_NATIVE_PROTOCOL_UDP => UDP_HEADER_LENGTH,
        _ => return Err(libc::EPROTONOSUPPORT),
    };
    if view.malformed && !view.noninitial_fragment && transport.len() < minimum_transport_length {
        return Err(libc::EBADMSG);
    }
    if !view.noninitial_fragment && transport.len() >= 4 {
        view.tuple.source_port = read_u16(transport, 0).ok_or(libc::EBADMSG)?;
        view.tuple.destination_port = read_u16(transport, 2).ok_or(libc::EBADMSG)?;
        view.ports = true;
    }
    if !view.noninitial_fragment
        && u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_TCP
        && transport.len() >= TCP_HEADER_LENGTH
    {
        view.flags = *transport.get(13).ok_or(libc::EBADMSG)?;
    }
    Ok(())
}

fn decode_ipv4(network: &[u8], view: &mut ParsedPacket) -> Result<(), i32> {
    let header = network.get(..IPV4_HEADER_LENGTH).ok_or(libc::EBADMSG)?;
    let version_and_length = *header.first().ok_or(libc::EBADMSG)?;
    if version_and_length >> 4 != 4 {
        return Err(libc::EBADMSG);
    }
    view.ip_header_length = usize::from(version_and_length & 15)
        .checked_mul(4)
        .ok_or(libc::EBADMSG)?;
    view.network_length = usize::from(read_u16(header, 2).ok_or(libc::EBADMSG)?);
    if view.ip_header_length < IPV4_HEADER_LENGTH
        || view.network_length < view.ip_header_length
        || view.ip_header_length > network.len()
    {
        return Err(libc::EBADMSG);
    }
    view.malformed = view.network_length > network.len();
    let fragment = read_u16(header, 6).ok_or(libc::EBADMSG)?;
    view.fragment = fragment & IPV4_FRAGMENT_MASK != 0;
    view.noninitial_fragment = fragment & IPV4_FRAGMENT_OFFSET_MASK != 0;
    view.extended |= view.ip_header_length != IPV4_HEADER_LENGTH;
    view.ttl = *header.get(8).ok_or(libc::EBADMSG)?;
    view.ipv4_checksum = read_u16(header, 10).ok_or(libc::EBADMSG)?;
    view.traffic_class = *header.get(1).ok_or(libc::EBADMSG)?;
    view.tuple.family = MWAN_NATIVE_FAMILY_IPV4 as u8;
    view.tuple.protocol = *header.get(9).ok_or(libc::EBADMSG)?;
    let source: [u8; 4] = read_array(header, 12).ok_or(libc::EBADMSG)?;
    let destination: [u8; 4] = read_array(header, 16).ok_or(libc::EBADMSG)?;
    view.tuple
        .source
        .get_mut(..4)
        .ok_or(libc::EBADMSG)?
        .copy_from_slice(&source);
    view.tuple
        .destination
        .get_mut(..4)
        .ok_or(libc::EBADMSG)?
        .copy_from_slice(&destination);
    Ok(())
}

fn decode_ipv6(network: &[u8], view: &mut ParsedPacket) -> Result<(), i32> {
    let header = network.get(..IPV6_HEADER_LENGTH).ok_or(libc::EBADMSG)?;
    if header.first().ok_or(libc::EBADMSG)? >> 4 != 6 {
        return Err(libc::EBADMSG);
    }
    view.ip_header_length = IPV6_HEADER_LENGTH;
    view.network_length = IPV6_HEADER_LENGTH
        .checked_add(usize::from(read_u16(header, 4).ok_or(libc::EBADMSG)?))
        .ok_or(libc::EBADMSG)?;
    view.malformed = view.network_length > network.len();
    view.ttl = *header.get(7).ok_or(libc::EBADMSG)?;
    view.traffic_class = ((header[0] & 15) * 16) | (header[1] >> 4);
    view.flow_label =
        (u32::from(header[1] & 15) * 65_536) | (u32::from(header[2]) * 256) | u32::from(header[3]);
    view.tuple.family = MWAN_NATIVE_FAMILY_IPV6 as u8;
    view.tuple.protocol = *header.get(6).ok_or(libc::EBADMSG)?;
    view.tuple.source = read_array(header, 8).ok_or(libc::EBADMSG)?;
    view.tuple.destination = read_array(header, 24).ok_or(libc::EBADMSG)?;
    let limit = view.network_length.min(network.len());
    for _ in 0..MAX_IPV6_EXTENSION_HEADERS {
        let protocol = i32::from(view.tuple.protocol);
        if !matches!(
            protocol,
            libc::IPPROTO_HOPOPTS
                | libc::IPPROTO_ROUTING
                | libc::IPPROTO_DSTOPTS
                | libc::IPPROTO_FRAGMENT
                | libc::IPPROTO_AH
        ) {
            break;
        }
        let remaining = limit
            .checked_sub(view.ip_header_length)
            .ok_or(libc::EBADMSG)?;
        if remaining < IPV6_EXTENSION_MINIMUM_LENGTH {
            return Err(libc::EBADMSG);
        }
        let extension =
            read_range(network, view.ip_header_length, remaining).ok_or(libc::EBADMSG)?;
        let encoded_length = usize::from(*extension.get(1).ok_or(libc::EBADMSG)?);
        let size = if protocol == libc::IPPROTO_FRAGMENT {
            view.fragment = true;
            view.noninitial_fragment =
                read_u16(extension, 2).ok_or(libc::EBADMSG)? & IPV6_FRAGMENT_OFFSET_MASK != 0;
            IPV6_EXTENSION_MINIMUM_LENGTH
        } else if protocol == libc::IPPROTO_AH {
            encoded_length
                .checked_add(2)
                .and_then(|length| length.checked_mul(4))
                .ok_or(libc::EBADMSG)?
        } else {
            encoded_length
                .checked_add(1)
                .and_then(|length| length.checked_mul(8))
                .ok_or(libc::EBADMSG)?
        };
        if size > remaining {
            return Err(libc::EBADMSG);
        }
        view.ip_header_length = view
            .ip_header_length
            .checked_add(size)
            .ok_or(libc::EBADMSG)?;
        view.tuple.protocol = *extension.first().ok_or(libc::EBADMSG)?;
        view.extended = true;
        if view.noninitial_fragment {
            break;
        }
    }
    Ok(())
}

pub fn eligible(frame: &[u8], view: &ParsedPacket, direction: &MwanNativeDirection) -> bool {
    if view.malformed
        || view.extended
        || view.fragment
        || view.ttl <= 1
        || view.network_length > usize::from(direction.output_mtu)
        || !view.ports
    {
        return false;
    }
    eligible_headers(frame, view).unwrap_or(false)
}

fn eligible_headers(frame: &[u8], view: &ParsedPacket) -> Option<bool> {
    let network = read_range(frame, view.network_offset, view.network_length)?;
    let transport = read_range(frame, view.transport_offset, view.transport_length)?;
    match u32::from(view.tuple.protocol) {
        MWAN_NATIVE_PROTOCOL_TCP => {
            if transport.len() < TCP_HEADER_LENGTH {
                return Some(false);
            }
            let length_and_reserved = *transport.get(12)?;
            let header_length = usize::from(length_and_reserved >> 4).checked_mul(4)?;
            if length_and_reserved & 15 != 0
                || header_length < TCP_HEADER_LENGTH
                || header_length > transport.len()
                || view.flags & TCP_ACK_FLAG == 0
                || view.flags & TCP_EXCEPTION_FLAGS != 0
            {
                return Some(false);
            }
        }
        MWAN_NATIVE_PROTOCOL_UDP => {
            if transport.len() < UDP_HEADER_LENGTH
                || usize::from(read_u16(transport, 4)?) != transport.len()
            {
                return Some(false);
            }
        }
        _ => return Some(false),
    }
    let mut sum;
    match u32::from(view.tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => {
            if fold_checksum(sum_words(network.get(..view.ip_header_length)?)) != u16::MAX {
                return Some(false);
            }
            if u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_UDP
                && read_u16(transport, 6)? == 0
            {
                return Some(true);
            }
            sum = sum_words(network.get(12..20)?);
            sum += u64::from(view.tuple.protocol);
            sum += u64::try_from(transport.len()).ok()?;
        }
        MWAN_NATIVE_FAMILY_IPV6 => {
            if u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_UDP
                && read_u16(transport, 6)? == 0
            {
                return Some(false);
            }
            sum = sum_words(network.get(8..40)?);
            let length = u32::try_from(transport.len()).ok()?;
            sum += sum_words(&length.to_be_bytes());
            sum += u64::from(view.tuple.protocol);
        }
        _ => return Some(false),
    }
    sum += sum_words(transport);
    Some(fold_checksum(sum) == u16::MAX)
}

pub fn restore(
    frame: &mut [u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
) -> Result<(), DecodeError> {
    validate_edit(frame, view, direction)?;
    match u32::from(view.tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => restore_ipv4(frame, view, direction)?,
        MWAN_NATIVE_FAMILY_IPV6 => {
            let source = view
                .network_offset
                .checked_add(8)
                .ok_or_else(|| view.error(libc::EBADMSG))?;
            let destination = view
                .network_offset
                .checked_add(24)
                .ok_or_else(|| view.error(libc::EBADMSG))?;
            write_bytes(frame, source, &direction.original.source, view)?;
            write_bytes(frame, destination, &direction.original.destination, view)?;
        }
        _ => return Err(view.error(libc::EPROTONOSUPPORT)),
    }
    write_bytes(frame, 0, &direction.original_destination_mac, view)?;
    write_bytes(
        frame,
        ETHERNET_ADDRESS_LENGTH,
        &direction.original_source_mac,
        view,
    )
}

fn restore_ipv4(
    frame: &mut [u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
) -> Result<(), DecodeError> {
    let checksum_offset = view
        .network_offset
        .checked_add(10)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let source_offset = view
        .network_offset
        .checked_add(12)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let destination_offset = view
        .network_offset
        .checked_add(16)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let original_source = direction
        .original
        .source
        .get(..4)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let original_destination = direction
        .original
        .destination
        .get(..4)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let translated_source = direction
        .translated
        .source
        .get(..4)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let translated_destination = direction
        .translated
        .destination
        .get(..4)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let ip_checksum = read_u16(frame, checksum_offset).ok_or_else(|| view.error(libc::EBADMSG))?;
    let ip_checksum = checksum_replace(ip_checksum, translated_source, original_source)
        .and_then(|checksum| {
            checksum_replace(checksum, translated_destination, original_destination)
        })
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let [source_high, source_low] = direction.original.source_port.to_be_bytes();
    let [destination_high, destination_low] = direction.original.destination_port.to_be_bytes();
    let ports = [source_high, source_low, destination_high, destination_low];
    let transport_checksum = inverse_transport_checksum(frame, view, direction, &ports)?;
    write_bytes(frame, checksum_offset, &ip_checksum.to_be_bytes(), view)?;
    if let Some((offset, checksum)) = transport_checksum {
        write_bytes(frame, offset, &checksum.to_be_bytes(), view)?;
    }
    write_bytes(frame, source_offset, original_source, view)?;
    write_bytes(frame, destination_offset, original_destination, view)?;
    if view.ports {
        write_bytes(frame, view.transport_offset, &ports, view)?;
    }
    Ok(())
}

fn inverse_transport_checksum(
    frame: &[u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
    ports: &[u8; 4],
) -> Result<Option<(usize, u16)>, DecodeError> {
    if !view.ports {
        return Ok(None);
    }
    let udp = u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_UDP;
    let field = match u32::from(view.tuple.protocol) {
        MWAN_NATIVE_PROTOCOL_TCP if view.transport_length >= 18 => 16,
        MWAN_NATIVE_PROTOCOL_UDP if view.transport_length >= UDP_HEADER_LENGTH => 6,
        _ => return Ok(None),
    };
    let offset = view
        .transport_offset
        .checked_add(field)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let checksum = read_u16(frame, offset).ok_or_else(|| view.error(libc::EBADMSG))?;
    // IPv4 UDP wire zero disables the checksum and must survive inverse NAT.
    if udp && checksum == 0 {
        return Ok(None);
    }
    let previous_ports = read_range(frame, view.transport_offset, ports.len())
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let checksum = checksum_replace(
        checksum,
        &direction.translated.source[..4],
        &direction.original.source[..4],
    )
    .and_then(|value| {
        checksum_replace(
            value,
            &direction.translated.destination[..4],
            &direction.original.destination[..4],
        )
    })
    .and_then(|value| checksum_replace(value, previous_ports, ports))
    .ok_or_else(|| view.error(libc::EBADMSG))?;
    // UDP transmits a computed zero as all ones, preserving checksum presence.
    let checksum = if udp && checksum == 0 {
        u16::MAX
    } else {
        checksum
    };
    Ok(Some((offset, checksum)))
}

pub fn forward(
    frame: &mut [u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
) -> Result<(), DecodeError> {
    validate_edit(frame, view, direction)?;
    let ttl = view
        .ttl
        .checked_sub(1)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    set_hop_limit(frame, view, ttl)?;
    write_bytes(frame, 0, &direction.output_destination_mac, view)?;
    write_bytes(
        frame,
        ETHERNET_ADDRESS_LENGTH,
        &direction.output_source_mac,
        view,
    )
}

pub fn restore_forwarded(
    frame: &mut [u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
) -> Result<(), DecodeError> {
    validate_edit(frame, view, direction)?;
    let forwarded_ttl = view
        .ttl
        .checked_sub(1)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let ttl_field = match u32::from(view.tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => 8,
        MWAN_NATIVE_FAMILY_IPV6 => 7,
        _ => return Err(view.error(libc::EPROTONOSUPPORT)),
    };
    let ttl_offset = view
        .network_offset
        .checked_add(ttl_field)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    if frame.get(ttl_offset) != Some(&forwarded_ttl)
        || frame.get(..ETHERNET_ADDRESS_LENGTH) != Some(direction.output_destination_mac.as_slice())
        || read_range(frame, ETHERNET_ADDRESS_LENGTH, ETHERNET_ADDRESS_LENGTH)
            != Some(direction.output_source_mac.as_slice())
    {
        return Err(view.error(libc::EBADMSG));
    }
    write_bytes(frame, ttl_offset, &[view.ttl], view)?;
    if u32::from(view.tuple.family) == MWAN_NATIVE_FAMILY_IPV4 {
        let checksum_offset = view
            .network_offset
            .checked_add(10)
            .ok_or_else(|| view.error(libc::EBADMSG))?;
        write_bytes(
            frame,
            checksum_offset,
            &view.ipv4_checksum.to_be_bytes(),
            view,
        )?;
    }
    restore(frame, view, direction)
}

fn set_hop_limit(frame: &mut [u8], view: &ParsedPacket, ttl: u8) -> Result<(), DecodeError> {
    match u32::from(view.tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => {
            let ttl_offset = view
                .network_offset
                .checked_add(8)
                .ok_or_else(|| view.error(libc::EBADMSG))?;
            let checksum_offset = view
                .network_offset
                .checked_add(10)
                .ok_or_else(|| view.error(libc::EBADMSG))?;
            let old =
                read_array::<2>(frame, ttl_offset).ok_or_else(|| view.error(libc::EBADMSG))?;
            let new = [ttl, view.tuple.protocol];
            let checksum =
                read_u16(frame, checksum_offset).ok_or_else(|| view.error(libc::EBADMSG))?;
            let checksum =
                checksum_replace(checksum, &old, &new).ok_or_else(|| view.error(libc::EBADMSG))?;
            write_bytes(frame, checksum_offset, &checksum.to_be_bytes(), view)?;
            write_bytes(frame, ttl_offset, &[ttl], view)?;
        }
        MWAN_NATIVE_FAMILY_IPV6 => {
            let offset = view
                .network_offset
                .checked_add(7)
                .ok_or_else(|| view.error(libc::EBADMSG))?;
            write_bytes(frame, offset, &[ttl], view)?;
        }
        _ => return Err(view.error(libc::EPROTONOSUPPORT)),
    }
    Ok(())
}

fn validate_edit(
    frame: &[u8],
    view: &ParsedPacket,
    direction: &MwanNativeDirection,
) -> Result<(), DecodeError> {
    if frame.len() != view.frame_length
        || !view.ethernet_valid
        || frame.get(..ETHERNET_HEADER_LENGTH).is_none()
        || read_range(frame, view.network_offset, view.ip_header_length).is_none()
        || read_range(frame, view.transport_offset, view.transport_length).is_none()
        || direction.original.family != view.tuple.family
        || direction.translated.family != view.tuple.family
    {
        return Err(view.error(libc::EBADMSG));
    }
    Ok(())
}

fn read_range(bytes: &[u8], offset: usize, length: usize) -> Option<&[u8]> {
    bytes.get(offset..offset.checked_add(length)?)
}

fn read_array<const LENGTH: usize>(bytes: &[u8], offset: usize) -> Option<[u8; LENGTH]> {
    read_range(bytes, offset, LENGTH)?.try_into().ok()
}

fn read_u16(bytes: &[u8], offset: usize) -> Option<u16> {
    Some(u16::from_be_bytes(read_array(bytes, offset)?))
}

fn write_bytes(
    frame: &mut [u8],
    offset: usize,
    bytes: &[u8],
    view: &ParsedPacket,
) -> Result<(), DecodeError> {
    let end = offset
        .checked_add(bytes.len())
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    let destination = frame
        .get_mut(offset..end)
        .ok_or_else(|| view.error(libc::EBADMSG))?;
    destination.copy_from_slice(bytes);
    Ok(())
}

fn sum_words(bytes: &[u8]) -> u64 {
    let mut sum = 0;
    let mut words = bytes.chunks_exact(2);
    for word in &mut words {
        if let [high, low] = word {
            sum += u64::from(u16::from_be_bytes([*high, *low]));
        }
    }
    if let Some(high) = words.remainder().first() {
        sum += u64::from(*high) << 8;
    }
    sum
}

fn fold_checksum(mut sum: u64) -> u16 {
    while sum > CHECKSUM_WORD_MASK {
        sum = (sum & CHECKSUM_WORD_MASK) + (sum >> 16);
    }
    sum as u16
}

fn checksum_replace(checksum: u16, before: &[u8], after: &[u8]) -> Option<u16> {
    if before.len() != after.len() || !before.len().is_multiple_of(2) {
        return None;
    }
    if before == after {
        return Some(checksum);
    }
    let mut sum = u64::from(!checksum);
    for (old, new) in before.chunks_exact(2).zip(after.chunks_exact(2)) {
        let ([old_high, old_low], [new_high, new_low]) = (old, new) else {
            return None;
        };
        sum += u64::from(!u16::from_be_bytes([*old_high, *old_low]));
        sum += u64::from(u16::from_be_bytes([*new_high, *new_low]));
    }
    Some(!fold_checksum(sum))
}

#[cfg(test)]
#[path = "packet_tests.rs"]
mod tests;

#[cfg(test)]
#[path = "udp4_tests.rs"]
mod udp4_tests;
