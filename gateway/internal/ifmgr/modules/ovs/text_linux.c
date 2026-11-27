//go:build linux && cgo

/*
 * This cgo implementation uses libopenvswitch to parse expressions and check
 * FlowMod messages. It also reports the table references in instructions and
 * bucket actions. For a bucket, it returns the actions in the order OVS would
 * execute them.
 */

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

void mwan_string_free(char *s) { free(s); }

mwan_ports *mwan_ports_new(void) {
    struct ofputil_port_map *ports = xmalloc(sizeof *ports);
    ofputil_port_map_init(ports);
    return (mwan_ports *)ports;
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

void mwan_ports_free(mwan_ports *ports) {
    ofputil_port_map_destroy((struct ofputil_port_map *)ports);
    free(ports);
}

int mwan_port_add(mwan_ports *ports, uint32_t number, const char *name) {
    ofp_port_t port;
    int error = ofputil_port_from_ofp11(htonl(number), &port);
    if (!error) {
        ofputil_port_map_put((struct ofputil_port_map *)ports, port, name);
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

struct mwan_wire mwan_parse(const mwan_ports *ports, int kind, const char *text,
                            uint32_t group_namespace, uint32_t max_group_id) {
    struct ofpbuf wire;
    enum ofputil_protocol protocols = OFPUTIL_P_ANY;
    ofpbuf_init(&wire, 128);
    char *error;
    if (kind == 0) {
        struct ofputil_flow_mod fm;
        error = check_fields(text);
        if (!error) {
            error = parse_ofp_str(&fm, -1, text, (struct ofputil_port_map *)ports, NULL, &protocols);
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
            .port_map = (struct ofputil_port_map *)ports, .ofpacts = &actions, .usable_protocols = &protocols,
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

struct mwan_wire mwan_inspect(const uint8_t *data, size_t size, int actions, uint8_t *tables) {
    struct ofpbuf wire, unpacked, ordered, encoded;
    struct mwan_wire result = {0};
    ofpbuf_use_const(&wire, data, size);
    ofpbuf_init(&unpacked, size);
    enum ofperr error = actions
        ? ofpacts_pull_openflow_actions(&wire, size, OFP15_VERSION, NULL, NULL, &unpacked)
        : ofpacts_pull_openflow_instructions(&wire, size, OFP15_VERSION, NULL, NULL, &unpacked);
    if (error) {
        result.error = xstrdup(ofperr_get_name(error));
    } else {
        const struct ofpact *a;
        OFPACT_FOR_EACH_FLATTENED(a, unpacked.data, unpacked.size) {
            switch (a->type) {
            case OFPACT_GOTO_TABLE: tables[ofpact_get_GOTO_TABLE(a)->table_id] = 1; break;
            case OFPACT_RESUBMIT: tables[ofpact_get_RESUBMIT(a)->table_id] = 1; break;
            case OFPACT_LEARN: tables[ofpact_get_LEARN(a)->table_id] = 1; break;
            case OFPACT_CT: tables[ofpact_get_CT(a)->recirc_table] = 1; break;
            default: break;
            }
        }
        if (actions) {
            ofpbuf_init(&ordered, size);
            ofpbuf_init(&encoded, size);
            ofpacts_execute_action_set(&ordered, &unpacked);
            OFPACT_FOR_EACH_FLATTENED(a, ordered.data, ordered.size) {
                if (a->type == OFPACT_RESUBMIT && ofpact_get_RESUBMIT(a)->table_id == 255) {
                    result.current_table = 1;
                } else if (a->type == OFPACT_GROUP) {
                    result.has_group = 1;
                    result.target_group = ofpact_get_GROUP(a)->group_id;
                }
            }
            ofpacts_put_openflow_actions(ordered.data, ordered.size, &encoded, OFP15_VERSION);
            result.size = encoded.size;
            result.data = ofpbuf_steal_data(&encoded);
            ofpbuf_uninit(&encoded);
            ofpbuf_uninit(&ordered);
        }
    }
    ofpbuf_uninit(&unpacked);
    return result;
}
