/* SPDX-License-Identifier: BSD-3-Clause */
#include "internal.h"
#include <errno.h>
#include <string.h>
#include <rte_ip.h>
#include <rte_tcp.h>
#include <rte_udp.h>

struct packet_view {
	uint8_t *frame, *ip, *transport;
	uint32_t network_length, transport_length;
	uint16_t network_offset;
	uint8_t ttl, flags;
	bool fragment, noninitial_fragment, extended, ports;
	struct mwan_native_tuple tuple;
};

static uint16_t read16(const uint8_t *value) { return (uint16_t)(value[0] * 256U + value[1]); }
static void write16(uint8_t *value, uint16_t word) { value[0] = (uint8_t)(word / 256U); value[1] = (uint8_t)word; }

/* Apply the inverse field delta instead of recomputing a valid checksum. */
static void checksum_replace(uint8_t *checksum, const uint8_t *before, const uint8_t *after, uint32_t size)
{
	if (!memcmp(before, after, size))
		return;
	uint32_t sum = (uint16_t)~read16(checksum);
	for (uint32_t i = 0; i < size; i += 2)
		sum += (uint16_t)~read16(before + i) + read16(after + i);
	while (sum > UINT16_MAX)
		sum = (sum & UINT16_MAX) + (sum / 65536U);
	write16(checksum, (uint16_t)~sum);
}

static int decode(struct rte_mbuf *packet, struct packet_view *view)
{
	memset(view, 0, sizeof(*view));
	if (rte_pktmbuf_linearize(packet))
		return -ENOBUFS;
	uint32_t length = rte_pktmbuf_pkt_len(packet);
	if (length < RTE_ETHER_HDR_LEN)
		return -EBADMSG;
	view->frame = rte_pktmbuf_mtod(packet, uint8_t *);
	uint16_t ether_type = read16(view->frame + 12);
	view->network_offset = RTE_ETHER_HDR_LEN;
	for (uint32_t vlan = 0; ether_type == RTE_ETHER_TYPE_VLAN || ether_type == RTE_ETHER_TYPE_QINQ; vlan++) {
		if (vlan == 2 || length < (uint32_t)view->network_offset + RTE_VLAN_HLEN)
			return -EBADMSG;
		ether_type = read16(view->frame + view->network_offset + 2);
		view->network_offset += RTE_VLAN_HLEN;
		view->extended = true;
	}
	view->ip = view->frame + view->network_offset;
	uint32_t available = length - view->network_offset, header_length;
	if (ether_type == RTE_ETHER_TYPE_IPV4) {
		if (available < sizeof(struct rte_ipv4_hdr) || view->ip[0] / 16 != 4)
			return -EBADMSG;
		header_length = (view->ip[0] & 15U) * 4U;
		view->network_length = read16(view->ip + 2);
		if (header_length < sizeof(struct rte_ipv4_hdr) || view->network_length < header_length || view->network_length > available)
			return -EBADMSG;
		uint16_t fragment = read16(view->ip + 6);
		view->fragment = (fragment & UINT16_C(0x3fff)) != 0;
		view->noninitial_fragment = (fragment & UINT16_C(0x1fff)) != 0;
		view->extended |= header_length != sizeof(struct rte_ipv4_hdr);
		view->ttl = view->ip[8];
		view->tuple.family = MWAN_NATIVE_FAMILY_IPV4;
		view->tuple.protocol = view->ip[9];
		memcpy(view->tuple.source, view->ip + 12, 4);
		memcpy(view->tuple.destination, view->ip + 16, 4);
	} else if (ether_type == RTE_ETHER_TYPE_IPV6) {
		if (available < sizeof(struct rte_ipv6_hdr) || view->ip[0] / 16 != 6)
			return -EBADMSG;
		header_length = sizeof(struct rte_ipv6_hdr);
		view->network_length = header_length + read16(view->ip + 4);
		if (view->network_length > available)
			return -EBADMSG;
		view->ttl = view->ip[7];
		view->tuple.family = MWAN_NATIVE_FAMILY_IPV6;
		view->tuple.protocol = view->ip[6];
		memcpy(view->tuple.source, view->ip + 8, 16);
		memcpy(view->tuple.destination, view->ip + 24, 16);
		for (uint32_t headers = 0; headers < 8; headers++) {
			uint8_t protocol = view->tuple.protocol;
			if (protocol != IPPROTO_HOPOPTS && protocol != IPPROTO_ROUTING && protocol != IPPROTO_DSTOPTS && protocol != IPPROTO_FRAGMENT && protocol != IPPROTO_AH)
				break;
			if (view->network_length - header_length < 8)
				return -EBADMSG;
			uint8_t *extension = view->ip + header_length;
			uint32_t size = (extension[1] + 1U) * 8U;
			if (protocol == IPPROTO_AH)
				size = (extension[1] + 2U) * 4U;
			if (protocol == IPPROTO_FRAGMENT) {
				size = 8;
				view->fragment = true;
				view->noninitial_fragment = (read16(extension + 2) & UINT16_C(0xfff8)) != 0;
			}
			if (size > view->network_length - header_length)
				return -EBADMSG;
			header_length += size;
			view->tuple.protocol = extension[0];
			view->extended = true;
			if (view->noninitial_fragment)
				break;
		}
	} else {
		return -EPROTONOSUPPORT;
	}
	view->transport = view->ip + header_length;
	view->transport_length = view->network_length - header_length;
	if (view->tuple.protocol != MWAN_NATIVE_PROTOCOL_TCP && view->tuple.protocol != MWAN_NATIVE_PROTOCOL_UDP)
		return -EPROTONOSUPPORT;
	if (!view->noninitial_fragment && view->transport_length >= 4) {
		view->tuple.source_port = read16(view->transport);
		view->tuple.destination_port = read16(view->transport + 2);
		view->ports = true;
	}
	if (!view->noninitial_fragment && view->tuple.protocol == MWAN_NATIVE_PROTOCOL_TCP && view->transport_length >= sizeof(struct rte_tcp_hdr))
		view->flags = view->transport[13];
	return 0;
}

static struct mwn_leg *packet_leg(struct mwan_native *owner, uint32_t origin, const struct packet_view *view)
{
	struct mwn_key key = {.origin = origin, .tuple = view->tuple};
	void *value = NULL;
	if (view->ports && rte_hash_lookup_data(owner->lookup, &key, &value) >= 0)
		return value;
	if (!view->fragment || view->ports)
		return NULL;
	for (uint32_t i = 0; i < owner->config.pair_capacity; i++) {
		struct mwn_pair *pair = &owner->pairs[i];
		if (pair->state == MWAN_NATIVE_PAIR_ABSENT)
			continue;
		for (uint32_t j = 0; j < MWAN_NATIVE_DIRECTION_COUNT; j++) {
			struct mwn_leg *leg = &pair->legs[j];
			const struct mwan_native_tuple *tuple = &leg->spec.translated;
			if (leg->hash_added && leg->spec.origin_id == origin && tuple->family == view->tuple.family &&
			    tuple->protocol == view->tuple.protocol && !memcmp(tuple->source, view->tuple.source, 16) &&
			    !memcmp(tuple->destination, view->tuple.destination, 16))
				return leg;
		}
	}
	return NULL;
}

static void restore(struct packet_view *view, const struct mwan_native_direction *direction)
{
	const struct mwan_native_tuple *original = &direction->original;
	const struct mwan_native_tuple *translated = &direction->translated;
	if (view->tuple.family == MWAN_NATIVE_FAMILY_IPV4) {
		uint8_t *tcp_checksum = NULL;
		if (view->ports && view->tuple.protocol == MWAN_NATIVE_PROTOCOL_TCP && view->transport_length >= 18)
			tcp_checksum = view->transport + 16;
		checksum_replace(view->ip + 10, translated->source, original->source, 4);
		checksum_replace(view->ip + 10, translated->destination, original->destination, 4);
		if (tcp_checksum) {
			checksum_replace(tcp_checksum, translated->source, original->source, 4);
			checksum_replace(tcp_checksum, translated->destination, original->destination, 4);
		}
		memcpy(view->ip + 12, original->source, 4);
		memcpy(view->ip + 16, original->destination, 4);
		if (view->ports) {
			uint8_t ports[4];
			write16(ports, original->source_port);
			write16(ports + 2, original->destination_port);
			if (tcp_checksum)
				checksum_replace(tcp_checksum, view->transport, ports, sizeof(ports));
			memcpy(view->transport, ports, sizeof(ports));
		}
	}
	memcpy(view->frame, direction->original_destination_mac, RTE_ETHER_ADDR_LEN);
	memcpy(view->frame + RTE_ETHER_ADDR_LEN, direction->original_source_mac, RTE_ETHER_ADDR_LEN);
}

static bool eligible(const struct packet_view *view, const struct mwan_native_direction *direction)
{
	if (view->extended || view->fragment || view->ttl <= 1 || view->network_length > direction->output_mtu || !view->ports)
		return false;
	if (view->tuple.protocol == MWAN_NATIVE_PROTOCOL_TCP) {
		if (view->transport_length < sizeof(struct rte_tcp_hdr) ||
		    (view->transport[12] & 15U) || view->transport[12] / 16U * 4U < sizeof(struct rte_tcp_hdr) ||
		    view->transport[12] / 16U * 4U > view->transport_length ||
		    !(view->flags & RTE_TCP_ACK_FLAG) ||
		    (view->flags & (RTE_TCP_SYN_FLAG | RTE_TCP_FIN_FLAG | RTE_TCP_RST_FLAG | RTE_TCP_URG_FLAG)))
			return false;
	} else if (view->transport_length < sizeof(struct rte_udp_hdr) || read16(view->transport + 4) != view->transport_length) {
		return false;
	}
	if (view->tuple.family == MWAN_NATIVE_FAMILY_IPV4) {
		if (rte_ipv4_cksum((const struct rte_ipv4_hdr *)view->ip) != 0)
			return false;
		if (view->tuple.protocol == MWAN_NATIVE_PROTOCOL_UDP && !read16(view->transport + 6))
			return true;
		return rte_ipv4_udptcp_cksum_verify((const struct rte_ipv4_hdr *)view->ip, view->transport) == 0;
	}
	return rte_ipv6_udptcp_cksum_verify((const struct rte_ipv6_hdr *)view->ip, view->transport) == 0;
}

static void flush_pending(struct mwan_native *owner)
{
	while (owner->pending_count) {
		struct rte_mbuf *packets[MWN_BURST];
		uint16_t port = owner->pending[owner->pending_head].port;
		uint16_t count = 0;
		while (count < MWN_BURST && count < owner->pending_count) {
			uint32_t index = (owner->pending_head + count) % owner->config.mbuf_count;
			if (owner->pending[index].port != port)
				break;
			packets[count++] = owner->pending[index].packet;
		}
		uint16_t sent = rte_eth_tx_burst(port, 0, packets, count);
		for (uint16_t i = 0; i < sent; i++) {
			struct mwn_pending *pending = &owner->pending[owner->pending_head];
			if (pending->kernel)
				owner->stats.returned_to_kernel++;
			else
				owner->stats.forwarded++;
			pending->packet = NULL;
			owner->pending_head = (owner->pending_head + 1) % owner->config.mbuf_count;
			owner->pending_count--;
		}
		if (sent != count)
			break;
	}
	owner->stats.pending_packets = owner->pending_count;
}

static int process_packet(struct mwan_native *owner, uint32_t origin,
			  struct rte_mbuf *packet, struct mwan_native_error *error)
{
	struct rte_ether_hdr header;
	const struct rte_ether_hdr *ethernet = rte_pktmbuf_read(packet, 0, sizeof(header), &header);
	/* Exact rules admit unicast MACs; DATA also receives native group copies. */
	if (ethernet && rte_is_multicast_ether_addr(&ethernet->dst_addr)) {
		owner->stats.discarded_group_frames++;
		rte_pktmbuf_free(packet);
		return 0;
	}
	struct packet_view view;
	int rc = decode(packet, &view);
	struct mwan_native_packet_diagnostic diagnostic = {.decode_status = rc};
	if (view.frame) {
		diagnostic.validity |= MWAN_NATIVE_PACKET_ETHERNET_VALID;
		diagnostic.ether_type = read16(view.frame + 12);
		memcpy(diagnostic.destination_mac, view.frame, RTE_ETHER_ADDR_LEN);
		memcpy(diagnostic.source_mac, view.frame + RTE_ETHER_ADDR_LEN, RTE_ETHER_ADDR_LEN);
	}
	if (view.tuple.family) {
		diagnostic.validity |= MWAN_NATIVE_PACKET_IP_VALID;
		diagnostic.tuple = view.tuple;
	}
	if (view.ports)
		diagnostic.validity |= MWAN_NATIVE_PACKET_PORTS_VALID;
	if (rc) {
		owner->stats.malformed++;
		goto quarantine;
	}
	struct mwn_leg *leg = packet_leg(owner, origin, &view);
	if (!leg) {
		diagnostic.failure_flags |= MWAN_NATIVE_PACKET_KEY_MISSING;
	} else {
		if (memcmp(view.frame, leg->spec.original_destination_mac, RTE_ETHER_ADDR_LEN))
			diagnostic.failure_flags |= MWAN_NATIVE_PACKET_DESTINATION_MAC_MISMATCH;
		if (memcmp(view.frame + RTE_ETHER_ADDR_LEN, leg->spec.original_source_mac, RTE_ETHER_ADDR_LEN))
			diagnostic.failure_flags |= MWAN_NATIVE_PACKET_SOURCE_MAC_MISMATCH;
	}
	if (diagnostic.failure_flags) {
		owner->stats.lookup_misses++;
		rc = -ENOENT;
		goto quarantine;
	}
	bool kernel = owner->quiescing || owner->faulted || leg->pair->state != MWAN_NATIVE_PAIR_INSTALLED || !eligible(&view, &leg->spec);
	uint16_t output = owner->data[leg->spec.egress_data_id].port;
	struct rte_eth_link link;
	if (!kernel && (rte_eth_link_get_nowait(output, &link) || !link.link_status))
		kernel = true;
	if (kernel) {
		owner->stats.unsupported++;
		restore(&view, &leg->spec);
		output = owner->data[owner->config.origins[origin].data].port;
	} else {
		if (view.tuple.family == MWAN_NATIVE_FAMILY_IPV4) {
			uint8_t ttl_protocol[] = {(uint8_t)(view.ttl - 1), view.ip[9]};
			checksum_replace(view.ip + 10, view.ip + 8, ttl_protocol, sizeof(ttl_protocol));
			view.ip[8]--;
		} else {
			view.ip[7]--;
		}
		memcpy(view.frame, leg->spec.output_destination_mac, RTE_ETHER_ADDR_LEN);
		memcpy(view.frame + RTE_ETHER_ADDR_LEN, leg->spec.output_source_mac, RTE_ETHER_ADDR_LEN);
	}
	/* RX checksum metadata must not request a different TX checksum. */
	packet->ol_flags = 0;
	if (owner->pending_count == owner->config.mbuf_count) {
		rc = -ENOBUFS;
		goto quarantine;
	}
	uint32_t index = (owner->pending_head + owner->pending_count) % owner->config.mbuf_count;
	owner->pending[index] = (struct mwn_pending){.packet = packet, .port = output, .kernel = kernel};
	owner->pending_count++;
	return 0;
quarantine:
	owner->quarantined = packet;
	owner->stats.quarantined_packets++;
	owner->faulted = true;
	int failure = mwn_error(error, -rc, MWAN_NATIVE_OP_PACKET, MWAN_NATIVE_OBJECT_ORIGIN, origin, 0, "Packet lacks a verified inverse; retained for recovery and admissions stopped");
	error->packet = diagnostic;
	return failure;
}

int mwn_poll(struct mwan_native *owner, uint32_t budget, struct mwan_native_error *error)
{
	if (owner->quarantined)
		return mwn_error(error, EIO, MWAN_NATIVE_OP_PACKET, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Unrecoverable packet retained; controller recovery is required");
	flush_pending(owner);
	if (owner->pending_count)
		return 0;
	for (uint32_t visited = 0; visited < owner->config.data_count && budget; visited++) {
		uint32_t data_id = owner->next_rx++ % owner->config.data_count;
		struct mwn_data *data = &owner->data[data_id];
		if (data->origin == MWAN_NATIVE_NO_ID || data->port == MWN_NO_PORT || !owner->started[data->port])
			continue;
		uint32_t origin = data->origin;
		struct rte_mbuf *packets[MWN_BURST];
		uint16_t count = rte_eth_rx_burst(data->port, 0, packets, (uint16_t)RTE_MIN(budget, MWN_BURST));
		budget -= count;
		owner->stats.received += count;
		for (uint16_t i = 0; i < count; i++) {
			int rc = process_packet(owner, origin, packets[i], error);
			if (rc) {
				/* Preserve every packet already dequeued from this burst. */
				for (uint16_t j = i + 1; j < count; j++) {
					uint32_t slot = (owner->pending_head + owner->pending_count) % owner->config.mbuf_count;
					owner->pending[slot] = (struct mwn_pending){.packet = packets[j], .port = MWN_NO_PORT};
					owner->pending_count++;
				}
				return rc;
			}
		}
		flush_pending(owner);
		if (owner->pending_count)
			break;
	}
	return 0;
}
