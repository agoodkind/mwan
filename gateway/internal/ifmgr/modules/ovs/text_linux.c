//go:build linux && cgo

#include <config.h>
#include <arpa/inet.h>
#include <stdlib.h>
#include <string.h>
#include "nx-match.h"
#include "openvswitch/ofp-actions.h"
#include "openvswitch/ofp-flow.h"
#include "openvswitch/ofp-parse.h"
#include "openvswitch/ofp-port.h"
#include "openvswitch/ofpbuf.h"
#include "text.h"

struct ofputil_port_map *mwan_ports_new(void) {
    struct ofputil_port_map *ports = xmalloc(sizeof *ports);
    ofputil_port_map_init(ports);
    return ports;
}

const char *mwan_validate_flow(const uint8_t *wire) {
    struct ofputil_flow_mod flow;
    struct ofpbuf actions;
    ofpbuf_init(&actions, 128);
    enum ofperr error = ofputil_decode_flow_mod(&flow, (const struct ofp_header *)wire, OFPUTIL_P_OF15_OXM,
                                                NULL, NULL, &actions, OFPP_MAX, 255);
    if (!error) {
        minimatch_destroy(&flow.match);
    }
    ofpbuf_uninit(&actions);
    return error ? ofperr_get_name(error) : NULL;
}

void mwan_ports_free(struct ofputil_port_map *ports) {
    ofputil_port_map_destroy(ports);
    free(ports);
}

int mwan_port_add(struct ofputil_port_map *ports, uint32_t number, const char *name) {
    ofp_port_t port;
    int error = ofputil_port_from_ofp11(htonl(number), &port);
    if (!error) {
        ofputil_port_map_put(ports, port, name);
    }
    return error;
}

static char *check_fields(const char *text) {
    char *copy = xstrdup(text);
    char *cursor = copy, *name, *value, *error = NULL;
    while (ofputil_parse_key_value(&cursor, &name, &value)) {
        const struct ofp_protocol *protocol;
        if (ofp_parse_protocol(name, &protocol)) {
            if (!*value) {
                continue;
            }
        } else if (mf_from_name(name) || strchr(name, '[')) {
            continue;
        }
        error = xasprintf("expected a packet match field or protocol, got %s", name);
        break;
    }
    free(copy);
    return error;
}

static char *map_groups(struct ofpbuf *actions, uint32_t group_namespace,
                        uint32_t max_group_id) {
    struct ofpact_group *group;
    OFPACT_FOR_EACH_TYPE_FLATTENED(group, GROUP, actions->data, actions->size) {
        if (!group->group_id || group->group_id > max_group_id) {
            return xstrdup("group action requires a configured logical group ID");
        }
        group->group_id |= group_namespace;
    }
    return NULL;
}

struct mwan_wire mwan_parse(const struct ofputil_port_map *ports, int kind, const char *text,
                            uint32_t group_namespace, uint32_t max_group_id) {
    struct ofpbuf wire;
    enum ofputil_protocol protocols = OFPUTIL_P_ANY;
    ofpbuf_init(&wire, 128);
    char *error;
    if (kind == 0) {
        struct ofputil_flow_mod fm;
        error = check_fields(text);
        if (!error) {
            error = parse_ofp_str(&fm, -1, text, ports, NULL, &protocols);
        }
        if (!error) {
            struct match match;
            minimatch_expand(&fm.match, &match);
            if (protocols & OFPUTIL_P_OF15_OXM) {
                oxm_put_raw(&wire, &match, OFP15_VERSION);
            }
            minimatch_destroy(&fm.match);
        }
    } else {
        struct ofpbuf actions;
        ofpbuf_init(&actions, 128);
        struct ofpact_parse_params params = {
            .port_map = ports, .ofpacts = &actions, .usable_protocols = &protocols,
        };
        if (kind == 1) {
            error = ofpacts_parse_instructions(text, &params);
        } else {
            error = ofpacts_parse_actions(text, &params);
        }
        if (!error) {
            error = map_groups(&actions, group_namespace, max_group_id);
        }
        if (!error && (protocols & OFPUTIL_P_OF15_OXM)) {
            if (kind == 1) {
                ofpacts_put_openflow_instructions(actions.data, actions.size, &wire, OFP15_VERSION);
            } else {
                ofpacts_put_openflow_actions(actions.data, actions.size, &wire, OFP15_VERSION);
            }
        }
        ofpbuf_uninit(&actions);
    }
    if (!error && !(protocols & OFPUTIL_P_OF15_OXM)) {
        error = xstrdup("expression is incompatible with OpenFlow 1.5");
    }
    struct mwan_wire result = {.error = error};
    if (!result.error) {
        result.size = wire.size;
        result.data = ofpbuf_steal_data(&wire);
    }
    ofpbuf_uninit(&wire);
    return result;
}
