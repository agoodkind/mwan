/* SPDX-License-Identifier: BSD-3-Clause */
#ifndef MWAN_DPDK_SHIM_H
#define MWAN_DPDK_SHIM_H

#include <stdbool.h>
#include <stdint.h>
#include <rte_config.h>
#include <rte_cycles.h>
#include <rte_eal.h>
#include <rte_errno.h>
#include <rte_ethdev.h>
#include <rte_flow.h>
#include <rte_lcore.h>
#include <rte_mbuf.h>
#include <rte_mempool.h>
#include <rte_pmd_bnxt.h>
#include <rte_random.h>
#include <rte_version.h>

uint16_t mwn_rx_burst(uint16_t port, uint16_t queue, struct rte_mbuf **packets, uint16_t count);
uint16_t mwn_tx_burst(uint16_t port, uint16_t queue, struct rte_mbuf **packets, uint16_t count);
int mwn_mbuf_linearize(struct rte_mbuf *packet);
uint32_t mwn_mbuf_length(const struct rte_mbuf *packet);
uint8_t *mwn_mbuf_data(struct rte_mbuf *packet);
const void *mwn_mbuf_read(const struct rte_mbuf *packet, uint32_t offset, uint32_t length, void *buffer);
void mwn_mbuf_free(struct rte_mbuf *packet);
void mwn_mbuf_clear_tx_offloads(struct rte_mbuf *packet);
uint16_t mwn_mbuf_refcount(const struct rte_mbuf *packet);
bool mwn_mbuf_is_direct(const struct rte_mbuf *packet);
bool mwn_mbuf_is_external(const struct rte_mbuf *packet);
int mwn_errno(void);
uint16_t mwn_max_ethports(void);
uint16_t mwn_mbuf_headroom(void);
const char *mwn_dpdk_version(void);
const void *mwn_mbuf_buffer(const struct rte_mbuf *packet);
uint16_t mwn_mbuf_data_offset(const struct rte_mbuf *packet);
uint16_t mwn_mbuf_data_length(const struct rte_mbuf *packet);
uint16_t mwn_mbuf_buffer_length(const struct rte_mbuf *packet);
void mwn_eth_conf_set_mtu(struct rte_eth_conf *configuration, uint32_t mtu);
const char *mwn_eth_driver_name(const struct rte_eth_dev_info *information);
const struct rte_eth_rxconf *mwn_eth_default_rxconf(const struct rte_eth_dev_info *information);
const struct rte_eth_txconf *mwn_eth_default_txconf(const struct rte_eth_dev_info *information);
bool mwn_eth_link_status(const struct rte_eth_link *link);
void mwn_flow_attr_set_transfer(struct rte_flow_attr *attributes, bool transfer);
void mwn_flow_group_attr_set_transfer(struct rte_flow_group_attr *attributes, bool transfer);
void mwn_flow_port_id_set(struct rte_flow_action_port_id *destination, uint32_t port);
void mwn_flow_eth_set(struct rte_flow_item_eth *item, const uint8_t source[6], const uint8_t destination[6], uint16_t ether_type);
void mwn_flow_ipv4_set(struct rte_flow_item_ipv4 *item, uint32_t source, uint32_t destination, uint8_t protocol);
void mwn_flow_ipv6_set(struct rte_flow_item_ipv6 *item, const uint8_t source[16], const uint8_t destination[16], uint8_t protocol);
void mwn_flow_tcp_set(struct rte_flow_item_tcp *item, uint16_t source, uint16_t destination);
void mwn_flow_udp_set(struct rte_flow_item_udp *item, uint16_t source, uint16_t destination);
bool mwn_flow_count_hits_valid(const struct rte_flow_query_count *count);
bool mwn_flow_count_bytes_valid(const struct rte_flow_query_count *count);
uint64_t mwn_flow_count_hits(const struct rte_flow_query_count *count);
uint64_t mwn_flow_count_bytes(const struct rte_flow_query_count *count);

#endif
