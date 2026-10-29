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
	struct mwan_native_pair_result reobserved_retained = {0};
	if (mwan_native_pair_reobserve(owner, first_result.handle, &first, retained_guards,
				       &reobserved_retained, &error) != -ESTALE ||
	    reobserved_retained.handle.instance != first_result.handle.instance ||
	    reobserved_retained.handle.generation != first_result.handle.generation ||
	    reobserved_retained.handle.slot != first_result.handle.slot || reobserved_retained.handle.reserved ||
	    reobserved_retained.state != MWAN_NATIVE_PAIR_DRAINING ||
	    reobserved_retained.installed_mask || reobserved_retained.cleanup_failed_mask ||
	    reobserved_retained.restoration_mask != first.requested_direction_mask) {
		fprintf(stderr, "Selector replacement accepted an old hardware-slot generation\n");
		goto cleanup;
	}
	printf("REUSE_REJECTED_REOBSERVE slot=%u generation=%" PRIu64 " state=%u retained=%u\n",
		reobserved_retained.handle.slot, reobserved_retained.handle.generation,
		reobserved_retained.state, retirement.retained_pairs);
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
	       first->retained_directions == second->retained_directions &&
	       first->shared_binding_capacity == second->shared_binding_capacity &&
	       first->shared_bindings == second->shared_bindings &&
	       first->shared_identity_bindings == second->shared_identity_bindings &&
	       first->reusable_shared_slots == second->reusable_shared_slots &&
	       first->shared_binding_conflicts == second->shared_binding_conflicts &&
	       first->shared_binding_capacity_rejections == second->shared_binding_capacity_rejections &&
	       first->shared_pair_releases == second->shared_pair_releases;
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
	struct mwan_native_pair_result rejected_active = {0};
	if (mwan_native_pair_reobserve(owner, handle, &pair, guards, &rejected_active, &error) != -EBUSY ||
	    !same_resume_handle(rejected_active.handle, handle) ||
	    rejected_active.state != MWAN_NATIVE_PAIR_INSTALLED ||
	    rejected_active.installed_mask != pair.requested_direction_mask ||
	    rejected_active.cleanup_failed_mask) {
		fprintf(stderr, "Selector replacement accepted an installed pair or changed its ownership\n");
		goto cleanup;
	}
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
		if (i != 3) {
			memset(&rejected, 0, sizeof(rejected));
			rc = mwan_native_pair_reobserve(owner, invalid_handle, &invalid, invalid_guards,
						 &rejected, &error);
			valid_result = i == 7 ||
				(same_resume_handle(rejected.handle, handle) &&
				 rejected.state == MWAN_NATIVE_PAIR_DRAINING &&
				 rejected.requested_direction_mask == pair.requested_direction_mask &&
				 !rejected.installed_mask && !rejected.cleanup_failed_mask &&
				 rejected.restoration_mask == pair.requested_direction_mask);
			if (rc != expected || !valid_result ||
			    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error)) {
				fprintf(stderr, "Selector replacement did not reject %s while retaining the pair\n", rejections[i]);
				goto cleanup;
			}
			printf("REOBSERVE_REJECTED %s state=%u retained=%u\n",
				rejections[i], rejected.state, retired.retained_pairs);
		}
	}
	struct mwan_native_pair_result unchanged = {0};
	if (mwan_native_pair_resume_observed(owner, handle, &pair, guards, &unchanged, &error) ||
	    !same_resume_handle(unchanged.handle, handle) ||
	    unchanged.installed_mask != pair.requested_direction_mask || unchanged.cleanup_failed_mask ||
	    mwan_native_pair_remove(owner, handle, &unchanged, &error) ||
	    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error)) {
		fprintf(stderr, "Rejected selector changes altered the original guards\n");
		goto cleanup;
	}
	struct mwan_native_packet_guard changed_guards[MWAN_NATIVE_DIRECTION_COUNT];
	memcpy(changed_guards, guards, sizeof(changed_guards));
	if (pair.directions[requested].original.family == MWAN_NATIVE_FAMILY_IPV6)
		changed_guards[requested].flow_label ^= 1U;
	else
		changed_guards[requested].traffic_class ^= 4U;
	struct mwan_native_pair_result changed = {0};
	if (mwan_native_pair_resume_observed(owner, handle, &pair, changed_guards, &changed, &error) != -EINVAL ||
	    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error) ||
	    mwan_native_pair_reobserve(owner, handle, &pair, changed_guards, &changed, &error) ||
	    !same_resume_handle(changed.handle, handle) || changed.state != MWAN_NATIVE_PAIR_INSTALLED ||
	    changed.installed_mask != pair.requested_direction_mask || changed.cleanup_failed_mask ||
	    changed.restoration_mask != pair.requested_direction_mask ||
	    mwan_native_pair_remove(owner, handle, &changed, &error) ||
	    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error)) {
		fprintf(stderr, "Explicit selector replacement did not preserve the cleaned primary generation\n");
		goto cleanup;
	}
	if (mwan_native_pair_resume_observed(owner, handle, &pair, guards, &changed, &error) != -EINVAL ||
	    mwan_native_pair_reobserve(owner, handle, &pair, guards, &changed, &error) ||
	    mwan_native_pair_remove(owner, handle, &changed, &error) ||
	    resume_still_draining(owner, handle, pair.requested_direction_mask, &retired, &error)) {
		fprintf(stderr, "Selector replacement did not commit new guards or restore the original guards explicitly\n");
		goto cleanup;
	}
	printf("REOBSERVE_BOUNDARIES slot=%u generation=%" PRIu64 " api_only=1\n", handle.slot, handle.generation);
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

enum {
	RECYCLE_MIN_CYCLES = 3,
	RECYCLE_MAX_CYCLES = 16,
	RECYCLE_MAX_PHASE_SECONDS = 30,
	RECYCLE_EXTERNAL_PROOF_REQUIRED = 2,
	RECYCLE_RX_PAUSE_NS = 100000000
};

static int recycle_pair_state(uint64_t cycle, const char *phase,
			      const struct mwan_native_pair_result *result,
			      uint32_t state, uint32_t mask)
{
	printf("RECYCLE_PAIR cycle=%" PRIu64 " phase=%s instance=%" PRIu64
	       " slot=%u generation=%" PRIu64 " state=%u requested=%u installed=%u"
	       " restoration=%u cleanup_failed=%u\n", cycle, phase, result->handle.instance,
	       result->handle.slot, result->handle.generation, result->state,
	       result->requested_direction_mask, result->installed_mask,
	       result->restoration_mask, result->cleanup_failed_mask);
	fflush(stdout);
	uint32_t installed = state == MWAN_NATIVE_PAIR_INSTALLED ? mask : 0;
	if (!result->handle.instance || !result->handle.generation || result->handle.slot ||
	    result->handle.reserved || result->state != state ||
	    result->requested_direction_mask != mask || result->installed_mask != installed ||
	    result->restoration_mask != mask || result->cleanup_failed_mask) {
		fprintf(stderr, "Cycle %" PRIu64 " %s did not preserve the expected pair ownership\n", cycle, phase);
		return -1;
	}
	return 0;
}

static int recycle_storage(struct mwan_native *owner, uint64_t cycle, const char *phase,
			   uint32_t retained, uint32_t draining, struct mwan_native_error *error)
{
	struct mwan_native_retirement_stats current = {0};
	if (mwan_native_retirement_query(owner, &current, error))
		return -1;
	printf("RECYCLE_STORAGE cycle=%" PRIu64 " phase=%s active_capacity=%u retained_capacity=%u"
	       " retained=%u draining=%u retained_directions=%" PRIu64 "\n", cycle, phase,
	       current.active_pair_capacity, current.retained_pair_capacity, current.retained_pairs,
	       current.draining_pairs, current.retained_directions);
	fflush(stdout);
	if (current.active_pair_capacity != 1 || current.retained_pair_capacity != 1 ||
	    current.retained_pairs != retained || current.draining_pairs != draining ||
	    current.retained_directions != retained) {
		fprintf(stderr, "Cycle %" PRIu64 " %s reported unexpected retained storage\n", cycle, phase);
		return -1;
	}
	return 0;
}

static int recycle_worker(uint64_t cycle, const char *phase,
			  const struct mwan_native_worker_stats *before,
			  const struct mwan_native_worker_stats *current)
{
	if (current->forwarded < before->forwarded ||
	    current->returned_to_kernel < before->returned_to_kernel ||
	    current->received < before->received) {
		fprintf(stderr, "Cycle %" PRIu64 " %s reset worker counters across the same owner\n", cycle, phase);
		return -1;
	}
	printf("RECYCLE_WORKER cycle=%" PRIu64 " phase=%s received=%" PRIu64
	       " forwarded=%" PRIu64 " returned=%" PRIu64 " phase_forwarded=%" PRIu64
	       " phase_returned=%" PRIu64 " pending=%" PRIu64 " quarantined=%" PRIu64
	       " malformed=%" PRIu64 " lookup_misses=%" PRIu64 " transmit_dropped=%" PRIu64 "\n",
	       cycle, phase, current->received, current->forwarded, current->returned_to_kernel,
	       current->forwarded - before->forwarded,
	       current->returned_to_kernel - before->returned_to_kernel,
	       current->pending_packets, current->quarantined_packets,
	       current->malformed, current->lookup_misses, current->transmit_dropped);
	fflush(stdout);
	if (current->pending_packets || current->quarantined_packets ||
	    current->lookup_misses != before->lookup_misses ||
	    current->transmit_dropped != before->transmit_dropped) {
		fprintf(stderr, "Cycle %" PRIu64 " %s retained, lost or failed to identify a packet\n", cycle, phase);
		return -1;
	}
	return 0;
}

static int recycle_counters(struct mwan_native *owner, uint64_t cycle, const char *phase,
			    struct mwan_native_handle handle, uint32_t mask, uint64_t minimum,
			    struct mwan_native_error *error)
{
	struct mwan_native_pair_stats counters = {0};
	if (mwan_native_pair_query(owner, handle, &counters, error))
		return -1;
	if (!same_resume_handle(counters.result.handle, handle) ||
	    recycle_pair_state(cycle, phase, &counters.result, MWAN_NATIVE_PAIR_INSTALLED, mask)) {
		fprintf(stderr, "Counter query did not preserve the requested recycle generation\n");
		return -1;
	}
	uint32_t direction = mask == 1U ? 0U : 1U;
	const struct mwan_native_counter *counter = &counters.directions[direction];
	printf("RECYCLE_COUNTER cycle=%" PRIu64 " phase=%s instance=%" PRIu64
	       " slot=%u generation=%" PRIu64 " direction=%u valid=%u/%u error=%d"
	       " observed_monotonic_ns=%" PRIu64 " packets=%" PRIu64 " bytes=%" PRIu64 "\n",
	       cycle, phase, handle.instance, handle.slot, handle.generation, direction,
	       counter->packets_valid, counter->bytes_valid, counter->error,
	       counter->observed_monotonic_ns, counter->packets, counter->bytes);
	fflush(stdout);
	if (!counter->packets_valid || !counter->bytes_valid || counter->error ||
	    !counter->observed_monotonic_ns || !counter->bytes || counter->packets < minimum) {
		fprintf(stderr, "Cycle %" PRIu64 " %s did not provide the required valid hardware counts\n", cycle, phase);
		return -1;
	}
	return 0;
}

static int recycle_stale(struct mwan_native *owner, uint64_t cycle, const char *phase,
			 struct mwan_native_handle handle, const struct mwan_native_pair_spec *pair,
			 struct mwan_native_error *error)
{
	struct mwan_native_pair_stats counters = {0};
	struct mwan_native_pair_result removed = {0}, resumed = {0};
	struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT] = {0};
	int query = mwan_native_pair_query(owner, handle, &counters, error);
	int remove = mwan_native_pair_remove(owner, handle, &removed, error);
	int resume = mwan_native_pair_resume_observed(owner, handle, pair, guards, &resumed, error);
	printf("RECYCLE_STALE cycle=%" PRIu64 " phase=%s instance=%" PRIu64
	       " slot=%u generation=%" PRIu64 " query_rc=%d remove_rc=%d resume_rc=%d\n",
	       cycle, phase, handle.instance, handle.slot, handle.generation, query, remove, resume);
	fflush(stdout);
	if (query != -ESTALE || remove != -ESTALE || resume != -ESTALE ||
	    counters.result.installed_mask || counters.result.restoration_mask ||
	    counters.result.cleanup_failed_mask ||
	    removed.installed_mask || removed.restoration_mask ||
	    removed.cleanup_failed_mask || resumed.installed_mask || resumed.restoration_mask ||
	    resumed.cleanup_failed_mask) {
		fprintf(stderr, "A stale generation operated on the replacement during cycle %" PRIu64 "\n", cycle);
		return -1;
	}
	return 0;
}

static int recycle_slots(const char *config_path, const char *first_path,
			  const char *second_path, const char *reused_path,
			  uint64_t cycles, uint64_t seconds,
			  uint64_t minimum_forwarded, uint64_t minimum_returned)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec first = {0}, second = {0}, reused = {0};
	if (read_config(config_path, &config) || read_pair(first_path, &first) ||
	    read_pair(second_path, &second) || read_pair(reused_path, &reused)) {
		fprintf(stderr, "Invalid recycle-slot configuration\n");
		return EXIT_FAILURE;
	}
	uint32_t mask = first.requested_direction_mask;
	uint32_t direction = mask == 1U ? 0U : 1U;
	if (config.pair_capacity != 1 || (mask != 1U && mask != 2U) ||
	    second.requested_direction_mask != mask || reused.requested_direction_mask != mask ||
	    first.cookie == reused.cookie ||
	    first.directions[direction].origin_id != reused.directions[direction].origin_id ||
	    memcmp(&first.directions[direction].translated, &reused.directions[direction].translated,
		   sizeof(first.directions[direction].translated)) ||
	    !memcmp(&first.directions[direction].original, &reused.directions[direction].original,
		    sizeof(first.directions[direction].original)) ||
	    memcmp(first.directions[direction].original_source_mac,
		   reused.directions[direction].original_source_mac, 6) ||
	    memcmp(first.directions[direction].original_destination_mac,
		   reused.directions[direction].original_destination_mac, 6)) {
		fprintf(stderr, "Recycle slots requires P=1, one common direction, and FIRST/REUSED with different cookies/original tuples but identical translated tuples, origins and input MACs\n");
		return EXIT_FAILURE;
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result current = {0}, bridge = {0}, retired = {0}, rejected = {0};
	struct mwan_native_worker_stats stats = {0}, before = {0};
	const struct mwan_native_pair_spec *active_pair = &first;
	const char *active_name = "FIRST";
	bool complete = false;
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, active_pair, &current, &error))
		goto cleanup;
	struct mwan_native *identity = owner;
	uint64_t instance = current.handle.instance;
	puts("RECYCLE_CONTRACT external_unique_packet_ids=required boundary_proven=0 source=external_only");
	fflush(stdout);
	for (uint64_t cycle = 1; cycle <= cycles; cycle++) {
		const struct mwan_native_pair_spec *replacement = active_pair == &first ? &reused : &first;
		const char *replacement_name = active_pair == &first ? "REUSED" : "FIRST";
		if (recycle_pair_state(cycle, "current", &current, MWAN_NATIVE_PAIR_INSTALLED, mask))
			goto cleanup;
		printf("READY_RECYCLE_CURRENT cycle=%" PRIu64 " pair=%s seconds=%" PRIu64
		       " cookie=%" PRIu64 " policy=%" PRIu64 "\n", cycle, active_name, seconds,
		       active_pair->cookie, active_pair->policy_generation);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "recycle current", &stats, &error) ||
		    recycle_worker(cycle, "current", &before, &stats) ||
		    recycle_counters(owner, cycle, "current", current.handle, mask, minimum_forwarded, &error))
			goto cleanup;
		if (stats.forwarded - before.forwarded < minimum_forwarded) {
			fprintf(stderr, "Current pair did not forward the required fresh packets\n");
			goto cleanup;
		}
		printf("QUEUE_RECYCLE_OLD cycle=%" PRIu64 " pair=%s rx_pause_ns=%u purpose=queue_external_old_packets_not_fence\n",
		       cycle, active_name, RECYCLE_RX_PAUSE_NS);
		fflush(stdout);
		struct timespec pause = {.tv_nsec = RECYCLE_RX_PAUSE_NS};
		if (nanosleep(&pause, NULL)) {
			perror("queue recycle old packets");
			goto cleanup;
		}
		struct mwan_native_handle old_handle = current.handle;
		if (mwan_native_pair_remove(owner, old_handle, &retired, &error) ||
		    recycle_pair_state(cycle, "old_removed", &retired, MWAN_NATIVE_PAIR_DRAINING, mask) ||
		    mwan_native_pair_install(owner, &second, &bridge, &error) ||
		    recycle_pair_state(cycle, "bridge", &bridge, MWAN_NATIVE_PAIR_INSTALLED, mask) ||
		    recycle_storage(owner, cycle, "retained_old", 1, 0, &error))
			goto cleanup;
		if (bridge.handle.instance != instance || bridge.handle.generation <= old_handle.generation) {
			fprintf(stderr, "Bridge pair did not reuse the same owner's slot with a newer generation\n");
			goto cleanup;
		}
		memset(&rejected, 0, sizeof(rejected));
		int collision = mwan_native_pair_install(owner, replacement, &rejected, &error);
		printf("RECYCLE_COLLISION cycle=%" PRIu64 " replacement=%s rc=%d\n",
		       cycle, replacement_name, collision);
		fflush(stdout);
		if (collision != -EEXIST || rejected.installed_mask || rejected.restoration_mask) {
			fprintf(stderr, "Tuple reuse did not reject while its old inverse remained owned\n");
			goto cleanup;
		}
		printf("READY_RECYCLE_BRIDGE cycle=%" PRIu64 " pair=SECOND old_pair=%s seconds=%" PRIu64
		       " cookie=%" PRIu64 " policy=%" PRIu64 " send_second_and_stop_old_after_queue=required\n",
		       cycle, active_name, seconds, second.cookie, second.policy_generation);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "recycle bridge", &stats, &error) ||
		    recycle_worker(cycle, "bridge", &before, &stats) ||
		    recycle_counters(owner, cycle, "bridge", bridge.handle, mask, minimum_forwarded, &error))
			goto cleanup;
		if (stats.forwarded - before.forwarded < minimum_forwarded ||
		    stats.returned_to_kernel - before.returned_to_kernel < minimum_returned) {
			fprintf(stderr, "Bridge phase did not meet forwarding and kernel-return minima; external IDs must identify the restored old packets\n");
			goto cleanup;
		}
		struct mwan_native_pair_stats old_counters = {0};
		int old_query = mwan_native_pair_query(owner, old_handle, &old_counters, &error);
		printf("RECYCLE_OLD_QUERY cycle=%" PRIu64 " rc=%d\n", cycle, old_query);
		fflush(stdout);
		if (old_query != -ENOENT || !same_resume_handle(old_counters.result.handle, old_handle) ||
		    recycle_pair_state(cycle, "retained_old", &old_counters.result, MWAN_NATIVE_PAIR_DRAINING, mask)) {
			fprintf(stderr, "The old retained handle lost its inverse ownership before recycling\n");
			goto cleanup;
		}
		if (mwan_native_pair_remove(owner, bridge.handle, &retired, &error) ||
		    recycle_pair_state(cycle, "bridge_removed", &retired, MWAN_NATIVE_PAIR_DRAINING, mask) ||
		    recycle_storage(owner, cycle, "before_disable", 1, 1, &error))
			goto cleanup;
		printf("READY_RECYCLE_STOP cycle=%" PRIu64 " seconds=%" PRIu64
		       " stop_all_generators=required observation_is_not_fence=1\n", cycle, seconds);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "recycle stop", &stats, &error) ||
		    recycle_worker(cycle, "stop", &before, &stats))
			goto cleanup;
		if (stats.forwarded != before.forwarded) {
			fprintf(stderr, "Removed rules forwarded packets during the stop phase\n");
			goto cleanup;
		}
		if (mwan_native_close(&owner, MWAN_NATIVE_DISABLE, &error) ||
		    owner != identity || recycle_storage(owner, cycle, "disabled", 0, 0, &error)) {
			fprintf(stderr, "Disable did not preserve the owner and reclaim software inverse storage\n");
			goto cleanup;
		}
		before = stats;
		if (mwan_native_poll(owner, 64, &stats, &error) ||
		    recycle_worker(cycle, "disabled", &before, &stats) || stats.forwarded != before.forwarded) {
			fprintf(stderr, "Disabled-owner snapshot did not preserve the removal boundary\n");
			goto cleanup;
		}
		if (mwan_native_open(&config, &owner, &error) ||
		    owner != identity || recycle_storage(owner, cycle, "enabled", 0, 0, &error)) {
			fprintf(stderr, "Identical enable did not preserve the native owner\n");
			goto cleanup;
		}
		printf("READY_RECYCLE_UNMAPPED cycle=%" PRIu64 " seconds=%" PRIu64
		       " old_pair=%s old_original_kernel_probes=required unexpected_data_must_fail=1\n", cycle, seconds, active_name);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "recycle unmapped", &stats, &error) ||
		    recycle_worker(cycle, "unmapped", &before, &stats))
			goto cleanup;
		if (stats.forwarded != before.forwarded ||
		    stats.returned_to_kernel != before.returned_to_kernel) {
			fprintf(stderr, "Unmapped phase processed old DATA traffic after reclamation\n");
			goto cleanup;
		}
		if (mwan_native_pair_install(owner, replacement, &current, &error) ||
		    recycle_pair_state(cycle, "replacement", &current, MWAN_NATIVE_PAIR_INSTALLED, mask) ||
		    recycle_storage(owner, cycle, "replacement", 0, 0, &error))
			goto cleanup;
		if (current.handle.instance != instance ||
		    current.handle.generation <= bridge.handle.generation) {
			fprintf(stderr, "Recycled tuple did not receive a newer generation on the same owner\n");
			goto cleanup;
		}
		if (recycle_stale(owner, cycle, "old", old_handle, active_pair, &error) ||
		    recycle_stale(owner, cycle, "bridge", bridge.handle, &second, &error))
			goto cleanup;
		printf("READY_RECYCLE_REUSED cycle=%" PRIu64 " pair=%s seconds=%" PRIu64
		       " cookie=%" PRIu64 " policy=%" PRIu64 " compare_external_unique_ids=required\n",
		       cycle, replacement_name, seconds, replacement->cookie, replacement->policy_generation);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "recycle replacement", &stats, &error) ||
		    recycle_worker(cycle, "replacement", &before, &stats) ||
		    recycle_counters(owner, cycle, "replacement", current.handle, mask, minimum_forwarded, &error))
			goto cleanup;
		if (stats.forwarded - before.forwarded < minimum_forwarded) {
			fprintf(stderr, "Reused tuple did not forward fresh packets after stale operations\n");
			goto cleanup;
		}
		active_pair = replacement;
		active_name = replacement_name;
	}
	if (mwan_native_pair_remove(owner, current.handle, &retired, &error) ||
	    recycle_pair_state(cycles, "final_removed", &retired, MWAN_NATIVE_PAIR_DRAINING, mask))
		goto cleanup;
	printf("READY_RECYCLE_FINAL_STOP seconds=%" PRIu64 " stop_all_generators=required\n", seconds);
	fflush(stdout);
	before = stats;
	if (resume_poll_phase(owner, seconds, "recycle final stop", &stats, &error) ||
	    recycle_worker(cycles, "final_stop", &before, &stats))
		goto cleanup;
	if (stats.forwarded != before.forwarded) {
		fprintf(stderr, "Removed final rules forwarded packets during final stop\n");
		goto cleanup;
	}
	complete = true;
cleanup:
	if (!complete && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		complete = false;
	}
	if (owner) {
		fprintf(stderr, "Final shutdown retained the native owner\n");
		complete = false;
	}
	if (!complete)
		return EXIT_FAILURE;
	printf("RECYCLE_API_COMPLETE cycles=%" PRIu64 " successful_installs=%" PRIu64
	       " owner_released=1 boundary_proven=0 external_packet_proof=required\n",
	       cycles, 1 + 2 * cycles);
	fprintf(stderr, "Public native APIs do not certify device completion or external packet completeness; the reclamation fence claim remains unproved.\n");
	return RECYCLE_EXTERNAL_PROOF_REQUIRED;
}

static int shared_storage(struct mwan_native *owner, uint32_t expected_bindings,
			  uint64_t releases, bool reusable, struct mwan_native_error *error)
{
	struct mwan_native_retirement_stats stats = {0};
	if (mwan_native_retirement_query(owner, &stats, error))
		return -1;
	printf("SHARED_STORAGE bindings=%u capacity=%u identity_bindings=%u reusable=%u"
	       " retained=%u retained_directions=%" PRIu64 " releases=%" PRIu64
	       " conflicts=%" PRIu64 " capacity_rejections=%" PRIu64 "\n",
	       stats.shared_bindings, stats.shared_binding_capacity, stats.shared_identity_bindings,
	       stats.reusable_shared_slots, stats.retained_pairs, stats.retained_directions,
	       stats.shared_pair_releases, stats.shared_binding_conflicts, stats.shared_binding_capacity_rejections);
	fflush(stdout);
	if (stats.shared_bindings != expected_bindings || stats.shared_binding_capacity != 2 ||
	    stats.retained_pairs || stats.retained_directions ||
	    stats.reusable_shared_slots != (reusable ? 1U : 0U) ||
	    stats.shared_pair_releases != releases) {
		fprintf(stderr, "Shared release changed the bounded storage contract\n");
		return -1;
	}
	return 0;
}

static int shared_stale(struct mwan_native *owner, uint64_t cycle,
			struct mwan_native_handle handle, const struct mwan_native_pair_spec *spec,
			struct mwan_native_error *error)
{
	struct mwan_native_pair_result released = {0}, reobserved = {0};
	struct mwan_native_packet_guard guards[MWAN_NATIVE_DIRECTION_COUNT] = {0};
	if (recycle_stale(owner, cycle, "released_before_replacement", handle, spec, error))
		return -1;
	int release = mwan_native_pair_release_shared(owner, handle, &released, error);
	int reobserve = mwan_native_pair_reobserve(owner, handle, spec, guards, &reobserved, error);
	if (release != -ESTALE || reobserve != -ESTALE || released.installed_mask ||
	    released.restoration_mask || reobserved.installed_mask || reobserved.restoration_mask) {
		fprintf(stderr, "A released handle remained usable before replacement\n");
		return -1;
	}
	return 0;
}

static int shared_release_pair(struct mwan_native *owner, struct mwan_native_handle handle,
			       uint32_t mask, struct mwan_native_error *error)
{
	struct mwan_native_pair_result result = {0};
	if (mwan_native_pair_remove(owner, handle, &result, error) ||
	    result.state != MWAN_NATIVE_PAIR_DRAINING || result.installed_mask ||
	    result.cleanup_failed_mask || result.shared_restoration_mask != mask ||
	    result.restoration_mask != mask || !same_resume_handle(result.handle, handle))
		return -1;
	if (mwan_native_pair_release_shared(owner, handle, &result, error) ||
	    result.state != MWAN_NATIVE_PAIR_RELEASED || result.installed_mask ||
	    result.cleanup_failed_mask || result.shared_restoration_mask != mask ||
	    !same_resume_handle(result.handle, handle))
		return -1;
	return 0;
}

static int shared_capacity(const char *config_path, uint32_t field, const char *pair_path)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec first = {0}, second = {0}, third = {0};
	if (read_config(config_path, &config) || config.pair_capacity != 1 || read_pair(pair_path, &first) ||
	    (first.requested_direction_mask != 1 && first.requested_direction_mask != 2)) {
		fprintf(stderr, "Shared capacity requires P=1 and one exact IPv6 direction\n");
		return EXIT_FAILURE;
	}
	for (uint32_t i = 0; i < config.origin_count; i++)
		config.origins[i].ipv6_restore_field = field;
	uint32_t direction = first.requested_direction_mask == 1 ? 0 : 1;
	if (first.directions[direction].original.family != MWAN_NATIVE_FAMILY_IPV6) {
		fprintf(stderr, "Shared capacity requires an IPv6 pair\n");
		return EXIT_FAILURE;
	}
	second = first;
	third = first;
	second.directions[direction].original_source_mac[5] ^= 1;
	third.directions[direction].original_source_mac[5] ^= 2;
	struct mwan_native *owner = NULL;
	struct mwan_native_pair_result result = {0}, rejected = {0};
	struct mwan_native_error error = {0};
	struct mwan_native_retirement_stats stats = {0};
	bool complete = false;
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, &first, &result, &error) ||
	    shared_release_pair(owner, result.handle, first.requested_direction_mask, &error) ||
	    mwan_native_pair_install(owner, &second, &result, &error) ||
	    shared_release_pair(owner, result.handle, first.requested_direction_mask, &error) ||
	    shared_storage(owner, 2, 2, true, &error))
		goto cleanup;
	int capacity = mwan_native_pair_install(owner, &third, &rejected, &error);
	if (capacity != -ENOSPC || rejected.installed_mask || rejected.restoration_mask ||
	    mwan_native_retirement_query(owner, &stats, &error) ||
	    stats.shared_binding_capacity_rejections != 1 || stats.shared_bindings != 2 ||
	    mwan_native_pair_install(owner, &first, &result, &error) ||
	    shared_release_pair(owner, result.handle, first.requested_direction_mask, &error) ||
	    shared_storage(owner, 2, 3, true, &error)) {
		fprintf(stderr, "Full shared capacity did not reject a new MAC key and accept an existing key\n");
		goto cleanup;
	}
	complete = true;
cleanup:
	if (!complete && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		complete = false;
	}
	if (complete)
		puts("PASS shared capacity rejection, separate MAC bindings and existing-key admission at capacity");
	return complete && !owner ? EXIT_SUCCESS : EXIT_FAILURE;
}

static int shared_conflict(const char *config_path, uint32_t field,
			   const char *first_path, const char *conflict_path,
			   uint64_t seconds, uint64_t minimum_returned)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec first = {0}, conflict = {0};
	if (read_config(config_path, &config) || config.pair_capacity != 1 ||
	    read_pair(first_path, &first) || read_pair(conflict_path, &conflict) ||
	    (first.requested_direction_mask != 1 && first.requested_direction_mask != 2) ||
	    conflict.requested_direction_mask != first.requested_direction_mask) {
		fprintf(stderr, "Shared conflict requires P=1 and one common requested direction\n");
		return EXIT_FAILURE;
	}
	for (uint32_t i = 0; i < config.origin_count; i++)
		config.origins[i].ipv6_restore_field = field;
	struct mwan_native *owner = NULL;
	struct mwan_native_pair_result result = {0}, rejected = {0};
	struct mwan_native_error error = {0};
	struct mwan_native_pair_stats baseline = {0}, queued = {0};
	struct mwan_native_retirement_stats retirement = {0};
	struct mwan_native_worker_stats stats = {0};
	uint32_t direction = first.requested_direction_mask == 1 ? 0 : 1;
	bool complete = false;
	if (mwan_native_open(&config, &owner, &error) ||
	    mwan_native_pair_install(owner, &first, &result, &error) ||
	    mwan_native_pair_query(owner, result.handle, &baseline, &error))
		goto cleanup;
	puts("QUEUE_SHARED_CONFLICT_OLD acknowledgment=QUEUED_CONFLICT polling=paused");
	fflush(stdout);
	char acknowledgment[64];
	if (!fgets(acknowledgment, sizeof(acknowledgment), stdin) || strcmp(acknowledgment, "QUEUED_CONFLICT\n")) {
		fprintf(stderr, "Send the finite FIRST batch, then write QUEUED_CONFLICT to stdin\n");
		goto cleanup;
	}
	if (mwan_native_pair_query(owner, result.handle, &queued, &error))
		goto cleanup;
	const struct mwan_native_counter *before = &baseline.directions[direction];
	const struct mwan_native_counter *after = &queued.directions[direction];
	if (!before->packets_valid || before->error || !after->packets_valid || after->error ||
	    after->packets < before->packets || after->packets - before->packets < minimum_returned) {
		fprintf(stderr, "The accepted rule did not count the required finite batch\n");
		goto cleanup;
	}
	printf("SHARED_CONFLICT_COUNTER baseline=%" PRIu64 " queued=%" PRIu64 "\n", before->packets, after->packets);
	struct mwan_native_handle handle = result.handle;
	if (shared_release_pair(owner, handle, first.requested_direction_mask, &error) ||
	    shared_stale(owner, 0, handle, &first, &error))
		goto cleanup;
	int collision = mwan_native_pair_install(owner, &conflict, &rejected, &error);
	if (collision != -EEXIST || rejected.installed_mask || rejected.restoration_mask ||
	    mwan_native_retirement_query(owner, &retirement, &error) ||
	    retirement.shared_binding_conflicts != 1 || retirement.shared_bindings != 1 ||
	    retirement.shared_pair_releases != 1 || retirement.retained_pairs ||
	    retirement.reusable_shared_slots != 1) {
		fprintf(stderr, "The public ABI did not reject a permanent inverse conflict before creation\n");
		goto cleanup;
	}
	printf("READY_SHARED_CONFLICT_RETURN seconds=%" PRIu64 " original_replay=forbidden\n", seconds);
	fflush(stdout);
	if (resume_poll_phase(owner, seconds, "shared conflict return", &stats, &error) ||
	    stats.returned_to_kernel < minimum_returned || stats.forwarded || stats.pending_packets ||
	    stats.quarantined_packets || stats.lookup_misses)
		goto cleanup;
	printf("SHARED_CONFLICT_COMPLETE returned=%" PRIu64 " identities=%u external_exact_frame_capture=required\n",
	       stats.returned_to_kernel, retirement.shared_identity_bindings);
	complete = true;
cleanup:
	if (!complete && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		complete = false;
	}
	return complete && !owner ? EXIT_SUCCESS : EXIT_FAILURE;
}

static int shared_release(const char *config_path, uint32_t field, uint64_t seconds,
			  uint64_t minimum_forwarded, uint64_t minimum_returned,
			  int pair_count, char **pair_paths)
{
	struct mwan_native_config config = {0};
	struct mwan_native_pair_spec *pairs = calloc((size_t)pair_count, sizeof(*pairs));
	if (!pairs || read_config(config_path, &config) || config.pair_capacity != 1) {
		fprintf(stderr, "Shared release requires a valid configuration with pair_capacity=1\n");
		free(pairs);
		return EXIT_FAILURE;
	}
	for (uint32_t i = 0; i < config.origin_count; i++)
		config.origins[i].ipv6_restore_field = field;
	uint32_t mask = 0, direction_index = 0;
	for (int i = 0; i < pair_count; i++) {
		if (read_pair(pair_paths[i], &pairs[i])) {
			fprintf(stderr, "Cannot read exact shared pair %s\n", pair_paths[i]);
			free(pairs);
			return EXIT_FAILURE;
		}
		if (i == 0) {
			mask = pairs[i].requested_direction_mask;
			direction_index = mask == 1 ? 0 : 1;
		}
		const struct mwan_native_direction *first = &pairs[0].directions[direction_index];
		const struct mwan_native_direction *current = &pairs[i].directions[direction_index];
		const uint8_t *first_original = field == MWAN_NATIVE_IPV6_RESTORE_SOURCE ? first->original.source : first->original.destination;
		const uint8_t *first_translated = field == MWAN_NATIVE_IPV6_RESTORE_SOURCE ? first->translated.source : first->translated.destination;
		const uint8_t *current_original = field == MWAN_NATIVE_IPV6_RESTORE_SOURCE ? current->original.source : current->original.destination;
		const uint8_t *current_translated = field == MWAN_NATIVE_IPV6_RESTORE_SOURCE ? current->translated.source : current->translated.destination;
		if ((mask != 1 && mask != 2) || pairs[i].requested_direction_mask != mask ||
		    current->original.family != MWAN_NATIVE_FAMILY_IPV6 || current->origin_id != first->origin_id ||
		    memcmp(current_original, first_original, 16) || memcmp(current_translated, first_translated, 16) ||
		    memcmp(current->original_source_mac, first->original_source_mac, 6) ||
		    memcmp(current->original_destination_mac, first->original_destination_mac, 6)) {
			fprintf(stderr, "Shared release requires one fixed address binding and MAC context across all exact pairs\n");
			free(pairs);
			return EXIT_FAILURE;
		}
		for (int j = 0; j < i; j++) {
			if (!memcmp(&current->translated, &pairs[j].directions[direction_index].translated, sizeof(current->translated))) {
				fprintf(stderr, "Shared release requires distinct translated full tuples\n");
				free(pairs);
				return EXIT_FAILURE;
			}
		}
	}
	struct mwan_native *owner = NULL;
	struct mwan_native_error error = {0};
	struct mwan_native_pair_result current = {0};
	struct mwan_native_worker_stats stats = {0}, before = {0};
	bool complete = false;
	if (mwan_native_open(&config, &owner, &error) || mwan_native_pair_install(owner, &pairs[0], &current, &error))
		goto cleanup;
	uint64_t instance = current.handle.instance;
	for (int cycle = 0; cycle < pair_count; cycle++) {
		printf("READY_SHARED_ACTIVE cycle=%d pair=%s seconds=%" PRIu64 " generation=%" PRIu64 "\n",
		       cycle, pair_paths[cycle], seconds, current.handle.generation);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "shared active", &stats, &error) ||
		    recycle_worker((uint64_t)cycle, "shared_active", &before, &stats) ||
		    stats.forwarded - before.forwarded < minimum_forwarded ||
		    shared_storage(owner, 1, (uint64_t)cycle, false, &error))
			goto cleanup;
		struct mwan_native_pair_stats baseline = {0}, queued = {0};
		if (mwan_native_pair_query(owner, current.handle, &baseline, &error))
			goto cleanup;
		printf("QUEUE_SHARED_OLD cycle=%d pair=%s acknowledgment=QUEUED_%d polling=paused\n",
		       cycle, pair_paths[cycle], cycle);
		fflush(stdout);
		char acknowledgment[64], expected[64];
		snprintf(expected, sizeof(expected), "QUEUED_%d\n", cycle);
		if (!fgets(acknowledgment, sizeof(acknowledgment), stdin) || strcmp(acknowledgment, expected)) {
			fprintf(stderr, "Send the finite old-rule batch, then write %s to stdin\n", expected);
			goto cleanup;
		}
		if (mwan_native_pair_query(owner, current.handle, &queued, &error))
			goto cleanup;
		const struct mwan_native_counter *first_counter = &baseline.directions[direction_index];
		const struct mwan_native_counter *queued_counter = &queued.directions[direction_index];
		if (!first_counter->packets_valid || first_counter->error || !queued_counter->packets_valid ||
		    queued_counter->error || queued_counter->packets < first_counter->packets ||
		    queued_counter->packets - first_counter->packets < minimum_returned) {
			fprintf(stderr, "The old hardware rule did not count the required finite batch\n");
			goto cleanup;
		}
		printf("SHARED_OLD_COUNTER cycle=%d baseline=%" PRIu64 " queued=%" PRIu64 "\n",
		       cycle, first_counter->packets, queued_counter->packets);
		struct mwan_native_handle old = current.handle;
		if (shared_release_pair(owner, old, mask, &error) ||
		    shared_stale(owner, (uint64_t)cycle, old, &pairs[cycle], &error) ||
		    shared_storage(owner, 1, (uint64_t)cycle + 1, true, &error))
			goto cleanup;
		int next = (cycle + 1) % pair_count;
		if (mwan_native_pair_install(owner, &pairs[next], &current, &error) ||
		    current.handle.instance != instance || current.handle.generation <= old.generation ||
		    shared_storage(owner, 1, (uint64_t)cycle + 1, false, &error))
			goto cleanup;
		printf("READY_SHARED_DELAYED cycle=%d replacement=%s seconds=%" PRIu64 " original_replay=forbidden\n",
		       cycle, pair_paths[next], seconds);
		fflush(stdout);
		before = stats;
		if (resume_poll_phase(owner, seconds, "shared delayed", &stats, &error) ||
		    recycle_worker((uint64_t)cycle, "shared_delayed", &before, &stats) ||
		    stats.returned_to_kernel - before.returned_to_kernel < minimum_returned)
			goto cleanup;
		printf("SHARED_DELAYED_COMPLETE cycle=%d returned=%" PRIu64 " external_capture=required\n",
		       cycle, stats.returned_to_kernel - before.returned_to_kernel);
		fflush(stdout);
	}
	if (shared_release_pair(owner, current.handle, mask, &error) ||
	    shared_stale(owner, (uint64_t)pair_count, current.handle, &pairs[0], &error) ||
	    shared_storage(owner, 1, (uint64_t)pair_count + 1, true, &error))
		goto cleanup;
	complete = true;
cleanup:
	if (!complete && error.code)
		report_error(&error);
	if (owner && mwan_native_close(&owner, MWAN_NATIVE_FINAL_SHUTDOWN, &error)) {
		report_error(&error);
		complete = false;
	}
	free(pairs);
	if (!complete || owner)
		return EXIT_FAILURE;
	printf("SHARED_API_COMPLETE cycles=%d external_exact_frame_capture=required\n", pair_count);
	return EXIT_SUCCESS;
}

int main(int argc, char **argv)
{
	if (argc == 5 && !strcmp(argv[1], "--shared-capacity")) {
		uint32_t field = 0;
		if (!strcmp(argv[3], "source"))
			field = MWAN_NATIVE_IPV6_RESTORE_SOURCE;
		if (!strcmp(argv[3], "destination"))
			field = MWAN_NATIVE_IPV6_RESTORE_DESTINATION;
		if (!field) {
			fprintf(stderr, "Shared capacity requires source or destination as its profile\n");
			return EXIT_FAILURE;
		}
		return shared_capacity(argv[2], field, argv[4]);
	}
	if (argc == 8 && !strcmp(argv[1], "--shared-conflict")) {
		uint32_t field = 0;
		uint64_t seconds, minimum_returned;
		if (!strcmp(argv[3], "source"))
			field = MWAN_NATIVE_IPV6_RESTORE_SOURCE;
		if (!strcmp(argv[3], "destination"))
			field = MWAN_NATIVE_IPV6_RESTORE_DESTINATION;
		if (!field || positive(argv[6], &seconds) || seconds > 30 || positive(argv[7], &minimum_returned)) {
			fprintf(stderr, "Shared conflict requires source or destination, 1-30 seconds and a positive return minimum\n");
			return EXIT_FAILURE;
		}
		return shared_conflict(argv[2], field, argv[4], argv[5], seconds, minimum_returned);
	}
	if (argc > 1 && !strcmp(argv[1], "--shared-release")) {
		uint64_t seconds, minimum_forwarded, minimum_returned;
		uint32_t field = MWAN_NATIVE_IPV6_RESTORE_LEGACY;
		if (argc >= 15 && !strcmp(argv[3], "source"))
			field = MWAN_NATIVE_IPV6_RESTORE_SOURCE;
		if (argc >= 15 && !strcmp(argv[3], "destination"))
			field = MWAN_NATIVE_IPV6_RESTORE_DESTINATION;
		if (argc < 15 || argc > 263 || !field || positive(argv[4], &seconds) || seconds > 30 ||
		    positive(argv[5], &minimum_forwarded) || positive(argv[6], &minimum_returned)) {
			fprintf(stderr, "Usage: %s --shared-release CONFIG source|destination SECONDS MIN_FORWARD MIN_RETURN PAIR... (8-256 pairs)\n", argv[0]);
			return EXIT_FAILURE;
		}
		return shared_release(argv[2], field, seconds, minimum_forwarded, minimum_returned, argc - 7, &argv[7]);
	}
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
	if (argc > 1 && !strcmp(argv[1], "--recycle-slots")) {
		if (argc != 10) {
			fprintf(stderr, "Usage: %s --recycle-slots CONFIG FIRST_PAIR SECOND_PAIR REUSED_PAIR CYCLES PHASE_SECONDS MIN_FORWARDED MIN_RETURNED\n", argv[0]);
			return EXIT_FAILURE;
		}
		uint64_t cycles, seconds, minimum_forwarded, minimum_returned;
		if (positive(argv[6], &cycles) || cycles < RECYCLE_MIN_CYCLES || cycles > RECYCLE_MAX_CYCLES ||
		    positive(argv[7], &seconds) || seconds > RECYCLE_MAX_PHASE_SECONDS ||
		    positive(argv[8], &minimum_forwarded) || positive(argv[9], &minimum_returned)) {
			fprintf(stderr, "Recycle slots requires 3-16 cycles, 1-30 seconds per phase and positive packet minima\n");
			return EXIT_FAILURE;
		}
		return recycle_slots(argv[2], argv[3], argv[4], argv[5], cycles, seconds,
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
		fprintf(stderr, "       %s --recycle-slots CONFIG FIRST_PAIR SECOND_PAIR REUSED_PAIR CYCLES PHASE_SECONDS MIN_FORWARDED MIN_RETURNED\n", argv[0]);
		fprintf(stderr, "       %s --guarded-pair CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED TRAFFIC_CLASS FLOW_LABEL\n", argv[0]);
		fprintf(stderr, "       %s --resume-identical CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED TRAFFIC_CLASS FLOW_LABEL\n", argv[0]);
		fprintf(stderr, "       %s --shared-release CONFIG source|destination SECONDS MIN_FORWARD MIN_RETURN PAIR... (8-256 pairs)\n", argv[0]);
		fprintf(stderr, "       %s --shared-conflict CONFIG source|destination FIRST_PAIR CONFLICT_PAIR SECONDS MIN_RETURN\n", argv[0]);
		fprintf(stderr, "       %s --shared-capacity CONFIG source|destination PAIR\n", argv[0]);
		fprintf(stderr, "CONFIG starts with MWAN_NATIVE_CONFIG 3\n");
		fprintf(stderr, "Resources: cpu memory_mb memory_mode pair_capacity mbuf_count drain_ms rx_desc tx_desc mtu file_prefix hugepage_directory\n");
		fprintf(stderr, "Counts: controllers receivers data_devices origins\n");
		fprintf(stderr, "Controller rows: pci physical_receiver_id\nReceiver rows: controller_id vf mac\n");
		fprintf(stderr, "DATA rows: pci controller_id vf mac; controller_id=4294967295 and vf=0 select output-only\n");
		fprintf(stderr, "Origin rows: controller_id kind receiver_id data_id return_mode source_vf; kind=0 return_mode=0 source_vf=0 select the supported physical path\n");
		fprintf(stderr, "PAIR starts with MWAN_NATIVE_PAIR 3, followed by requested_direction_mask cookie policy_generation\n");
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
