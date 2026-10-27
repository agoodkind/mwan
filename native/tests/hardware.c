/* SPDX-License-Identifier: BSD-3-Clause */
#include "mwan_native.h"
#include <arpa/inet.h>
#include <errno.h>
#include <inttypes.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static int mac(const char *text, uint8_t value[6])
{
	unsigned int octets[6];
	char extra;
	if (sscanf(text, "%x:%x:%x:%x:%x:%x%c", &octets[0], &octets[1], &octets[2], &octets[3], &octets[4], &octets[5], &extra) != 6)
		return -1;
	for (unsigned int i = 0; i < 6; i++) {
		if (octets[i] > UINT8_MAX)
			return -1;
		value[i] = (uint8_t)octets[i];
	}
	return 0;
}

static int pci(const char *text, struct mwan_native_pci *value)
{
	unsigned int domain, bus, device, function;
	char extra;
	if (sscanf(text, "%x:%x:%x.%x%c", &domain, &bus, &device, &function, &extra) != 4 ||
	    domain > UINT16_MAX || bus > UINT8_MAX || device >= 32 || function >= 8)
		return -1;
	value->domain = (uint16_t)domain;
	value->bus = (uint8_t)bus;
	value->device = (uint8_t)device;
	value->function = (uint8_t)function;
	return 0;
}

static int end_of_file(FILE *file)
{
	char extra;
	return fscanf(file, " %c", &extra) == EOF && !ferror(file) ? 0 : -1;
}

static int read_config(const char *path, struct mwan_native_config *config)
{
	FILE *file = fopen(path, "r");
	if (!file)
		return -1;
	unsigned int version, rx, tx, mtu;
	int rc = -1;
	if (fscanf(file, "MWAN_NATIVE_CONFIG %u", &version) != 1 || version != MWAN_NATIVE_ABI_VERSION)
		goto done;
	config->abi_version = version;
	config->record_size = sizeof(*config);
	if (fscanf(file, "%" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32 " %u %u %u %63s %255s",
		   &config->cpu, &config->memory_mb, &config->memory_mode, &config->pair_capacity,
		   &config->mbuf_count, &config->drain_timeout_ms, &rx, &tx, &mtu,
		   config->file_prefix, config->hugepage_directory) != 11 ||
	    rx > UINT16_MAX || tx > UINT16_MAX || mtu > UINT16_MAX)
		goto done;
	config->rx_descriptors = (uint16_t)rx;
	config->tx_descriptors = (uint16_t)tx;
	config->mtu = (uint16_t)mtu;
	if (!strcmp(config->hugepage_directory, "-"))
		config->hugepage_directory[0] = 0;
	if (fscanf(file, "%" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32,
		   &config->controller_count, &config->receiver_count, &config->data_count, &config->origin_count) != 4 ||
	    config->controller_count > MWAN_NATIVE_MAX_CONTROLLERS || config->receiver_count > MWAN_NATIVE_MAX_RECEIVERS ||
	    !config->data_count || config->data_count > MWAN_NATIVE_MAX_DATA || config->origin_count > MWAN_NATIVE_MAX_ORIGINS)
		goto done;
	for (uint32_t i = 0; i < config->controller_count; i++) {
		char name[32];
		struct mwan_native_controller *controller = &config->controllers[i];
		if (fscanf(file, "%31s %" SCNu32, name, &controller->physical_receiver) != 2 ||
		    pci(name, &controller->pci))
			goto done;
	}
	for (uint32_t i = 0; i < config->receiver_count; i++) {
		unsigned int vf;
		char address[18];
		struct mwan_native_receiver *receiver = &config->receivers[i];
		if (fscanf(file, "%" SCNu32 " %u %17s", &receiver->controller, &vf, address) != 3 ||
		    vf > UINT16_MAX || mac(address, receiver->mac))
			goto done;
		receiver->vf = (uint16_t)vf;
	}
	for (uint32_t i = 0; i < config->data_count; i++) {
		unsigned int vf;
		char name[32], address[18];
		struct mwan_native_data *data = &config->data[i];
		if (fscanf(file, "%31s %" SCNu32 " %u %17s", name, &data->controller, &vf, address) != 4 ||
		    vf > UINT16_MAX || pci(name, &data->pci) || mac(address, data->mac))
			goto done;
		data->vf = (uint16_t)vf;
	}
	for (uint32_t i = 0; i < config->origin_count; i++) {
		unsigned int source_vf;
		struct mwan_native_origin *origin = &config->origins[i];
		if (fscanf(file, "%" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32 " %" SCNu32 " %u",
			   &origin->controller, &origin->kind, &origin->receiver, &origin->data,
			   &origin->return_mode, &source_vf) != 6 || source_vf > UINT16_MAX)
			goto done;
		origin->source_vf = (uint16_t)source_vf;
	}
	rc = end_of_file(file);
done:
	fclose(file);
	return rc;
}

static int read_direction(FILE *file, struct mwan_native_direction *direction)
{
	unsigned int family, protocol, source_port, destination_port, translated_source_port, translated_destination_port, mtu;
	char source[46], destination[46], translated_source[46], translated_destination[46];
	char source_mac[18], destination_mac[18], output_source_mac[18], output_destination_mac[18];
	if (fscanf(file, "%" SCNu32 " %" SCNu32 " %u %u %45s %45s %u %u %45s %45s %u %u %17s %17s %17s %17s %u",
		   &direction->origin_id, &direction->egress_data_id,
		   &family, &protocol, source, destination, &source_port, &destination_port,
		   translated_source, translated_destination, &translated_source_port, &translated_destination_port,
		   source_mac, destination_mac, output_source_mac, output_destination_mac, &mtu) != 17 ||
	    (family != MWAN_NATIVE_FAMILY_IPV4 && family != MWAN_NATIVE_FAMILY_IPV6) || protocol > UINT8_MAX ||
	    source_port > UINT16_MAX || destination_port > UINT16_MAX || translated_source_port > UINT16_MAX ||
	    translated_destination_port > UINT16_MAX || mtu > UINT16_MAX)
		return -1;
	int address_family = family == MWAN_NATIVE_FAMILY_IPV4 ? AF_INET : AF_INET6;
	direction->original.family = direction->translated.family = (uint8_t)family;
	direction->original.protocol = direction->translated.protocol = (uint8_t)protocol;
	direction->original.source_port = (uint16_t)source_port;
	direction->original.destination_port = (uint16_t)destination_port;
	direction->translated.source_port = (uint16_t)translated_source_port;
	direction->translated.destination_port = (uint16_t)translated_destination_port;
	direction->output_mtu = (uint16_t)mtu;
	if (inet_pton(address_family, source, direction->original.source) != 1 ||
	    inet_pton(address_family, destination, direction->original.destination) != 1 ||
	    inet_pton(address_family, translated_source, direction->translated.source) != 1 ||
	    inet_pton(address_family, translated_destination, direction->translated.destination) != 1 ||
	    mac(source_mac, direction->original_source_mac) || mac(destination_mac, direction->original_destination_mac) ||
	    mac(output_source_mac, direction->output_source_mac) || mac(output_destination_mac, direction->output_destination_mac))
		return -1;
	return 0;
}

static int read_pair(const char *path, struct mwan_native_pair_spec *pair)
{
	FILE *file = fopen(path, "r");
	if (!file)
		return -1;
	int rc = -1;
	unsigned int version;
	if (fscanf(file, "MWAN_NATIVE_PAIR %u", &version) != 1 || version != MWAN_NATIVE_ABI_VERSION)
		goto done;
	if (fscanf(file, "%" SCNu32 " %" SCNu64 " %" SCNu64,
		   &pair->requested_direction_mask, &pair->cookie, &pair->policy_generation) != 3 ||
	    !pair->requested_direction_mask || pair->requested_direction_mask > 3)
		goto done;
	const uint32_t masks[MWAN_NATIVE_DIRECTION_COUNT] = {1U, 2U};
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++)
		if ((pair->requested_direction_mask & masks[i]) && read_direction(file, &pair->directions[i]))
			goto done;
	rc = end_of_file(file);
done:
	fclose(file);
	return rc;
}

static uint64_t now_ns(void)
{
	struct timespec now;
	if (clock_gettime(CLOCK_MONOTONIC, &now))
		return 0;
	return (uint64_t)now.tv_sec * UINT64_C(1000000000) + (uint64_t)now.tv_nsec;
}

static void report_error(const struct mwan_native_error *error)
{
	fprintf(stderr, "native failure code=%d operation=%u object=%u/%u direction=%u flow_error=%u message=%s\n",
		error->code, error->operation, error->object_kind, error->object_id, error->direction, error->flow_error_type, error->message);
	const struct mwan_native_packet_diagnostic *packet = &error->packet;
	if (!packet->validity && !packet->decode_status)
		return;
	fprintf(stderr, "packet validity=%u failure_flags=%u ether_type=0x%04x decode_status=%d family=%u protocol=%u source_port=%u destination_port=%u\n",
		packet->validity, packet->failure_flags, packet->ether_type, packet->decode_status,
		packet->tuple.family, packet->tuple.protocol, packet->tuple.source_port, packet->tuple.destination_port);
	fprintf(stderr, "packet source_mac=%02x:%02x:%02x:%02x:%02x:%02x destination_mac=%02x:%02x:%02x:%02x:%02x:%02x\n",
		packet->source_mac[0], packet->source_mac[1], packet->source_mac[2], packet->source_mac[3], packet->source_mac[4], packet->source_mac[5],
		packet->destination_mac[0], packet->destination_mac[1], packet->destination_mac[2], packet->destination_mac[3], packet->destination_mac[4], packet->destination_mac[5]);
	if (packet->validity & MWAN_NATIVE_PACKET_IP_VALID) {
		char source[INET6_ADDRSTRLEN], destination[INET6_ADDRSTRLEN];
		int family = packet->tuple.family == MWAN_NATIVE_FAMILY_IPV4 ? AF_INET : AF_INET6;
		if (inet_ntop(family, packet->tuple.source, source, sizeof(source)) &&
		    inet_ntop(family, packet->tuple.destination, destination, sizeof(destination)))
			fprintf(stderr, "packet source=%s destination=%s\n", source, destination);
	}
}

static int unsigned_value(const char *text, uint64_t *value)
{
	char *end;
	if (!text[0] || text[0] == '-')
		return -1;
	errno = 0;
	*value = strtoull(text, &end, 10);
	return errno || *end ? -1 : 0;
}

static int positive(const char *text, uint64_t *value)
{
	return unsigned_value(text, value) || !*value ? -1 : 0;
}

static int reject_overlap(const struct mwan_native_config *config)
{
	if (!config->controller_count || !config->data_count)
		return -1;
	struct mwan_native_config invalid = *config;
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	invalid.data[0].pci = invalid.controllers[0].pci;
	int rc = mwan_native_open(&invalid, &owner, &error);
	return rc == -EINVAL && error.operation == MWAN_NATIVE_OP_CONFIG &&
	       error.object_kind == MWAN_NATIVE_OBJECT_DATA && error.object_id == 0 && !owner ? 0 : -1;
}

static uint64_t report_counters(const char *label, const struct mwan_native_pair_stats *counters)
{
	uint64_t hits = 0;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		const struct mwan_native_counter *counter = &counters->directions[i];
		printf("%s direction=%u valid=%u/%u error=%d packets=%" PRIu64 " bytes=%" PRIu64 "\n",
			label, i, counter->packets_valid, counter->bytes_valid, counter->error, counter->packets, counter->bytes);
		hits += counter->packets;
	}
	return hits;
}

static int reject_swapped_data(const char *path)
{
	struct mwan_native_config config = {0};
	if (read_config(path, &config)) {
		fprintf(stderr, "Invalid hardware identity test configuration\n");
		return EXIT_FAILURE;
	}
	uint32_t first = config.data_count, second = config.data_count;
	for (uint32_t i = 0; i < config.data_count && first == config.data_count; i++) {
		if (config.data[i].controller == MWAN_NATIVE_NO_ID)
			continue;
		for (uint32_t j = i + 1; j < config.data_count; j++) {
			if (config.data[i].controller == config.data[j].controller) {
				first = i;
				second = j;
				break;
			}
		}
	}
	if (first == config.data_count) {
		fprintf(stderr, "Identity test requires two DATA devices on one controller\n");
		return EXIT_FAILURE;
	}
	uint16_t vf = config.data[first].vf;
	config.data[first].vf = config.data[second].vf;
	config.data[second].vf = vf;
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	int rc = mwan_native_open(&config, &owner, &error);
	bool passed = rc == -EXDEV && owner && error.operation == MWAN_NATIVE_OP_PORT &&
		error.object_kind == MWAN_NATIVE_OBJECT_DATA && error.object_id == first;
	if (!passed) {
		fprintf(stderr, "Swapped DATA VF identities did not produce the required PCI mismatch\n");
		report_error(&error);
	}
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS swapped DATA VF identities rejected before activation; final shutdown succeeded");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

static void adjust_address_sum(uint8_t address[16], uint16_t adjustment)
{
	uint32_t word = (uint32_t)address[14] * 256U + address[15] + adjustment;
	word = (word & UINT16_MAX) + (word / 65536U);
	address[14] = (uint8_t)(word / 256U);
	address[15] = (uint8_t)word;
}

static int reject_ipv6_mappings(const char *config_path, const char *pair_path)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec pair = {0};
	if (read_config(config_path, &config) || read_pair(pair_path, &pair)) {
		fprintf(stderr, "Invalid IPv6 mapping rejection configuration\n");
		return EXIT_FAILURE;
	}
	uint32_t direction = pair.requested_direction_mask & 1U ? 0 : 1;
	if (pair.directions[direction].original.family != MWAN_NATIVE_FAMILY_IPV6 ||
	    pair.directions[direction].translated.family != MWAN_NATIVE_FAMILY_IPV6) {
		fprintf(stderr, "Mapping rejection test requires an IPv6 pair\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result result = {0};
	bool passed = false;
	if (mwan_native_open(&config, &owner, &error))
		goto cleanup;
	const char *cases[] = {"source", "destination", "cancelling-deltas", "source-port", "destination-port"};
	for (uint32_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
		struct mwan_native_pair_spec invalid = pair;
		struct mwan_native_tuple *translated = &invalid.directions[direction].translated;
		switch (i) {
		case 0:
			adjust_address_sum(translated->source, 1);
			break;
		case 1:
			adjust_address_sum(translated->destination, 1);
			break;
		case 2:
			adjust_address_sum(translated->source, 1);
			adjust_address_sum(translated->destination, UINT16_MAX - 1U);
			break;
		case 3:
			translated->source_port = translated->source_port == UINT16_MAX ? 1 : translated->source_port + 1;
			break;
		case 4:
			translated->destination_port = translated->destination_port == UINT16_MAX ? 1 : translated->destination_port + 1;
			break;
		}
		memset(&result, 0, sizeof(result));
		int rc = mwan_native_pair_install(owner, &invalid, &result, &error);
		if (rc != -ENOTSUP || error.operation != MWAN_NATIVE_OP_VALIDATE ||
		    error.object_kind != MWAN_NATIVE_OBJECT_ORIGIN ||
		    error.object_id != invalid.directions[direction].origin_id || error.direction != direction ||
		    result.state != MWAN_NATIVE_PAIR_ABSENT || result.installed_mask ||
		    result.restoration_mask || result.cleanup_failed_mask) {
			fprintf(stderr, "IPv6 %s mapping did not reject before admission\n", cases[i]);
			goto cleanup;
		}
		printf("REJECTED %s installed=%u retained=%u\n", cases[i], result.installed_mask, result.restoration_mask);
	}
	if (mwan_native_pair_install(owner, &pair, &result, &error) ||
	    result.state != MWAN_NATIVE_PAIR_INSTALLED || result.installed_mask != pair.requested_direction_mask ||
	    mwan_native_pair_remove(owner, result.handle, &result, &error) ||
	    result.state != MWAN_NATIVE_PAIR_DRAINING || result.installed_mask ||
	    result.restoration_mask != pair.requested_direction_mask)
		goto cleanup;
	passed = true;
cleanup:
	if (!passed && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS IPv6 mapping rejection, valid admission, owned removal and final shutdown");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

static int reuse_slots(const char *config_path, const char *first_path,
		      const char *second_path, const char *third_path,
		      uint64_t seconds, uint64_t minimum_forwarded,
		      uint64_t minimum_returned)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec first = {0}, second = {0}, third = {0};
	if (read_config(config_path, &config) || read_pair(first_path, &first) ||
	    read_pair(second_path, &second) || read_pair(third_path, &third) ||
	    config.pair_capacity != 1) {
		fprintf(stderr, "Slot reuse requires pair_capacity=1 and three distinct valid pairs\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result first_result = {0}, second_result = {0}, retired = {0}, rejected = {0};
	struct mwan_native_pair_stats counters = {0};
	struct mwan_native_retirement_stats retirement = {0};
	struct mwan_native_worker_stats stats = {0};
	bool passed = false;
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, &first, &first_result, &error))
		goto cleanup;
	printf("READY_REUSE_FIRST seconds=%" PRIu64 " slot=%u generation=%" PRIu64 "\n",
		seconds, first_result.handle.slot, first_result.handle.generation);
	fflush(stdout);
	uint64_t deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (now_ns() < deadline)
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
	if (mwan_native_pair_query(owner, first_result.handle, &counters, &error) ||
	    report_counters("REUSE_FIRST_COUNTER", &counters) < minimum_forwarded)
		goto cleanup;
	uint64_t before_forwarded = stats.forwarded;
	uint64_t before_returned = stats.returned_to_kernel;
	puts("QUEUE_REUSE_FIRST: continue the first pair during the 100 ms RX pause");
	fflush(stdout);
	struct timespec pause = {.tv_nsec = 100000000};
	if (nanosleep(&pause, NULL) ||
	    mwan_native_pair_remove(owner, first_result.handle, &retired, &error) ||
	    mwan_native_pair_install(owner, &second, &second_result, &error))
		goto cleanup;
	if (second_result.handle.slot != first_result.handle.slot ||
	    second_result.handle.generation <= first_result.handle.generation ||
	    mwan_native_pair_remove(owner, first_result.handle, &retired, &error) ||
	    retired.state != MWAN_NATIVE_PAIR_DRAINING || retired.installed_mask ||
	    retired.restoration_mask != first.requested_direction_mask ||
	    mwan_native_retirement_query(owner, &retirement, &error) ||
	    retirement.active_pair_capacity != 1 || retirement.retained_pair_capacity != 1 ||
	    retirement.retained_pairs != 1 || retirement.draining_pairs)
		goto cleanup;
	struct mwan_native_pair_spec conflict = first;
	conflict.policy_generation++;
	conflict.cookie++;
	if (mwan_native_pair_install(owner, &conflict, &rejected, &error) != -EEXIST ||
	    rejected.installed_mask || rejected.restoration_mask)
		goto cleanup;
	struct mwan_native_packet_guard retained_guards[MWAN_NATIVE_DIRECTION_COUNT] = {0};
	struct mwan_native_pair_result resumed_retained = {0};
	if (mwan_native_pair_resume_observed(owner, first_result.handle, &first, retained_guards,
					    &resumed_retained, &error) != -ESTALE ||
	    resumed_retained.handle.instance != first_result.handle.instance ||
	    resumed_retained.handle.generation != first_result.handle.generation ||
	    resumed_retained.handle.slot != first_result.handle.slot || resumed_retained.handle.reserved ||
	    resumed_retained.state != MWAN_NATIVE_PAIR_DRAINING || resumed_retained.installed_mask ||
	    resumed_retained.cleanup_failed_mask ||
	    resumed_retained.restoration_mask != first.requested_direction_mask) {
		fprintf(stderr, "Retained generation resumed after its hardware slot changed ownership\n");
		goto cleanup;
	}
	printf("REUSE_REJECTED_RESUME slot=%u generation=%" PRIu64 " state=%u retained=%u\n",
		resumed_retained.handle.slot, resumed_retained.handle.generation,
		resumed_retained.state, retirement.retained_pairs);
	printf("READY_REUSE_SECOND seconds=%" PRIu64 " slot=%u generation=%" PRIu64 " retained=%u\n",
		seconds, second_result.handle.slot, second_result.handle.generation, retirement.retained_pairs);
	fflush(stdout);
	deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (now_ns() < deadline)
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
	if (mwan_native_pair_query(owner, second_result.handle, &counters, &error) ||
	    report_counters("REUSE_SECOND_COUNTER", &counters) < minimum_forwarded ||
	    stats.forwarded - before_forwarded < minimum_forwarded ||
	    stats.returned_to_kernel - before_returned < minimum_returned ||
	    stats.pending_packets || stats.quarantined_packets)
		goto cleanup;
	if (mwan_native_pair_query(owner, first_result.handle, &counters, &error) != -ENOENT ||
	    counters.result.state != MWAN_NATIVE_PAIR_DRAINING || counters.result.installed_mask ||
	    counters.result.restoration_mask != first.requested_direction_mask ||
	    mwan_native_pair_remove(owner, second_result.handle, &retired, &error) ||
	    mwan_native_pair_install(owner, &third, &rejected, &error) != -ENOSPC ||
	    rejected.installed_mask || rejected.restoration_mask ||
	    mwan_native_pair_remove(owner, first_result.handle, &retired, &error) ||
	    retired.state != MWAN_NATIVE_PAIR_DRAINING ||
	    retired.restoration_mask != first.requested_direction_mask ||
	    mwan_native_retirement_query(owner, &retirement, &error) ||
	    retirement.retained_pairs != 1 || retirement.draining_pairs != 1)
		goto cleanup;
	printf("REUSE verified_slot=%u first_generation=%" PRIu64 " second_generation=%" PRIu64
		" forwarded=%" PRIu64 " returned=%" PRIu64 " retained=%u draining=%u\n",
		first_result.handle.slot, first_result.handle.generation, second_result.handle.generation,
		stats.forwarded - before_forwarded, stats.returned_to_kernel - before_returned,
		retirement.retained_pairs, retirement.draining_pairs);
	passed = true;
cleanup:
	if (!passed)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS hardware-slot reuse, retained inverse restoration, conflict rejection and bounded admission");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

static int guarded_pair(const char *config_path, const char *pair_path,
			uint64_t seconds, uint64_t minimum_forwarded,
			uint64_t minimum_returned, uint8_t traffic_class,
			uint32_t flow_label)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec pair = {0};
	if (read_config(config_path, &config) || read_pair(pair_path, &pair)) {
		fprintf(stderr, "Invalid guarded pair configuration\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result result = {0};
	struct mwan_native_pair_stats counters = {0};
	struct mwan_native_worker_stats stats = {0};
	struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT] = {0};
	bool passed = false;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		guards[i].traffic_class = traffic_class;
		guards[i].flow_label = flow_label;
	}
	if (mwan_native_open(&config, &owner, &error))
		goto cleanup;
	uint32_t requested = pair.requested_direction_mask & 1U ? 0 : 1;
	guards[requested].reserved[0] = 1;
	if (mwan_native_pair_install_observed(owner, &pair, guards, &result, &error) != -EINVAL ||
	    result.installed_mask || result.restoration_mask)
		goto cleanup;
	guards[requested].reserved[0] = 0;
	if (mwan_native_pair_install_observed(owner, &pair, guards, &result, &error))
		goto cleanup;
	printf("READY_GUARDED seconds=%" PRIu64 " traffic_class=%u flow_label=%u\n",
		seconds, traffic_class, flow_label);
	fflush(stdout);
	uint64_t deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (now_ns() < deadline)
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
	if (mwan_native_pair_query(owner, result.handle, &counters, &error) ||
	    report_counters("GUARDED_COUNTER", &counters) < minimum_forwarded + minimum_returned ||
	    stats.forwarded < minimum_forwarded || stats.returned_to_kernel < minimum_returned ||
	    stats.pending_packets || stats.quarantined_packets)
		goto cleanup;
	printf("GUARDED forwarded=%" PRIu64 " returned=%" PRIu64 "\n",
		stats.forwarded, stats.returned_to_kernel);
	passed = true;
cleanup:
	if (!passed)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS observed selector admission and Linux return for packet exceptions");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

static bool same_resume_handle(struct mwan_native_handle first,
			       struct mwan_native_handle second)
{
	return first.instance == second.instance && first.generation == second.generation &&
	       first.slot == second.slot && first.reserved == second.reserved;
}

static bool same_resume_retirement(const struct mwan_native_retirement_stats *first,
				   const struct mwan_native_retirement_stats *second)
{
	return first->active_pair_capacity == second->active_pair_capacity &&
	       first->retained_pair_capacity == second->retained_pair_capacity &&
	       first->retained_pairs == second->retained_pairs &&
	       first->draining_pairs == second->draining_pairs &&
	       first->retained_directions == second->retained_directions;
}

static int resume_poll_phase(struct mwan_native *owner, uint64_t seconds,
			     const char *phase, struct mwan_native_worker_stats *stats,
			     struct mwan_native_error *error)
{
	uint64_t started = now_ns();
	uint64_t duration = seconds * UINT64_C(1000000000);
	if (!started || UINT64_MAX - started < duration) {
		fprintf(stderr, "Cannot establish the %s phase deadline\n", phase);
		return -1;
	}
	uint64_t deadline = started + duration;
	for (;;) {
		uint64_t now = now_ns();
		if (!now) {
			fprintf(stderr, "Cannot read the %s phase clock\n", phase);
			return -1;
		}
		if (now >= deadline)
			return 0;
		if (mwan_native_poll(owner, 64, stats, error))
			return -1;
	}
}

static int resume_installed_counters(struct mwan_native *owner,
				     struct mwan_native_handle handle, uint32_t mask,
				     const char *label, uint64_t minimum,
				     struct mwan_native_error *error)
{
	struct mwan_native_pair_stats counters = {0};
	if (mwan_native_pair_query(owner, handle, &counters, error))
		return -1;
	if (!same_resume_handle(counters.result.handle, handle) ||
	    counters.result.state != MWAN_NATIVE_PAIR_INSTALLED ||
	    counters.result.requested_direction_mask != mask || counters.result.installed_mask != mask ||
	    counters.result.restoration_mask != mask || counters.result.cleanup_failed_mask) {
		fprintf(stderr, "%s did not report the installed generation and inverse mapping\n", label);
		return -1;
	}
	const uint32_t masks[MWAN_NATIVE_DIRECTION_COUNT] = {1U, 2U};
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		if (!(mask & masks[i]))
			continue;
		const struct mwan_native_counter *counter = &counters.directions[i];
		if (!counter->packets_valid || !counter->bytes_valid || counter->error ||
		    !counter->observed_monotonic_ns || (counter->packets && !counter->bytes)) {
			fprintf(stderr, "%s direction %u does not provide valid hardware counters\n", label, i);
			return -1;
		}
	}
	if (report_counters(label, &counters) < minimum) {
		fprintf(stderr, "%s did not record the required hardware packets\n", label);
		return -1;
	}
	return 0;
}

static int resume_still_draining(struct mwan_native *owner,
				 struct mwan_native_handle handle, uint32_t mask,
				 const struct mwan_native_retirement_stats *retired,
				 struct mwan_native_error *error)
{
	struct mwan_native_pair_stats counters = {0};
	struct mwan_native_retirement_stats current = {0};
	int rc = mwan_native_pair_query(owner, handle, &counters, error);
	if (rc != -ENOENT || !same_resume_handle(counters.result.handle, handle) ||
	    counters.result.state != MWAN_NATIVE_PAIR_DRAINING ||
	    counters.result.requested_direction_mask != mask || counters.result.installed_mask ||
	    counters.result.cleanup_failed_mask || counters.result.restoration_mask != mask) {
		fprintf(stderr, "Rejected resumption changed the draining generation or inverse mapping\n");
		return -1;
	}
	if (mwan_native_retirement_query(owner, &current, error))
		return -1;
	if (!same_resume_retirement(&current, retired)) {
		fprintf(stderr, "Rejected resumption changed retirement storage\n");
		return -1;
	}
	return 0;
}

static int resume_identical(const char *config_path, const char *pair_path,
			    uint64_t seconds, uint64_t minimum_forwarded,
			    uint64_t minimum_returned, uint8_t traffic_class,
			    uint32_t flow_label)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec pair = {0};
	if (read_config(config_path, &config) || read_pair(pair_path, &pair)) {
		fprintf(stderr, "Invalid identical resumption configuration\n");
		return EXIT_FAILURE;
	}
	if (!pair.policy_generation || pair.policy_generation == UINT64_MAX) {
		fprintf(stderr, "Identical resumption requires policy_generation between 1 and UINT64_MAX minus 1\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result result = {0};
	struct mwan_native_worker_stats stats = {0};
	struct mwan_native_retirement_stats installed = {0}, retired = {0}, resumed = {0};
	struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT] = {0};
	bool passed = false;
	for (uint32_t i = 0; i < MWAN_NATIVE_DIRECTION_COUNT; i++) {
		guards[i].traffic_class = traffic_class;
		guards[i].flow_label = flow_label;
	}
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install_observed(owner, &pair, guards, &result, &error) ||
	    mwan_native_retirement_query(owner, &installed, &error))
		goto cleanup;
	struct mwan_native_handle handle = result.handle;
	printf("READY_RESUME_INSTALLED seconds=%" PRIu64 " slot=%u generation=%" PRIu64
	       " policy=%" PRIu64 " traffic_class=%u flow_label=%u\n",
		seconds, handle.slot, handle.generation, pair.policy_generation, traffic_class, flow_label);
	fflush(stdout);
	if (resume_poll_phase(owner, seconds, "installed", &stats, &error) ||
	    resume_installed_counters(owner, handle, pair.requested_direction_mask,
				  "RESUME_INITIAL_COUNTER", minimum_forwarded, &error))
		goto cleanup;
	if (stats.forwarded < minimum_forwarded || stats.pending_packets || stats.quarantined_packets) {
		fprintf(stderr, "Initial resumption phase did not forward the required packets\n");
		goto cleanup;
	}
	uint64_t forwarded_before = stats.forwarded;
	uint64_t returned_before = stats.returned_to_kernel;
	puts("QUEUE_RESUME_RETIRE: continue the exact pair during the 100 ms RX pause");
	fflush(stdout);
	struct timespec pause = {.tv_nsec = 100000000};
	if (nanosleep(&pause, NULL)) {
		fprintf(stderr, "Cannot complete the resumption RX pause\n");
		goto cleanup;
	}
	if (mwan_native_pair_remove(owner, handle, &result, &error) ||
	    !same_resume_handle(result.handle, handle) || result.state != MWAN_NATIVE_PAIR_DRAINING ||
	    result.installed_mask || result.cleanup_failed_mask ||
	    result.restoration_mask != pair.requested_direction_mask ||
	    mwan_native_retirement_query(owner, &retired, &error))
		goto cleanup;
	if (retired.active_pair_capacity != installed.active_pair_capacity ||
	    retired.retained_pair_capacity != installed.retained_pair_capacity ||
	    retired.retained_pairs != installed.retained_pairs ||
	    retired.retained_directions != installed.retained_directions ||
	    retired.draining_pairs != installed.draining_pairs + 1) {
		fprintf(stderr, "Removal changed the primary generation's retirement storage\n");
		goto cleanup;
	}
	printf("READY_RESUME_REMOVED seconds=%" PRIu64 " slot=%u generation=%" PRIu64 "\n",
		seconds, handle.slot, handle.generation);
	fflush(stdout);
	if (resume_poll_phase(owner, seconds, "removed", &stats, &error))
		goto cleanup;
	uint64_t returned_removed = stats.returned_to_kernel - returned_before;
	if (stats.forwarded != forwarded_before || returned_removed < minimum_returned ||
	    stats.pending_packets || stats.quarantined_packets) {
		fprintf(stderr, "Removed resumption phase did not return the required packets without forwarding\n");
		goto cleanup;
	}
	printf("RESUME_REMOVED forwarded=%" PRIu64 " returned=%" PRIu64 "\n",
		stats.forwarded - forwarded_before, returned_removed);
	uint32_t requested = pair.requested_direction_mask & 1U ? 0 : 1;
	const char *rejections[] = {"cookie", "output-mtu", "output-mac", "guard", "pair-reserved",
				    "direction-reserved", "guard-reserved", "handle-reserved", "policy-regression"};
	for (uint32_t i = 0; i < sizeof(rejections) / sizeof(rejections[0]); i++) {
		struct mwan_native_pair_spec invalid = pair;
		struct mwan_native_packet_guard invalid_guards[MWAN_NATIVE_DIRECTION_COUNT];
		struct mwan_native_handle invalid_handle = handle;
		struct mwan_native_pair_result rejected = {0};
		memcpy(invalid_guards, guards, sizeof(invalid_guards));
		switch (i) {
		case 0:
			invalid.cookie ^= UINT64_C(1);
			break;
		case 1:
			if (invalid.directions[requested].output_mtu > 68)
				invalid.directions[requested].output_mtu--;
			else
				invalid.directions[requested].output_mtu++;
			break;
		case 2:
			invalid.directions[requested].output_destination_mac[5] ^= 1U;
			break;
		case 3:
			invalid_guards[requested].traffic_class ^= 1U;
			break;
		case 4:
			invalid.reserved = 1;
			break;
		case 5:
			invalid.directions[requested].reserved = 1;
			break;
		case 6:
			invalid_guards[requested].reserved[0] = 1;
			break;
		case 7:
			invalid_handle.reserved = 1;
			break;
		case 8:
			invalid.policy_generation--;
			break;
		}
		int expected = -EINVAL;
		if (i == 7)
			expected = -ESTALE;
		int rc = mwan_native_pair_resume_observed(owner, invalid_handle, &invalid, invalid_guards,
						&rejected, &error);
		bool valid_result = i == 7 ||
			(same_resume_handle(rejected.handle, handle) &&
			 rejected.state == MWAN_NATIVE_PAIR_DRAINING &&
			 rejected.requested_direction_mask == pair.requested_direction_mask &&
			 !rejected.installed_mask && !rejected.cleanup_failed_mask &&
			 rejected.restoration_mask == pair.requested_direction_mask);
		if (rc != expected || !valid_result ||
		    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error)) {
			fprintf(stderr, "Identical resumption did not reject %s while retaining the original pair\n", rejections[i]);
			goto cleanup;
		}
		printf("RESUME_REJECTED %s state=%u retained=%u\n",
			rejections[i], MWAN_NATIVE_PAIR_DRAINING, retired.retained_pairs);
	}
	pair.policy_generation++;
	if (mwan_native_pair_resume_observed(owner, handle, &pair, guards, &result, &error) ||
	    !same_resume_handle(result.handle, handle) || result.state != MWAN_NATIVE_PAIR_INSTALLED ||
	    result.installed_mask != pair.requested_direction_mask || result.cleanup_failed_mask ||
	    result.restoration_mask != pair.requested_direction_mask ||
	    mwan_native_retirement_query(owner, &resumed, &error))
		goto cleanup;
	if (!same_resume_retirement(&installed, &resumed)) {
		fprintf(stderr, "Exact resumption allocated another generation or changed retirement storage\n");
		goto cleanup;
	}
	uint64_t forwarded_resumed = stats.forwarded;
	printf("READY_RESUME_IDENTICAL seconds=%" PRIu64 " slot=%u generation=%" PRIu64
	       " policy=%" PRIu64 " retained=%u\n",
		seconds, result.handle.slot, result.handle.generation, pair.policy_generation, resumed.retained_pairs);
	fflush(stdout);
	if (resume_poll_phase(owner, seconds, "resumed", &stats, &error) ||
	    resume_installed_counters(owner, handle, pair.requested_direction_mask,
				  "RESUME_FINAL_COUNTER", minimum_forwarded, &error))
		goto cleanup;
	if (stats.forwarded - forwarded_resumed < minimum_forwarded ||
	    stats.pending_packets || stats.quarantined_packets) {
		fprintf(stderr, "Exact resumption did not restore forwarding for the same pair\n");
		goto cleanup;
	}
	printf("RESUME_IDENTICAL initial_forwarded=%" PRIu64 " removed_returned=%" PRIu64
	       " resumed_forwarded=%" PRIu64 " slot=%u generation=%" PRIu64 "\n",
		forwarded_before, returned_removed, stats.forwarded - forwarded_resumed,
		handle.slot, handle.generation);
	passed = true;
cleanup:
	if (!passed && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS identical pair resumption, retained inverse mapping and unchanged generation");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

static int retire_pending(const char *config_path, const char *pair_path,
			  uint64_t seconds, uint64_t minimum_pending,
			  uint64_t minimum_returned)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec pair = {0};
	if (read_config(config_path, &config) || read_pair(pair_path, &pair)) {
		fprintf(stderr, "Invalid pending retirement configuration\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result result = {0};
	struct mwan_native_worker_stats stats = {0};
	bool passed = false;
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, &pair, &result, &error))
		goto cleanup;
	printf("READY_PENDING_RETIRE seconds=%" PRIu64 " minimum_pending=%" PRIu64 "\n",
		seconds, minimum_pending);
	fflush(stdout);
	uint64_t deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (stats.pending_packets < minimum_pending && now_ns() < deadline) {
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
		if (stats.unsupported || stats.returned_to_kernel || stats.malformed || stats.quarantined_packets) {
			fprintf(stderr, "Pending pressure requires only eligible forwarded packets before retirement\n");
			goto cleanup;
		}
	}
	if (stats.pending_packets < minimum_pending) {
		fprintf(stderr, "External traffic did not create the required unsent forwarded queue\n");
		goto cleanup;
	}
	uint64_t pending_before = stats.pending_packets;
	uint64_t forwarded_before = stats.forwarded;
	uint64_t returned_before = stats.returned_to_kernel;
	if (mwan_native_pair_remove(owner, result.handle, &result, &error) ||
	    result.state != MWAN_NATIVE_PAIR_DRAINING || result.installed_mask ||
	    result.restoration_mask != pair.requested_direction_mask)
		goto cleanup;
	printf("PENDING_RETIRED queued=%" PRIu64 " forwarded_before=%" PRIu64 "\n",
		pending_before, forwarded_before);
	fflush(stdout);
	deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (now_ns() < deadline)
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
	uint64_t returned = stats.returned_to_kernel - returned_before;
	if (stats.forwarded != forwarded_before || returned < pending_before ||
	    returned < minimum_returned || stats.pending_packets || stats.quarantined_packets) {
		fprintf(stderr, "Retired pending frames did not complete kernel return without further forwarding\n");
		goto cleanup;
	}
	printf("PENDING_RETIRE queued=%" PRIu64 " returned=%" PRIu64 " forwarded_after=%" PRIu64 "\n",
		pending_before, returned, stats.forwarded - forwarded_before);
	passed = true;
cleanup:
	if (!passed && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS pending forwarded frame retirement and kernel return");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}

int main(int argc, char **argv)
{
	if (argc == 3 && !strcmp(argv[1], "--reject-swapped-data"))
		return reject_swapped_data(argv[2]);
	if (argc == 4 && !strcmp(argv[1], "--reject-ipv6-mappings"))
		return reject_ipv6_mappings(argv[2], argv[3]);
	if (argc == 7 && !strcmp(argv[1], "--pending-retire")) {
		uint64_t seconds, minimum_pending, minimum_returned;
		if (positive(argv[4], &seconds) || seconds > 3600 ||
		    positive(argv[5], &minimum_pending) || positive(argv[6], &minimum_returned)) {
			fprintf(stderr, "Invalid pending retirement duration or packet minimum\n");
			return EXIT_FAILURE;
		}
		return retire_pending(argv[2], argv[3], seconds, minimum_pending, minimum_returned);
	}
	if (argc == 9 && !strcmp(argv[1], "--reuse-slots")) {
		uint64_t seconds, minimum_forwarded, minimum_returned;
		if (positive(argv[6], &seconds) || seconds > 3600 ||
		    positive(argv[7], &minimum_forwarded) || positive(argv[8], &minimum_returned)) {
			fprintf(stderr, "Invalid slot reuse duration or packet minimum\n");
			return EXIT_FAILURE;
		}
		return reuse_slots(argv[2], argv[3], argv[4], argv[5], seconds,
				  minimum_forwarded, minimum_returned);
	}
	if (argc == 9 && !strcmp(argv[1], "--guarded-pair")) {
		uint64_t seconds, minimum_forwarded, minimum_returned, traffic_class, flow_label;
		if (positive(argv[4], &seconds) || seconds > 3600 ||
		    positive(argv[5], &minimum_forwarded) || positive(argv[6], &minimum_returned) ||
		    unsigned_value(argv[7], &traffic_class) || traffic_class > UINT8_MAX ||
		    unsigned_value(argv[8], &flow_label) || flow_label > UINT32_C(0xfffff)) {
			fprintf(stderr, "Invalid guarded pair duration, packet minimum or selector\n");
			return EXIT_FAILURE;
		}
		return guarded_pair(argv[2], argv[3], seconds, minimum_forwarded, minimum_returned,
				    (uint8_t)traffic_class, (uint32_t)flow_label);
	}
	if (argc == 9 && !strcmp(argv[1], "--resume-identical")) {
		uint64_t seconds, minimum_forwarded, minimum_returned, traffic_class, flow_label;
		if (positive(argv[4], &seconds) || seconds > 3600 ||
		    positive(argv[5], &minimum_forwarded) || positive(argv[6], &minimum_returned) ||
		    unsigned_value(argv[7], &traffic_class) || traffic_class > UINT8_MAX ||
		    unsigned_value(argv[8], &flow_label) || flow_label > UINT32_C(0xfffff)) {
			fprintf(stderr, "Invalid identical resumption duration, packet minimum or selector\n");
			return EXIT_FAILURE;
		}
		return resume_identical(argv[2], argv[3], seconds, minimum_forwarded, minimum_returned,
					(uint8_t)traffic_class, (uint32_t)flow_label);
	}
	if (argc != 6 && argc != 7) {
		fprintf(stderr, "Usage: %s CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED [CONTINUING_PAIR]\n", argv[0]);
		fprintf(stderr, "       %s --reject-swapped-data CONFIG\n", argv[0]);
		fprintf(stderr, "       %s --reject-ipv6-mappings CONFIG PAIR\n", argv[0]);
		fprintf(stderr, "       %s --pending-retire CONFIG PAIR SECONDS MIN_PENDING MIN_RETURNED\n", argv[0]);
		fprintf(stderr, "       %s --reuse-slots CONFIG FIRST_PAIR SECOND_PAIR THIRD_PAIR SECONDS MIN_FORWARDED MIN_RETURNED\n", argv[0]);
		fprintf(stderr, "       %s --guarded-pair CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED TRAFFIC_CLASS FLOW_LABEL\n", argv[0]);
		fprintf(stderr, "       %s --resume-identical CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED TRAFFIC_CLASS FLOW_LABEL\n", argv[0]);
		fprintf(stderr, "CONFIG starts with MWAN_NATIVE_CONFIG 2\n");
		fprintf(stderr, "Resources: cpu memory_mb memory_mode pair_capacity mbuf_count drain_ms rx_desc tx_desc mtu file_prefix hugepage_directory\n");
		fprintf(stderr, "Counts: controllers receivers data_devices origins\n");
		fprintf(stderr, "Controller rows: pci physical_receiver_id\nReceiver rows: controller_id vf mac\n");
		fprintf(stderr, "DATA rows: pci controller_id vf mac; controller_id=4294967295 and vf=0 select output-only\n");
		fprintf(stderr, "Origin rows: controller_id kind receiver_id data_id return_mode source_vf; kind=0 return_mode=0 source_vf=0 select the supported physical path\n");
		fprintf(stderr, "PAIR starts with MWAN_NATIVE_PAIR 2, followed by requested_direction_mask cookie policy_generation\n");
		fprintf(stderr, "Each requested direction, in bit order: origin_id egress_data_id family protocol original_src original_dst original_sport original_dport translated_src translated_dst translated_sport translated_dport original_src_mac original_dst_mac output_src_mac output_dst_mac mtu\n");
		fprintf(stderr, "Memory mode 0 uses hugepages; 1 uses anonymous memory. Use - for an empty hugepage directory. Unrequested directions do not have rows.\n");
		return EXIT_FAILURE;
	}
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec pair = {0};
	struct mwan_native_pair_spec continuing = {0};
	uint64_t seconds, minimum_forwarded, minimum_returned;
	if (read_config(argv[1], &config) || read_pair(argv[2], &pair) ||
	    positive(argv[3], &seconds) || seconds > 3600 ||
	    positive(argv[4], &minimum_forwarded) || positive(argv[5], &minimum_returned) ||
	    (argc == 7 && read_pair(argv[6], &continuing))) {
		fprintf(stderr, "Invalid hardware test configuration or packet minimum\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result result = {0};
	struct mwan_native_pair_result continuing_result = {0};
	struct mwan_native_pair_stats counters = {0};
	struct mwan_native_worker_stats stats = {0};
	bool passed = false;
	if (reject_overlap(&config)) {
		fprintf(stderr, "Cross-role PCI overlap did not fail before EAL initialization\n");
		return EXIT_FAILURE;
	}
	if (mwan_native_open(&config, &owner, &error) || mwan_native_pair_install(owner, &pair, &result, &error))
		goto cleanup;
	if (argc == 7 && mwan_native_pair_install(owner, &continuing, &continuing_result, &error))
		goto cleanup;
	printf("READY requested=%u installed=%u generation=%" PRIu64 "\n", result.requested_direction_mask, result.installed_mask, result.handle.generation);
	fflush(stdout);
	uint64_t deadline = now_ns() + seconds * UINT64_C(1000000000);
	while (now_ns() < deadline)
		if (mwan_native_poll(owner, 64, &stats, &error))
			goto cleanup;
	if (mwan_native_pair_query(owner, result.handle, &counters, &error))
		goto cleanup;
	uint64_t hits = report_counters("COUNTER", &counters);
	if (argc == 7) {
		struct mwan_native_pair_stats continuing_counters = {0};
		if (mwan_native_pair_query(owner, continuing_result.handle, &continuing_counters, &error))
			goto cleanup;
		if (report_counters("CONTINUING_COUNTER", &continuing_counters) < minimum_forwarded) {
			fprintf(stderr, "Continuing pair did not receive the required packets before retirement\n");
			goto cleanup;
		}
	}
	printf("WORKER received=%" PRIu64 " forwarded=%" PRIu64 " returned=%" PRIu64 " pending=%" PRIu64 " quarantined=%" PRIu64 " discarded_group=%" PRIu64 " malformed=%" PRIu64 "\n",
		stats.received, stats.forwarded, stats.returned_to_kernel, stats.pending_packets, stats.quarantined_packets,
		stats.discarded_group_frames, stats.malformed);
	if (stats.forwarded < minimum_forwarded || stats.returned_to_kernel < minimum_returned ||
	    hits < minimum_forwarded + minimum_returned || stats.pending_packets || stats.quarantined_packets) {
		fprintf(stderr, "Hardware traffic did not satisfy the requested packet minima\n");
		goto cleanup;
	}
	if (argc == 7) {
		/* External traffic fills RX while the owner pauses before deletion. */
		puts("QUEUE_RETIRING: continue both pairs during the 100 ms RX pause");
		fflush(stdout);
		struct timespec pause = {.tv_nsec = 100000000};
		if (nanosleep(&pause, NULL))
			goto cleanup;
	}
	uint64_t remove_start = now_ns();
	if (mwan_native_pair_remove(owner, result.handle, &result, &error) ||
	    result.state != MWAN_NATIVE_PAIR_DRAINING || result.installed_mask ||
	    result.restoration_mask != pair.requested_direction_mask)
		goto cleanup;
	printf("RETIRED elapsed_ns=%" PRIu64 " installed=%u retained=%u\n", now_ns() - remove_start,
		result.installed_mask, result.restoration_mask);
	fflush(stdout);
	if (argc == 7) {
		uint64_t before_forwarded = stats.forwarded, before_returned = stats.returned_to_kernel;
		deadline = now_ns() + seconds * UINT64_C(1000000000);
		while (now_ns() < deadline)
			if (mwan_native_poll(owner, 64, &stats, &error))
				goto cleanup;
		if (mwan_native_pair_query(owner, continuing_result.handle, &counters, &error) ||
		    counters.result.installed_mask != continuing.requested_direction_mask ||
		    stats.forwarded - before_forwarded < minimum_forwarded ||
		    stats.returned_to_kernel - before_returned < minimum_returned) {
			fprintf(stderr, "Retirement did not preserve forwarding and late packet restoration\n");
			goto cleanup;
		}
		printf("RETIREMENT forwarded=%" PRIu64 " returned=%" PRIu64 "\n",
			stats.forwarded - before_forwarded, stats.returned_to_kernel - before_returned);
	}
	if (mwan_native_close(&owner, MWAN_NATIVE_DISABLE, &error) ||
	    mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, &pair, &result, &error) ||
	    result.restoration_mask != pair.requested_direction_mask)
		goto cleanup;
	passed = true;
cleanup:
	if (!passed && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		passed = false;
	}
	if (passed)
		puts("PASS hardware packet minima, owned removal, disable/reopen and final shutdown");
	return passed ? EXIT_SUCCESS : EXIT_FAILURE;
}
