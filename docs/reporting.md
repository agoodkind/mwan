# MWAN reporting

MWAN sends daily email reports about each internet connection, or WAN. WANCONFIG identifies the monitored connections and their interfaces. The monitor reads network state and sends probes without changing the gateway's network configuration.

## Report contents

The report compares WANs on shared graphs and measures CPU and RAM in the Linux environment running the monitor.

Report measurements:

| Graph | Measurement | Grouping |
|---|---|---|
| Usage | Downloaded and uploaded data, in GiB. | Cumulative daily totals for the current UTC day and the preceding 29 UTC days. |
| Average traffic rate | Download and upload rates, in Mbps. | Interface bytes divided by collection time for each WAN. |
| Latency | Gateway and Cloudflare reply times, in ms. | Separate WAN, target, and address-family lines. |
| Packet loss | Failed probe attempts, in percent. | Separate WAN, target, and address-family lines. |
| CPU and RAM | Used CPU and memory, in percent. | Overall CPU, each visible logical CPU, and RAM. |

Overall CPU compares used and total CPU time across the visible cores, while each core's line uses only that core's counters. RAM usage excludes memory that Linux reports as available. The combined graph starts with the first valid CPU interval and ends with the last valid CPU interval. Missing measurements within that period appear as gaps.

Daily emails cover the configured local reporting period across daylight-saving changes. Missing intervals or unavailable WAN counters make the report partial.

## Client usage

Client tables use byte counters from connections that the monitor can attribute to a LAN client and WAN. Each table ranks observed clients by downloaded plus uploaded bytes and lists the destination addresses with the largest byte counts. The monitor identifies clients and WANs from Linux connection records.

Missing or unreliable counters make client accounting incomplete for the affected WAN. The usage graph also counts traffic that originates on the gateway, while client tables count tracked LAN connections.

## Connectivity measurements

Gateway and Cloudflare probes use the observed WAN's interface and source address. A fresh probe uses a new ping identifier, which Linux treats as a new connection. A persistent probe reuses its identifier until the renewal interval ends.

The monitor starts attempts at the configured probe interval even when earlier requests still await replies. MWAN uses fresh probe results for incident alerts and persistent probe results to check an existing connection.

## Incident emails

MWAN combines concurrent WAN failures into one incident. Each email identifies the affected WANs and triggering failures under **Current incident**, then separates **Other WANs for comparison** with a visible divider. The graphs compare all monitored WANs and label the incident members.

A newly affected WAN joins the open incident and appears in the next scheduled reminder. MWAN sends recovery only after every affected WAN and local gateway failure satisfies the recovery interval.

Unavailable probes or counters cannot establish recovery. MWAN waits for complete healthy measurements and treats local send and receive buffer errors as gateway failures, separately from ISP failures.

## History and delivery

MWAN stores WAN history under stable connection identities and records each CPU core separately. Renaming an interface does not change the history associated with its WAN identity.

MWAN saves each pending report before delivery and removes it after the mail transport accepts the message. Failed reports remain pending across a restart, and the monitor retries them after the next complete collection interval. Collection runs separately from rendering and mail delivery.

MWAN skips daily periods without observations after collection resumes. A manual summary uses saved measurements without closing the scheduled report period.

RRDtool stores graph history and renders solid lines with distinct colors, explicit units, and aligned legends. MWAN uses Liberation Mono for graph text and `send-email` for message rendering and delivery, including its standard footer.

