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
	info->direction_size = sizeof(struct mwan_native_direction);
	info->controller_size = sizeof(struct mwan_native_controller);
	info->receiver_size = sizeof(struct mwan_native_receiver);
	info->data_size = sizeof(struct mwan_native_data);
	info->origin_size = sizeof(struct mwan_native_origin);
	info->error_size = sizeof(struct mwan_native_error);
	info->worker_stats_size = sizeof(struct mwan_native_worker_stats);
	info->pair_stats_size = sizeof(struct mwan_native_pair_stats);
	snprintf(info->dpdk_version, sizeof(info->dpdk_version), "%s", rte_version());
	snprintf(info->source_sha256, sizeof(info->source_sha256), "%s", MWAN_DPDK_SOURCE_SHA256);
	snprintf(info->patch_sha256, sizeof(info->patch_sha256), "%s", MWAN_DPDK_PATCH_SHA256);
	return 0;
}

int mwn_error(struct mwan_native_error *error, int code, uint32_t operation,
	      uint32_t object_kind, uint32_t object_id, uint32_t direction, const char *message)
{
	if (code < 0)
		code = -code;
	if (!code)
		code = EIO;
	if (error) {
		memset(error, 0, sizeof(*error));
		error->code = code;
		error->operation = operation;
		error->object_kind = object_kind;
		error->object_id = object_id;
		error->direction = direction;
		snprintf(error->message, sizeof(error->message), "%s", message);
	}
	return -code;
}

int mwn_flow_error(struct mwan_native_error *error, int code, uint32_t operation,
		   uint32_t object_kind, uint32_t object_id, uint32_t direction, const struct rte_flow_error *detail)
{
	int rc = mwn_error(error, code, operation, object_kind, object_id, direction,
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
		return mwn_error(error, ENODEV, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "EAL owner is unavailable");
	if (!pthread_equal(owner->thread, pthread_self()))
		return mwn_error(error, EPERM, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Operation requires the EAL owner thread");
	if (owner->terminal_failure.code) {
		*error = owner->terminal_failure;
		return -error->code;
	}
	if (!owner->eal_ready)
		return mwn_error(error, ENODEV, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "EAL owner is unavailable");
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

static bool mac_valid(const uint8_t mac[6])
{
	struct rte_ether_addr address;
	memcpy(&address, mac, sizeof(address));
	return rte_is_valid_assigned_ether_addr(&address);
}

static int controllers_valid(const struct mwan_native_config *c, struct mwan_native_error *e)
{
	for (uint32_t i = 0; i < c->controller_count; i++) {
		const struct mwan_native_controller *controller = &c->controllers[i];
		if (!pci_valid(controller->pci) || controller->reserved ||
		    controller->physical_receiver >= c->receiver_count ||
		    c->receivers[controller->physical_receiver].controller != i)
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_CONTROLLER, i, 0, "Invalid controller or physical miss receiver");
		for (uint32_t j = 0; j < i; j++)
			if (!memcmp(&controller->pci, &c->controllers[j].pci, sizeof(controller->pci)))
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_CONTROLLER, i, 0, "Controller PCI function appears more than once");
	}
	return 0;
}

static int receivers_valid(const struct mwan_native_config *c, struct mwan_native_error *e)
{
	for (uint32_t i = 0; i < c->receiver_count; i++) {
		const struct mwan_native_receiver *receiver = &c->receivers[i];
		if (receiver->controller >= c->controller_count || receiver->reserved ||
		    receiver->padding[0] || receiver->padding[1] || !mac_valid(receiver->mac))
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_RECEIVER, i, 0, "Invalid kernel receiver");
		for (uint32_t j = 0; j < i; j++) {
			const struct mwan_native_receiver *other = &c->receivers[j];
			if (receiver->controller == other->controller &&
			    (receiver->vf == other->vf || !memcmp(receiver->mac, other->mac, 6)))
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_RECEIVER, i, 0, "Kernel receiver VF or MAC appears more than once on a controller");
		}
	}
	return 0;
}

static int data_valid(const struct mwan_native_config *c, struct mwan_native_error *e)
{
	uint32_t port_count = c->controller_count + c->receiver_count + c->data_count;
	for (uint32_t i = 0; i < c->data_count; i++) {
		const struct mwan_native_data *data = &c->data[i];
		if (!pci_valid(data->pci) || data->reserved || data->padding[0] || data->padding[1] ||
		    !mac_valid(data->mac) ||
		    (data->controller != MWAN_NATIVE_NO_ID && data->controller >= c->controller_count) ||
		    (data->controller == MWAN_NATIVE_NO_ID && data->vf))
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_DATA, i, 0, "Invalid DATA device");
		for (uint32_t j = 0; j < c->controller_count; j++)
			if (!memcmp(&data->pci, &c->controllers[j].pci, sizeof(data->pci)))
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_DATA, i, 0, "DATA and controller PCI functions overlap");
		for (uint32_t j = 0; j < i; j++) {
			const struct mwan_native_data *other = &c->data[j];
			if (!memcmp(&data->pci, &other->pci, sizeof(data->pci)) ||
			    (data->controller != MWAN_NATIVE_NO_ID && data->controller == other->controller &&
			     (data->vf == other->vf || !memcmp(data->mac, other->mac, 6))))
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_DATA, i, 0, "DATA device or represented VF appears more than once");
		}
		if (data->controller == MWAN_NATIVE_NO_ID)
			continue;
		port_count++;
		for (uint32_t j = 0; j < c->receiver_count; j++)
			if (data->controller == c->receivers[j].controller &&
			    (data->vf == c->receivers[j].vf || !memcmp(data->mac, c->receivers[j].mac, 6)))
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_DATA, i, 0, "DATA and kernel receiver identities overlap");
	}
	if (port_count > RTE_MAX_ETHPORTS)
		return mwn_error(e, ENOSPC, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Graph exceeds the SDK ethdev limit");
	return 0;
}

static int origins_valid(const struct mwan_native_config *c, struct mwan_native_error *e)
{
	for (uint32_t i = 0; i < c->origin_count; i++) {
		const struct mwan_native_origin *origin = &c->origins[i];
		if (origin->controller >= c->controller_count || origin->receiver >= c->receiver_count ||
		    origin->data >= c->data_count || origin->reserved ||
		    c->receivers[origin->receiver].controller != origin->controller ||
		    c->data[origin->data].controller != origin->controller)
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_ORIGIN, i, 0, "Origin references do not identify one controller");
		if (origin->kind != MWAN_NATIVE_ORIGIN_PHYSICAL || origin->return_mode != MWAN_NATIVE_RETURN_ORIGINAL)
			return mwn_error(e, ENOTSUP, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_ORIGIN, i, 0, "Only physical origins with original-frame return are supported");
		if (origin->source_vf)
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_ORIGIN, i, 0, "Physical origin must not select a source VF");
		for (uint32_t j = 0; j < i; j++)
			if (origin->data == c->origins[j].data)
				return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_ORIGIN, i, 0, "Origins require distinct DATA RX bindings");
	}
	return 0;
}

static int config_valid(const struct mwan_native_config *c, struct mwan_native_error *e)
{
	if (!c || c->abi_version != MWAN_NATIVE_ABI_VERSION || c->record_size != sizeof(*c) ||
	    c->controller_count > MWAN_NATIVE_MAX_CONTROLLERS || c->receiver_count > MWAN_NATIVE_MAX_RECEIVERS ||
	    !c->data_count || c->data_count > MWAN_NATIVE_MAX_DATA || c->origin_count > MWAN_NATIVE_MAX_ORIGINS ||
	    !c->pair_capacity || c->pair_capacity > 65536 || !c->memory_mb ||
	    !c->drain_timeout_ms || !c->rx_descriptors || !c->tx_descriptors ||
	    c->mbuf_count < c->rx_descriptors || c->mbuf_count > UINT32_C(1048576) ||
	    c->mtu < RTE_ETHER_MIN_MTU || c->mtu > 9600 || c->reserved ||
	    c->memory_mode > MWAN_NATIVE_MEMORY_ANONYMOUS ||
	    !memchr(c->file_prefix, 0, sizeof(c->file_prefix)) || !c->file_prefix[0] ||
	    !memchr(c->hugepage_directory, 0, sizeof(c->hugepage_directory)))
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Invalid native configuration or ABI");
	for (const char *p = c->file_prefix; *p; p++)
		if (!isalnum((unsigned char)*p) && *p != '_' && *p != '-')
			return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Invalid EAL file prefix");
	if (c->hugepage_directory[0] && c->hugepage_directory[0] != '/')
		return mwn_error(e, EINVAL, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Hugepage directory must be absolute");
	int rc = controllers_valid(c, e);
	if (!rc)
		rc = receivers_valid(c, e);
	if (!rc)
		rc = data_valid(c, e);
	if (!rc)
		rc = origins_valid(c, e);
	return rc;
}

static int append_vf(char *list, size_t size, uint16_t vf)
{
	size_t used = strlen(list);
	int written = snprintf(list + used, size - used, "%s%u", used ? "," : "", vf);
	return written < 0 || (size_t)written >= size - used ? -E2BIG : 0;
}

static int start_eal(struct mwan_native *owner, struct mwan_native_error *error)
{
	char values[12 + 2 * (MWAN_NATIVE_MAX_CONTROLLERS + MWAN_NATIVE_MAX_DATA)][512];
	char *args[RTE_DIM(values)];
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
	for (uint32_t i = 0; i < owner->config.controller_count; i++) {
		char name[32], representors[128] = "", kernels[128] = "";
		for (uint32_t j = 0; j < owner->config.receiver_count; j++) {
			const struct mwan_native_receiver *receiver = &owner->config.receivers[j];
			if (receiver->controller != i)
				continue;
			if (append_vf(representors, sizeof(representors), receiver->vf) ||
			    append_vf(kernels, sizeof(kernels), receiver->vf))
				return mwn_error(error, E2BIG, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_CONTROLLER, i, 0, "Representor argument exceeds its bound");
		}
		for (uint32_t j = 0; j < owner->config.data_count; j++)
			if (owner->config.data[j].controller == i &&
			    append_vf(representors, sizeof(representors), owner->config.data[j].vf))
				return mwn_error(error, E2BIG, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_CONTROLLER, i, 0, "Representor argument exceeds its bound");
		pci_name(name, sizeof(name), owner->config.controllers[i].pci);
		ARG("-a");
		ARG("%s,representor=[%s],kernel-owned-representors=[%s],app-id=0", name, representors, kernels);
	}
	for (uint32_t i = 0; i < owner->config.data_count; i++) {
		char name[32];
		pci_name(name, sizeof(name), owner->config.data[i].pci);
		ARG("-a");
		ARG("%s,app-id=255", name);
	}
#undef ARG
	int rc = rte_eal_init((int)n, args);
	if (rc < 0)
		return mwn_error(error, rte_errno, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "EAL initialization failed; inspect DPDK diagnostics");
	owner->eal_ready = true;
	return 0;
}

static int port_id(const char *name, uint16_t *id, uint32_t kind, uint32_t index, struct mwan_native_error *error)
{
	int rc = rte_eth_dev_get_port_by_name(name, id);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, kind, index, 0, name);
	struct rte_eth_dev_info info;
	rc = rte_eth_dev_info_get(*id, &info);
	if (rc || !info.driver_name || strcmp(info.driver_name, "net_bnxt"))
		return mwn_error(error, ENOTSUP, MWAN_NATIVE_OP_PORT, kind, index, 0, "Graph requires the BNXT PMD");
	return 0;
}

static int representor_id(struct mwan_native *owner, uint32_t controller, uint16_t vf,
			  uint16_t *id, uint32_t kind, uint32_t index, struct mwan_native_error *error)
{
	char name[96], bdf[32];
	pci_name(bdf, sizeof(bdf), owner->config.controllers[controller].pci);
	snprintf(name, sizeof(name), "net_%s_representor_%u", bdf, vf);
	return port_id(name, id, kind, index, error);
}

static int discover(struct mwan_native *owner, struct mwan_native_error *error)
{
	for (uint32_t i = 0; i < owner->config.controller_count; i++) {
		char name[32];
		pci_name(name, sizeof(name), owner->config.controllers[i].pci);
		int rc = port_id(name, &owner->controllers[i].port, MWAN_NATIVE_OBJECT_CONTROLLER, i, error);
		if (rc)
			return rc;
	}
	for (uint32_t i = 0; i < owner->config.receiver_count; i++) {
		const struct mwan_native_receiver *receiver = &owner->config.receivers[i];
		int rc = representor_id(owner, receiver->controller, receiver->vf, &owner->receivers[i], MWAN_NATIVE_OBJECT_RECEIVER, i, error);
		if (rc)
			return rc;
	}
	for (uint32_t i = 0; i < owner->config.data_count; i++) {
		const struct mwan_native_data *data = &owner->config.data[i];
		char name[32];
		pci_name(name, sizeof(name), data->pci);
		int rc = port_id(name, &owner->data[i].port, MWAN_NATIVE_OBJECT_DATA, i, error);
		if (rc)
			return rc;
		struct rte_ether_addr mac;
		rc = rte_eth_macaddr_get(owner->data[i].port, &mac);
		if (rc || memcmp(mac.addr_bytes, data->mac, RTE_ETHER_ADDR_LEN))
			return mwn_error(error, EINVAL, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_DATA, i, 0, "DATA VF MAC does not match its configured identity");
		if (data->controller == MWAN_NATIVE_NO_ID)
			continue;
		rc = representor_id(owner, data->controller, data->vf, &owner->data[i].representor, MWAN_NATIVE_OBJECT_DATA, i, error);
		if (rc)
			return rc;
	}
	/* Resolve the complete graph before changing any representor role. */
	for (uint32_t i = 0; i < owner->config.receiver_count; i++) {
		int rc = rte_pmd_bnxt_rep_role_set(owner->receivers[i], RTE_PMD_BNXT_REP_ROLE_KERNEL);
		if (rc)
			return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_RECEIVER, i, 0, "Kernel receiver role configuration failed");
	}
	for (uint32_t i = 0; i < owner->config.data_count; i++) {
		if (owner->data[i].representor == MWN_NO_PORT)
			continue;
		int rc = rte_pmd_bnxt_rep_role_set(owner->data[i].representor, RTE_PMD_BNXT_REP_ROLE_DIRECT_DATA);
		if (rc)
			return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_DATA, i, 0, "DATA representor role configuration failed");
	}
	return 0;
}

#define MWN_MAX_PORTS (MWAN_NATIVE_MAX_CONTROLLERS + MWAN_NATIVE_MAX_RECEIVERS + 2 * MWAN_NATIVE_MAX_DATA)

static uint32_t owned_ports(struct mwan_native *owner, uint16_t ports[MWN_MAX_PORTS], bool shutdown)
{
	uint32_t count = 0;
	if (!shutdown)
		for (uint32_t i = 0; i < owner->config.controller_count; i++)
			ports[count++] = owner->controllers[i].port;
	for (uint32_t i = 0; i < owner->config.data_count; i++)
		ports[count++] = owner->data[i].port;
	if (shutdown)
		for (uint32_t i = 0; i < owner->config.data_count; i++)
			ports[count++] = owner->data[i].representor;
	for (uint32_t i = 0; i < owner->config.receiver_count; i++)
		ports[count++] = owner->receivers[i];
	if (!shutdown)
		for (uint32_t i = 0; i < owner->config.data_count; i++)
			ports[count++] = owner->data[i].representor;
	if (shutdown)
		for (uint32_t i = 0; i < owner->config.controller_count; i++)
			ports[count++] = owner->controllers[i].port;
	return count;
}

static int configure_port(struct mwan_native *owner, uint16_t port, struct mwan_native_error *error)
{
	struct rte_eth_conf conf = {0};
	struct rte_eth_dev_info info;
	int rc = rte_eth_dev_info_get(port, &info);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_PORT, port, 0, "Device information failed");
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
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_PORT, port, 0, "Port queue configuration failed");
	return 0;
}

static int start_port(struct mwan_native *owner, uint16_t port, struct mwan_native_error *error)
{
	if (port == MWN_NO_PORT || owner->started[port])
		return 0;
	int rc = rte_eth_dev_start(port);
	if (rc)
		return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_PORT, port, 0, "Port start failed");
	owner->started[port] = true;
	return 0;
}

static int miss(struct mwan_native *owner, uint32_t i, bool install, struct mwan_native_error *error)
{
	struct mwn_controller *controller = &owner->controllers[i];
	uint32_t receiver = owner->config.controllers[i].physical_receiver;
	struct rte_flow_group_attr attr = {.transfer = 1};
	struct rte_flow_action_port_id destination = {.id = owner->receivers[receiver]};
	struct rte_flow_action actions[] = {
		{.type = install ? RTE_FLOW_ACTION_TYPE_PORT_ID : RTE_FLOW_ACTION_TYPE_END, .conf = install ? &destination : NULL},
		{.type = RTE_FLOW_ACTION_TYPE_END}
	};
	struct rte_flow_error detail = {0};
	int rc = rte_flow_group_set_miss_actions(controller->port, 0, &attr, actions, &detail);
	if (rc)
		return mwn_flow_error(error, rc, MWAN_NATIVE_OP_MISS, MWAN_NATIVE_OBJECT_CONTROLLER, i, 0, &detail);
	controller->miss = install;
	return 0;
}

static int activate(struct mwan_native *owner, struct mwan_native_error *error)
{
	uint16_t ports[MWN_MAX_PORTS];
	uint32_t count = owned_ports(owner, ports, false);
	for (uint32_t i = 0; i < count; i++) {
		int rc = start_port(owner, ports[i], error);
		if (rc)
			return rc;
	}
	for (uint32_t i = 0; i < owner->config.controller_count; i++) {
		int rc = miss(owner, i, true, error);
		if (rc)
			return rc;
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
			return mwn_error(error, EBUSY, MWAN_NATIVE_OP_CONFIG, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Reopen requires the identical healthy EAL configuration");
		return owner->active ? 0 : activate(owner, error);
	}
	struct mwan_native *owner = calloc(1, sizeof(*owner));
	if (!owner)
		return mwn_error(error, ENOMEM, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Owner allocation failed");
	owner->config = *config;
	owner->thread = pthread_self();
	for (uint32_t i = 0; i < MWAN_NATIVE_MAX_CONTROLLERS; i++)
		owner->controllers[i].port = MWN_NO_PORT;
	for (uint32_t i = 0; i < MWAN_NATIVE_MAX_RECEIVERS; i++)
		owner->receivers[i] = MWN_NO_PORT;
	for (uint32_t i = 0; i < MWAN_NATIVE_MAX_DATA; i++)
		owner->data[i] = (struct mwn_data){.port = MWN_NO_PORT, .representor = MWN_NO_PORT, .origin = MWAN_NATIVE_NO_ID};
	for (uint32_t i = 0; i < config->origin_count; i++)
		owner->data[config->origins[i].data].origin = i;
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
		rc = mwn_error(error, ENOMEM, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Packet or pair storage allocation failed");
		goto failed;
	}
	rc = discover(owner, error);
	if (rc)
		goto failed;
	uint16_t ports[MWN_MAX_PORTS];
	uint32_t count = owned_ports(owner, ports, false);
	for (uint32_t i = 0; i < count; i++) {
		if (ports[i] == MWN_NO_PORT)
			continue;
		rc = configure_port(owner, ports[i], error);
		if (rc)
			goto failed;
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
		int rc = mwn_poll(owner, MWN_BURST * owner->config.data_count, error);
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
	return mwn_error(error, ETIMEDOUT, MWAN_NATIVE_OP_DRAIN, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Packet drain did not become quiet; restoration records retained");
}

int mwan_native_poll(struct mwan_native *owner, uint32_t budget,
		     struct mwan_native_worker_stats *stats, struct mwan_native_error *error)
{
	int rc = mwn_owner(owner, error);
	if (rc)
		return rc;
	if (!stats || !budget)
		return mwn_error(error, EINVAL, MWAN_NATIVE_OP_PACKET, MWAN_NATIVE_OBJECT_ORIGIN, 0, 0, "Packet budget and statistics are required");
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
	for (uint32_t i = 0; i < owner->config.controller_count; i++) {
		if (!owner->controllers[i].miss)
			continue;
		rc = miss(owner, i, false, error);
		if (rc) {
			owner->faulted = true;
			return rc;
		}
	}
	uint16_t ports[MWN_MAX_PORTS];
	uint32_t count = owned_ports(owner, ports, true);
	for (uint32_t i = 0; i < count; i++) {
		uint16_t port = ports[i];
		if (port == MWN_NO_PORT || !owner->started[port])
			continue;
		rc = rte_eth_dev_stop(port);
		if (rc) {
			owner->faulted = true;
			return mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_PORT, port, 0, "Port stop failed");
		}
		owner->started[port] = false;
	}
	owner->active = false;
	/* A successful DATA stop releases queued buffers before record reuse. */
	if (owner->pairs && owner->lookup)
		for (uint32_t i = 0; i < owner->config.pair_capacity; i++)
			mwn_pair_forget(owner, i);
	if (mode == MWAN_NATIVE_DISABLE)
		return 0;
	for (uint32_t i = 0; i < count; i++) {
		uint16_t port = ports[i];
		if (port == MWN_NO_PORT || !rte_eth_dev_is_valid_port(port))
			continue;
		rc = rte_eth_dev_close(port);
		if (rc) {
			rc = mwn_error(error, -rc, MWAN_NATIVE_OP_PORT, MWAN_NATIVE_OBJECT_PORT, port, 0, "Port close failed; process recovery is required");
			owner->terminal_failure = *error;
			owner->faulted = true;
			return rc;
		}
	}
	rte_hash_free(owner->lookup);
	rte_mempool_free(owner->pool);
	owner->lookup = NULL;
	owner->pool = NULL;
	rc = rte_eal_cleanup();
	owner->eal_ready = false;
	if (rc) {
		rc = mwn_error(error, -rc, MWAN_NATIVE_OP_EAL, MWAN_NATIVE_OBJECT_NONE, 0, 0, "Final EAL cleanup failed; process recovery is required");
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
