// Package nptpolicy defines RFC 6296 address mappings independently of packet backends.
package nptpolicy

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// MaxExceptions preserves the exception bound required by the BPF fallback.
const MaxExceptions = 16

// PrefixPair requires canonical IPv6 unicast prefixes of at most /64 and a nonzero ID.
type PrefixPair struct {
	ID                    uint16
	Internal              netip.Prefix
	External              netip.Prefix
	SourceExceptions      []netip.Addr
	DestinationExceptions []netip.Addr
}

// ValidatePair checks prefix and exception constraints required by the kernel program.
func ValidatePair(pair PrefixPair) error {
	for _, prefix := range []netip.Prefix{pair.Internal, pair.External} {
		if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Addr().Is4In6() || prefix.Bits() > 64 || !prefix.Addr().IsGlobalUnicast() || prefix != prefix.Masked() {
			return fmt.Errorf("NPTv6 pair %s / %s requires canonical IPv6 unicast prefixes at most /64", pair.Internal, pair.External)
		}
	}
	if pair.ID == 0 {
		return fmt.Errorf("NPTv6 pair requires a nonzero identity")
	}
	if len(pair.SourceExceptions) > MaxExceptions || len(pair.DestinationExceptions) > MaxExceptions {
		return fmt.Errorf("NPTv6 pair %d exceeds %d exceptions per direction", pair.ID, MaxExceptions)
	}
	for _, addresses := range [][]netip.Addr{pair.SourceExceptions, pair.DestinationExceptions} {
		for _, addr := range addresses {
			if !addr.Is6() || addr.Is4In6() {
				return fmt.Errorf("NPTv6 pair %d exception %s is not IPv6", pair.ID, addr)
			}
		}
	}
	return nil
}

func fold(value uint32) uint16 {
	for value > 0xffff {
		value = value&0xffff + value>>16
	}
	return uint16(value)
}

// Adjustment supplies the checksum correction stored in each BPF policy.
func Adjustment(from, to netip.Prefix) uint16 {
	f, t := from.Addr().As16(), to.Addr().As16()
	var source, target uint32
	for i := 0; i < 8; i += 2 {
		source += uint32(binary.BigEndian.Uint16(f[i : i+2]))
		target += uint32(binary.BigEndian.Uint16(t[i : i+2]))
	}
	return fold(uint32(fold(source)) + uint32(^fold(target)))
}

// TranslateAddress translates an IPv6 address between the pair's prefixes. It rejects an address with no available RFC 6296 correction word.
func TranslateAddress(pair PrefixPair, address netip.Addr, outbound bool) (netip.Addr, error) {
	if err := ValidatePair(pair); err != nil {
		return netip.Addr{}, err
	}
	from, to := pair.External, pair.Internal
	if outbound {
		from, to = pair.Internal, pair.External
	}
	bits := max(from.Bits(), to.Bits())
	if !netip.PrefixFrom(from.Addr(), bits).Contains(address) {
		return netip.Addr{}, fmt.Errorf("address %s is outside the translatable part of %s", address, from)
	}
	result, replacement := address.As16(), to.Addr().As16()
	word := 3
	if bits > 48 {
		word = 4
		allZero := true
		for i := 8; i < 16; i++ {
			allZero = allZero && result[i] == 0
		}
		if allZero {
			return netip.Addr{}, fmt.Errorf("address %s uses the RFC 6296 excluded zero identifier", address)
		}
		for word < 8 && binary.BigEndian.Uint16(result[word*2:word*2+2]) == 0xffff {
			word++
		}
	}
	if word == 8 || binary.BigEndian.Uint16(result[word*2:word*2+2]) == 0xffff {
		return netip.Addr{}, fmt.Errorf("address %s has no RFC 6296 correction word", address)
	}
	for bit := range bits {
		mask := byte(1 << (7 - bit%8))
		result[bit/8] = result[bit/8]&^mask | replacement[bit/8]&mask
	}
	value := fold(uint32(binary.BigEndian.Uint16(result[word*2:word*2+2])) + uint32(Adjustment(from, to)))
	if value == 0xffff {
		value = 0
	}
	binary.BigEndian.PutUint16(result[word*2:word*2+2], value)
	return netip.AddrFrom16(result), nil
}
