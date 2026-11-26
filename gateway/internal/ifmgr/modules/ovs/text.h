#include <stddef.h>
#include <stdint.h>

struct ofputil_port_map;
struct ofputil_port_map *mwan_ports_new(void);
void mwan_ports_free(struct ofputil_port_map *);
int mwan_port_add(struct ofputil_port_map *, uint32_t, const char *);
struct mwan_wire {
    char *error;
    void *data;
    size_t size;
};
struct mwan_wire mwan_parse(const struct ofputil_port_map *, int kind, const char *text,
                            uint32_t group_namespace, uint32_t max_group_id);
const char *mwan_validate_flow(const void *wire);
