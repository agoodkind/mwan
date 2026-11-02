/* SPDX-License-Identifier: BSD-3-Clause */
#ifndef MWAN_NATIVE_H
#define MWAN_NATIVE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define MWAN_NATIVE_ABI_VERSION 3U
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
#define MWAN_NATIVE_OBSERVATION_BATCH_LIMIT 256U
#define MWAN_NATIVE_OBSERVATION_FRAME_LIMIT 65536U
#define MWAN_NATIVE_VXLAN_VERSION 1U
#define MWAN_NATIVE_VXLAN_CONTEXT_ABSENT 0U
#define MWAN_NATIVE_VXLAN_CONTEXT_READY 1U
#define MWAN_NATIVE_VXLAN_RULE_INSTALLED 2U
#define MWAN_NATIVE_VXLAN_CLEANUP_FAILED 3U

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
	MWAN_NATIVE_PAIR_FAULTED = 3,
	MWAN_NATIVE_PAIR_RELEASED = 4
};

enum mwan_native_ipv6_restore_field {
	MWAN_NATIVE_IPV6_RESTORE_LEGACY = 0,
	MWAN_NATIVE_IPV6_RESTORE_SOURCE = 1,
	MWAN_NATIVE_IPV6_RESTORE_DESTINATION = 2
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

/* Additional records do not change ABI2 layouts. Endpoint identity includes
 * the declared receiver index, represented VF, PCI function and Linux ifindex.
 */
struct mwan_native_vxlan_endpoint {
	uint32_t version;
	uint32_t record_size;
	uint32_t receiver_id;
	uint32_t linux_ifindex;
	uint16_t source_vf;
	uint16_t reserved;
	struct mwan_native_pci pci;
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
	uint32_t ipv6_restore_field;
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

struct mwan_native_frame {
	const uint8_t *data;
	uint32_t length;
	uint32_t reserved;
};

struct mwan_native_observation {
	struct mwan_native_tuple tuple;
	uint8_t source_mac[6];
	uint8_t destination_mac[6];
	uint32_t network_length;
	uint32_t flow_label;
	uint8_t traffic_class;
	uint8_t tcp_flags;
	uint8_t ttl;
	uint8_t eligible;
	int32_t decode_status;
};

/* Only a trusted kernel capture supplies these host-order scalar fields.
 * Frame bytes exclude the 10-byte virtual network header. Both lengths
 * describe Ethernet bytes. Callers zero both reserved fields.
 */
struct mwan_native_kernel_metadata {
	uint32_t auxiliary_status;
	uint32_t packet_length;
	uint32_t captured_length;
	uint16_t mac_offset;
	uint16_t network_offset;
	uint8_t vnet_flags;
	uint8_t gso_type;
	uint16_t header_length;
	uint16_t gso_size;
	uint16_t checksum_start;
	uint16_t checksum_offset;
	uint16_t reserved16;
	uint32_t reserved32;
};

struct mwan_native_kernel_frame {
	struct mwan_native_frame frame;
	struct mwan_native_kernel_metadata metadata;
};

struct mwan_native_kernel_observation {
	struct mwan_native_observation observation;
	uint32_t effective_network_length;
	uint32_t reserved;
};

struct mwan_native_packet_guard {
	/* Packet policy must be certified independently of ECN. The traffic class
	 * comparison uses its DSCP bits; flow_label still requires an exact match.
	 */
	uint32_t flow_label;
	uint8_t traffic_class;
	uint8_t reserved[3];
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
	uint32_t shared_restoration_mask;
	uint32_t padding;
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

/* The fixed envelope contains Ethernet, IPv4, UDP and VXLAN without options.
 * Inner addresses and ports remain unchanged. inner_mtu excludes Ethernet.
 * The source Linux VF MTU must not exceed inner_mtu. The underlay MTU must
 * include the 50 bytes between the outer IP header and inner IP packet.
 */
struct mwan_native_vxlan_plan {
	uint32_t version;
	uint32_t record_size;
	uint64_t cookie;
	uint64_t policy_generation;
	struct mwan_native_tuple inner;
	uint8_t inner_source_mac[6];
	uint8_t inner_destination_mac[6];
	uint8_t outer_source_mac[6];
	uint8_t outer_destination_mac[6];
	uint8_t outer_source[4];
	uint8_t outer_destination[4];
	uint32_t vni;
	uint16_t outer_source_port;
	uint16_t outer_destination_port;
	uint16_t inner_mtu;
	uint16_t underlay_mtu;
	uint8_t outer_ttl;
	uint8_t outer_tos;
	uint16_t reserved;
};

struct mwan_native_vxlan_result {
	struct mwan_native_handle context;
	uint64_t rule_generation;
	uint32_t state;
	uint32_t reserved;
};

struct mwan_native_vxlan_stats {
	struct mwan_native_vxlan_result result;
	struct mwan_native_counter counter;
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

/* Retained pair storage has the same bound as configured pair_capacity.
 * DRAINING generations retain their inverse mappings after hardware-slot reuse.
 */
struct mwan_native_retirement_stats {
	uint32_t active_pair_capacity;
	uint32_t retained_pair_capacity;
	uint32_t retained_pairs;
	uint32_t draining_pairs;
	uint64_t retained_directions;
	uint32_t shared_binding_capacity;
	uint32_t shared_bindings;
	uint32_t shared_identity_bindings;
	uint32_t reusable_shared_slots;
	uint64_t shared_binding_conflicts;
	uint64_t shared_binding_capacity_rejections;
	uint64_t shared_pair_releases;
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
int mwan_native_observe_batch(const struct mwan_native_frame *frames,
			      struct mwan_native_observation *observations,
			      uint32_t count);
/* Callers provide aligned, live arrays for count records and readable frame
 * buffers for the synchronous call. Inputs cannot overlap writable outputs.
 * Callers keep inputs immutable and grant exclusive access to outputs.
 * The library does not retain pointers. Invalid descriptors reject the batch
 * without writes. Unsupported content rejects only its observation, with
 * effective_network_length zero. The result never authorizes forwarding.
 */
int mwan_native_observe_kernel_batch(const struct mwan_native_kernel_frame *frames,
				     struct mwan_native_kernel_observation *observations,
				     uint32_t count);

/* The owner calls every operation on one OS thread. A null owner initializes
 * EAL once. A disabled owner reopens only the identical graph configuration.
 */
int mwan_native_open(const struct mwan_native_config *config,
		     struct mwan_native **owner, struct mwan_native_error *error);
int mwan_native_pair_install(struct mwan_native *owner,
			     const struct mwan_native_pair_spec *spec,
			     struct mwan_native_pair_result *result,
			     struct mwan_native_error *error);
int mwan_native_pair_install_observed(struct mwan_native *owner,
				      const struct mwan_native_pair_spec *spec,
				      const struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT],
				      struct mwan_native_pair_result *result,
				      struct mwan_native_error *error);
/* Callers revalidate the connection and forwarding evidence before resuming.
 * Only an active owner's fully cleaned primary DRAINING pair can resume.
 * Resume preserves the current primary generation and its inverse records.
 * Only policy_generation may advance; every other spec and guard field must match.
 */
int mwan_native_pair_resume_observed(struct mwan_native *owner,
				     struct mwan_native_handle handle,
				     const struct mwan_native_pair_spec *spec,
				     const struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT],
				     struct mwan_native_pair_result *result,
				     struct mwan_native_error *error);
int mwan_native_pair_query(struct mwan_native *owner,
			   struct mwan_native_handle handle,
			   struct mwan_native_pair_stats *stats,
			   struct mwan_native_error *error);
/* Reobservation requires complete removal and fresh connection and policy proof.
 * It preserves the primary handle, inverse records and every mapping field.
 * Only policy_generation may advance and packet guards may change.
 * Failed validation or hardware replacement preserves the previous guards.
 */
int mwan_native_pair_reobserve(struct mwan_native *owner,
			      struct mwan_native_handle handle,
			      const struct mwan_native_pair_spec *spec,
			      const struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT],
			      struct mwan_native_pair_result *result,
			      struct mwan_native_error *error);
/* Removal deletes hardware rules and retains inverse records in DRAINING.
 * A later install can reuse the hardware slot after copying its old generation
 * into bounded retained storage. Retained handles remain DRAINING after reuse.
 */
int mwan_native_pair_remove(struct mwan_native *owner,
			    struct mwan_native_handle handle,
			    struct mwan_native_pair_result *result,
			    struct mwan_native_error *error);
/* Release consumes a fully cleaned primary DRAINING handle and preserves all
 * shared IPv6 bindings. Every requested direction must use shared restoration.
 */
int mwan_native_pair_release_shared(struct mwan_native *owner,
				    struct mwan_native_handle handle,
				    struct mwan_native_pair_result *result,
				    struct mwan_native_error *error);
int mwan_native_poll(struct mwan_native *owner, uint32_t packet_budget,
		     struct mwan_native_worker_stats *stats,
		     struct mwan_native_error *error);
int mwan_native_retirement_query(struct mwan_native *owner,
				 struct mwan_native_retirement_stats *stats,
				 struct mwan_native_error *error);

/* Disable drains owned flows and stops ports while retaining EAL and owner.
 * Final shutdown also closes ports, cleans EAL and clears owner on success.
 * A cleanup failure retains owner and its unusable handles for reporting.
 */
int mwan_native_close(struct mwan_native **owner, uint32_t mode,
		      struct mwan_native_error *error);

/* These calls require the ABI2 owner thread and separate, aligned, valid records.
 * Validation copies the complete plan and does not require DPDK or EAL.
 * Each context owns at most one outbound rule on its declared kernel VF TX.
 * Close removes the rule before releasing its context. ABI2 disable/final
 * shutdown also remove these rules before stopping any port. Failed removal
 * retains the context, plan and uncertain hardware ownership and blocks reuse.
 * Removal does not establish Linux VXLAN fallback or TCP continuation.
 */
int mwan_native_vxlan_plan_validate(const struct mwan_native_vxlan_plan *plan,
				    struct mwan_native_error *error);
int mwan_native_vxlan_context_open(struct mwan_native *owner,
				   const struct mwan_native_vxlan_endpoint *endpoint,
				   struct mwan_native_vxlan_result *result,
				   struct mwan_native_error *error);
int mwan_native_vxlan_install(struct mwan_native *owner,
			      struct mwan_native_handle context,
			      const struct mwan_native_vxlan_plan *plan,
			      struct mwan_native_vxlan_result *result,
			      struct mwan_native_error *error);
int mwan_native_vxlan_query(struct mwan_native *owner,
			    struct mwan_native_vxlan_result rule,
			    struct mwan_native_vxlan_stats *stats,
			    struct mwan_native_error *error);
int mwan_native_vxlan_remove(struct mwan_native *owner,
			     struct mwan_native_vxlan_result rule,
			     struct mwan_native_vxlan_result *result,
			     struct mwan_native_error *error);
int mwan_native_vxlan_context_close(struct mwan_native *owner,
				    struct mwan_native_handle context,
				    struct mwan_native_vxlan_result *result,
				    struct mwan_native_error *error);

#ifdef __cplusplus
}
#endif
#endif
