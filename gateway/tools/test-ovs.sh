#!/usr/bin/env bash
set -euo pipefail

export OVS_RUNDIR=/run/openvswitch
export MWAN_TEST_OVS_SOCKET="$OVS_RUNDIR/db.sock"
install -d "$OVS_RUNDIR" /etc/systemd/network /var/lib/mwan
ovsdb-tool create "$OVS_RUNDIR/conf.db" /usr/share/openvswitch/vswitch.ovsschema
ovsdb-server --remote="punix:$MWAN_TEST_OVS_SOCKET" --detach --pidfile "$OVS_RUNDIR/conf.db"
ovs-vsctl --db="unix:$MWAN_TEST_OVS_SOCKET" --no-wait init
ovs-vswitchd --detach --pidfile "unix:$MWAN_TEST_OVS_SOCKET"

go test -tags ovs -count=1 -v ./internal/ifmgr/modules/ovs
