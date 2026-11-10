//go:build (386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64 || wasm) && linux

// Package bpf installs RFC 6296 NPTv6 translation on Linux interfaces.
package bpf

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"structs"

	"github.com/cilium/ebpf"
)

// Byte layouts must match the C structs in npt.c; the kernel reads map values as C structs.
type nptPair struct {
	_                     structs.HostLayout
	Internal              [16]uint8
	External              [16]uint8
	Mask                  [8]uint8
	SourceExceptions      [16][16]uint8
	DestinationExceptions [16][16]uint8
	ID                    uint16
	ForwardAdjustment     uint16
	ReverseAdjustment     uint16
	InternalBits          uint8
	ExternalBits          uint8
	SourceCount           uint8
	DestinationCount      uint8
	Padding               [2]uint8
}

type nptPolicy struct {
	_            structs.HostLayout
	Internal     uint32
	Count        uint32
	Ethernet     uint32
	Padding      uint32
	IngressOrder [64]uint8
	EgressOrder  [64]uint8
	Pairs        [64]nptPair
}

type nptObjects struct {
	NptEgress  *ebpf.Program `ebpf:"npt_egress"`
	NptIngress *ebpf.Program `ebpf:"npt_ingress"`
	Policies   *ebpf.Map     `ebpf:"policies"`
}

func loadNptObjects(objects *nptObjects, options *ebpf.CollectionOptions) error {
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(nptObjectBytes))
	if err != nil {
		slog.Error("NPTv6 embedded object could not be decoded", "err", err)
		return fmt.Errorf("decode NPTv6 object: %w", err)
	}
	if err := spec.LoadAndAssign(objects, options); err != nil {
		slog.Error("NPTv6 object could not be assigned", "err", err)
		return fmt.Errorf("assign NPTv6 object: %w", err)
	}
	return nil
}

func (objects *nptObjects) Close() error {
	err := errors.Join(objects.NptEgress.Close(), objects.NptIngress.Close(), objects.Policies.Close())
	if err != nil {
		slog.Error("NPTv6 program and policy descriptors could not all be closed", "err", err)
	}
	return err
}

//go:embed npt_bpfel.o
var nptObjectBytes []byte
