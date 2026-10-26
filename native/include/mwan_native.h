/* SPDX-License-Identifier: BSD-3-Clause */
#ifndef MWAN_NATIVE_H
#define MWAN_NATIVE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define MWAN_NATIVE_ABI_VERSION 2U
#define MWAN_NATIVE_MAX_CONTROLLERS 4U
#define MWAN_NATIVE_MAX_RECEIVERS 8U
#define MWAN_NATIVE_MAX_DATA 8U
#define MWAN_NATIVE_MAX_ORIGINS 8U
#define MWAN_NATIVE_NO_ID UINT32_MAX
#define MWAN_NATIVE_TEXT_SIZE 192U
#define MWAN_NATIVE_PATH_SIZE 256U
#define MWAN_NATIVE_PREFIX_SIZE 64U
#define MWAN_NATIVE_DIRECTION_COUNT 2U
#define MWAN_NATIVE_FAMILY_IPV4 4U
#define MWAN_NATIVE_FAMILY_IPV6 6U
#define MWAN_NATIVE_PROTOCOL_TCP 6U
#define MWAN_NATIVE_PROTOCOL_UDP 17U

/* Integers use host byte order. Address and MAC arrays use network byte order.
 * IPv4 addresses occupy the first four bytes; the remaining bytes are zero.
 * Callers zero reserved fields. The library copies every input record.
 */
struct mwan_native;

enum mwan_native_memory_mode {
	MWAN_NATIVE_MEMORY_HUGEPAGES = 0,
	MWAN_NATIVE_MEMORY_ANONYMOUS = 1
};

enum mwan_native_close_mode {
	MWAN_NATIVE_DISABLE = 0,
	MWAN_NATIVE_FINAL_SHUTDOWN = 1
};

enum mwan_native_return_mode {
	MWAN_NATIVE_RETURN_ORIGINAL = 0,
	MWAN_NATIVE_RETURN_PEER_UNICAST = 1
};

enum mwan_native_ingress_origin {
	MWAN_NATIVE_ORIGIN_PHYSICAL = 0,
	MWAN_NATIVE_ORIGIN_NATIVE_VF = 1
};

enum mwan_native_pair_state {
	MWAN_NATIVE_PAIR_ABSENT = 0,
	MWAN_NATIVE_PAIR_INSTALLED = 1,
	MWAN_NATIVE_PAIR_DRAINING = 2,
	MWAN_NATIVE_PAIR_FAULTED = 3
};

enum mwan_native_operation {
	MWAN_NATIVE_OP_NONE = 0,
	MWAN_NATIVE_OP_CONFIG = 1,
	MWAN_NATIVE_OP_EAL = 2,
	MWAN_NATIVE_OP_PORT = 3,
	MWAN_NATIVE_OP_MISS = 4,
	MWAN_NATIVE_OP_VALIDATE = 5,
	MWAN_NATIVE_OP_CREATE = 6,
	MWAN_NATIVE_OP_QUERY = 7,
	MWAN_NATIVE_OP_DESTROY = 8,
	MWAN_NATIVE_OP_DRAIN = 9,
	MWAN_NATIVE_OP_PACKET = 10
};

enum mwan_native_object_kind {
	MWAN_NATIVE_OBJECT_NONE = 0,
	MWAN_NATIVE_OBJECT_CONTROLLER = 1,
	MWAN_NATIVE_OBJECT_RECEIVER = 2,
	MWAN_NATIVE_OBJECT_DATA = 3,
	MWAN_NATIVE_OBJECT_ORIGIN = 4,
	MWAN_NATIVE_OBJECT_PORT = 5,
	MWAN_NATIVE_OBJECT_PAIR = 6
};

struct mwan_native_pci {
	uint16_t domain;
	uint8_t bus;
	uint8_t device;
	uint8_t function;
	uint8_t reserved[3];
};

/* References are array indexes. Each controller owns one physical miss. */
struct mwan_native_controller {
	struct mwan_native_pci pci;
	uint32_t physical_receiver;
	uint32_t reserved;
};

struct mwan_native_receiver {
	uint32_t controller;
	uint16_t vf;
	uint16_t reserved;
	uint8_t mac[6];
	uint8_t padding[2];
};

/* Output-only DATA uses controller=MWAN_NATIVE_NO_ID and vf=0. */
struct mwan_native_data {
	struct mwan_native_pci pci;
	uint32_t controller;
	uint16_t vf;
	uint16_t reserved;
	uint8_t mac[6];
	uint8_t padding[2];
};

/* Each origin requires a distinct DATA RX binding. Source VF interception
 * and peer-unicast return remain unsupported in this ABI implementation.
 */
struct mwan_native_origin {
	uint32_t controller;
	uint32_t kind;
	uint32_t receiver;
	uint32_t data;
	uint32_t return_mode;
	uint16_t source_vf;
	uint16_t reserved;
};

struct mwan_native_config {
	uint32_t abi_version;
	uint32_t record_size;
	uint32_t cpu;
	uint32_t memory_mb;
	uint32_t memory_mode;
	uint32_t controller_count;
	uint32_t receiver_count;
	uint32_t data_count;
	uint32_t origin_count;
	uint32_t pair_capacity;
	uint32_t mbuf_count;
	uint32_t drain_timeout_ms;
	uint16_t rx_descriptors;
	uint16_t tx_descriptors;
	uint16_t mtu;
	uint16_t reserved;
	char hugepage_directory[MWAN_NATIVE_PATH_SIZE];
	char file_prefix[MWAN_NATIVE_PREFIX_SIZE];
	struct mwan_native_controller controllers[MWAN_NATIVE_MAX_CONTROLLERS];
	struct mwan_native_receiver receivers[MWAN_NATIVE_MAX_RECEIVERS];
	struct mwan_native_data data[MWAN_NATIVE_MAX_DATA];
	struct mwan_native_origin origins[MWAN_NATIVE_MAX_ORIGINS];
};

struct mwan_native_tuple {
	uint8_t family;
	uint8_t protocol;
	uint16_t reserved;
	uint8_t source[16];
	uint8_t destination[16];
	uint16_t source_port;
	uint16_t destination_port;
};

/* Ports use host byte order. TCP flags use their eight wire bits. Hardware
 * rules do not constrain flags; the worker inspects them before forwarding.
 */
struct mwan_native_direction {
	uint32_t origin_id;
	uint32_t egress_data_id;
	struct mwan_native_tuple original;
	struct mwan_native_tuple translated;
	uint8_t original_source_mac[6];
	uint8_t original_destination_mac[6];
	uint8_t output_source_mac[6];
	uint8_t output_destination_mac[6];
	uint16_t output_mtu;
	uint16_t reserved;
};

struct mwan_native_pair_spec {
	uint64_t cookie;
	uint64_t policy_generation;
	uint32_t requested_direction_mask;
	uint32_t reserved;
	struct mwan_native_direction directions[MWAN_NATIVE_DIRECTION_COUNT];
};

struct mwan_native_handle {
	uint64_t instance;
	uint64_t generation;
	uint32_t slot;
	uint32_t reserved;
};

struct mwan_native_pair_result {
	struct mwan_native_handle handle;
	uint32_t state;
	uint32_t installed_mask;
	uint32_t cleanup_failed_mask;
	uint32_t requested_direction_mask;
	uint32_t restoration_mask;
	uint32_t reserved;
};

enum mwan_native_packet_validity {
	MWAN_NATIVE_PACKET_ETHERNET_VALID = 1,
	MWAN_NATIVE_PACKET_IP_VALID = 2,
	MWAN_NATIVE_PACKET_PORTS_VALID = 4
};

enum mwan_native_packet_failure {
	MWAN_NATIVE_PACKET_KEY_MISSING = 1,
	MWAN_NATIVE_PACKET_SOURCE_MAC_MISMATCH = 2,
	MWAN_NATIVE_PACKET_DESTINATION_MAC_MISMATCH = 4
};

/* The snapshot records headers at RX, after any hardware rewrite. */
struct mwan_native_packet_diagnostic {
	uint32_t validity;
	uint32_t failure_flags;
	int32_t decode_status;
	uint16_t ether_type;
	uint16_t reserved;
	struct mwan_native_tuple tuple;
	uint8_t source_mac[6];
	uint8_t destination_mac[6];
};

struct mwan_native_error {
	int32_t code;
	uint32_t operation;
	uint32_t object_id;
	uint32_t direction;
	uint32_t flow_error_type;
	uint32_t object_kind;
	char message[MWAN_NATIVE_TEXT_SIZE];
	struct mwan_native_packet_diagnostic packet;
};

struct mwan_native_counter {
	uint64_t packets;
	uint64_t bytes;
	uint64_t observed_monotonic_ns;
	int32_t error;
	uint8_t packets_valid;
	uint8_t bytes_valid;
	uint8_t reserved[2];
};

struct mwan_native_pair_stats {
	struct mwan_native_pair_result result;
	struct mwan_native_counter directions[MWAN_NATIVE_DIRECTION_COUNT];
};

struct mwan_native_worker_stats {
	uint64_t received;
	uint64_t forwarded;
	uint64_t returned_to_kernel;
	uint64_t unsupported;
	uint64_t lookup_misses;
	uint64_t malformed;
	uint64_t transmit_dropped;
	uint64_t pending_packets;
	uint64_t quarantined_packets;
	uint64_t discarded_group_frames;
};

struct mwan_native_build_info {
	uint32_t abi_version;
	uint32_t config_size;
	uint32_t pair_spec_size;
	uint32_t direction_size;
	uint32_t controller_size;
	uint32_t receiver_size;
	uint32_t data_size;
	uint32_t origin_size;
	uint32_t error_size;
	uint32_t worker_stats_size;
	uint32_t pair_stats_size;
	uint32_t reserved;
	char dpdk_version[32];
	char source_sha256[65];
	char patch_sha256[65];
};

uint32_t mwan_native_abi_version(void);
int mwan_native_build_info(struct mwan_native_build_info *info);

/* The owner calls every operation on one OS thread. A null owner initializes
 * EAL once. A disabled owner reopens only the identical graph configuration.
 */
int mwan_native_open(const struct mwan_native_config *config,
		     struct mwan_native **owner, struct mwan_native_error *error);
int mwan_native_pair_install(struct mwan_native *owner,
			     const struct mwan_native_pair_spec *spec,
			     struct mwan_native_pair_result *result,
			     struct mwan_native_error *error);
int mwan_native_pair_query(struct mwan_native *owner,
			   struct mwan_native_handle handle,
			   struct mwan_native_pair_stats *stats,
			   struct mwan_native_error *error);
/* Removal deletes hardware rules and retains inverse records in DRAINING.
 * Retained records consume pair capacity until disable stops the DATA queues.
 */
int mwan_native_pair_remove(struct mwan_native *owner,
			    struct mwan_native_handle handle,
			    struct mwan_native_pair_result *result,
			    struct mwan_native_error *error);
int mwan_native_poll(struct mwan_native *owner, uint32_t packet_budget,
		     struct mwan_native_worker_stats *stats,
		     struct mwan_native_error *error);

/* Disable drains owned flows and stops ports while retaining EAL and owner.
 * Final shutdown also closes ports, cleans EAL and clears owner on success.
 * A cleanup failure retains owner and its unusable handles for reporting.
 */
int mwan_native_close(struct mwan_native **owner, uint32_t mode,
		      struct mwan_native_error *error);

#ifdef __cplusplus
}
#endif
#endif
