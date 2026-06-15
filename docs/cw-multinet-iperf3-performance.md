# cw-multinet iperf3 Performance Test

This test measures `cw-multinet` directly, outside of free5GC and PacketRusher. The client and server pods are pinned to different worker nodes and communicate only through a fresh cw-multinet secondary interface.

## Test Environment

- Cluster kubeconfig: `/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04`
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
KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 \
  kubectl apply -f examples/iperf3-cw-multinet-perf.yaml

KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 \
  kubectl -n cw-multinet-perf wait --for=condition=Ready \
  pod/iperf3-server pod/iperf3-client --timeout=180s
```

The test namespace was deleted after the results were captured:

```sh
KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 \
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
