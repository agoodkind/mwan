#!/usr/bin/env python3
"""Compare the unit files the daemon renders with the hand-authored templates.

For one gateway group the script loads the configs inventory with PyYAML,
renders mwan/config/network.json.j2 and every template the group's
mwan_networkd_files names with Jinja2, runs the render program over the
rendered document, and compares each rendered file with the template that
has the same [Match] block. Comment lines and blank lines are stripped from
both sides, and the keys within one section are compared as a set. A missing
file, an extra file, a missing key, an extra key, or a differing value is
reported with its file, section, and key.

A template is excluded when its interface belongs to a provider with the
link-files leaf set to hand-authored, and skipped when its interface belongs
to no provider entry at all.

This script serves one comparison and is not a gate.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import subprocess
import sys
from collections.abc import Iterator, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Union

import jinja2
import yaml

JsonValue = Union[str, int, float, bool, None, list["JsonValue"], dict[str, "JsonValue"]]

LINK_FILES_RENDERED = "rendered"
LINK_FILES_HAND_AUTHORED = "hand-authored"
COMMENT_PREFIXES = ("#", ";")
KIND_LINK = "link"
KIND_NETWORK = "network"
KIND_NETDEV = "netdev"
TRUE_WORDS = {"1", "true", "yes", "on"}


# ---------------------------------------------------------------------------
# Ansible filters the templates use
# ---------------------------------------------------------------------------


def filter_combine(first: dict[str, JsonValue], second: dict[str, JsonValue]) -> dict[str, JsonValue]:
    merged = dict(first)
    merged.update(second)
    return merged


def filter_to_nice_json(value: JsonValue) -> str:
    return json.dumps(value, indent=4, sort_keys=True, separators=(",", ": "))


def filter_bool(value: JsonValue) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value == 1
    if isinstance(value, str):
        return value.strip().lower() in TRUE_WORDS
    return False


def filter_ipaddr(value: str, query: str) -> str:
    if query == "address":
        return str(ipaddress.ip_interface(value).ip)
    if query.isdigit():
        network = ipaddress.ip_network(value, strict=False)
        host = network.network_address + int(query)
        return f"{host}/{network.prefixlen}"
    raise ValueError(f"ipaddr query {query!r} is not implemented by this script")


# ---------------------------------------------------------------------------
# Lazy inventory variables
# ---------------------------------------------------------------------------


class LazyVars(Mapping[str, JsonValue]):
    """Inventory variables that template themselves on first access.

    Ansible renders a variable's template when the variable is read, and a
    group file can reference a variable defined further down or in another
    file. This mapping does the same: a string holding a Jinja2 expression is
    rendered against the mapping itself when it is first read, and lists and
    dicts are walked so nested references resolve too.
    """

    def __init__(self, raw: dict[str, JsonValue], environment: jinja2.Environment) -> None:
        self._raw = raw
        self._environment = environment
        self._cache: dict[str, JsonValue] = {}
        self._resolving: set[str] = set()

    def __getitem__(self, key: str) -> JsonValue:
        if key in self._cache:
            return self._cache[key]
        if key not in self._raw:
            raise KeyError(key)
        if key in self._resolving:
            raise RecursionError(f"variable {key} references itself")
        self._resolving.add(key)
        try:
            resolved = self.resolve(self._raw[key])
        finally:
            self._resolving.discard(key)
        self._cache[key] = resolved
        return resolved

    def __contains__(self, key: object) -> bool:
        return key in self._raw

    def __iter__(self) -> Iterator[str]:
        return iter(self._raw)

    def __len__(self) -> int:
        return len(self._raw)

    def resolve(self, value: JsonValue) -> JsonValue:
        if isinstance(value, str):
            if "{{" in value or "{%" in value:
                return render_string(self._environment, value, self)
            return value
        if isinstance(value, list):
            resolved_list: list[JsonValue] = []
            for item in value:
                resolved_list.append(self.resolve(item))
            return resolved_list
        if isinstance(value, dict):
            resolved_dict: dict[str, JsonValue] = {}
            for name, item in value.items():
                resolved_dict[name] = self.resolve(item)
            return resolved_dict
        return value


def render_string(environment: jinja2.Environment, source: str, variables: Mapping[str, JsonValue]) -> str:
    template = environment.from_string(source)
    context = template.new_context(vars=variables, shared=True)
    return environment.concat(template.root_render_func(context))


def make_environment() -> jinja2.Environment:
    environment = jinja2.Environment(
        trim_blocks=True,
        lstrip_blocks=False,
        keep_trailing_newline=True,
        undefined=jinja2.StrictUndefined,
        autoescape=False,
    )
    environment.filters["combine"] = filter_combine
    environment.filters["to_nice_json"] = filter_to_nice_json
    environment.filters["bool"] = filter_bool
    environment.filters["ansible.utils.ipaddr"] = filter_ipaddr
    return environment


def load_yaml_file(path: Path) -> dict[str, JsonValue]:
    with path.open(encoding="utf-8") as handle:
        loaded = yaml.safe_load(handle)
    if loaded is None:
        return {}
    if not isinstance(loaded, dict):
        raise ValueError(f"{path} does not hold a mapping")
    return loaded


def load_group_vars(configs: Path, group: str, environment: jinja2.Environment) -> LazyVars:
    group_vars = configs / "ansible" / "inventory" / "group_vars"
    raw: dict[str, JsonValue] = {}
    # vault.yml is encrypted and no template here reads a vault variable.
    for name in ("service_mapping.yml", "vars.yml"):
        raw.update(load_yaml_file(group_vars / "all" / name))
    raw.update(load_yaml_file(group_vars / f"{group}.yml"))
    return LazyVars(raw, environment)


# ---------------------------------------------------------------------------
# Unit files
# ---------------------------------------------------------------------------


@dataclass
class Section:
    name: str
    entries: list[tuple[str, str]] = field(default_factory=list)

    def values_by_key(self) -> dict[str, list[str]]:
        grouped: dict[str, list[str]] = {}
        for key, value in self.entries:
            grouped.setdefault(key, []).append(value)
        for values in grouped.values():
            values.sort()
        return grouped


@dataclass
class UnitFile:
    label: str
    kind: str
    sections: list[Section]

    def first_section(self, name: str) -> Section | None:
        for section in self.sections:
            if section.name == name:
                return section
        return None

    def interface(self) -> str:
        """The interface the file belongs to, read where each kind names it."""
        if self.kind == KIND_LINK:
            section = self.first_section("Link")
        elif self.kind == KIND_NETWORK:
            section = self.first_section("Match")
        else:
            section = self.first_section("NetDev")
        if section is None:
            return ""
        for key, value in section.entries:
            if key == "Name":
                return value
        return ""

    def pair_key(self) -> tuple[str, tuple[tuple[str, str], ...]]:
        """What pairs a rendered file with a template: the kind and the [Match] block.

        A .netdev has no [Match] section, and its [NetDev] Name stands in.
        """
        if self.kind == KIND_NETDEV:
            return (self.kind, (("Name", self.interface()),))
        match = self.first_section("Match")
        if match is None:
            return (self.kind, ())
        return (self.kind, tuple(sorted(match.entries)))


def kind_of(name: str) -> str:
    suffix = Path(name).suffix.lstrip(".")
    if suffix not in (KIND_LINK, KIND_NETWORK, KIND_NETDEV):
        raise ValueError(f"{name} is not a .link, .network, or .netdev file")
    return suffix


def parse_unit(label: str, text: str) -> UnitFile:
    sections: list[Section] = []
    current: Section | None = None
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if line == "" or line.startswith(COMMENT_PREFIXES):
            continue
        if line.startswith("[") and line.endswith("]"):
            current = Section(name=line[1:-1])
            sections.append(current)
            continue
        if current is None:
            raise ValueError(f"{label}: line {raw_line!r} precedes the first section")
        key, separator, value = line.partition("=")
        if separator == "":
            raise ValueError(f"{label}: line {raw_line!r} has no '='")
        current.entries.append((key.strip(), value.strip()))
    return UnitFile(label=label, kind=kind_of(label), sections=sections)


def compare_units(rendered: UnitFile, hand: UnitFile) -> list[str]:
    """Differences between two files, each naming the file, section, and key."""
    differences: list[str] = []
    rendered_by_name: dict[str, list[Section]] = {}
    hand_by_name: dict[str, list[Section]] = {}
    order: list[str] = []
    for section in rendered.sections:
        if section.name not in order:
            order.append(section.name)
        rendered_by_name.setdefault(section.name, []).append(section)
    for section in hand.sections:
        if section.name not in order:
            order.append(section.name)
        hand_by_name.setdefault(section.name, []).append(section)
    for name in order:
        rendered_sections = rendered_by_name.get(name, [])
        hand_sections = hand_by_name.get(name, [])
        if len(rendered_sections) != len(hand_sections):
            differences.append(
                f"{rendered.label} [{name}]: section appears {len(rendered_sections)} times in the rendered"
                f" file and {len(hand_sections)} times in {hand.label}"
            )
        for rendered_section, hand_section in zip(rendered_sections, hand_sections):
            differences.extend(compare_sections(rendered.label, hand.label, rendered_section, hand_section))
    return differences


def compare_sections(rendered_label: str, hand_label: str, rendered: Section, hand: Section) -> list[str]:
    differences: list[str] = []
    rendered_values = rendered.values_by_key()
    hand_values = hand.values_by_key()
    for key in sorted(set(rendered_values) | set(hand_values)):
        if key not in rendered_values:
            differences.append(
                f"{rendered_label} [{rendered.name}] {key}: missing in the rendered file;"
                f" {hand_label} has {format_values(hand_values[key])}"
            )
        elif key not in hand_values:
            differences.append(
                f"{rendered_label} [{rendered.name}] {key}: extra in the rendered file"
                f" ({format_values(rendered_values[key])}); {hand_label} has no such key"
            )
        elif rendered_values[key] != hand_values[key]:
            differences.append(
                f"{rendered_label} [{rendered.name}] {key}: rendered {format_values(rendered_values[key])},"
                f" {hand_label} has {format_values(hand_values[key])}"
            )
    return differences


def format_values(values: list[str]) -> str:
    quoted: list[str] = []
    for value in values:
        quoted.append(repr(value))
    return ", ".join(quoted)


# ---------------------------------------------------------------------------
# Providers
# ---------------------------------------------------------------------------


@dataclass
class Provider:
    name: str
    link_files: str
    interfaces: set[str]


def provider_entries(variables: LazyVars) -> list[Provider]:
    """Each provider's name, its link-files value, and the interfaces its files name.

    The interface name follows network.json.j2: the device name, or the device
    name and the VLAN id joined by a dot. A hand-authored VLAN provider also
    owns its parent's files, because that parent has no entry of its own, and
    a rendered VLAN provider names its parent in link.vlan.parent.
    """
    raw = variables["mwan_providers"]
    if not isinstance(raw, list):
        raise ValueError("mwan_providers is not a list")
    providers: list[Provider] = []
    for entry in raw:
        if not isinstance(entry, dict):
            raise ValueError("a mwan_providers entry is not a mapping")
        name = str(entry["name"])
        iface = str(entry["iface"])
        vlan_id = entry.get("vlan_id")
        interfaces = {iface}
        if vlan_id not in (None, ""):
            interfaces.add(f"{iface}.{vlan_id}")
        link = entry.get("link")
        if isinstance(link, dict):
            vlan = link.get("vlan")
            if isinstance(vlan, dict):
                interfaces.add(str(vlan["parent"]))
        link_files = entry.get("link_files")
        if link_files is None:
            link_files = ""
        providers.append(Provider(name=name, link_files=str(link_files), interfaces=interfaces))
    return providers


def provider_for_interface(providers: list[Provider], interface: str) -> Provider | None:
    for provider in providers:
        if interface in provider.interfaces:
            return provider
    return None


# ---------------------------------------------------------------------------
# One group
# ---------------------------------------------------------------------------


@dataclass
class GroupReport:
    group: str
    lines: list[str] = field(default_factory=list)
    differences: list[str] = field(default_factory=list)
    failed: bool = False

    def say(self, line: str) -> None:
        self.lines.append(line)


def render_network_json(configs: Path, variables: LazyVars, environment: jinja2.Environment, out: Path) -> Path:
    source = (configs / "mwan" / "config" / "network.json.j2").read_text(encoding="utf-8")
    rendered = render_string(environment, source, variables)
    path = out / "network.json"
    path.write_text(rendered, encoding="utf-8")
    return path


def run_render(render_program: Path, network_json: Path, out: Path, report: GroupReport) -> bool:
    completed = subprocess.run(
        [str(render_program), "-network", str(network_json), "-out", str(out)],
        capture_output=True,
        text=True,
        check=False,
    )
    for line in completed.stdout.splitlines():
        report.say(f"  render: {line}")
    for line in completed.stderr.splitlines():
        report.say(f"  render stderr: {line}")
    if completed.returncode != 0:
        report.say(f"  render program exited {completed.returncode}; no rendered file to compare")
        return False
    return True


def rendered_units(out: Path) -> list[UnitFile]:
    units: list[UnitFile] = []
    for path in sorted(out.iterdir()):
        if path.name == "network.json":
            continue
        units.append(parse_unit(path.name, path.read_text(encoding="utf-8")))
    return units


@dataclass
class HandUnit:
    template: str
    unit: UnitFile
    provider: Provider | None


def hand_units(
    configs: Path, variables: LazyVars, environment: jinja2.Environment, providers: list[Provider], report: GroupReport
) -> list[HandUnit]:
    names = variables["mwan_networkd_files"]
    if not isinstance(names, list):
        raise ValueError("mwan_networkd_files is not a list")
    units: list[HandUnit] = []
    for name in names:
        template_name = str(name)
        template_path = configs / "mwan" / "networkd" / f"{template_name}.j2"
        try:
            text = render_string(environment, template_path.read_text(encoding="utf-8"), variables)
        except jinja2.UndefinedError as error:
            report.say(f"  template {template_name}: not rendered ({error}); skipped")
            continue
        unit = parse_unit(Path(template_name).name, text)
        provider = provider_for_interface(providers, unit.interface())
        units.append(HandUnit(template=template_name, unit=unit, provider=provider))
    return units


def compare_group(group: str, configs: Path, render_program: Path, work: Path) -> GroupReport:
    report = GroupReport(group=group)
    environment = make_environment()
    variables = load_group_vars(configs, group, environment)
    providers = provider_entries(variables)
    for provider in providers:
        report.say(
            f"  provider {provider.name}: link_files={provider.link_files or '(unset)'}"
            f" interfaces={sorted(provider.interfaces)}"
        )

    out = work / group
    out.mkdir(parents=True, exist_ok=True)
    network_json = render_network_json(configs, variables, environment, out)
    report.say(f"  network.json rendered to {network_json}")
    if not run_render(render_program, network_json, out, report):
        report.failed = True
        return report

    rendered = rendered_units(out)
    for unit in rendered:
        report.say(f"  rendered file {unit.label}: interface {unit.interface() or '(none)'}")

    hand = hand_units(configs, variables, environment, providers, report)
    compared: list[HandUnit] = []
    for item in hand:
        interface = item.unit.interface() or "(none)"
        if item.provider is None:
            report.say(f"  template {item.template}: interface {interface} belongs to no provider entry; skipped")
        elif item.provider.link_files == LINK_FILES_HAND_AUTHORED:
            report.say(
                f"  template {item.template}: interface {interface} belongs to provider {item.provider.name},"
                f" and that entry sets link-files to hand-authored; excluded"
            )
        else:
            report.say(f"  template {item.template}: interface {interface} belongs to provider {item.provider.name}; compared")
            compared.append(item)

    hand_by_key: dict[tuple[str, tuple[tuple[str, str], ...]], HandUnit] = {}
    for item in compared:
        key = item.unit.pair_key()
        if key in hand_by_key:
            report.differences.append(
                f"{item.template} and {hand_by_key[key].template} have the same [Match] block; neither can be paired"
            )
            continue
        hand_by_key[key] = item

    unpaired = dict(hand_by_key)
    for unit in rendered:
        key = unit.pair_key()
        partner = unpaired.pop(key, None)
        if partner is None:
            report.differences.append(
                f"{unit.label}: missing hand-authored template; no compared template has [Match] {dict(key[1])}"
            )
            continue
        report.say(f"  pair: {unit.label} <-> {partner.template} (interface {unit.interface()})")
        found = compare_units(unit, partner.unit)
        if found:
            report.differences.extend(found)
        else:
            report.say("    identical outside comment lines and blank lines")
    for key, item in unpaired.items():
        report.differences.append(
            f"{item.template}: extra hand-authored template; no rendered file has [Match] {dict(key[1])}"
        )
    return report


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def parse_arguments(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--configs", required=True, type=Path, help="checkout of agoodkind/configs")
    parser.add_argument("--render", required=True, type=Path, help="the built render program")
    parser.add_argument("--work", required=True, type=Path, help="directory that receives every rendered file")
    parser.add_argument("--group", required=True, action="append", help="gateway group to compare; repeatable")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    arguments = parse_arguments(argv)
    exit_code = 0
    for group in arguments.group:
        print(f"== group {group}")
        try:
            report = compare_group(group, arguments.configs, arguments.render, arguments.work)
        except (jinja2.TemplateError, ValueError, KeyError, RecursionError, OSError) as error:
            print(f"  comparison failed before any file was compared: {error!r}")
            print(f"RESULT group {group}: failed")
            exit_code = 1
            continue
        for line in report.lines:
            print(line)
        if report.failed:
            print(f"RESULT group {group}: failed")
            exit_code = 1
            continue
        for difference in report.differences:
            print(f"  DIFFERENCE {difference}")
        print(f"RESULT group {group}: {len(report.differences)} difference(s)")
        if report.differences:
            exit_code = 1
    return exit_code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
