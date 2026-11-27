/*
 * Go code uses these C declarations to pass expressions and port numbers to
 * libopenvswitch and read back bytes, errors, and action dependencies.
 *
 *   Go expression and port map -> libopenvswitch -> OpenFlow bytes or error
 */

#include <stddef.h>
#include <stdint.h>

typedef uint8_t mwan_ports;
mwan_ports *mwan_ports_new(void);
void mwan_ports_free(mwan_ports *);
int mwan_port_add(mwan_ports *, uint32_t, const char *);
struct mwan_wire {
    char *error;
    char *data;
    size_t size;
    int current_table;
    uint32_t target_group;
    int has_group;
};
struct mwan_wire mwan_parse(const mwan_ports *, int kind, const char *text,
                            uint32_t group_namespace, uint32_t max_group_id);
const char *mwan_validate_flow(const uint8_t *wire);
void mwan_string_free(char *);

struct mwan_wire mwan_inspect(const uint8_t *, size_t, int actions, uint8_t *tables);
