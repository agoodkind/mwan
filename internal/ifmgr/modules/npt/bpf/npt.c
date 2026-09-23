//go:build ignore

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <bpf/bpf_helpers.h>

#define MAX_PAIRS 64
#define MAX_EXCEPTIONS 16
#define HAIRPIN_MASK 0xffff0000U
#define HAIRPIN_TAG 0x4e500000U

struct address { __u8 bytes[16]; };

struct pair {
 __u8 internal[16];
 __u8 external[16];
 __u8 mask[8];
 __u8 source_exceptions[MAX_EXCEPTIONS][16];
 __u8 destination_exceptions[MAX_EXCEPTIONS][16];
 __u16 id;
 __u16 forward_adjustment;
 __u16 reverse_adjustment;
 __u8 internal_bits;
 __u8 external_bits;
 __u8 source_count;
 __u8 destination_count;
 __u8 padding[2];
};
struct policy {
 __u32 internal;
 __u32 count;
 __u32 ethernet;
 __u32 padding;
 __u8 ingress_order[MAX_PAIRS];
 __u8 egress_order[MAX_PAIRS];
 struct pair pairs[MAX_PAIRS];
};
struct {
 __uint(type, BPF_MAP_TYPE_HASH);
 __uint(max_entries, 1024);
 __uint(map_flags, BPF_F_NO_PREALLOC);
 __type(key, __u32);
 __type(value, struct policy);
} policies SEC(".maps");

__noinline int contains(const struct address *a, const struct address *p, __u32 bits) {
 if (!a || !p) return 0;
 const __u8 *address = a->bytes, *prefix = p->bytes;
 if (bits > 64) return 0;
 for (int i = 0; i < 8; i++) {
  __u32 used = bits > i * 8 ? bits - i * 8 : 0;
  if (used > 8) used = 8;
  __u8 mask = used ? (__u8)(0xff << (8 - used)) : 0;
  if ((address[i] & mask) != (prefix[i] & mask)) return 0;
 }
 return 1;
}
static __always_inline int equal(const __u8 *a, const __u8 *b) {
 for (int i = 0; i < 16; i++) if (a[i] != b[i]) return 0;
 return 1;
}
__noinline int excepted(const struct pair *pair, const struct address *a, int source) {
 if (!pair || !a) return 0;
 const __u8 *address = a->bytes;
 for (int i = 0; i < MAX_EXCEPTIONS; i++) {
  if (source) {
   if (i >= pair->source_count) break;
   if (equal(address, pair->source_exceptions[i])) return 1;
  } else {
   if (i >= pair->destination_count) break;
   if (equal(address, pair->destination_exceptions[i])) return 1;
  }
 }
 return 0;
}
__noinline int translate(struct address *a, const struct pair *pair, int outbound) {
 if (!pair || !a) return -1;
 __u8 *address = a->bytes;
 const __u8 *from = outbound ? pair->internal : pair->external;
 const __u8 *to = outbound ? pair->external : pair->internal;
 __u32 bits = pair->internal_bits > pair->external_bits ? pair->internal_bits : pair->external_bits;
 if (!contains(a, (const struct address *)from, bits)) return -1;
 int word = 3;
 if (bits > 48) {
  int nonzero = 0;
  for (int i = 8; i < 16; i++) nonzero |= address[i];
  if (!nonzero) return -1;
  word = 8;
  for (int i = 4; i < 8; i++) {
   if (address[i*2] != 0xff || address[i*2+1] != 0xff) { word = i; break; }
  }
 }
 if (word >= 8 || (address[word*2] == 0xff && address[word*2+1] == 0xff)) return -1;
 for (int i = 0; i < 8; i++) {
  __u8 mask = pair->mask[i];
  address[i] = (address[i] & ~mask) | (to[i] & mask);
 }
 __u32 sum = ((__u32)address[word*2] << 8) | address[word*2+1];
 sum += outbound ? pair->forward_adjustment : pair->reverse_adjustment;
 sum = (sum & 0xffff) + (sum >> 16);
 sum = (sum & 0xffff) + (sum >> 16);
 if (sum == 0xffff) sum = 0;
 address[word*2] = sum >> 8;
 address[word*2+1] = sum;
 return 0;
}
static __always_inline int ipv6_offset(struct __sk_buff *skb, int ethernet) {
 __u8 header[22];
 if (bpf_skb_load_bytes(skb, 0, header, sizeof(header))) return -1;
 if (!ethernet) return (header[0] >> 4) == 6 ? 0 : -1;
 __u16 protocol = ((__u16)header[12] << 8) | header[13];
 if (protocol == 0x86dd) return 14;
 if (protocol != 0x8100 && protocol != 0x88a8) return -1;
 protocol = ((__u16)header[16] << 8) | header[17];
 if (protocol == 0x86dd) return 18;
 if ((protocol == 0x8100 || protocol == 0x88a8) && header[20] == 0x86 && header[21] == 0xdd) return 22;
 return -1;
}
static __noinline int translate_icmp_error(struct __sk_buff *skb, const struct pair *pair, __u32 offset, int outbound) {
 __u8 next;
 if (bpf_skb_load_bytes(skb, offset + 6, &next, 1)) return -1;
 __u32 position = offset + 40;
 for (int depth = 0; depth < 16; depth++) {
  if (next == 58) break;
  __u8 extension[4];
  if (next != 0 && next != 43 && next != 60 && next != 44 && next != 51) return 0;
  if (bpf_skb_load_bytes(skb, position, extension, sizeof(extension))) return -1;
  if (next == 44) {
   if (extension[2] || (extension[3] & 0xf8)) return 0;
   position += 8;
  } else if (next == 51) position += ((__u32)extension[1] + 2) * 4;
  else position += ((__u32)extension[1] + 1) * 8;
  next = extension[0];
 }
 if (next != 58) return 0;
 __u8 type;
 if (bpf_skb_load_bytes(skb, position, &type, 1)) return -1;
 if (type >= 128) return 0;
 __u8 version;
 if (bpf_skb_load_bytes(skb, position + 8, &version, 1)) return -1;
 if ((version >> 4) != 6) return 0;
 for (int field = 0; field < 2; field++) {
  struct address quoted;
  __u32 address_offset = position + 16 + field * 16;
  if (bpf_skb_load_bytes(skb, address_offset, &quoted, sizeof(quoted))) return -1;
  const __u8 *prefix = outbound ? pair->internal : pair->external;
  __u32 bits = outbound ? pair->internal_bits : pair->external_bits;
  if (!contains(&quoted, (const struct address *)prefix, bits)) continue;
  if (excepted(pair, &quoted, outbound)) continue;
  if (translate(&quoted, pair, outbound)) return -1;
  if (bpf_skb_store_bytes(skb, address_offset, &quoted, sizeof(quoted), BPF_F_INVALIDATE_HASH)) return -1;
 }
 return 0;
}
static __always_inline int process(struct __sk_buff *skb, int ingress) {
 __u32 ifindex = skb->ifindex;
 struct policy *policy = bpf_map_lookup_elem(&policies, &ifindex);
 if (!policy) return TC_ACT_OK;
 int offset = ipv6_offset(skb, policy->ethernet);
 if (offset < 0) return TC_ACT_OK;
 __u8 address[16];
 int source = !ingress;
 __u32 address_offset = offset + (source ? 8 : 24);
 if (bpf_skb_load_bytes(skb, address_offset, address, sizeof(address))) return TC_ACT_SHOT;
 int selected = -1;
 for (int i = 0; i < MAX_PAIRS; i++) {
  if (i >= policy->count) break;
  __u32 index = source ? policy->egress_order[i] : policy->ingress_order[i];
  if (index >= MAX_PAIRS) return TC_ACT_SHOT;
  const struct pair *pair = &policy->pairs[index];
  if (policy->internal && !ingress) {
   if ((skb->priority & HAIRPIN_MASK) == HAIRPIN_TAG && (skb->priority & 0xffff) == pair->id) { selected = index; break; }
  } else {
   const __u8 *prefix = source ? pair->internal : pair->external;
   __u32 bits = source ? pair->internal_bits : pair->external_bits;
   if (contains((const struct address *)address, (const struct address *)prefix, bits)) { selected = index; break; }
  }
 }
 if (policy->internal && ingress && (skb->priority & HAIRPIN_MASK) == HAIRPIN_TAG) skb->priority = 0;
 if (selected < 0 || selected >= MAX_PAIRS) return TC_ACT_OK;
 const struct pair *pair = &policy->pairs[selected];
 if (excepted(pair, (const struct address *)address, source)) {
  if (policy->internal && !ingress) skb->priority = 0;
  return TC_ACT_OK;
 }
 if (translate((struct address *)address, pair, source)) return TC_ACT_SHOT;
 if (bpf_skb_store_bytes(skb, address_offset, address, sizeof(address), BPF_F_INVALIDATE_HASH)) return TC_ACT_SHOT;
 if (translate_icmp_error(skb, pair, offset, source)) return TC_ACT_SHOT;
 if (policy->internal) {
  if (ingress) { skb->priority = HAIRPIN_TAG | pair->id; skb->mark = 0; }
  else skb->priority = 0;
 }
 return TC_ACT_OK;
}
SEC("tc/ingress") int npt_ingress(struct __sk_buff *skb) { return process(skb, 1); }
SEC("tc/egress") int npt_egress(struct __sk_buff *skb) { return process(skb, 0); }
char npt_license[] SEC("license") = "GPL";
