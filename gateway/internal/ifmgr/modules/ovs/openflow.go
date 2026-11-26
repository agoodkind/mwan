package ovs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"strings"
	"time"

	"antrea.io/libOpenflow/common"
	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

const (
	flowHeaderLength = 8
	bundleID         = 1
	bundleFlags      = openflow15.BF_ATOMIC | openflow15.BF_ORDERED
)

var (
	errOpenFlowBridge   = errors.New("OpenFlow socket does not identify this bridge")
	errOpenFlowHeader   = errors.New("invalid OpenFlow 1.5 message header")
	errOpenFlowRejected = errors.New("OVS rejected an OpenFlow message")
	errOpenFlowBundle   = errors.New("unexpected OpenFlow bundle reply")
	errDuplicateFlow    = errors.New(
		"OpenFlow rules have identical tables, priorities, and matches",
	)
)

func installOpenFlow(
	ctx context.Context,
	database databaseClient,
	config ovsconfig.Config,
	health map[string]wanstate.MemberHealth,
) error {
	var interfaces []*interfaceRow
	if err := readRows(ctx, database, &interfaces); err != nil {
		return err
	}

	numbers := make(map[string]uint32, len(config.Ports))

	for _, device := range interfaces {
		if device.OpenFlow != nil && *device.OpenFlow > 0 && *device.OpenFlow <= math.MaxUint32 {
			numbers[device.Name] = uint32(*device.OpenFlow)
		}
	}

	bridgeID, err := readBridgeID(ctx, database, config.Bridge)
	if err != nil {
		return err
	}

	return installOnSwitch(ctx, config.OpenFlow, bridgeID, numbers, health)
}

func compileFlows(
	ctx context.Context,
	rules []ovsconfig.Flow,
	numbers map[string]uint32,
	health map[string]wanstate.MemberHealth,
	installed map[flowIdentity]flowContents,
) ([]*openflow15.FlowMod, error) {
	flows := make([]*openflow15.FlowMod, 0, len(rules)+1)
	removeOwned := openflow15.NewFlowMod()
	removeOwned.Command = openflow15.FC_DELETE
	removeOwned.TableId = openflow15.OFPTT_ALL
	removeOwned.Cookie = mwanFlowCookie
	removeOwned.CookieMask = mwanFlowMask
	flows = append(flows, removeOwned)

	seen := make(map[flowIdentity]string, len(rules))
	for _, rule := range rules {
		flow, err := marshalFlow(ctx, rule, numbers)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"OpenFlow rule compilation failed",
				"rule",
				rule.Name,
				"err",
				err,
			)

			return nil, fmt.Errorf("compile OpenFlow rule %q: %w", rule.Name, err)
		}

		key, err := ruleKey(0, flow.TableId, flow.Priority, flow.Match, nil)
		if err != nil {
			return nil, fmt.Errorf("encode match for OpenFlow rule %q: %w", rule.Name, err)
		}

		if original, found := seen[key.identity]; found {
			return nil, fmt.Errorf("%w: %q and %q", errDuplicateFlow, original, rule.Name)
		}

		seen[key.identity] = rule.Name
		entry := installed[key.identity]
		if entryActive(rule.Condition, health, entry.cookie&mwanFlowMask == mwanFlowCookie) {
			flows = append(flows, flow)
		}
	}

	return flows, nil
}

func readBridgeID(ctx context.Context, database databaseClient, bridgeName string) (string, error) {
	var bridges []*bridgeRow
	if err := readRows(ctx, database, &bridges); err != nil {
		return "", err
	}

	for _, bridge := range bridges {
		if bridge.Name == bridgeName && bridge.DatapathID != nil {
			return *bridge.DatapathID, nil
		}
	}

	slog.ErrorContext(
		ctx,
		"OVSDB did not report a datapath ID",
		"bridge",
		bridgeName,
		"err",
		errOpenFlowBridge,
	)

	return "", fmt.Errorf(
		"%w: OVSDB did not report a datapath ID for %q",
		errOpenFlowBridge,
		bridgeName,
	)
}

func installOnSwitch(
	ctx context.Context,
	config *ovsconfig.OpenFlowConfig,
	bridgeID string,
	numbers map[string]uint32,
	health map[string]wanstate.MemberHealth,
) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", config.Socket)
	if err != nil {
		slog.ErrorContext(ctx, "OpenFlow connection failed", "socket", config.Socket, "err", err)

		return fmt.Errorf("connect to OpenFlow socket %s: %w", config.Socket, err)
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.DebugContext(ctx, "OpenFlow connection close failed", "err", closeErr)
		}
	}()

	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		slog.ErrorContext(ctx, "OpenFlow deadline failed", "err", err)

		return fmt.Errorf("set OpenFlow deadline: %w", err)
	}

	stop := context.AfterFunc(
		ctx,
		func() {
			if deadlineErr := conn.SetDeadline(time.Unix(0, 0)); deadlineErr != nil {
				slog.DebugContext(ctx, "OpenFlow cancellation failed", "err", deadlineErr)
			}
		},
	)
	defer stop()

	if err := checkOpenFlowBridge(ctx, conn, bridgeID); err != nil {
		return err
	}

	var installed installedEntries
	installed.flows, err = readFlows(ctx, conn)
	if err != nil {
		return err
	}

	installed.groups, err = readGroups(ctx, conn)
	if err != nil {
		return err
	}

	flows, groups, err := compileOpenFlow(ctx, config, numbers, health, installed)
	if err != nil {
		return err
	}

	same, err := rulesMatch(installed.flows, flows[1:])
	if err != nil {
		return err
	}

	changes, err := groupChanges(installed.groups, groups)
	if err != nil || (same && len(changes) == 0) {
		return err
	}

	if err := replaceEntries(ctx, conn, flows, changes); err != nil {
		slog.ErrorContext(ctx, "OpenFlow bundle failed", "bridge", bridgeID, "err", err)

		return fmt.Errorf("apply OpenFlow bundle: %w", err)
	}

	return nil
}

func checkOpenFlowBridge(ctx context.Context, conn net.Conn, bridgeID string) error {
	hello := openflow15.NewOfp15Header()
	hello.Type = openflow15.Type_Hello
	if err := sendOpenFlow(ctx, conn, &hello); err != nil {
		return err
	}

	if _, err := receiveOpenFlow(ctx, conn, openflow15.Type_Hello, 0); err != nil {
		return err
	}

	request := openflow15.NewFeaturesRequest()
	if err := sendOpenFlow(ctx, conn, request); err != nil {
		return err
	}

	frame, err := receiveOpenFlow(ctx, conn, openflow15.Type_FeaturesReply, request.Xid)
	if err != nil {
		return err
	}

	features := openflow15.NewFeaturesReply()
	if len(frame) < int(features.Len()) {
		return errOpenFlowHeader
	}

	if err := features.UnmarshalBinary(frame); err != nil {
		slog.ErrorContext(ctx, "OpenFlow features reply could not be read", "err", err)

		return fmt.Errorf("decode OpenFlow features: %w", err)
	}

	if !strings.EqualFold(hex.EncodeToString(features.DPID), bridgeID) {
		return fmt.Errorf(
			"%w: expected %s, received %s",
			errOpenFlowBridge,
			bridgeID,
			features.DPID,
		)
	}

	return nil
}

func replaceEntries(
	ctx context.Context,
	conn net.Conn,
	flows []*openflow15.FlowMod,
	groups []*openflow15.GroupMod,
) error {
	if err := exchangeBundleControl(
		ctx,
		conn,
		openflow15.BCT_OPEN_REQUEST,
		openflow15.BCT_OPEN_REPLY,
	); err != nil {
		return err
	}

	if err := stageBundle(ctx, conn, flows, groups); err != nil {
		// Closing this connection discards its pending bundle if OVS cannot confirm a discard.
		if errors.Is(err, errOpenFlowRejected) {
			discardErr := exchangeBundleControl(
				ctx,
				conn,
				openflow15.BCT_DISCARD_REQUEST,
				openflow15.BCT_DISCARD_REPLY,
			)
			if discardErr != nil {
				slog.WarnContext(ctx, "OpenFlow bundle discard failed", "err", discardErr)

				return errors.Join(err, discardErr)
			}
		}

		return err
	}

	return exchangeBundleControl(
		ctx,
		conn,
		openflow15.BCT_COMMIT_REQUEST,
		openflow15.BCT_COMMIT_REPLY,
	)
}

func stageBundle(
	ctx context.Context,
	conn net.Conn,
	flows []*openflow15.FlowMod,
	groups []*openflow15.GroupMod,
) error {
	add := func(xid uint32, message util.Message) error {
		entry := openflow15.NewBndleAdd(bundleID, bundleFlags)
		entry.Xid, entry.Message = xid, message

		return sendOpenFlow(ctx, conn, entry)
	}
	if err := add(flows[0].Xid, flows[0]); err != nil {
		return err
	}

	for _, group := range groups {
		if err := add(group.Xid, group); err != nil {
			return err
		}
	}

	for _, flow := range flows[1:] {
		if err := add(flow.Xid, flow); err != nil {
			return err
		}
	}

	// OVS can reject one addition and accept others in that bundle.
	// Read all prior errors before committing.
	barrier := openflow15.NewBarrierRequest()
	if err := sendOpenFlow(ctx, conn, barrier); err != nil {
		return err
	}

	if _, err := receiveOpenFlow(ctx, conn, openflow15.Type_BarrierReply, barrier.Xid); err != nil {
		return err
	}

	return exchangeBundleControl(
		ctx,
		conn,
		openflow15.BCT_CLOSE_REQUEST,
		openflow15.BCT_CLOSE_REPLY,
	)
}

func exchangeBundleControl(ctx context.Context, conn net.Conn, request, reply uint16) error {
	control := openflow15.NewBundleCtrl(bundleID, request, bundleFlags)
	if err := sendOpenFlow(ctx, conn, control); err != nil {
		return err
	}

	frame, err := receiveOpenFlow(ctx, conn, openflow15.Type_BundleControl, control.Xid)
	if err != nil {
		return err
	}

	response := new(openflow15.BundleCtrl)
	if len(frame) < int(response.Len()) {
		return errOpenFlowHeader
	}

	if err := response.UnmarshalBinary(frame); err != nil {
		slog.ErrorContext(ctx, "OpenFlow bundle reply could not be read", "err", err)

		return fmt.Errorf("decode OpenFlow bundle reply: %w", err)
	}

	if response.Type != reply || response.BundleId != bundleID {
		return fmt.Errorf(
			"%w: bundle %d, type %d",
			errOpenFlowBundle,
			response.BundleId,
			response.Type,
		)
	}

	return nil
}

func sendOpenFlow(ctx context.Context, conn net.Conn, message util.Message) error {
	frame, err := message.MarshalBinary()
	if err != nil {
		slog.ErrorContext(ctx, "OpenFlow message encoding failed", "err", err)

		return fmt.Errorf("encode OpenFlow message: %w", err)
	}

	return writeOpenFlow(ctx, conn, frame)
}

func writeOpenFlow(ctx context.Context, conn net.Conn, frame []byte) error {
	if err := ctx.Err(); err != nil {
		slog.DebugContext(ctx, "OpenFlow write canceled", "err", err)

		return fmt.Errorf("write OpenFlow message: %w", err)
	}

	if _, err := conn.Write(frame); err != nil {
		slog.ErrorContext(ctx, "OpenFlow write failed", "err", err)

		return fmt.Errorf("write OpenFlow message: %w", err)
	}

	return nil
}

func receiveOpenFlow(
	ctx context.Context,
	conn net.Conn,
	messageType uint8,
	xid uint32,
) ([]byte, error) {
	for {
		header, frame, err := readOpenFlow(ctx, conn)
		if err != nil {
			return nil, err
		}

		switch header.Type {
		case openflow15.Type_Error:
			rejection := openflow15.NewErrorMsg()
			if err := rejection.UnmarshalBinary(frame); err != nil {
				slog.ErrorContext(ctx, "OpenFlow rejection could not be read", "err", err)

				return nil, fmt.Errorf("decode OpenFlow error: %w", err)
			}

			return nil, fmt.Errorf(
				"%w: type %d, code %d",
				errOpenFlowRejected,
				rejection.Type,
				rejection.Code,
			)
		case openflow15.Type_EchoRequest:
			frame[1] = openflow15.Type_EchoReply
			if err := writeOpenFlow(ctx, conn, frame); err != nil {
				return nil, err
			}
		case messageType:
			if xid == 0 || header.Xid == xid {
				return frame, nil
			}
		}
	}
}

func readOpenFlow(ctx context.Context, conn net.Conn) (common.Header, []byte, error) {
	var header common.Header
	var encoded [flowHeaderLength]byte
	if _, err := io.ReadFull(conn, encoded[:]); err != nil {
		slog.ErrorContext(ctx, "OpenFlow header read failed", "err", err)

		return header, nil, fmt.Errorf("read OpenFlow header: %w", err)
	}

	if err := header.UnmarshalBinary(encoded[:]); err != nil {
		slog.ErrorContext(ctx, "OpenFlow header could not be read", "err", err)

		return header, nil, fmt.Errorf("decode OpenFlow header: %w", err)
	}

	if header.Version != openflow15.VERSION || header.Length < flowHeaderLength {
		return header, nil, fmt.Errorf(
			"%w: version %d, length %d",
			errOpenFlowHeader,
			header.Version,
			header.Length,
		)
	}

	frame := make([]byte, header.Length)
	copy(frame, encoded[:])
	if _, err := io.ReadFull(conn, frame[flowHeaderLength:]); err != nil {
		slog.ErrorContext(ctx, "OpenFlow message read failed", "err", err)

		return header, nil, fmt.Errorf("read OpenFlow message: %w", err)
	}

	return header, frame, nil
}
