#!/usr/bin/env bash
# This runner starts a disposable OVS instance in the privileged Linux
# builder. Most tests use its database socket. TestOVSRestartRecovery starts
# its own daemons and receives a private network namespace from unshare.

set -euo pipefail

export OVS_RUNDIR=/run/openvswitch
export MWAN_TEST_OVS_SOCKET="$OVS_RUNDIR/db.sock"
install -d "$OVS_RUNDIR" /etc/systemd/network /var/lib/mwan
ovsdb-tool create "$OVS_RUNDIR/conf.db" /usr/share/openvswitch/vswitch.ovsschema
ovsdb-server --remote="punix:$MWAN_TEST_OVS_SOCKET" --detach --pidfile "$OVS_RUNDIR/conf.db"
ovs-vsctl --db="unix:$MWAN_TEST_OVS_SOCKET" --no-wait init
ovs-vswitchd --detach --pidfile "unix:$MWAN_TEST_OVS_SOCKET"

go test -tags ovs -count=1 -v -exec 'unshare --net' -run '^TestOVSRestartRecovery$' ./internal/commandline
go test -tags ovs -count=1 -v ./internal/ifmgr/modules/ovs
go test -tags ovs -count=1 -v -run '^TestOVS(RuleUpdateMovesPackets|HealthConditions|Readable.*)$' ./internal/commandline
