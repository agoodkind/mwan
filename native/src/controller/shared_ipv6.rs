use super::*;

#[derive(Clone, Copy, Hash, PartialEq, Eq)]
struct Key {
    origin: u32,
    source_mac: [u8; 6],
    destination_mac: [u8; 6],
    translated: [u8; 16],
}

pub(super) struct Bindings {
    values: HashMap<Key, [u8; 16]>,
    capacity: u32,
    pub(super) conflicts: u64,
    pub(super) capacity_rejections: u64,
    pub(super) releases: u64,
}

pub(super) struct Reservation {
    entries: [Option<(Key, [u8; 16])>; 2],
}

impl Bindings {
    pub(super) fn new(pair_capacity: u32) -> NativeResult<Self> {
        let capacity = pair_capacity.checked_mul(2).ok_or_else(|| {
            fault(
                libc::EOVERFLOW,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Shared IPv6 binding capacity overflow",
            )
        })?;
        let mut values = HashMap::new();
        values.try_reserve(capacity as usize).map_err(|_| {
            fault(
                libc::ENOMEM,
                MWAN_NATIVE_OP_CONFIG,
                MWAN_NATIVE_OBJECT_PAIR,
                0,
                0,
                "Shared IPv6 binding allocation failed",
            )
        })?;
        Ok(Self {
            values,
            capacity,
            conflicts: 0,
            capacity_rejections: 0,
            releases: 0,
        })
    }

    fn entry(
        config: &MwanNativeConfig,
        direction: &MwanNativeDirection,
    ) -> Option<(Key, [u8; 16])> {
        if u32::from(direction.original.family) != MWAN_NATIVE_FAMILY_IPV6 {
            return None;
        }
        let field = config.origins[direction.origin_id as usize].ipv6_restore_field;
        let (translated, original) = match field {
            MWAN_NATIVE_IPV6_RESTORE_SOURCE => {
                (direction.translated.source, direction.original.source)
            }
            MWAN_NATIVE_IPV6_RESTORE_DESTINATION => (
                direction.translated.destination,
                direction.original.destination,
            ),
            _ => return None,
        };
        Some((
            Key {
                origin: direction.origin_id,
                source_mac: direction.original_source_mac,
                destination_mac: direction.original_destination_mac,
                translated,
            },
            original,
        ))
    }

    pub(super) fn contains(
        &self,
        config: &MwanNativeConfig,
        direction: &MwanNativeDirection,
    ) -> bool {
        Self::entry(config, direction)
            .is_some_and(|(key, original)| self.values.get(&key) == Some(&original))
    }

    pub(super) fn prepare(
        &mut self,
        config: &MwanNativeConfig,
        spec: &MwanNativePairSpec,
    ) -> NativeResult<Reservation> {
        let mut entries = [None; 2];
        for (index, mask) in DIRECTION_MASKS.iter().enumerate() {
            if spec.requested_direction_mask & mask != 0 {
                entries[index] = Self::entry(config, &spec.directions[index]);
            }
        }
        let mut additional = 0;
        for (index, entry) in entries.iter().enumerate() {
            let Some((key, original)) = entry else {
                continue;
            };
            let existing = self.values.get(key).or_else(|| {
                entries[..index]
                    .iter()
                    .flatten()
                    .find(|(other, _)| other == key)
                    .map(|(_, value)| value)
            });
            if existing.is_some_and(|value| value != original) {
                self.conflicts = self.conflicts.wrapping_add(1);
                return Err(fault(
                    libc::EEXIST,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_ORIGIN,
                    key.origin,
                    index as u32,
                    "Shared IPv6 address inverse conflicts with a permanent binding",
                ));
            }
            if existing.is_none() {
                additional += 1;
            }
        }
        if self.values.len() + additional > self.capacity as usize {
            self.capacity_rejections = self.capacity_rejections.wrapping_add(1);
            return Err(fault(
                libc::ENOSPC,
                MWAN_NATIVE_OP_VALIDATE,
                MWAN_NATIVE_OBJECT_PAIR,
                MWAN_NATIVE_NO_ID,
                0,
                "Shared IPv6 binding capacity exhausted",
            ));
        }
        Ok(Reservation { entries })
    }

    pub(super) fn publish(&mut self, reservation: Reservation) {
        for (key, original) in reservation.entries.into_iter().flatten() {
            self.values.insert(key, original);
        }
    }

    pub(super) fn packet(
        &self,
        config: &MwanNativeConfig,
        origin: usize,
        view: &packet::ParsedPacket,
    ) -> Option<PacketLeg> {
        if u32::from(view.tuple.family) != MWAN_NATIVE_FAMILY_IPV6
            || (!view.ports && !view.fragment)
        {
            return None;
        }
        let profile = config.origins[origin];
        let translated = match profile.ipv6_restore_field {
            MWAN_NATIVE_IPV6_RESTORE_SOURCE => view.tuple.source,
            MWAN_NATIVE_IPV6_RESTORE_DESTINATION => view.tuple.destination,
            _ => return None,
        };
        let original = *self.values.get(&Key {
            origin: origin as u32,
            source_mac: view.source_mac,
            destination_mac: view.destination_mac,
            translated,
        })?;
        let mut spec = MwanNativeDirection {
            origin_id: origin as u32,
            egress_data_id: profile.data,
            original: view.tuple,
            translated: view.tuple,
            original_source_mac: view.source_mac,
            original_destination_mac: view.destination_mac,
            ..MwanNativeDirection::default()
        };
        if profile.ipv6_restore_field == MWAN_NATIVE_IPV6_RESTORE_SOURCE {
            spec.original.source = original;
        } else {
            spec.original.destination = original;
        }
        Some(PacketLeg {
            spec,
            guard: None,
            handle: None,
            installed: false,
        })
    }

    pub(super) fn stats(&self, stats: &mut MwanNativeRetirementStats) {
        stats.shared_binding_capacity = self.capacity;
        stats.shared_bindings = self.values.len() as u32;
        stats.shared_identity_bindings = self
            .values
            .iter()
            .filter(|(key, value)| key.translated == **value)
            .count() as u32;
        stats.shared_binding_conflicts = self.conflicts;
        stats.shared_binding_capacity_rejections = self.capacity_rejections;
        stats.shared_pair_releases = self.releases;
    }

    pub(super) fn clear(&mut self) {
        self.values.clear();
    }
}
