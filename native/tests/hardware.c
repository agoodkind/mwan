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

static int positive(const char *text, uint64_t *value)
{
	char *end;
	if (!text[0] || text[0] == '-')
		return -1;
	errno = 0;
	*value = strtoull(text, &end, 10);
	return errno || *end || !*value ? -1 : 0;
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

int main(int argc, char **argv)
{
	if (argc != 6 && argc != 7) {
		fprintf(stderr, "Usage: %s CONFIG PAIR SECONDS MIN_FORWARDED MIN_RETURNED [CONTINUING_PAIR]\n", argv[0]);
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
	printf("WORKER received=%" PRIu64 " forwarded=%" PRIu64 " returned=%" PRIu64 " pending=%" PRIu64 " quarantined=%" PRIu64 " discarded_group=%" PRIu64 "\n",
		stats.received, stats.forwarded, stats.returned_to_kernel, stats.pending_packets, stats.quarantined_packets,
		stats.discarded_group_frames);
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
