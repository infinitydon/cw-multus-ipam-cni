# cw-multinet iperf3 Performance Test

This test measures `cw-multinet` directly, outside of free5GC and PacketRusher. The client and server pods are pinned to different worker nodes and communicate only through a fresh cw-multinet secondary interface.

## Test Environment

- Cluster: CoreWeave test cluster
- Date: 2026-06-15
- Namespace: `cw-multinet-perf`
- Server node: `g46cd14`
- Client node: `g80b396`
- Server primary IP: `10.8.0.38`
- Client primary IP: `10.8.0.150`
- Test NAD: `perf-net`
- CNI type: `cw-multinet`
- VNI: `6301`
- VXLAN UDP port: `14789`
- MTU: `1450`
- Server secondary interface: `perf0`, `10.250.0.10/24`
- Client secondary interface: `perf0`, `10.250.0.20/24`
- Image: `ghcr.io/nicolaka/netshoot:latest`

The temporary manifest used for this test is stored at `examples/iperf3-cw-multinet-perf.yaml`.

## Deployment

```sh
export KUBECONFIG=/path/to/kubeconfig
kubectl apply -f examples/iperf3-cw-multinet-perf.yaml

kubectl -n cw-multinet-perf wait --for=condition=Ready \
  pod/iperf3-server pod/iperf3-client --timeout=180s
```

The test namespace was deleted after the results were captured:

```sh
kubectl delete namespace cw-multinet-perf
```

## Interface Validation

Server:

```text
perf0@if795      UP             10.250.0.10/24
```

Client:

```text
perf0@if19667    UP             10.250.0.20/24
10.250.0.10 dev perf0 src 10.250.0.20 uid 0
```

The first ping immediately after pod creation failed until the node agent reconciled the brand-new VNI. The agent then logged:

```text
attached vxlan=vx-cwm-6301 bridge=br-cwm-6301
added fdb peer=10.176.243.1 link=vx-cwm-6301
```

After that reconcile cycle, cross-node ping succeeded:

```text
5 packets transmitted, 5 received, 0% packet loss
rtt min/avg/max/mdev = 0.244/0.381/0.853/0.236 ms
```

## Commands

TCP single stream:

```sh
kubectl -n cw-multinet-perf exec iperf3-client -- \
  iperf3 -c 10.250.0.10 -B 10.250.0.20 -p 5201 -t 15
```

TCP parallel streams:

```sh
kubectl -n cw-multinet-perf exec iperf3-client -- \
  iperf3 -c 10.250.0.10 -B 10.250.0.20 -p 5201 -t 15 -P 4
```

TCP reverse:

```sh
kubectl -n cw-multinet-perf exec iperf3-client -- \
  iperf3 -c 10.250.0.10 -B 10.250.0.20 -p 5201 -t 15 -R
```

UDP:

```sh
kubectl -n cw-multinet-perf exec iperf3-client -- \
  iperf3 -c 10.250.0.10 -B 10.250.0.20 -p 5201 -u -b 10G -t 15
```

## Results

| Test | Direction | Duration | Result |
| --- | --- | ---: | --- |
| Ping | client to server | 5 packets | `0%` loss, `0.381 ms` average RTT |
| TCP single stream | `10.250.0.20 -> 10.250.0.10` | 15s | `39.1 GBytes`, `22.4 Gbits/sec` receiver |
| TCP 4 parallel streams | `10.250.0.20 -> 10.250.0.10` | 15s | `125 GBytes`, `71.3 Gbits/sec` receiver |
| TCP reverse single stream | `10.250.0.10 -> 10.250.0.20` | 15s | `22.5 GBytes`, `12.9 Gbits/sec` receiver |
| UDP target 10G | `10.250.0.20 -> 10.250.0.10` | 15s | `2.40 GBytes`, `1.37 Gbits/sec`, `0%` loss, `0.007 ms` jitter |

TCP single-stream client summary:

```text
[  5]   0.00-15.00  sec  39.1 GBytes  22.4 Gbits/sec  184            sender
[  5]   0.00-15.00  sec  39.1 GBytes  22.4 Gbits/sec                  receiver
```

TCP 4-stream client summary:

```text
[SUM]   0.00-15.00  sec   125 GBytes  71.4 Gbits/sec  368             sender
[SUM]   0.00-15.00  sec   125 GBytes  71.3 Gbits/sec                  receiver
```

TCP reverse client summary:

```text
[  5]   0.00-15.00  sec  22.5 GBytes  12.9 Gbits/sec    0            sender
[  5]   0.00-15.00  sec  22.5 GBytes  12.9 Gbits/sec                  receiver
```

UDP client summary:

```text
[  5]   0.00-15.00  sec  2.40 GBytes  1.37 Gbits/sec  0.000 ms  0/1844169 (0%)  sender
[  5]   0.00-15.00  sec  2.40 GBytes  1.37 Gbits/sec  0.007 ms  0/1844169 (0%)  receiver
```

## Notes

- This is a direct CNI overlay test. It does not use AMF, SMF, UPF, PacketRusher, GTP-U, PFCP, or the UE VRF.
- The test uses a new VNI, so the first packets can fail until the `cw-multinet-agent` sees and reconciles the new host VXLAN device. The current agent sync period is `30s`.
- The TCP parallel result is the best indicator of aggregate cw-multinet inter-node throughput on this cluster: `71.3 Gbits/sec` receiver throughput over VXLAN.

## Event-Driven Reconcile Retest

After the initial test, the agent was updated to remove the first-packet race for new VNIs:

- Watches Kubernetes Nodes with a shared informer and reconciles peers on node changes.
- Watches `NetworkAttachmentDefinition` resources with a dynamic informer.
- Parses `type: cw-multinet` NAD configs and pre-creates declared bridge/VXLAN devices on every node.
- Subscribes to Linux netlink link events and reconciles when local `vx-cwm-*` devices appear.
- Keeps the periodic reconcile loop as a self-healing fallback.

Deployed image:

```text
installer=ghcr.io/infinitydon/cw-multus-ipam-cni:7b7286b
agent=ghcr.io/infinitydon/cw-multus-ipam-cni:7b7286b
```

The free5GC/PacketRusher releases and the `free5gc-cwm` namespace were deleted before this retest.

### Pre-Warmed NAD Test

For VNI `6401`, only the namespace and NAD were created first. Before creating any pods, both node agents pre-created the overlay:

```text
g46cd14:
created bridge=br-cwm-6401
created vxlan=vx-cwm-6401 vni=6401 bridge=br-cwm-6401
added fdb peer=10.176.243.1 link=vx-cwm-6401

g80b396:
created bridge=br-cwm-6401
created vxlan=vx-cwm-6401 vni=6401 bridge=br-cwm-6401
added fdb peer=10.176.244.201 link=vx-cwm-6401
```

After the pods became Ready, the first ping succeeded immediately:

```text
PING 10.251.0.10 (10.251.0.10) 56(84) bytes of data.
64 bytes from 10.251.0.10: icmp_seq=1 ttl=64 time=0.590 ms

5 packets transmitted, 5 received, 0% packet loss
rtt min/avg/max/mdev = 0.265/0.343/0.590/0.123 ms
```

Throughput on VNI `6401`:

| Test | Direction | Duration | Result |
| --- | --- | ---: | --- |
| Ping | client to server | 5 packets | `0%` loss, `0.343 ms` average RTT |
| TCP single stream | `10.251.0.20 -> 10.251.0.10` | 15s | `39.0 GBytes`, `22.4 Gbits/sec` receiver |
| TCP 4 parallel streams | `10.251.0.20 -> 10.251.0.10` | 15s | `110 GBytes`, `62.8 Gbits/sec` receiver |
| TCP reverse single stream | `10.251.0.10 -> 10.251.0.20` | 15s | `23.2 GBytes`, `13.3 Gbits/sec` receiver |
| UDP target 10G | `10.251.0.20 -> 10.251.0.10` | 15s | `2.35 GBytes`, `1.35 Gbits/sec`, `0%` loss, `0.009 ms` jitter |

### Simultaneous Apply Test

For VNI `6402`, namespace, NAD, server pod, and client pod were applied together. Both node agents still created the overlay promptly:

```text
g46cd14:
created bridge=br-cwm-6402
created vxlan=vx-cwm-6402 vni=6402 bridge=br-cwm-6402
added fdb peer=10.176.243.1 link=vx-cwm-6402

g80b396:
created bridge=br-cwm-6402
created vxlan=vx-cwm-6402 vni=6402 bridge=br-cwm-6402
added fdb peer=10.176.244.201 link=vx-cwm-6402
```

The first ping after both pods became Ready also succeeded immediately:

```text
PING 10.252.0.10 (10.252.0.10) 56(84) bytes of data.
64 bytes from 10.252.0.10: icmp_seq=1 ttl=64 time=0.670 ms

5 packets transmitted, 5 received, 0% packet loss
rtt min/avg/max/mdev = 0.215/0.343/0.670/0.165 ms
```

Result: passed. The new event-driven/NAD-aware agent removed the observed initial `Destination Host Unreachable` behavior for fresh VNIs in both pre-warmed and simultaneous apply flows.

## Auto-VNI Allocation Retest

The agent was then updated so users no longer need to manually select VNIs for every NAD. If a live `cw-multinet` NAD omits `vni`, the agent allocates the next free VNI from its configured range and patches only that field into `spec.config`, preserving the rest of the CNI configuration.

Deployed image:

```text
installer=ghcr.io/infinitydon/cw-multus-ipam-cni:38b7eed
agent=ghcr.io/infinitydon/cw-multus-ipam-cni:38b7eed
```

Agent defaults:

```text
PREWARM_NADS=false
AUTO_ALLOCATE_VNI=true
VNI_RANGE_START=10000
VNI_RANGE_END=16777215
```

Input NAD intentionally omitted `vni`:

```json
{
  "cniVersion": "0.4.0",
  "name": "auto-net",
  "type": "cw-multinet",
  "capabilities": { "ips": true },
  "mtu": 1450,
  "vxlanPort": 14789,
  "ipam": {
    "type": "static",
    "addresses": []
  }
}
```

The agent patched the NAD to add `vni: 10000` while preserving the original config:

```json
{
  "capabilities": {
    "ips": true
  },
  "cniVersion": "0.4.0",
  "ipam": {
    "addresses": [],
    "type": "static"
  },
  "mtu": 1450,
  "name": "auto-net",
  "type": "cw-multinet",
  "vni": 10000,
  "vxlanPort": 14789
}
```

Cross-node pods then attached successfully:

- Server node: `g46cd14`
- Client node: `g80b396`
- Server secondary IP: `10.253.0.10/24`
- Client secondary IP: `10.253.0.20/24`

First ping after pod readiness succeeded:

```text
5 packets transmitted, 5 received, 0% packet loss
rtt min/avg/max/mdev = 0.220/0.326/0.675/0.174 ms
```

TCP single-stream throughput:

```text
[  5]   0.00-10.00  sec  26.0 GBytes  22.4 Gbits/sec  136            sender
[  5]   0.00-10.00  sec  26.0 GBytes  22.3 Gbits/sec                  receiver
```

Result: passed. A NAD without a manually supplied VNI was assigned a stable VNI, preserved the original CNI configuration, attached pods successfully, and delivered normal cross-node cw-multinet throughput.

## New-Node Lifecycle and Expanded Throughput Retest

Date: 2026-06-16

The cluster had a newly added worker node:

| Node | Internal IP | Age at test | Status |
| --- | --- | ---: | --- |
| `g46cd14` | `10.176.244.201` | `67d` | `Ready` |
| `g80b396` | `10.176.243.1` | `28d` | `Ready` |
| `g55e620` | `10.176.245.11` | `9h` | `Ready` |

`cw-multinet-cni` and Whereabouts were already running on all three nodes. The new node had:

```text
cw-multinet-system/cw-multinet-cni-97ld8   2/2 Running   node=g55e620
kube-system/whereabouts-ddrrh              1/1 Running   node=g55e620
```

The new node agent log showed that it joined normally:

```text
starting cw-multinet-agent ... nodeName=g55e620 ...
set /proc/sys/net/bridge/bridge-nf-call-iptables=0
set /proc/sys/net/bridge/bridge-nf-call-ip6tables=0
auto-vni allocator leader=g46cd14
```

### Lifecycle Proof

A temporary namespace `cw-multinet-node-lifecycle-test` was created with VNI `7601`, one pod pinned to the new node and one pod pinned to an older node:

| Pod | Node | Secondary IP |
| --- | --- | --- |
| `lifecycle-new-node` | `g55e620` | `10.252.60.20/24` |
| `lifecycle-old-node` | `g80b396` | `10.252.60.10/24` |

Both pods attached to the secondary interface:

```text
lifecycle-new-node:
test0  10.252.60.20/24

lifecycle-old-node:
test0  10.252.60.10/24
```

The agents reconciled the new VNI and included the newly added node in FDB peer programming:

```text
g55e620:
attached vxlan=vx-cwm-7601 bridge=br-cwm-7601
added fdb peer=10.176.244.201 link=vx-cwm-7601
added fdb peer=10.176.243.1 link=vx-cwm-7601

g80b396:
attached vxlan=vx-cwm-7601 bridge=br-cwm-7601
added fdb peer=10.176.245.11 link=vx-cwm-7601
```

Dataplane validation from the new node to the older node passed:

| Test | Direction | Result |
| --- | --- | --- |
| ICMP | `10.252.60.20 -> 10.252.60.10` | `5/5` received, `0%` loss, `0.320 ms` average RTT |
| iperf3 TCP single stream | `10.252.60.20 -> 10.252.60.10` | `15.4 GBytes`, `13.2 Gbits/sec` receiver |
| iperf3 TCP 4 parallel streams | `10.252.60.20 -> 10.252.60.10` | `57.7 GBytes`, `49.5 Gbits/sec` receiver |

Result: passed. The plugin handled the new worker node without code or chart changes.

### Three-Node iperf3 Matrix

A second temporary namespace `cw-multinet-node-matrix-test` was created with VNI `7602` and one pod pinned to each worker:

| Pod | Node | Secondary IP |
| --- | --- | --- |
| `matrix-g46cd14` | `g46cd14` | `10.252.61.10/24` |
| `matrix-g80b396` | `g80b396` | `10.252.61.20/24` |
| `matrix-g55e620` | `g55e620` | `10.252.61.30/24` |

Each direction was tested with `iperf3 -P 4 -t 10`:

| Direction | Receiver throughput |
| --- | ---: |
| `g46cd14 -> g80b396` | `35.2 Gbits/sec` |
| `g80b396 -> g46cd14` | `68.0 Gbits/sec` |
| `g55e620 -> g80b396` | `51.3 Gbits/sec` |
| `g80b396 -> g55e620` | `71.0 Gbits/sec` |
| `g55e620 -> g46cd14` | `59.5 Gbits/sec` |
| `g46cd14 -> g55e620` | `78.3 Gbits/sec` |

The matrix showed strong directional variance. The newly added node was not generally slower: traffic into `g55e620` reached `71.0-78.3 Gbits/sec`, while traffic out of it reached `51.3-59.5 Gbits/sec` in this run. The old node pair also showed directional variance, with `g46cd14 -> g80b396` at `35.2 Gbits/sec` and `g80b396 -> g46cd14` at `68.0 Gbits/sec`.

### Expanded iperf3 Mode Test

A third temporary namespace `cw-multinet-throughput-methods` was created with VNI `7603` and one pod per node:

| Pod | Node | Secondary IP |
| --- | --- | --- |
| `methods-g46cd14` | `g46cd14` | `10.252.62.10/24` |
| `methods-g80b396` | `g80b396` | `10.252.62.20/24` |
| `methods-g55e620` | `g55e620` | `10.252.62.30/24` |

The test compared `iperf3` TCP parallelism, reverse mode, bidirectional mode, and UDP target mode.

TCP results:

| Pair / Direction | TCP `-P4` | TCP `-P8` | Bidirectional `-P4` |
| --- | ---: | ---: | ---: |
| `g46cd14 -> g80b396` | `50.9 Gbits/sec` | `79.5 Gbits/sec` | `42.5 Gbits/sec` |
| `g80b396 -> g46cd14` | `64.6 Gbits/sec` | not run separately | `65.0 Gbits/sec` |
| `g55e620 -> g80b396` | `50.4 Gbits/sec` | `96.2 Gbits/sec` | `42.2 Gbits/sec` |
| `g80b396 -> g55e620` | `61.4 Gbits/sec` | not run separately | `64.6 Gbits/sec` |
| `g46cd14 -> g55e620` | `71.9 Gbits/sec` | `109 Gbits/sec` | `55.5 Gbits/sec` |
| `g55e620 -> g46cd14` | `64.2 Gbits/sec` | not run separately | `54.8 Gbits/sec` |

UDP target `50G` results:

| Direction | Result |
| --- | ---: |
| `g46cd14 -> g80b396` | `2.19 Gbits/sec`, `6.5%` loss |
| `g80b396 -> g46cd14` | `1.31 Gbits/sec`, `0%` loss |
| `g55e620 -> g80b396` | `2.59 Gbits/sec`, `6%` loss |
| `g80b396 -> g55e620` | `1.30 Gbits/sec`, `0%` loss |
| `g46cd14 -> g55e620` | `2.55 Gbits/sec`, `0%` loss |
| `g55e620 -> g46cd14` | `2.41 Gbits/sec`, `0%` loss |

Key observations:

- `iperf3 -P4` can understate the available overlay ceiling on some paths.
- `iperf3 -P8` produced the highest observed results:
  - `g46cd14 -> g55e620`: `109 Gbits/sec`
  - `g55e620 -> g80b396`: `96.2 Gbits/sec`
  - `g46cd14 -> g80b396`: `79.5 Gbits/sec`
- Bidirectional TCP showed roughly `100-110 Gbits/sec` aggregate throughput on the tested pairs.
- UDP mode was much lower than TCP and appears generator/receiver limited in these containers, so it should not be used as the overlay ceiling measurement here.

### nuttcp Comparison

An additional temporary namespace `cw-multinet-alt-throughput` was created with VNI `7604` and the same three-node layout:

| Pod | Node | Secondary IP |
| --- | --- | --- |
| `alt-g46cd14` | `g46cd14` | `10.252.63.10/24` |
| `alt-g80b396` | `g80b396` | `10.252.63.20/24` |
| `alt-g55e620` | `g55e620` | `10.252.63.30/24` |

`ghcr.io/nicolaka/netshoot:latest` is Alpine-based. It did not include `nuttcp`, `netperf`, `ntttcp`, or `sockperf` by default. Alpine package repositories provided `nuttcp`, so it was installed at runtime:

```sh
apk add --no-cache nuttcp
```

Installed version:

```text
nuttcp-8.2.2
```

Alpine did not provide `netperf`, `ntttcp`, or `sockperf` in the configured repositories.

`nuttcp` TCP results:

| Direction | `nuttcp -N4 -T10` | `nuttcp -N8 -T10` |
| --- | ---: | ---: |
| `g46cd14 -> g80b396` | `13.5 Gbits/sec` | `14.3 Gbits/sec` |
| `g80b396 -> g46cd14` | `15.7 Gbits/sec` | not run |
| `g55e620 -> g80b396` | `13.9 Gbits/sec` | `13.8 Gbits/sec` |
| `g80b396 -> g55e620` | `15.8 Gbits/sec` | not run |
| `g46cd14 -> g55e620` | `18.6 Gbits/sec` | `17.9 Gbits/sec` |
| `g55e620 -> g46cd14` | `19.7 Gbits/sec` | not run |

`nuttcp` UDP target `50G` results:

| Direction | Result |
| --- | ---: |
| `g46cd14 -> g80b396` | `1.58 Gbits/sec`, `0%` loss |
| `g80b396 -> g46cd14` | `0.87 Gbits/sec`, `0%` loss |
| `g55e620 -> g80b396` | `1.35 Gbits/sec`, `5.78%` loss |
| `g80b396 -> g55e620` | `0.85 Gbits/sec`, `0%` loss |
| `g46cd14 -> g55e620` | `1.56 Gbits/sec`, `0%` loss |
| `g55e620 -> g46cd14` | `1.82 Gbits/sec`, `0%` loss |

Conclusion: `nuttcp` was useful as an independent sanity check but could not drive this overlay path as hard as `iperf3` in the same pod environment. For this cluster and container image, `iperf3` with adequate parallelism is the better throughput ceiling tool.

All temporary namespaces from the June 16 retests were deleted after capturing results:

```text
cw-multinet-node-lifecycle-test
cw-multinet-node-matrix-test
cw-multinet-throughput-methods
cw-multinet-alt-throughput
```
