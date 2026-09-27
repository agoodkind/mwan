//go:build linux

package netif

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// StartRuleMonitor observes policy-rule removal once for the caller's network
// namespace. Rule messages have no interface identity.
func StartRuleMonitor(ctx context.Context, log *slog.Logger, handle func(RuleEvent)) error {
	socket, err := nl.Subscribe(unix.NETLINK_ROUTE,
		uint(unix.RTNLGRP_IPV4_RULE), uint(unix.RTNLGRP_IPV6_RULE))
	if err != nil {
		return fmt.Errorf("subscribe policy rules: %w", err)
	}
	if err := socket.SetReceiveTimeout(&unix.Timeval{Usec: 250000}); err != nil {
		socket.Close()
		return fmt.Errorf("set policy-rule receive timeout: %w", err)
	}
	go func() {
		defer socket.Close()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "policy-rule monitor panicked", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		for ctx.Err() == nil {
			messages, sender, err := socket.Receive()
			if errors.Is(err, unix.EAGAIN) {
				continue
			}
			if err != nil {
				log.WarnContext(ctx, "policy-rule subscription failed", "err", err)
				return
			}
			if sender.Pid != 0 {
				continue
			}
			for _, message := range messages {
				event, err := UnmarshalRuleDeletion(log, message)
				if err != nil {
					log.WarnContext(ctx, "policy-rule event unreadable", "err", err)
					continue
				}
				if event.Family != "" {
					handle(event)
				}
			}
		}
	}()
	return nil
}

// UnmarshalRuleDeletion decodes one kernel policy-rule deletion message.
func UnmarshalRuleDeletion(log *slog.Logger, message syscall.NetlinkMessage) (RuleEvent, error) {
	var empty RuleEvent
	if message.Header.Type != unix.RTM_DELRULE {
		return empty, nil
	}
	if len(message.Data) < unix.SizeofRtMsg {
		return empty, fmt.Errorf("policy-rule header has %d bytes", len(message.Data))
	}
	family := "inet"
	switch message.Data[0] {
	case unix.AF_INET:
	case unix.AF_INET6:
		family = "inet6"
	default:
		return empty, nil
	}
	event := RuleEvent{
		Family: family, TableID: int(message.Data[4]), Priority: 0,
		From: "", Mark: 0, IifName: "", UIDRange: "",
	}
	attributes, err := nl.ParseRouteAttr(message.Data[unix.SizeofRtMsg:])
	if err != nil {
		log.Warn("policy-rule attributes unreadable", "err", err)
		return empty, fmt.Errorf("parse policy-rule attributes: %w", err)
	}
	for _, attribute := range attributes {
		switch attribute.Attr.Type {
		case unix.FRA_PRIORITY:
			event.Priority, err = ruleAttributeInt(attribute.Value)
		case unix.FRA_TABLE:
			event.TableID, err = ruleAttributeInt(attribute.Value)
		case unix.FRA_FWMARK:
			if len(attribute.Value) != 4 {
				err = fmt.Errorf("fwmark has %d bytes", len(attribute.Value))
				break
			}
			event.Mark = binary.NativeEndian.Uint32(attribute.Value)
		case unix.FRA_IIFNAME:
			event.IifName = strings.TrimRight(string(attribute.Value), "\x00")
		case unix.FRA_SRC:
			event.From, err = ruleSource(family, message.Data[2], attribute.Value)
		case unix.FRA_UID_RANGE:
			if len(attribute.Value) != 8 {
				err = fmt.Errorf("uid range has %d bytes", len(attribute.Value))
				break
			}
			start := binary.NativeEndian.Uint32(attribute.Value[:4])
			end := binary.NativeEndian.Uint32(attribute.Value[4:])
			event.UIDRange = strconv.FormatUint(uint64(start), 10) + "-" + strconv.FormatUint(uint64(end), 10)
		}
		if err != nil {
			return empty, err
		}
	}
	return event, nil
}

func ruleAttributeInt(value []byte) (int, error) {
	if len(value) != 4 {
		return 0, fmt.Errorf("numeric policy-rule attribute has %d bytes", len(value))
	}
	return int(binary.NativeEndian.Uint32(value)), nil
}

func ruleSource(family string, bits byte, value []byte) (string, error) {
	address, ok := netip.AddrFromSlice(value)
	if !ok || (family == "inet" && !address.Is4()) || (family == "inet6" && !address.Is6()) {
		return "", fmt.Errorf("invalid %s policy-rule source", family)
	}
	prefix := netip.PrefixFrom(address, int(bits))
	if !prefix.IsValid() {
		return "", fmt.Errorf("invalid policy-rule source length %d", bits)
	}
	if int(bits) == address.BitLen() {
		return address.String(), nil
	}
	return prefix.Masked().String(), nil
}
