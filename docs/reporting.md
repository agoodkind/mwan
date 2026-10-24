# MWAN reporting

MWAN records measurements for each configured WAN and sends daily reports and incident emails through its notification service. WANCONFIG supplies the WAN identities, interfaces, and address families. Reporting observes the gateway without changing its network configuration.

## Report contents

Shared graphs compare the WANs during the reporting period. A separate graph compares CPU and RAM use in the Linux environment running the reporter.

Report measurements:

| Graph | Measurement | Grouping |
|---|---|---|
| Usage | Downloaded and uploaded data, in GiB. | Cumulative daily totals for the current UTC day and the preceding 29 UTC days. |
| Average speed | Observed download and upload rates, in Mbps. | Each WAN's average during each collection interval. |
| Latency | Gateway and Cloudflare reply times, in ms. | Separate WAN, target, and address-family lines. |
| Packet loss | Unanswered probes, in percent. | Separate WAN, target, and address-family lines. |
| CPU and RAM | Used CPU and memory, in percent. | Overall CPU, each visible logical CPU, and RAM. |

The speed graph uses interface counters rather than speed tests. CPU measurements compare successive Linux counter readings, while RAM use equals total memory minus available memory. Missing measurements appear as gaps, including the initial CPU reading before a preceding sample exists.

Usage totals follow UTC days independently of the daily email schedule. The daily report period follows the configured local reporting time across daylight-saving changes. Collection that starts during a period produces a partial report.

Each WAN has a top-talker table that ranks clients by downloaded plus uploaded bytes and includes their main destination addresses. Linux connection tracking supplies the original client addresses and connection marks that identify the selected WAN.

Connections without reliable byte counters prevent complete client attribution. The report identifies accounting gaps separately for each WAN rather than dividing traffic according to load-balancing weights. Interface totals and attributed client totals measure different sources and can differ.

## Connectivity measurements

Gateway and Cloudflare probes use the observed WAN's interface and source address. IPv4 and IPv6 measurements remain separate. Fresh probes use a new ICMP connection identifier for each attempt, while persistent probes reuse an identifier until renewal.

Probe attempts start at the configured cadence even when earlier requests still await replies. Fresh probes determine incident thresholds. Persistent probes record the behavior of an existing connection.

## Incident emails

Concurrent WAN failures form one incident. Emails list the affected WANs under **Current incident**, followed by a visible divider and **Other WANs for comparison**. Graphs retain every WAN and mark incident membership in their legends.

A newly affected WAN joins the existing incident and appears in the scheduled reminder. Partial WAN recovery does not close the incident. Recovery requires every affected WAN and any local gateway failure to satisfy the configured recovery interval.

Local UDP counter failures remain distinct from WAN failures. Incomplete collection intervals do not establish a healthy window or open an incident.

## History and delivery

The reporter stores RRD series and interval observations in its configured state directory. Stable connection identities preserve WAN history across interface renames. CPU history accommodates changes in the visible core set.

The monitor saves pending delivery state before sending mail and acknowledges it after the transport accepts the message. Failed delivery remains pending across a restart while collection continues. A manual summary sends the persisted observations without closing the scheduled report period.

RRDtool renders solid lines with distinct colors, explicit units, and aligned legends. Liberation Mono supplies the graph text. The existing `send-email` library renders the message, embeds the graphs, and preserves its standard system-information footer.

The `mwan report watch` command collects observations and sends scheduled reports. The `mwan report send` command sends a summary from saved history. Both commands use the existing TOML configuration and validated WANCONFIG document.
