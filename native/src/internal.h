/* SPDX-License-Identifier: BSD-3-Clause */
#ifndef MWAN_NATIVE_INTERNAL_H
#define MWAN_NATIVE_INTERNAL_H
#include "mwan_native.h"
#include <pthread.h>
#include <stdbool.h>
#include <rte_ethdev.h>
#include <rte_flow.h>
#include <rte_hash.h>
#include <rte_mbuf.h>
#define MWN_BURST 32U
#define MWN_NO_PORT UINT16_MAX
struct mwn_endpoint {
	uint16_t controller, kernel_rep, data_rep, data;
	bool miss;
};
struct mwn_key {
	uint32_t ingress;
	struct mwan_native_tuple tuple;
};
struct mwn_pair;
struct mwn_leg {
	struct mwan_native_direction spec;
	struct rte_flow *flow;
	struct mwn_pair *pair;
	struct mwn_key key;
	uint32_t index;
	bool hash_added, delete_failed;
};
struct mwn_pair {
	uint64_t generation, cookie, policy_generation;
	uint32_t state;
	uint32_t requested_mask;
	struct mwn_leg legs[MWAN_NATIVE_DIRECTION_COUNT];
};
struct mwn_pending {
	struct rte_mbuf *packet;
	uint16_t port;
	bool kernel;
};
struct mwan_native {
	pthread_t thread;
	struct mwan_native_config config;
	uint64_t instance;
	struct mwn_endpoint endpoints[MWAN_NATIVE_MAX_ENDPOINTS];
	struct rte_mempool *pool;
	struct rte_hash *lookup;
	struct mwn_pair *pairs;
	struct mwn_pending *pending;
	uint32_t pending_head, pending_count;
	uint32_t next_rx;
	struct rte_mbuf *quarantined;
	struct mwan_native_worker_stats stats;
	struct mwan_native_error terminal_failure;
	bool eal_ready, active, quiescing, faulted;
	bool started[RTE_MAX_ETHPORTS];
};
int mwn_error(struct mwan_native_error *, int, uint32_t, uint32_t, uint32_t, const char *);
int mwn_flow_error(struct mwan_native_error *, int, uint32_t, uint32_t, uint32_t, const struct rte_flow_error *);
int mwn_owner(struct mwan_native *, struct mwan_native_error *);
uint64_t mwn_now(void);
void mwn_pair_result(struct mwan_native *, uint32_t, struct mwan_native_pair_result *);
int mwn_pair_delete(struct mwan_native *, uint32_t, struct mwan_native_error *);
int mwn_drain(struct mwan_native *, struct mwan_native_error *);
void mwn_pair_forget(struct mwan_native *, uint32_t);
int mwn_poll(struct mwan_native *, uint32_t, struct mwan_native_error *);
#endif
