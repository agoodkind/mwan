#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "text.h"

int main(void) {
    const unsigned char original[] = {
        0,4,0,24,0,0,0,0,0,25,0,16,0x80,0,22,4,203,0,113,45,0,0,0,0,
        0,1,0,8,1,0,0,0
    };
    mwan_ports *ports = mwan_ports_new();
    struct mwan_wire wire = mwan_parse(ports, 1, "set_field:203.0.113.45->ip_src,goto_table:1",
                                       0x6d770000, 65535);
    if (wire.error) {
        fprintf(stderr, "%s\n", wire.error);
    }
    int failed = wire.error || wire.size != sizeof original || memcmp(wire.data, original, wire.size);
    if (failed) {
        fprintf(stderr, "IPv4 instructions differ from the original 32 wire bytes\n");
    }
    free(wire.error);
    free(wire.data);
    mwan_ports_free(ports);
    return failed;
}
