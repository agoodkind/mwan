/* SPDX-License-Identifier: BSD-3-Clause */
#include "dpdk_shim.h"
#include <string.h>

uint16_t mwn_rx_burst(uint16_t port, uint16_t queue, struct rte_mbuf **packets, uint16_t count)
{
	return rte_eth_rx_burst(port, queue, packets, count);
}

uint16_t mwn_tx_burst(uint16_t port, uint16_t queue, struct rte_mbuf **packets, uint16_t count)
{
	return rte_eth_tx_burst(port, queue, packets, count);
}

int mwn_mbuf_linearize(struct rte_mbuf *packet)
{
	return rte_pktmbuf_linearize(packet);
}

uint32_t mwn_mbuf_length(const struct rte_mbuf *packet)
{
	return rte_pktmbuf_pkt_len(packet);
}

uint8_t *mwn_mbuf_data(struct rte_mbuf *packet)
{
	return rte_pktmbuf_mtod(packet, uint8_t *);
}

const void *mwn_mbuf_read(const struct rte_mbuf *packet, uint32_t offset, uint32_t length, void *buffer)
{
	return rte_pktmbuf_read(packet, offset, length, buffer);
}

void mwn_mbuf_free(struct rte_mbuf *packet)
{
	rte_pktmbuf_free(packet);
}

void mwn_mbuf_clear_tx_offloads(struct rte_mbuf *packet)
{
	packet->ol_flags = 0;
}

uint16_t mwn_mbuf_refcount(const struct rte_mbuf *packet)
{
	return rte_mbuf_refcnt_read(packet);
}

bool mwn_mbuf_is_direct(const struct rte_mbuf *packet)
{
	return RTE_MBUF_DIRECT(packet);
}

bool mwn_mbuf_is_external(const struct rte_mbuf *packet)
{
	return RTE_MBUF_HAS_EXTBUF(packet);
}

int mwn_errno(void)
{
	return rte_errno;
}

uint16_t mwn_max_ethports(void)
{
	return RTE_MAX_ETHPORTS;
}

uint16_t mwn_mbuf_headroom(void)
{
	return RTE_PKTMBUF_HEADROOM;
}

const char *mwn_dpdk_version(void)
{
	return rte_version();
}

const void *mwn_mbuf_buffer(const struct rte_mbuf *packet)
{
	return packet->buf_addr;
}

uint16_t mwn_mbuf_data_offset(const struct rte_mbuf *packet)
{
	return packet->data_off;
}

uint16_t mwn_mbuf_data_length(const struct rte_mbuf *packet)
{
	return packet->data_len;
}

uint16_t mwn_mbuf_buffer_length(const struct rte_mbuf *packet)
{
	return packet->buf_len;
}

void mwn_eth_conf_set_mtu(struct rte_eth_conf *configuration, uint32_t mtu)
{
	configuration->rxmode.mtu = mtu;
}

void mwn_eth_conf_enable_lsc(struct rte_eth_conf *configuration)
{
	configuration->intr_conf.lsc = 1;
}

const char *mwn_eth_driver_name(const struct rte_eth_dev_info *information)
{
	return information->driver_name;
}

const struct rte_eth_rxconf *mwn_eth_default_rxconf(const struct rte_eth_dev_info *information)
{
	return &information->default_rxconf;
}

const struct rte_eth_txconf *mwn_eth_default_txconf(const struct rte_eth_dev_info *information)
{
	return &information->default_txconf;
}

bool mwn_eth_link_status(const struct rte_eth_link *link)
{
	return link->link_status;
}

void mwn_flow_attr_set_transfer(struct rte_flow_attr *attributes, bool transfer)
{
	attributes->transfer = transfer;
}

void mwn_flow_group_attr_set_transfer(struct rte_flow_group_attr *attributes, bool transfer)
{
	attributes->transfer = transfer;
}

void mwn_flow_port_id_set(struct rte_flow_action_port_id *destination, uint32_t port)
{
	destination->id = port;
}

void mwn_flow_eth_set(struct rte_flow_item_eth *item, const uint8_t source[6], const uint8_t destination[6], uint16_t ether_type)
{
	memcpy(item->hdr.src_addr.addr_bytes, source, sizeof(item->hdr.src_addr.addr_bytes));
	memcpy(item->hdr.dst_addr.addr_bytes, destination, sizeof(item->hdr.dst_addr.addr_bytes));
	item->hdr.ether_type = ether_type;
}

void mwn_flow_ipv4_set(struct rte_flow_item_ipv4 *item, uint32_t source, uint32_t destination, uint8_t protocol)
{
	item->hdr.src_addr = source;
	item->hdr.dst_addr = destination;
	item->hdr.next_proto_id = protocol;
}

void mwn_flow_ipv6_set(struct rte_flow_item_ipv6 *item, const uint8_t source[16], const uint8_t destination[16], uint8_t protocol)
{
	memcpy(item->hdr.src_addr.a, source, sizeof(item->hdr.src_addr.a));
	memcpy(item->hdr.dst_addr.a, destination, sizeof(item->hdr.dst_addr.a));
	item->hdr.proto = protocol;
}

void mwn_flow_tcp_set(struct rte_flow_item_tcp *item, uint16_t source, uint16_t destination)
{
	item->hdr.src_port = source;
	item->hdr.dst_port = destination;
}

void mwn_flow_udp_set(struct rte_flow_item_udp *item, uint16_t source, uint16_t destination)
{
	item->hdr.src_port = source;
	item->hdr.dst_port = destination;
}

bool mwn_flow_count_hits_valid(const struct rte_flow_query_count *count)
{
	return count->hits_set;
}

bool mwn_flow_count_bytes_valid(const struct rte_flow_query_count *count)
{
	return count->bytes_set;
}

uint64_t mwn_flow_count_hits(const struct rte_flow_query_count *count)
{
	return count->hits;
}

uint64_t mwn_flow_count_bytes(const struct rte_flow_query_count *count)
{
	return count->bytes;
}
