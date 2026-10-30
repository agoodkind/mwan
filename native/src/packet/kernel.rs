// SPDX-License-Identifier: BSD-3-Clause

use super::*;
use crate::abi::{MwanNativeKernelMetadata, MwanNativeKernelObservation};

// Linux if_packet.h and virtio_net.h define these scalar ABI values.
const STATUS_USER: u32 = 1;
const STATUS_CHECKSUM_PENDING: u32 = 8;
const STATUS_CHECKSUM_VALID: u32 = 128;
const STATUS_GSO_TCP: u32 = 256;
const ALLOWED_STATUS: u32 =
    STATUS_USER | STATUS_CHECKSUM_PENDING | STATUS_CHECKSUM_VALID | STATUS_GSO_TCP;
const VNET_NEEDS_CHECKSUM: u8 = 1;
const VNET_DATA_VALID: u8 = 2;
const GSO_NONE: u8 = 0;
const GSO_TCP_V4: u8 = 1;
const GSO_TCP_V6: u8 = 4;
const GSO_ECN: u8 = 0x80;
const GSO_BY_FRAGMENTS: u16 = u16::MAX;
const TCP_CHECKSUM_OFFSET: usize = 16;
const UDP_CHECKSUM_OFFSET: usize = 6;
const CHECKSUM_LENGTH: usize = 2;

pub(crate) fn observe_kernel(
    frame: &[u8],
    metadata: &MwanNativeKernelMetadata,
) -> MwanNativeKernelObservation {
    let mut result = MwanNativeKernelObservation::default();
    let view = match decode(frame) {
        Ok(view) => view,
        Err(_) => {
            result.observation = observe(frame);
            return result;
        }
    };
    result.observation = observation_headers(&view);
    match effective_length(frame, &view, metadata) {
        Ok(length) => {
            // Complete ordinary packets retain the original wire observer.
            if metadata.gso_type == GSO_NONE
                && metadata.auxiliary_status & (STATUS_CHECKSUM_PENDING | STATUS_CHECKSUM_VALID)
                    == 0
            {
                result.observation = observe(frame);
            } else {
                result.observation.eligible = 1;
            }
            if result.observation.eligible != 0 {
                result.effective_network_length = length;
            }
        }
        Err(code) => result.observation.decode_status = -code,
    }
    result
}

fn effective_length(
    frame: &[u8],
    view: &ParsedPacket,
    metadata: &MwanNativeKernelMetadata,
) -> Result<u32, i32> {
    if metadata.auxiliary_status & !ALLOWED_STATUS != 0
        || metadata.auxiliary_status & STATUS_USER == 0
        || metadata.packet_length as usize != frame.len()
        || metadata.captured_length != metadata.packet_length
        || metadata.mac_offset != 0
        || usize::from(metadata.network_offset) != ETHERNET_HEADER_LENGTH
        || metadata.vnet_flags & !(VNET_NEEDS_CHECKSUM | VNET_DATA_VALID) != 0
        || metadata.reserved16 != 0
        || metadata.reserved32 != 0
    {
        return Err(libc::EINVAL);
    }
    if view.malformed || view.extended || view.fragment || view.ttl <= 1 || !view.ports {
        return Err(libc::EBADMSG);
    }
    let header_length = transport_header_length(frame, view).ok_or(libc::EBADMSG)?;
    let network =
        read_range(frame, view.network_offset, view.network_length).ok_or(libc::EBADMSG)?;
    if u32::from(view.tuple.family) == MWAN_NATIVE_FAMILY_IPV4
        && fold_checksum(sum_words(
            network.get(..view.ip_header_length).ok_or(libc::EBADMSG)?,
        )) != u16::MAX
    {
        return Err(libc::EBADMSG);
    }
    let pending = metadata.auxiliary_status & STATUS_CHECKSUM_PENDING != 0;
    let valid = metadata.auxiliary_status & STATUS_CHECKSUM_VALID != 0;
    if pending != (metadata.vnet_flags & VNET_NEEDS_CHECKSUM != 0)
        || (pending && valid)
        || (metadata.vnet_flags & VNET_DATA_VALID != 0 && !valid)
    {
        return Err(libc::EINVAL);
    }
    if pending {
        let field = if u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_TCP {
            TCP_CHECKSUM_OFFSET
        } else {
            UDP_CHECKSUM_OFFSET
        };
        let checksum_end = usize::from(metadata.checksum_start)
            .checked_add(usize::from(metadata.checksum_offset))
            .and_then(|offset| offset.checked_add(CHECKSUM_LENGTH))
            .ok_or(libc::EINVAL)?;
        let header_end = view
            .transport_offset
            .checked_add(header_length)
            .ok_or(libc::EINVAL)?;
        if usize::from(metadata.checksum_start) != view.transport_offset
            || usize::from(metadata.checksum_offset) != field
            || checksum_end > header_end
        {
            return Err(libc::EINVAL);
        }
    } else if metadata.checksum_start != 0 || metadata.checksum_offset != 0 {
        return Err(libc::EINVAL);
    }
    // IPv6 UDP requires a wire checksum unless the kernel will complete it.
    if !pending
        && u32::from(view.tuple.family) == MWAN_NATIVE_FAMILY_IPV6
        && u32::from(view.tuple.protocol) == MWAN_NATIVE_PROTOCOL_UDP
        && read_u16(frame, view.transport_offset + UDP_CHECKSUM_OFFSET) == Some(0)
    {
        return Err(libc::EBADMSG);
    }
    if !pending && !valid && eligible_headers(frame, view) != Some(true) {
        return Err(libc::EBADMSG);
    }
    let length = if metadata.gso_type == GSO_NONE {
        if metadata.auxiliary_status & STATUS_GSO_TCP != 0
            || metadata.gso_size != 0
            || metadata.header_length != 0
        {
            return Err(libc::EINVAL);
        }
        view.network_length
    } else {
        gso_length(frame, view, metadata, header_length)?
    };
    if length == 0 || length > view.network_length || length > usize::from(u16::MAX) {
        return Err(libc::EINVAL);
    }
    u32::try_from(length).map_err(|_| libc::EINVAL)
}

fn gso_length(
    frame: &[u8],
    view: &ParsedPacket,
    metadata: &MwanNativeKernelMetadata,
    transport_header: usize,
) -> Result<usize, i32> {
    let expected_type = match u32::from(view.tuple.family) {
        MWAN_NATIVE_FAMILY_IPV4 => GSO_TCP_V4,
        MWAN_NATIVE_FAMILY_IPV6 => GSO_TCP_V6,
        _ => return Err(libc::EPROTONOSUPPORT),
    };
    let header_end = view
        .transport_offset
        .checked_add(transport_header)
        .ok_or(libc::EINVAL)?;
    let payload = view
        .transport_length
        .checked_sub(transport_header)
        .ok_or(libc::EINVAL)?;
    if u32::from(view.tuple.protocol) != MWAN_NATIVE_PROTOCOL_TCP
        || metadata.gso_type & !GSO_ECN != expected_type
        || metadata.auxiliary_status & STATUS_GSO_TCP == 0
        || metadata.gso_size == 0
        || metadata.gso_size == GSO_BY_FRAGMENTS
        || payload == 0
        || usize::from(metadata.header_length) < header_end
        || usize::from(metadata.header_length) > frame.len()
        || view.network_offset.checked_add(view.network_length) != Some(frame.len())
    {
        return Err(libc::EINVAL);
    }
    view.ip_header_length
        .checked_add(transport_header)
        .and_then(|length| length.checked_add(usize::from(metadata.gso_size).min(payload)))
        .ok_or(libc::EINVAL)
}
