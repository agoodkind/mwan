package vpp

import (
	"bufio"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"go.fd.io/govpp/binapi/vlib"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
)

const (
	profileSamplePeriod = time.Millisecond
	profileDrainPeriod  = 20 * time.Millisecond
	profileDataPages    = 16
	perfRecordHeader    = 8
	perfSampleLength    = 24
	profileLimitation   = "Instruction samples identify recognized packet functions. Receive queue elapsed intervals do not measure scheduled CPU or establish an exact empty-polling CPU split."
)

type perfSampler struct {
	thread     vlib.ThreadData
	fd         int
	memory     []byte
	meta       *unix.PerfEventMmapPage
	data       []byte
	startID    string
	images     []mappedImage
	executable string
	counts     SampleCounts
	symbols    map[symbolIdentity]uint64
}

type symbolIdentity struct {
	module string
	name   string
	class  SampleClass
}

type mappedImage struct {
	start   uint64
	end     uint64
	offset  uint64
	path    string
	loads   []loadSegment
	symbols []elf.Symbol
}

type loadSegment struct {
	offset uint64
	size   uint64
	vaddr  uint64
}

// Profile samples native threads reported by VPP and verifies their executable identity.
// Symbol coverage remains explicit when packet and polling work share a function.
func Profile(ctx context.Context, endpoints Endpoints, executable string, duration time.Duration) (ProfileResult, error) {
	return ProfileWithOptions(ctx, endpoints, executable, duration, ProfileOptions{QueuePolling: false})
}

// ProfileWithOptions keeps optional queue intervals independent of sampled CPU time.
func ProfileWithOptions(ctx context.Context, endpoints Endpoints, executable string, duration time.Duration, options ProfileOptions) (result ProfileResult, failure error) {
	var counts SampleCounts
	result = ProfileResult{Available: false, Reason: profileLimitation, Duration: duration, SamplePeriod: profileSamplePeriod, EmptyPollingAvailable: false, Counts: counts, Threads: nil, QueuePolling: nil}
	if duration <= 0 {
		return result, fmt.Errorf("VPP profile duration must be positive")
	}
	if !filepath.IsAbs(executable) {
		return result, fmt.Errorf("VPP profile executable must be an absolute path")
	}
	client, err := Open(ctx, endpoints, &vlib.ShowThreads{}, &vlib.ShowThreadsReply{})
	if err != nil {
		return result, err
	}
	defer client.Close()
	requestContext, cancel := client.requestContext(ctx)
	threads, err := vlib.NewServiceClient(client.connection).ShowThreads(requestContext, &vlib.ShowThreads{})
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "read VPP profiling threads failed", "error", err)
		return result, fmt.Errorf("read VPP profiling threads: %w", err)
	}
	if len(threads.ThreadData) == 0 {
		return result, fmt.Errorf("VPP binary API did not return any profiling threads")
	}
	var polling *queuePollSession
	if options.QueuePolling {
		polling, err = openQueuePollSession(ctx, endpoints)
		if err != nil {
			result.QueuePolling = &QueuePollProfile{Available: false, Reason: err.Error(), Duration: 0, Session: 0, Sequence: 0, Queues: nil}
			return result, err
		}
		defer func() {
			if closeError := polling.close(ctx); closeError != nil {
				failure = errors.Join(failure, closeError)
				result.Available = false
				result.Reason = failure.Error()
				if result.QueuePolling != nil {
					result.QueuePolling.Available = false
					result.QueuePolling.Reason = failure.Error()
				}
			}
		}()
	}
	return profileThreads(ctx, executable, threads.ThreadData, duration, polling)
}

func profileThreads(ctx context.Context, executable string, threads []vlib.ThreadData, duration time.Duration, polling *queuePollSession) (ProfileResult, error) {
	var counts SampleCounts
	result := ProfileResult{Available: false, Reason: profileLimitation, Duration: duration, SamplePeriod: profileSamplePeriod, EmptyPollingAvailable: false, Counts: counts, Threads: nil, QueuePolling: nil}
	var samplers []*perfSampler
	var openFailures []error
	defer func() {
		for _, sampler := range samplers {
			_ = sampler.close()
		}
	}()
	for _, thread := range threads {
		sampler, err := openSampler(thread, executable)
		if err != nil {
			result.Threads = append(result.Threads, ThreadProfile{Thread: thread, Available: false, Reason: err.Error(), CPUClockTime: 0, CPUClockAvailable: false, Counts: counts, Symbols: nil})
			openFailures = append(openFailures, err)
			continue
		}
		samplers = append(samplers, sampler)
	}
	if len(samplers) == 0 {
		slog.WarnContext(ctx, "open VPP native profiling events failed", "errors", openFailures)
		return result, errors.Join(fmt.Errorf("VPP native profiling could not open a perf event for any reported thread"), errors.Join(openFailures...))
	}
	if polling != nil {
		if err := polling.start(ctx); err != nil {
			result.QueuePolling = &QueuePollProfile{Available: false, Reason: err.Error(), Duration: 0, Session: 0, Sequence: 0, Queues: nil}
			return result, err
		}
	}
	elapsed, failure := sampleThreads(ctx, samplers, duration, clock.Real{})
	result.Duration = elapsed
	if polling != nil {
		queueProfile, err := polling.finish(ctx)
		result.QueuePolling = &queueProfile
		failure = errors.Join(failure, err)
	}
	for _, sampler := range samplers {
		profile, err := sampler.result()
		if err != nil {
			failure = errors.Join(failure, err)
		}
		result.Threads = append(result.Threads, profile)
		addSampleCounts(&result.Counts, profile.Counts)
	}
	complete := len(result.Threads) == len(samplers)
	for _, thread := range result.Threads {
		complete = complete && thread.Available
	}
	result.Available = failure == nil && complete && result.Counts.Total > 0 && result.Counts.Lost == 0
	if failure != nil {
		slog.WarnContext(ctx, "VPP native sample collection failed", "error", failure)
		result.Reason = failure.Error()
		return result, failure
	}
	if !result.Available {
		slog.WarnContext(ctx, "VPP native profile is incomplete", "samples", result.Counts.Total, "lost", result.Counts.Lost, "open_errors", openFailures)
		return result, errors.Join(fmt.Errorf("VPP native profile is incomplete: %d samples, %d lost records, %d unavailable threads", result.Counts.Total, result.Counts.Lost, len(result.Threads)-len(samplers)), errors.Join(openFailures...))
	}
	return result, nil
}

func sampleThreads(ctx context.Context, samplers []*perfSampler, duration time.Duration, wallClock clock.Clock) (elapsed time.Duration, failure error) {
	started := wallClock.Now()
	defer func() {
		elapsed = wallClock.Now().Sub(started)
		for _, sampler := range samplers {
			if err := unix.IoctlSetInt(sampler.fd, unix.PERF_EVENT_IOC_DISABLE, 0); err != nil {
				failure = errors.Join(failure, fmt.Errorf("disable VPP thread %d perf event: %w", sampler.thread.PID, err))
			}
		}
		if failure != nil {
			slog.WarnContext(ctx, "sample VPP native threads failed", "error", failure)
		}
	}()
	for _, sampler := range samplers {
		if err := unix.IoctlSetInt(sampler.fd, unix.PERF_EVENT_IOC_RESET, 0); err != nil {
			return 0, fmt.Errorf("reset VPP thread %d perf event: %w", sampler.thread.PID, err)
		}
		if err := unix.IoctlSetInt(sampler.fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
			return 0, fmt.Errorf("enable VPP thread %d perf event: %w", sampler.thread.PID, err)
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(profileDrainPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("collect VPP native profile: %w", ctx.Err())
		case <-timer.C:
			return 0, nil
		case <-ticker.C:
			for _, sampler := range samplers {
				if err := sampler.drain(); err != nil {
					return 0, err
				}
			}
		}
	}
}

func openSampler(thread vlib.ThreadData, executable string) (*perfSampler, error) {
	threadID := int(thread.PID)
	if threadID <= 0 {
		return nil, fmt.Errorf("VPP thread %s has an invalid native thread ID", thread.Name)
	}
	startID, err := threadStartID(threadID)
	if err != nil {
		return nil, err
	}
	if err := verifyThreadExecutable(threadID, executable); err != nil {
		return nil, err
	}
	images, err := readMappedImages(threadID)
	if err != nil {
		return nil, err
	}
	attribute := unix.PerfEventAttr{
		Type: unix.PERF_TYPE_SOFTWARE, Size: uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
		Config: unix.PERF_COUNT_SW_CPU_CLOCK, Sample: uint64(profileSamplePeriod),
		Sample_type: unix.PERF_SAMPLE_IP | unix.PERF_SAMPLE_TID,
		Read_format: unix.PERF_FORMAT_TOTAL_TIME_ENABLED | unix.PERF_FORMAT_TOTAL_TIME_RUNNING,
		Bits:        unix.PerfBitDisabled | unix.PerfBitExcludeKernel | unix.PerfBitExcludeHv,
		Wakeup:      1,
	}
	fd, err := unix.PerfEventOpen(&attribute, threadID, -1, -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		slog.Warn("open VPP native perf event failed", "thread_id", threadID, "error", err)
		return nil, fmt.Errorf("open native perf event for VPP thread %d; permit perf_event_open with CAP_PERFMON and the host perf policy: %w", threadID, err)
	}
	pageSize := os.Getpagesize()
	memory, err := unix.Mmap(fd, 0, pageSize*(1+profileDataPages), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		slog.Warn("map VPP native perf ring failed", "thread_id", threadID, "error", err)
		return nil, fmt.Errorf("map VPP thread %d perf sample ring: %w", threadID, err)
	}
	meta := (*unix.PerfEventMmapPage)(unsafe.Pointer(&memory[0]))
	if meta.Data_offset == 0 || meta.Data_size == 0 || meta.Data_offset+meta.Data_size > uint64(len(memory)) {
		_ = unix.Munmap(memory)
		_ = unix.Close(fd)
		return nil, fmt.Errorf("VPP thread %d perf sample ring has invalid metadata", threadID)
	}
	var counts SampleCounts
	return &perfSampler{thread: thread, fd: fd, memory: memory, meta: meta, data: memory[meta.Data_offset : meta.Data_offset+meta.Data_size], startID: startID, images: images, executable: executable, counts: counts, symbols: make(map[symbolIdentity]uint64)}, nil
}

func threadStartID(threadID int) (string, error) {
	path := filepath.Join("/proc", strconv.Itoa(threadID), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("read VPP profiling thread identity failed", "thread_id", threadID, "error", err)
		return "", fmt.Errorf("read VPP thread %d identity: %w", threadID, err)
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return "", fmt.Errorf("VPP thread %d stat record omits the command boundary", threadID)
	}
	fields := strings.Fields(string(data[end+1:]))
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("VPP thread %d stat record omits its start time", threadID)
	}
	return fields[startTimeIndex], nil
}

func verifyThreadExecutable(threadID int, executable string) error {
	want, err := os.Stat(executable)
	if err != nil {
		slog.Warn("read VPP profiling executable failed", "executable", executable, "error", err)
		return fmt.Errorf("read configured VPP executable %s: %w", executable, err)
	}
	actual, err := os.Stat(filepath.Join("/proc", strconv.Itoa(threadID), "exe"))
	if err != nil {
		slog.Warn("verify VPP profiling executable failed", "thread_id", threadID, "error", err)
		return fmt.Errorf("verify VPP thread %d executable: %w", threadID, err)
	}
	if !os.SameFile(want, actual) {
		return fmt.Errorf("VPP thread %d does not use configured executable %s", threadID, executable)
	}
	return nil
}

func readMappedImages(threadID int) ([]mappedImage, error) {
	path := filepath.Join("/proc", strconv.Itoa(threadID), "maps")
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("read VPP profiling executable mappings failed", "thread_id", threadID, "error", err)
		return nil, fmt.Errorf("read VPP thread %d executable mappings: %w", threadID, err)
	}
	defer func() { _ = file.Close() }()
	var images []mappedImage
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 || len(fields[1]) < 3 || fields[1][2] != 'x' || !strings.HasPrefix(fields[5], "/") {
			continue
		}
		bounds := strings.Split(fields[0], "-")
		if len(bounds) != 2 {
			return nil, fmt.Errorf("VPP thread %d maps record has invalid bounds", threadID)
		}
		start, startError := strconv.ParseUint(bounds[0], 16, 64)
		end, endError := strconv.ParseUint(bounds[1], 16, 64)
		offset, offsetError := strconv.ParseUint(fields[2], 16, 64)
		if err := errors.Join(startError, endError, offsetError); err != nil {
			slog.Warn("parse VPP profiling executable mapping failed", "thread_id", threadID, "error", err)
			return nil, fmt.Errorf("parse VPP thread %d executable mapping: %w", threadID, err)
		}
		module := strings.Join(fields[5:], " ")
		image := mappedImage{start: start, end: end, offset: offset, path: module, loads: nil, symbols: nil}
		modulePath := filepath.Join("/proc", strconv.Itoa(threadID), "root", strings.TrimPrefix(module, "/"))
		binaryFile, err := elf.Open(modulePath)
		if err == nil {
			image.loads, image.symbols = executableSymbols(binaryFile)
			_ = binaryFile.Close()
		}
		// A missing symbol table leaves samples unresolved instead of assigning
		// them to a nearby function or another mapped image.
		images = append(images, image)
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("scan VPP profiling executable mappings failed", "thread_id", threadID, "error", err)
		return nil, fmt.Errorf("scan VPP executable mappings: %w", err)
	}
	return images, nil
}

func executableSymbols(file *elf.File) ([]loadSegment, []elf.Symbol) {
	var segments []loadSegment
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD && program.Flags&elf.PF_X != 0 {
			segments = append(segments, loadSegment{offset: program.Off, size: program.Filesz, vaddr: program.Vaddr})
		}
	}
	regular, _ := file.Symbols()
	dynamic, _ := file.DynamicSymbols()
	var symbols []elf.Symbol
	for _, symbol := range append(regular, dynamic...) {
		if elf.ST_TYPE(symbol.Info) == elf.STT_FUNC && symbol.Section != elf.SHN_UNDEF && symbol.Size > 0 {
			symbols = append(symbols, symbol)
		}
	}
	slices.SortFunc(symbols, func(left, right elf.Symbol) int {
		if left.Value < right.Value {
			return -1
		}
		if left.Value > right.Value {
			return 1
		}
		return strings.Compare(left.Name, right.Name)
	})
	return segments, symbols
}

func (sampler *perfSampler) drain() error {
	head := atomic.LoadUint64(&sampler.meta.Data_head)
	tail := atomic.LoadUint64(&sampler.meta.Data_tail)
	if head < tail || head-tail > uint64(len(sampler.data)) {
		return fmt.Errorf("VPP thread %d perf ring overran before sampling completed", sampler.thread.PID)
	}
	for head-tail >= perfRecordHeader {
		header := sampler.ringBytes(tail, perfRecordHeader)
		typeID := binary.NativeEndian.Uint32(header[:4])
		recordLength := binary.NativeEndian.Uint16(header[6:8])
		size := uint64(recordLength)
		if size < perfRecordHeader || size > head-tail || size > uint64(len(sampler.data)) {
			return fmt.Errorf("VPP thread %d perf record has invalid size %d", sampler.thread.PID, size)
		}
		record := sampler.ringBytes(tail, int(recordLength))
		switch typeID {
		case unix.PERF_RECORD_SAMPLE:
			if size != perfSampleLength {
				return fmt.Errorf("VPP thread %d perf sample has unexpected size %d", sampler.thread.PID, size)
			}
			threadID := binary.NativeEndian.Uint32(record[20:24])
			if threadID != sampler.thread.PID {
				return fmt.Errorf("VPP perf sample thread %d differs from requested thread %d", threadID, sampler.thread.PID)
			}
			sampler.recordSample(binary.NativeEndian.Uint64(record[8:16]))
		case unix.PERF_RECORD_LOST:
			if size < perfSampleLength {
				return fmt.Errorf("VPP perf lost record has invalid size %d", size)
			}
			sampler.counts.Lost += binary.NativeEndian.Uint64(record[16:24])
		case unix.PERF_RECORD_LOST_SAMPLES:
			if size < 16 {
				return fmt.Errorf("VPP perf lost-sample record has invalid size %d", size)
			}
			sampler.counts.Lost += binary.NativeEndian.Uint64(record[8:16])
		case unix.PERF_RECORD_THROTTLE:
			return fmt.Errorf("kernel throttled VPP thread %d perf sampling", sampler.thread.PID)
		}
		tail += size
	}
	atomic.StoreUint64(&sampler.meta.Data_tail, tail)
	return nil
}

func (sampler *perfSampler) ringBytes(offset uint64, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = sampler.data[(offset+uint64(index))%uint64(len(sampler.data))]
	}
	return result
}

func (sampler *perfSampler) recordSample(instruction uint64) {
	sampler.counts.Total++
	identity := symbolIdentity{module: "", name: "[unresolved]", class: sampleUnresolved}
	for _, image := range sampler.images {
		if instruction < image.start || instruction >= image.end {
			continue
		}
		identity.module = image.path
		offset := image.offset + instruction - image.start
		for _, segment := range image.loads {
			if offset < segment.offset || offset-segment.offset >= segment.size {
				continue
			}
			address := segment.vaddr + offset - segment.offset
			for _, symbol := range image.symbols {
				if symbol.Value > address {
					break
				}
				if address-symbol.Value < symbol.Size {
					identity.name = symbol.Name
					identity.class = symbolClass(symbol.Name)
				}
			}
		}
		break
	}
	sampler.symbols[identity]++
	switch identity.class {
	case samplePacketWork:
		sampler.counts.PacketWork++
	case sampleUnresolved:
		sampler.counts.Unresolved++
	case sampleOther:
		sampler.counts.Other++
	}
}

func symbolClass(name string) SampleClass {
	for _, prefix := range []string{"nat64_in2out_node_fn", "nat64_in2out_slowpath_node_fn", "nat64_out2in_node_fn", "ip4_lookup_node_fn", "ip6_lookup_node_fn"} {
		if strings.HasPrefix(name, prefix) {
			return samplePacketWork
		}
	}
	for _, function := range []string{"nat64_in2out_tcp_udp", "nat64_in2out_icmp", "nat64_in2out_unk_proto", "nat64_out2in_tcp_udp", "nat64_out2in_icmp", "nat64_out2in_unk_proto"} {
		if name == function || strings.HasPrefix(name, function+".") {
			return samplePacketWork
		}
	}
	return sampleOther
}

func (sampler *perfSampler) result() (ThreadProfile, error) {
	result := ThreadProfile{Thread: sampler.thread, Available: false, Reason: "", CPUClockTime: 0, CPUClockAvailable: false, Counts: sampler.counts, Symbols: nil}
	if err := sampler.drain(); err != nil {
		result.Reason = err.Error()
		return result, err
	}
	result.Counts = sampler.counts
	currentID, err := threadStartID(int(sampler.thread.PID))
	if err != nil || currentID != sampler.startID {
		result.Reason = "VPP thread identity changed during profiling"
		slog.Warn("VPP thread identity changed during profiling", "thread_id", sampler.thread.PID, "error", err)
		return result, errors.Join(fmt.Errorf("%s: thread %d", result.Reason, sampler.thread.PID), err)
	}
	if err := verifyThreadExecutable(int(sampler.thread.PID), sampler.executable); err != nil {
		return result, err
	}
	images, err := readMappedImages(int(sampler.thread.PID))
	if err != nil {
		return result, err
	}
	if mappingIdentity(images) != mappingIdentity(sampler.images) {
		return result, fmt.Errorf("VPP thread %d executable mappings changed during profiling", sampler.thread.PID)
	}
	var counter [24]byte
	length, err := unix.Read(sampler.fd, counter[:])
	if err != nil {
		result.Reason = err.Error()
		slog.Warn("read VPP thread CPU clock failed", "thread_id", sampler.thread.PID, "error", err)
		return result, fmt.Errorf("read VPP thread %d CPU clock: %w", sampler.thread.PID, err)
	}
	if length != len(counter) {
		return result, fmt.Errorf("VPP thread %d CPU clock returned %d bytes; expected %d", sampler.thread.PID, length, len(counter))
	}
	clock := binary.NativeEndian.Uint64(counter[:8])
	if clock > math.MaxInt64 {
		return result, fmt.Errorf("VPP thread %d CPU clock exceeds the supported duration", sampler.thread.PID)
	}
	enabled := binary.NativeEndian.Uint64(counter[8:16])
	running := binary.NativeEndian.Uint64(counter[16:24])
	if running == enabled && (enabled > 0 || clock == 0) {
		result.CPUClockTime, result.CPUClockAvailable = time.Duration(clock), true
	}
	for identity, count := range sampler.symbols {
		result.Symbols = append(result.Symbols, SymbolSamples{Module: identity.module, Symbol: identity.name, Class: identity.class, Samples: count})
	}
	slices.SortFunc(result.Symbols, func(left, right SymbolSamples) int {
		if left.Samples > right.Samples {
			return -1
		}
		if left.Samples < right.Samples {
			return 1
		}
		return strings.Compare(left.Module+left.Symbol, right.Module+right.Symbol)
	})
	result.Available = result.Counts.Lost == 0 && result.CPUClockAvailable
	if !result.Available {
		result.Reason = "VPP thread profile lacks complete CPU clock samples"
	}
	return result, nil
}

func mappingIdentity(images []mappedImage) string {
	var identity strings.Builder
	for _, image := range images {
		fmt.Fprintf(&identity, "%x-%x/%x/%s\n", image.start, image.end, image.offset, image.path)
	}
	return identity.String()
}

func addSampleCounts(total *SampleCounts, current SampleCounts) {
	total.Total += current.Total
	total.PacketWork += current.PacketWork
	total.EmptyPolling += current.EmptyPolling
	total.Other += current.Other
	total.Unresolved += current.Unresolved
	total.Lost += current.Lost
}

func (sampler *perfSampler) close() error {
	err := errors.Join(unix.Munmap(sampler.memory), unix.Close(sampler.fd))
	if err != nil {
		slog.Warn("close VPP native profiling event failed", "thread_id", sampler.thread.PID, "error", err)
		return err
	}
	return nil
}
