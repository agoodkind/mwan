/* SPDX-License-Identifier: BSD-3-Clause */
#include "internal.h"
#include <errno.h>
#include <string.h>
#include <rte_byteorder.h>
#include <rte_errno.h>
#include <rte_ip.h>
#include <rte_tcp.h>
#include <rte_udp.h>

static int tuple_valid(const struct mwan_native_tuple *t)
{
	static const uint8_t zero[16];
	if (t->reserved || (t->family != MWAN_NATIVE_FAMILY_IPV4 && t->family != MWAN_NATIVE_FAMILY_IPV6) ||
	    (t->protocol != MWAN_NATIVE_PROTOCOL_TCP && t->protocol != MWAN_NATIVE_PROTOCOL_UDP) ||
	    !t->source_port || !t->destination_port || !memcmp(t->source, zero, 16) || !memcmp(t->destination, zero, 16))
		return -EINVAL;
	if (t->family == MWAN_NATIVE_FAMILY_IPV4 && (memcmp(t->source + 4, zero, 12) || memcmp(t->destination + 4, zero, 12)))
		return -EINVAL;
	return 0;
}

static bool same_addresses(const struct mwan_native_tuple *a, const struct mwan_native_tuple *b)
{
	return a->family == b->family && a->protocol == b->protocol &&
	       !memcmp(a->source, b->source, 16) && !memcmp(a->destination, b->destination, 16);
}

static int direction_valid(struct mwan_native *o, const struct mwan_native_direction *s,
			   struct mwan_native_error *e, uint32_t i)
{
	if (s->origin_id >= o->config.origin_count || s->egress_data_id >= o->config.data_count || s->reserved ||
	    tuple_valid(&s->original) || tuple_valid(&s->translated) ||
	    s->original.family != s->translated.family || s->original.protocol != s->translated.protocol ||
	    s->output_mtu < RTE_ETHER_MIN_MTU || s->output_mtu > o->config.mtu)
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, i, "Invalid exact direction");
	if ((s->original.family != MWAN_NATIVE_FAMILY_IPV4 || s->original.protocol != MWAN_NATIVE_PROTOCOL_TCP) &&
	    memcmp(&s->original, &s->translated, sizeof(s->original)))
		return mwn_error(e, ENOTSUP, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, i, "Translation requires IPv4 TCP");
	const uint8_t *macs[] = {s->original_source_mac, s->original_destination_mac, s->output_source_mac, s->output_destination_mac};
	for (uint32_t n = 0; n < RTE_DIM(macs); n++) {
		struct rte_ether_addr address;
		memcpy(&address, macs[n], sizeof(address));
		if (!rte_is_valid_assigned_ether_addr(&address))
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, i, "Direction requires unicast MAC addresses");
	}
	const struct mwan_native_origin *origin = &o->config.origins[s->origin_id];
	if (memcmp(s->original_destination_mac, o->config.receivers[origin->receiver].mac, RTE_ETHER_ADDR_LEN))
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, i, "Original destination must identify the origin's kernel receiver");
	return 0;
}

static bool collision(struct mwan_native *o, const struct mwan_native_direction *a, const struct mwan_native_direction *b)
{
	const struct mwan_native_origin *first = &o->config.origins[a->origin_id];
	const struct mwan_native_origin *second = &o->config.origins[b->origin_id];
	/* Software origin IDs do not distinguish identical physical selectors. */
	if (first->controller == second->controller &&
	    !memcmp(&a->original, &b->original, sizeof(a->original)) &&
	    !memcmp(a->original_source_mac, b->original_source_mac, RTE_ETHER_ADDR_LEN) &&
	    !memcmp(a->original_destination_mac, b->original_destination_mac, RTE_ETHER_ADDR_LEN))
		return true;
	if (a->origin_id != b->origin_id)
		return false;
	if (!memcmp(&a->original, &b->original, sizeof(a->original)) || !memcmp(&a->translated, &b->translated, sizeof(a->translated)))
		return true;
	/* A fragment without ports still needs one reversible address mapping. */
	return same_addresses(&a->translated, &b->translated) &&
	       (!same_addresses(&a->original, &b->original) ||
		memcmp(a->original_source_mac, b->original_source_mac, RTE_ETHER_ADDR_LEN) ||
		memcmp(a->original_destination_mac, b->original_destination_mac, RTE_ETHER_ADDR_LEN));
}

static int build_flow(struct mwan_native *o, struct mwn_leg *leg, bool validate, struct mwan_native_error *e)
{
	const struct mwan_native_direction *s = &leg->spec;
	struct rte_flow_attr attr = {.transfer = 1};
	struct rte_flow_item_eth eth = {0}, emask = {0};
	struct rte_flow_item_ipv4 ip4 = {0}, m4 = {0};
	struct rte_flow_item_ipv6 ip6 = {0}, m6 = {0};
	struct rte_flow_item_tcp tcp = {0}, tm = {0};
	struct rte_flow_item_udp udp = {0}, um = {0};
	struct rte_flow_item items[4] = {0};
	struct rte_flow_action actions[7] = {0};
	struct rte_flow_action_count count = {0};
	struct rte_flow_action_port_id port = {.id = o->data[o->config.origins[s->origin_id].data].representor};
	struct rte_flow_action_set_ipv4 src4 = {0}, dst4 = {0};
	struct rte_flow_action_set_tp srcp = {0}, dstp = {0};
	struct rte_flow_error detail = {0};
	uint32_t n = 0;
	memcpy(eth.hdr.src_addr.addr_bytes, s->original_source_mac, RTE_ETHER_ADDR_LEN);
	memcpy(eth.hdr.dst_addr.addr_bytes, s->original_destination_mac, RTE_ETHER_ADDR_LEN);
	memset(&emask.hdr.src_addr, 0xff, RTE_ETHER_ADDR_LEN);
	memset(&emask.hdr.dst_addr, 0xff, RTE_ETHER_ADDR_LEN);
	eth.hdr.ether_type = rte_cpu_to_be_16(s->original.family == MWAN_NATIVE_FAMILY_IPV4 ? RTE_ETHER_TYPE_IPV4 : RTE_ETHER_TYPE_IPV6);
	emask.hdr.ether_type = UINT16_MAX;
	items[0] = (struct rte_flow_item){.type = RTE_FLOW_ITEM_TYPE_ETH, .spec = &eth, .mask = &emask};
	if (s->original.family == MWAN_NATIVE_FAMILY_IPV4) {
		memcpy(&ip4.hdr.src_addr, s->original.source, 4);
		memcpy(&ip4.hdr.dst_addr, s->original.destination, 4);
		ip4.hdr.next_proto_id = s->original.protocol;
		m4.hdr.src_addr = UINT32_MAX;
		m4.hdr.dst_addr = UINT32_MAX;
		m4.hdr.next_proto_id = UINT8_MAX;
		items[1] = (struct rte_flow_item){.type = RTE_FLOW_ITEM_TYPE_IPV4, .spec = &ip4, .mask = &m4};
	} else {
		memcpy(&ip6.hdr.src_addr, s->original.source, 16);
		memcpy(&ip6.hdr.dst_addr, s->original.destination, 16);
		ip6.hdr.proto = s->original.protocol;
		memset(&m6.hdr.src_addr, 0xff, 16);
		memset(&m6.hdr.dst_addr, 0xff, 16);
		m6.hdr.proto = UINT8_MAX;
		items[1] = (struct rte_flow_item){.type = RTE_FLOW_ITEM_TYPE_IPV6, .spec = &ip6, .mask = &m6};
	}
	if (s->original.protocol == MWAN_NATIVE_PROTOCOL_TCP) {
		tcp.hdr.src_port = rte_cpu_to_be_16(s->original.source_port);
		tcp.hdr.dst_port = rte_cpu_to_be_16(s->original.destination_port);
		tm.hdr.src_port = UINT16_MAX;
		tm.hdr.dst_port = UINT16_MAX;
		items[2] = (struct rte_flow_item){.type = RTE_FLOW_ITEM_TYPE_TCP, .spec = &tcp, .mask = &tm};
	} else {
		udp.hdr.src_port = rte_cpu_to_be_16(s->original.source_port);
		udp.hdr.dst_port = rte_cpu_to_be_16(s->original.destination_port);
		um.hdr.src_port = UINT16_MAX;
		um.hdr.dst_port = UINT16_MAX;
		items[2] = (struct rte_flow_item){.type = RTE_FLOW_ITEM_TYPE_UDP, .spec = &udp, .mask = &um};
	}
	items[3].type = RTE_FLOW_ITEM_TYPE_END;
	if (memcmp(s->original.source, s->translated.source, 16)) {
		memcpy(&src4.ipv4_addr, s->translated.source, 4);
		actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_SET_IPV4_SRC, .conf = &src4};
	}
	if (memcmp(s->original.destination, s->translated.destination, 16)) {
		memcpy(&dst4.ipv4_addr, s->translated.destination, 4);
		actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_SET_IPV4_DST, .conf = &dst4};
	}
	if (s->original.source_port != s->translated.source_port) {
		srcp.port = rte_cpu_to_be_16(s->translated.source_port);
		actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_SET_TP_SRC, .conf = &srcp};
	}
	if (s->original.destination_port != s->translated.destination_port) {
		dstp.port = rte_cpu_to_be_16(s->translated.destination_port);
		actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_SET_TP_DST, .conf = &dstp};
	}
	actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_COUNT, .conf = &count};
	actions[n++] = (struct rte_flow_action){.type = RTE_FLOW_ACTION_TYPE_PORT_ID, .conf = &port};
	actions[n].type = RTE_FLOW_ACTION_TYPE_END;
	uint16_t controller = o->controllers[o->config.origins[s->origin_id].controller].port;
	if (validate) {
		int rc = rte_flow_validate(controller, &attr, items, actions, &detail);
		return rc ? mwn_flow_error(e, rc, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, leg->index, &detail) : 0;
	}
	leg->flow = rte_flow_create(controller, &attr, items, actions, &detail);
	return leg->flow ? 0 : mwn_flow_error(e, -rte_errno, MWAN_NATIVE_OP_CREATE, MWAN_NATIVE_OBJECT_ORIGIN, s->origin_id, leg->index, &detail);
}

void mwn_pair_result(struct mwan_native *o, uint32_t slot, struct mwan_native_pair_result *r)
{
	static const uint32_t masks[MWAN_NATIVE_DIRECTION_COUNT] = {1U, 2U};
	struct mwn_pair *p = &o->pairs[slot];
	memset(r, 0, sizeof(*r));
	r->handle = (struct mwan_native_handle){.instance = o->instance, .generation = p->generation, .slot = slot};
	r->state = p->state;
	r->requested_direction_mask = p->requested_mask;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		if (p->legs[i].flow)
			r->installed_mask |= masks[i];
		if (p->legs[i].delete_failed)
			r->cleanup_failed_mask |= masks[i];
		if (p->legs[i].hash_added)
			r->restoration_mask |= masks[i];
	}
}

static int handle_slot(struct mwan_native *o, struct mwan_native_handle h, struct mwan_native_error *e)
{
	if (h.reserved || h.instance != o->instance || h.slot >= o->config.pair_capacity || !h.generation || o->pairs[h.slot].generation != h.generation)
		return mwn_error(e, ESTALE, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_PAIR, h.slot, 0, "Stale pair handle");
	return 0;
}

void mwn_pair_forget(struct mwan_native *o, uint32_t slot)
{
	struct mwn_pair *p = &o->pairs[slot];
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		struct mwn_leg *leg = &p->legs[i];
		if (leg->hash_added)
			rte_hash_del_key(o->lookup, &leg->key);
		leg->hash_added = false;
	}
	p->state = MWAN_NATIVE_PAIR_ABSENT;
}

int mwn_pair_delete(struct mwan_native *o, uint32_t slot, struct mwan_native_error *e)
{
	struct mwn_pair *p = &o->pairs[slot];
	int failure = 0;
	if (p->state == MWAN_NATIVE_PAIR_ABSENT)
		return 0;
	p->state = MWAN_NATIVE_PAIR_DRAINING;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		struct mwn_leg *leg = &p->legs[i];
		if (leg->delete_failed) {
			failure = mwn_error(e, EIO, MWAN_NATIVE_OP_DESTROY, MWAN_NATIVE_OBJECT_ORIGIN, leg->spec.origin_id, i, "Consumed flow handle cannot be retried");
			continue;
		}
		if (!leg->flow)
			continue;
		struct rte_flow_error detail = {0};
		int rc = rte_flow_destroy(o->controllers[o->config.origins[leg->spec.origin_id].controller].port, leg->flow, &detail);
		/* BNXT can consume the FID even when hardware deletion fails. */
		leg->flow = NULL;
		if (rc) {
			leg->delete_failed = true;
			failure = mwn_flow_error(e, rc, MWAN_NATIVE_OP_DESTROY, MWAN_NATIVE_OBJECT_ORIGIN, leg->spec.origin_id, i, &detail);
		}
	}
	if (failure) {
		p->state = MWAN_NATIVE_PAIR_FAULTED;
		o->faulted = true;
	}
	return failure;
}

int mwan_native_pair_install(struct mwan_native *o, const struct mwan_native_pair_spec *s,
			     struct mwan_native_pair_result *r, struct mwan_native_error *e)
{
	int rc = mwn_owner(o, e);
	if (rc)
		return rc;
	if (!s || !r || !o->active || o->faulted || o->quiescing ||
	    !s->requested_direction_mask || s->requested_direction_mask > 3 || s->reserved)
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CREATE, MWAN_NATIVE_OBJECT_PAIR, 0, 0, "Worker cannot admit a pair");
	static const uint32_t masks[MWAN_NATIVE_DIRECTION_COUNT] = {1U, 2U};
	memset(r, 0, sizeof(*r));
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		if (!(s->requested_direction_mask & masks[i]))
			continue;
		rc = direction_valid(o, &s->directions[i], e, i);
		if (rc)
			return rc;
	}
	uint32_t slot = o->config.pair_capacity;
	for (uint32_t i = 0; i < o->config.pair_capacity; i++) {
		struct mwn_pair *p = &o->pairs[i];
		if (p->state == MWAN_NATIVE_PAIR_ABSENT) {
			if (slot == o->config.pair_capacity && p->generation != UINT64_MAX)
				slot = i;
			continue;
		}
		for (uint32_t a = 0; a < MWAN_NATIVE_DIRECTION_COUNT; a++)
			for (uint32_t b = 0; b < MWAN_NATIVE_DIRECTION_COUNT; b++)
				if ((s->requested_direction_mask & masks[a]) && (p->requested_mask & masks[b]) &&
				    collision(o, &s->directions[a], &p->legs[b].spec))
					return mwn_error(e, EEXIST, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->directions[a].origin_id, a, "Direction conflicts with an existing packet or restoration key");
	}
	if (slot == o->config.pair_capacity)
		return mwn_error(e, ENOSPC, MWAN_NATIVE_OP_CREATE, MWAN_NATIVE_OBJECT_PAIR, MWAN_NATIVE_NO_ID, 0, "Pair capacity exhausted");
	if (s->requested_direction_mask == 3 && collision(o, &s->directions[0], &s->directions[1]))
		return mwn_error(e, EEXIST, MWAN_NATIVE_OP_VALIDATE, MWAN_NATIVE_OBJECT_ORIGIN, s->directions[0].origin_id, 0, "Pair directions overlap");
	struct mwn_pair *p = &o->pairs[slot];
	uint64_t generation = p->generation + 1;
	memset(p, 0, sizeof(*p));
	p->generation = generation;
	p->cookie = s->cookie;
	p->policy_generation = s->policy_generation;
	p->requested_mask = s->requested_direction_mask;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		struct mwn_leg *leg = &p->legs[i];
		leg->spec = s->directions[i];
		leg->pair = p;
		leg->index = i;
		leg->key = (struct mwn_key){.origin = leg->spec.origin_id, .tuple = leg->spec.translated};
		if (!(p->requested_mask & masks[i]))
			continue;
		rc = build_flow(o, leg, true, e);
		if (rc)
			return rc;
	}
	p->state = MWAN_NATIVE_PAIR_DRAINING;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		struct mwn_leg *leg = &p->legs[i];
		if (!(p->requested_mask & masks[i]))
			continue;
		rc = rte_hash_add_key_data(o->lookup, &leg->key, leg);
		if (rc < 0) {
			mwn_error(e, -rc, MWAN_NATIVE_OP_CREATE, MWAN_NATIVE_OBJECT_ORIGIN, leg->spec.origin_id, i, "Packet lookup allocation failed");
			goto rollback;
		}
		leg->hash_added = true;
	}
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		if (!(p->requested_mask & masks[i]))
			continue;
		rc = build_flow(o, &p->legs[i], false, e);
		if (rc)
			goto rollback;
	}
	p->state = MWAN_NATIVE_PAIR_INSTALLED;
	mwn_pair_result(o, slot, r);
	return 0;
rollback: {
	struct mwan_native_error original = *e;
	int cleanup = mwn_pair_delete(o, slot, e);
	if (!cleanup) {
		*e = original;
	}
	mwn_pair_result(o, slot, r);
	return cleanup ? cleanup : rc;
}
}

int mwan_native_pair_remove(struct mwan_native *o, struct mwan_native_handle h,
			    struct mwan_native_pair_result *r, struct mwan_native_error *e)
{
	int rc = mwn_owner(o, e);
	if (rc)
		return rc;
	if (!r)
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_DESTROY, MWAN_NATIVE_OBJECT_PAIR, h.slot, 0, "Pair result is required");
	rc = handle_slot(o, h, e);
	if (rc)
		return rc;
	rc = mwn_pair_delete(o, h.slot, e);
	mwn_pair_result(o, h.slot, r);
	return rc;
}

int mwan_native_pair_query(struct mwan_native *o, struct mwan_native_handle h,
			   struct mwan_native_pair_stats *s, struct mwan_native_error *e)
{
	int rc = mwn_owner(o, e), failure = 0;
	if (rc)
		return rc;
	if (!s)
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_QUERY, MWAN_NATIVE_OBJECT_PAIR, h.slot, 0, "Counter result is required");
	memset(s, 0, sizeof(*s));
	rc = handle_slot(o, h, e);
	if (rc)
		return rc;
	mwn_pair_result(o, h.slot, &s->result);
	struct mwn_pair *p = &o->pairs[h.slot];
	static const uint32_t masks[MWAN_NATIVE_DIRECTION_COUNT] = {1U, 2U};
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		struct mwn_leg *leg = &p->legs[i];
		struct mwan_native_counter *out = &s->directions[i];
		if (!(p->requested_mask & masks[i])) {
			out->error = -ENOTSUP;
			continue;
		}
		struct rte_flow_query_count count = {0};
		struct rte_flow_action action = {.type = RTE_FLOW_ACTION_TYPE_COUNT};
		struct rte_flow_error detail = {0};
		if (p->state != MWAN_NATIVE_PAIR_INSTALLED || !leg->flow)
			rc = -ENOENT;
		else
			rc = rte_flow_query(o->controllers[o->config.origins[leg->spec.origin_id].controller].port, leg->flow, &action, &count, &detail);
		if (!rc && (!count.hits_set || !count.bytes_set))
			rc = -EAGAIN;
		out->error = rc;
		if (rc) {
			if (!failure)
				failure = mwn_flow_error(e, rc, MWAN_NATIVE_OP_QUERY, MWAN_NATIVE_OBJECT_ORIGIN, leg->spec.origin_id, i, &detail);
			continue;
		}
		out->packets = count.hits;
		out->bytes = count.bytes;
		out->packets_valid = 1;
		out->bytes_valid = 1;
		out->observed_monotonic_ns = mwn_now();
	}
	return failure;
}
