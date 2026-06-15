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
