/*
 * These C routines give the Go migration code access to the libyang schema
 * and sysrepo values. They manage the lifetime of value arrays and the
 * formatted option text returned by sysrepo.
 */

#ifndef MWAN_TUNNEL_UPGRADE_H
#define MWAN_TUNNEL_UPGRADE_H

#include <libyang/libyang.h>
#include <stdlib.h>
#include <sysrepo.h>
#include <sysrepo/values.h>

struct tunnel_options {
    sr_val_t *values;
    size_t count;
    int result;
    char *text;
};

static inline void has_legacy_tunnel(sr_conn_ctx_t *conn, const char *path, int *present) {
    const struct ly_ctx *context = sr_acquire_context(conn);
    *present = lys_find_path(context, NULL, path, 0) != NULL;
    sr_release_context(conn);
}

static inline void lock_tunnel_interfaces(sr_session_ctx_t *session, uint32_t timeout, int *result) {
    *result = sr_lock(session, "ietf-interfaces", timeout);
}

static inline void finish_tunnel_session(sr_session_ctx_t *session) {
    sr_session_stop(session);
}

static inline void read_tunnel_options(sr_session_ctx_t *session, const char *path,
                                       uint32_t timeout, struct tunnel_options *options) {
    *options = (struct tunnel_options){0};
    options->result = sr_get_items(session, path, timeout, 0, &options->values, &options->count);
    if (options->result == SR_ERR_NOT_FOUND) {
        options->result = SR_ERR_OK;
    }
}

static inline void format_tunnel_value(struct tunnel_options *options, size_t index) {
    free(options->text);
    options->text = sr_val_to_str(&options->values[index]);
}

static inline void free_tunnel_options(struct tunnel_options *options) {
    free(options->text);
    sr_free_values(options->values, options->count);
    free(options);
}

#endif
