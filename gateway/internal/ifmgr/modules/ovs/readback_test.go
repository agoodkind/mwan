package ovs

// An in-memory OpenFlow peer returns installed rules with an extension instruction.
// Reconciliation compares the instruction bytes for a rule mwan owns. A foreign
// rule conflicts only when it uses the same switch match, table, and priority.

import (
	"errors"
	"net"
	"testing"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
)

func TestRulesMatchExperimenterInstruction(t *testing.T) {
	installed := []byte{0xff, 0xff, 0, 8, 0, 0, 0x23, 0x20}

	tests := []struct {
		name          string
		desired       []byte
		same          bool
		foreign       bool
		priorityDelta uint16
		wantError     error
	}{
		{name: "existing instruction", desired: installed, same: true},
		{
			name:    "different experimenter",
			desired: []byte{0xff, 0xff, 0, 8, 0, 0, 0x23, 0x21},
			same:    false,
		},
		{
			name:      "conflicting foreign rule",
			desired:   installed,
			foreign:   true,
			wantError: errForeignFlow,
		},
		{name: "unrelated foreign rule", desired: installed, foreign: true, priorityDelta: 1},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				ctx := t.Context()
				reader, peer := net.Pipe()
				defer reader.Close()
				defer peer.Close()
				rule := openflow15.NewFlowMod()
				rule.Cookie = mwanFlowCookie
				rule.AddInstruction(wireInstruction{Buffer: util.NewBuffer(tt.desired)})
				result := make(chan error, 1)
				go func() {
					h, _, err := readOpenFlow(ctx, peer)
					if err != nil {
						result <- err

						return
					}

					desc := openflow15.NewFlowDesc()
					desc.TableId, desc.Priority, desc.Cookie = rule.TableId, rule.Priority, rule.Cookie
					desc.Priority += tt.priorityDelta
					if tt.foreign {
						desc.Cookie = 42
					}

					desc.AddInstruction(wireInstruction{Buffer: util.NewBuffer(installed)})
					reply := new(openflow15.MultipartReply)
					reply.Header = openflow15.NewOfp15Header()
					reply.Header.Type = openflow15.Type_MultiPartReply
					reply.Xid = h.Xid
					reply.Type = openflow15.MultipartType_FlowDesc
					reply.Body = []util.Message{desc}
					result <- sendOpenFlow(ctx, peer, reply)
				}()
				current, err := readFlows(ctx, reader)
				if err != nil {
					t.Fatal(err)
				}

				same, err := rulesMatch(current, []*openflow15.FlowMod{rule})
				if !errors.Is(err, tt.wantError) {
					t.Fatalf("rulesMatch error = %v, want %v", err, tt.wantError)
				}

				if err := <-result; err != nil {
					t.Fatal(err)
				}

				if same != tt.same {
					t.Fatalf("rulesMatch returned %t, want %t", same, tt.same)
				}
			},
		)
	}
}
