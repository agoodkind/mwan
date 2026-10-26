/* SPDX-License-Identifier: BSD-3-Clause */
#include "internal.h"
#include "mwan_native_build.h"
#include <ctype.h>
#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <rte_cycles.h>
#include <rte_eal.h>
#include <rte_errno.h>
#include <rte_jhash.h>
#include <rte_lcore.h>
#include <rte_random.h>
#include <rte_version.h>
#include <rte_pmd_bnxt.h>

uint32_t mwan_native_abi_version(void) { return MWAN_NATIVE_ABI_VERSION; }

int mwan_native_build_info(struct mwan_native_build_info *info)
{
	if (!info)
		return -EINVAL;
	memset(info, 0, sizeof(*info));
	info->abi_version = MWAN_NATIVE_ABI_VERSION;
	info->config_size = sizeof(struct mwan_native_config);
	info->pair_spec_size = sizeof(struct mwan_native_pair_spec);
	snprintf(info->dpdk_version, sizeof(info->dpdk_version), "%s", rte_version());
	snprintf(info->source_sha256, sizeof(info->source_sha256), "%s", MWAN_DPDK_SOURCE_SHA256);
	snprintf(info->patch_sha256, sizeof(info->patch_sha256), "%s", MWAN_DPDK_PATCH_SHA256);
	return 0;
}

int mwn_error(struct mwan_native_error *error, int code, uint32_t operation,
	      uint32_t endpoint, uint32_t direction, const char *message)
{
	if (code < 0)
		code = -code;
	if (!code)
		code = EIO;
	if (error) {
		memset(error, 0, sizeof(*error));
		error->code = code;
		error->operation = operation;
		error->endpoint = endpoint;
		error->direction = direction;
		snprintf(error->message, sizeof(error->message), "%s", message);
	}
	return -code;
}

int mwn_flow_error(struct mwan_native_error *error, int code, uint32_t operation,
		   uint32_t endpoint, uint32_t direction, const struct rte_flow_error *detail)
{
	int rc = mwn_error(error, code, operation, endpoint, direction,
			   detail && detail->message ? detail->message : "DPDK flow operation failed");
	if (error && detail)
		error->flow_error_type = detail->type;
	return rc;
}

int mwn_owner(struct mwan_native *owner, struct mwan_native_error *error)
{
	if (!error)
		return -EINVAL;
	memset(error, 0, sizeof(*error));
	if (!owner)
		return mwn_error(error, ENODEV, MWAN_NATIVE_OP_EAL, 0, 0, "EAL owner is unavailable");
	if (!pthread_equal(owner->thread, pthread_self()))
		return mwn_error(error, EPERM, MWAN_NATIVE_OP_EAL, 0, 0, "Operation requires the EAL owner thread");
	if (owner->terminal_failure.code) {
		*error = owner->terminal_failure;
		return -error->code;
	}
	if (!owner->eal_ready)
		return mwn_error(error, ENODEV, MWAN_NATIVE_OP_EAL, 0, 0, "EAL owner is unavailable");
	return 0;
}

uint64_t mwn_now(void)
{
	struct timespec now;
	if (clock_gettime(CLOCK_MONOTONIC, &now))
		return 0;
	return (uint64_t)now.tv_sec * UINT64_C(1000000000) + (uint64_t)now.tv_nsec;
}

static bool pci_valid(struct mwan_native_pci pci)
{
	return pci.device < 32 && pci.function < 8 &&
	       !pci.reserved[0] && !pci.reserved[1] && !pci.reserved[2];
}

static void pci_name(char *out, size_t size, struct mwan_native_pci pci)
{
	snprintf(out, size, "%04x:%02x:%02x.%x", pci.domain, pci.bus, pci.device, pci.function);
}

static int config_valid(const struct mwan_native_config *config, struct mwan_native_error *error)
{
	if (!config || config->abi_version != MWAN_NATIVE_ABI_VERSION || config->record_size != sizeof(*config) ||
	    !config->endpoint_count || config->endpoint_count > MWAN_NATIVE_MAX_ENDPOINTS ||
	    !config->pair_capacity || config->pair_capacity > 65536 || !config->memory_mb ||
	    !config->drain_timeout_ms || !config->rx_descriptors || !config->tx_descriptors ||
	    config->mbuf_count < config->rx_descriptors || config->mbuf_count > UINT32_C(1048576) ||
	    config->mtu < RTE_ETHER_MIN_MTU || config->mtu > 9600 || config->reserved ||
	    config->memory_mode > MWAN_NATIVE_MEMORY_ANONYMOUS ||
	    !memchr(config->file_prefix, 0, sizeof(config->file_prefix)) || !config->file_prefix[0] ||
	    !memchr(config->hugepage_directory, 0, sizeof(config->hugepage_directory)))
		return mwn_error(error, EINVAL, MWAN_NATIVE_OP_CONFIG, 0, 0, "Invalid native configuration");
	for (const char *p = config->file_prefix; *p; p++)
		if (!isalnum((unsigned char)*p) && *p != '_' && *p != '-')
			return mwn_error(error, EINVAL, MWAN_NATIVE_OP_CONFIG, 0, 0, "Invalid EAL file prefix");
	if (config->hugepage_directory[0] && config->hugepage_directory[0] != '/')
		return mwn_error(error, EINVAL, MWAN_NATIVE_OP_CONFIG, 0, 0, "Hugepage directory must be absolute");
	for (uint32_t i = 0; i < config->endpoint_count; i++) {
		const struct mwan_native_endpoint *ep = &config->endpoints[i];
		if (ep->reserved || ep->mode > MWAN_NATIVE_ENDPOINT_OUTPUT_ONLY || !pci_valid(ep->data) ||
		    (ep->mode == MWAN_NATIVE_ENDPOINT_INGRESS &&
		     (!pci_valid(ep->controller) || ep->kernel_vf == ep->data_vf ||
		      !memcmp(&ep->controller, &ep->data, sizeof(ep->data)))))
			return mwn_error(error, EINVAL, MWAN_NATIVE_OP_CONFIG, i, 0, "Invalid endpoint ownership");
		for (uint32_t j = 0; j < i; j++) {
			const struct mwan_native_endpoint *other = &config->endpoints[j];
			if (!memcmp(&ep->data, &other->data, sizeof(ep->data)) ||
			    (other->mode == MWAN_NATIVE_ENDPOINT_INGRESS &&
			     !memcmp(&ep->data, &other->controller, sizeof(ep->data))) ||
			    (ep->mode == MWAN_NATIVE_ENDPOINT_INGRESS &&
			     !memcmp(&ep->controller, &other->data, sizeof(ep->controller))) ||
			    (ep->mode == MWAN_NATIVE_ENDPOINT_INGRESS && other->mode == MWAN_NATIVE_ENDPOINT_INGRESS &&
			     !memcmp(&ep->controller, &other->controller, sizeof(ep->controller))))
				return mwn_error(error, EINVAL, MWAN_NATIVE_OP_CONFIG, i, 0, "Endpoint PCI functions overlap");
		}
	}
	return 0;
}

static int start_eal(struct mwan_native *owner, struct mwan_native_error *error)
{
	char values[32][512];
	char *args[32];
	uint32_t n = 0;
#define ARG(format, ...) do { \
	snprintf(values[n], sizeof(values[n]), format, ##__VA_ARGS__); \
	args[n] = values[n]; n++; \
} while (0)
	ARG("mwan-native");
	ARG("--lcores=0@%u", owner->config.cpu);
	ARG("--main-lcore=0");
	ARG("-m");
	ARG("%u", owner->config.memory_mb);
	ARG("--file-prefix=%s", owner->config.file_prefix);
	ARG("--no-telemetry");
	if (owner->config.memory_mode == MWAN_NATIVE_MEMORY_ANONYMOUS) {
		ARG("--no-huge");
		ARG("--no-shconf");
	} else if (owner->config.hugepage_directory[0]) {
		ARG("--huge-dir=%s", owner->config.hugepage_directory);
	}
	for (uint32_t i = 0; i < owner->config.endpoint_count; i++) {
		const struct mwan_native_endpoint *ep = &owner->config.endpoints[i];
		char name[32];
		if (ep->mode == MWAN_NATIVE_ENDPOINT_INGRESS) {
			pci_name(name, sizeof(name), ep->controller);
			ARG("-a");
			ARG("%s,representor=[%u,%u],kernel-owned-representors=[%u],app-id=0", name, ep->kernel_vf, ep->data_vf, ep->kernel_vf);
		}
		pci_name(name, sizeof(name), ep->data);
		ARG("-a");
		ARG("%s,app-id=255", name);
	}
#undef ARG
	int rc = rte_eal_init((int)n, args);
	if (rc < 0)
		return mwn_error(error, rte_errno, MWAN_NATIVE_OP_EAL, 0, 0, "EAL initialization failed; inspect DPDK diagnostics");
	owner->eal_ready = true;
	return 0;
}

static int port_id(const char *name, uint16_t *id, uint32_t endpoint, struct mwan_native_error *error)
{
	int rc = rte_eth_dev_get_port_by_name(name, id);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, endpoint, 0, name);
	struct rte_eth_dev_info info;
	rc = rte_eth_dev_info_get(*id, &info);
	if (rc || !info.driver_name || strcmp(info.driver_name, "net_bnxt"))
		return mwn_error(error, ENOTSUP, MWAN_NATIVE_OP_PORT, endpoint, 0, "Endpoint requires the BNXT PMD");
	return 0;
}

static int discover(struct mwan_native *owner, struct mwan_native_error *error)
{
	for (uint32_t i = 0; i < owner->config.endpoint_count; i++) {
		const struct mwan_native_endpoint *ep = &owner->config.endpoints[i];
		struct mwn_endpoint *runtime = &owner->endpoints[i];
		char name[96], bdf[32];
		pci_name(bdf, sizeof(bdf), ep->data);
		int rc = port_id(bdf, &runtime->data, i, error);
		if (rc)
			return rc;
		struct rte_ether_addr mac;
		rc = rte_eth_macaddr_get(runtime->data, &mac);
		if (rc || memcmp(mac.addr_bytes, ep->data_mac, RTE_ETHER_ADDR_LEN))
			return mwn_error(error, EINVAL, MWAN_NATIVE_OP_PORT, i, 0, "DATA VF MAC does not match its configured identity");
		if (ep->mode == MWAN_NATIVE_ENDPOINT_OUTPUT_ONLY)
			continue;
		pci_name(bdf, sizeof(bdf), ep->controller);
		rc = port_id(bdf, &runtime->controller, i, error);
		if (rc)
			return rc;
		snprintf(name, sizeof(name), "net_%s_representor_%u", bdf, ep->kernel_vf);
		rc = port_id(name, &runtime->kernel_rep, i, error);
		if (rc)
			return rc;
		snprintf(name, sizeof(name), "net_%s_representor_%u", bdf, ep->data_vf);
		rc = port_id(name, &runtime->data_rep, i, error);
		if (rc)
			return rc;
		rc = rte_pmd_bnxt_rep_role_set(runtime->kernel_rep, RTE_PMD_BNXT_REP_ROLE_KERNEL);
		if (!rc)
			rc = rte_pmd_bnxt_rep_role_set(runtime->data_rep, RTE_PMD_BNXT_REP_ROLE_DIRECT_DATA);
		if (rc)
			return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, i, 0, "Representor role configuration failed");
	}
	return 0;
}

static int configure_port(struct mwan_native *owner, uint16_t port, struct mwan_native_error *error)
{
	struct rte_eth_conf conf = {0};
	struct rte_eth_dev_info info;
	int rc = rte_eth_dev_info_get(port, &info);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, port, 0, "Device information failed");
	conf.rxmode.mtu = owner->config.mtu;
	rc = rte_eth_dev_configure(port, 1, 1, &conf);
	uint16_t rx = owner->config.rx_descriptors, tx = owner->config.tx_descriptors;
	if (!rc)
		rc = rte_eth_dev_adjust_nb_rx_tx_desc(port, &rx, &tx);
	int socket = rte_eth_dev_socket_id(port);
	if (socket < 0)
		socket = SOCKET_ID_ANY;
	if (!rc)
		rc = rte_eth_rx_queue_setup(port, 0, rx, socket, &info.default_rxconf, owner->pool);
	if (!rc)
		rc = rte_eth_tx_queue_setup(port, 0, tx, socket, &info.default_txconf);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, port, 0, "Port queue configuration failed");
	return 0;
}

static int start_port(struct mwan_native *owner, uint16_t port, struct mwan_native_error *error)
{
	if (port == MWN_NO_PORT || owner->started[port])
		return 0;
	int rc = rte_eth_dev_start(port);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, port, 0, "Port start failed");
	owner->started[port] = true;
	return 0;
}

static int miss(struct mwan_native *owner, uint32_t i, bool install, struct mwan_native_error *error)
{
	struct mwn_endpoint *ep = &owner->endpoints[i];
	struct rte_flow_group_attr attr = {.transfer = 1};
	struct rte_flow_action_port_id destination = {.id = ep->kernel_rep};
	struct rte_flow_action actions[] = {
		{.type = install ? RTE_FLOW_ACTION_TYPE_PORT_ID : RTE_FLOW_ACTION_TYPE_END, .conf = install ? &destination : NULL},
		{.type = RTE_FLOW_ACTION_TYPE_END}
	};
	struct rte_flow_error detail = {0};
	int rc = rte_flow_group_set_miss_actions(ep->controller, 0, &attr, actions, &detail);
	if (rc)
		return mwn_flow_error(error, rc, MWAN_NATIVE_OP_MISS, i, 0, &detail);
	ep->miss = install;
	return 0;
}

static int activate(struct mwan_native *owner, struct mwan_native_error *error)
{
	for (uint32_t i = 0; i < owner->config.endpoint_count; i++) {
		struct mwn_endpoint *ep = &owner->endpoints[i];
		uint16_t ports[] = {ep->controller, ep->data, ep->kernel_rep, ep->data_rep};
		for (uint32_t j = 0; j < RTE_DIM(ports); j++) {
			int rc = start_port(owner, ports[j], error);
			if (rc)
				return rc;
		}
		if (ep->controller != MWN_NO_PORT) {
			int rc = miss(owner, i, true, error);
			if (rc)
				return rc;
		}
	}
	owner->quiescing = false;
	owner->active = true;
	return 0;
}

int mwan_native_open(const struct mwan_native_config *config, struct mwan_native **result, struct mwan_native_error *error)
{
	if (!result || !error)
		return -EINVAL;
	memset(error, 0, sizeof(*error));
	int rc = config_valid(config, error);
	if (rc)
		return rc;
	if (*result) {
		struct mwan_native *owner = *result;
		rc = mwn_owner(owner, error);
		if (rc)
			return rc;
		if (owner->faulted || memcmp(config, &owner->config, sizeof(*config)))
			return mwn_error(error, EBUSY, MWAN_NATIVE_OP_CONFIG, 0, 0, "Reopen requires the identical healthy EAL configuration");
		return owner->active ? 0 : activate(owner, error);
	}
	struct mwan_native *owner = calloc(1, sizeof(*owner));
	if (!owner)
		return mwn_error(error, ENOMEM, MWAN_NATIVE_OP_EAL, 0, 0, "Owner allocation failed");
	owner->config = *config;
	owner->thread = pthread_self();
	for (uint32_t i = 0; i < MWAN_NATIVE_MAX_ENDPOINTS; i++)
		owner->endpoints[i] = (struct mwn_endpoint){.controller = MWN_NO_PORT, .kernel_rep = MWN_NO_PORT, .data_rep = MWN_NO_PORT, .data = MWN_NO_PORT};
	rc = start_eal(owner, error);
	if (rc) {
		free(owner);
		return rc;
	}
	*result = owner;
	owner->instance = rte_rand();
	if (!owner->instance)
		owner->instance = 1;
	owner->pairs = calloc(config->pair_capacity, sizeof(*owner->pairs));
	owner->pending = calloc(config->mbuf_count, sizeof(*owner->pending));
	char name[64];
	snprintf(name, sizeof(name), "mwn_pool_%" PRIx64, owner->instance);
	owner->pool = rte_pktmbuf_pool_create(name, config->mbuf_count, 0, 0,
		(uint16_t)(config->mtu + RTE_PKTMBUF_HEADROOM + RTE_ETHER_HDR_LEN + 2 * RTE_VLAN_HLEN), SOCKET_ID_ANY);
	snprintf(name, sizeof(name), "mwn_hash_%" PRIx64, owner->instance);
	struct rte_hash_parameters hash = {.name = name, .entries = RTE_MAX(8U, config->pair_capacity * 4),
		.key_len = sizeof(struct mwn_key), .hash_func = rte_jhash, .socket_id = SOCKET_ID_ANY};
	owner->lookup = rte_hash_create(&hash);
	if (!owner->pairs || !owner->pending || !owner->pool || !owner->lookup) {
		rc = mwn_error(error, ENOMEM, MWAN_NATIVE_OP_EAL, 0, 0, "Packet or pair storage allocation failed");
		goto failed;
	}
	rc = discover(owner, error);
	if (rc)
		goto failed;
	for (uint32_t i = 0; i < config->endpoint_count; i++) {
		struct mwn_endpoint *ep = &owner->endpoints[i];
		uint16_t ports[] = {ep->controller, ep->kernel_rep, ep->data_rep, ep->data};
		for (uint32_t j = 0; j < RTE_DIM(ports); j++) {
			if (ports[j] == MWN_NO_PORT)
				continue;
			rc = configure_port(owner, ports[j], error);
			if (rc)
				goto failed;
		}
	}
	rc = activate(owner, error);
	if (rc)
		goto failed;
	return 0;
failed:
	owner->faulted = true;
	return rc;
}

int mwn_drain(struct mwan_native *owner, struct mwan_native_error *error)
{
	uint64_t deadline = mwn_now() + (uint64_t)owner->config.drain_timeout_ms * UINT64_C(1000000);
	uint64_t quiet_since = 0;
	do {
		uint64_t before = owner->stats.received;
		int rc = mwn_poll(owner, MWN_BURST * owner->config.endpoint_count, error);
		if (rc)
			return rc;
		uint64_t now = mwn_now();
		if (before == owner->stats.received && !owner->pending_count) {
			if (!quiet_since)
				quiet_since = now;
			if (now - quiet_since >= UINT64_C(1000000))
				return 0;
		} else {
			quiet_since = 0;
		}
		rte_delay_us_sleep(50);
	} while (mwn_now() < deadline);
	return mwn_error(error, ETIMEDOUT, MWAN_NATIVE_OP_DRAIN, 0, 0, "Packet drain did not become quiet; restoration records retained");
}

int mwan_native_poll(struct mwan_native *owner, uint32_t budget,
		     struct mwan_native_worker_stats *stats, struct mwan_native_error *error)
{
	int rc = mwn_owner(owner, error);
	if (rc)
		return rc;
	if (!stats || !budget)
		return mwn_error(error, EINVAL, MWAN_NATIVE_OP_PACKET, 0, 0, "Packet budget and statistics are required");
	if (owner->active)
		rc = mwn_poll(owner, budget, error);
	*stats = owner->stats;
	return rc;
}

int mwan_native_close(struct mwan_native **pointer, uint32_t mode, struct mwan_native_error *error)
{
	if (!pointer || !error || mode > MWAN_NATIVE_FINAL_SHUTDOWN)
		return -EINVAL;
	if (!*pointer)
		return 0;
	struct mwan_native *owner = *pointer;
	int rc = mwn_owner(owner, error);
	if (rc)
		return rc;
	owner->quiescing = true;
	if (owner->pairs) {
		for (uint32_t i = 0; i < owner->config.pair_capacity; i++) {
			rc = mwn_pair_delete(owner, i, error);
			if (rc)
				return rc;
		}
	}
	if (owner->pool && owner->lookup && owner->pending) {
		rc = mwn_drain(owner, error);
		if (rc)
			return rc;
	}
	for (uint32_t i = 0; i < owner->config.endpoint_count; i++) {
		struct mwn_endpoint *ep = &owner->endpoints[i];
		if (ep->miss) {
			rc = miss(owner, i, false, error);
			if (rc) {
				owner->faulted = true;
				return rc;
			}
		}
		uint16_t ports[] = {ep->data, ep->data_rep, ep->kernel_rep, ep->controller};
		for (uint32_t j = 0; j < RTE_DIM(ports); j++) {
			uint16_t port = ports[j];
			if (port == MWN_NO_PORT || !owner->started[port])
				continue;
			rc = rte_eth_dev_stop(port);
			if (rc) {
				owner->faulted = true;
				return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, i, 0, "Port stop failed");
			}
			owner->started[port] = false;
		}
	}
	owner->active = false;
	/* A successful DATA stop releases queued buffers before record reuse. */
	if (owner->pairs && owner->lookup)
		for (uint32_t i = 0; i < owner->config.pair_capacity; i++)
			mwn_pair_forget(owner, i);
	if (mode == MWAN_NATIVE_DISABLE)
		return 0;
	for (uint32_t i = 0; i < owner->config.endpoint_count; i++) {
		struct mwn_endpoint *ep = &owner->endpoints[i];
		uint16_t ports[] = {ep->data, ep->data_rep, ep->kernel_rep, ep->controller};
		for (uint32_t j = 0; j < RTE_DIM(ports); j++) {
			if (ports[j] == MWN_NO_PORT || !rte_eth_dev_is_valid_port(ports[j]))
				continue;
			rc = rte_eth_dev_close(ports[j]);
			if (rc) {
				rc = mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, i, 0, "Port close failed; process recovery is required");
				owner->terminal_failure = *error;
				owner->faulted = true;
				return rc;
			}
		}
	}
	rte_hash_free(owner->lookup);
	rte_mempool_free(owner->pool);
	owner->lookup = NULL;
	owner->pool = NULL;
	rc = rte_eal_cleanup();
	owner->eal_ready = false;
	if (rc) {
		rc = mwn_error(error, -rc, MWAN_NATIVE_OP_EAL, 0, 0, "Final EAL cleanup failed; process recovery is required");
		owner->terminal_failure = *error;
		owner->faulted = true;
		return rc;
	}
	free(owner->pairs);
	free(owner->pending);
	free(owner);
	*pointer = NULL;
	return 0;
}
