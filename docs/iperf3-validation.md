# iperf3 UE-Plane Validation

This validation adds a long-running iperf3 server on the N6 secondary network and runs the iperf3 client from PacketRusher's UE VRF.

## Topology

```text
PacketRusher UE VRF
10.63.0.1/32
    |
    | GTP-U over N3
    v
UPF
N3 10.200.3.2/24
N6 10.200.6.2/24
    |
    | cw-multinet N6, VNI 5206
    v
iperf3 server
10.200.6.10/24
```

The server is deployed by `charts/free5gc-cw-multinet/templates/iperf3-server-deploy.yaml` when `iperf3.enabled=true`.

## Server Deployment

The iperf3 server is a normal Deployment, not a Job. It keeps running and listens on N6:

```yaml
iperf3:
  enabled: true
  image:
    repository: ghcr.io/nicolaka/netshoot
    tag: latest
  port: 5201
  n6:
    multusIP: "10.200.6.10"
    multusNetworkMask: "24"
```

The server adds a return route for the UE pool through the UPF N6 address:

```sh
ip route replace 10.63.0.0/16 via 10.200.6.2 dev n6
iperf3 -s -B 10.200.6.10 -p 5201
```

## Client Command

Run the client from PacketRusher's UE VRF:

```sh
KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 \
  NAMESPACE=free5gc-cwm \
  SERVER_IP=10.200.6.10 \
  UE_IP=10.63.0.1 \
  VRF=vrf0000000003 \
  DURATION=10 \
  PARALLEL=1 \
  ./scripts/run-packetrusher-iperf3.sh
```

## Live Test Report

Cluster: CoreWeave test cluster from `/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04`

Namespace: `free5gc-cwm`

Test date: 2026-06-15

Components:

- CNI: `cw-multinet`
- N6 VNI: `5206`
- PacketRusher N3: `10.200.3.3/24`
- UPF N3: `10.200.3.2/24`
- UPF N6: `10.200.6.2/24`
- iperf3 server N6: `10.200.6.10/24`
- UE VRF: `vrf0000000003`
- UE IP: `10.63.0.1/32`

Chart deployment:

- Helm release: `free5gc`
- Chart: `free5gc-cw-multinet-0.1.1`
- Release revision: `3`
- iperf3 server Deployment: `free5gc-iperf3-server`
- Server pod state: `1/1 Running`

Server validation:

```text
n6@if19621       UP             10.200.6.10/24
10.63.0.0/16 via 10.200.6.2 dev n6
```

Client command used:

```sh
KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 ./scripts/run-packetrusher-iperf3.sh
```

Client result:

```text
Connecting to host 10.200.6.10, port 5201
[  6] local 10.63.0.1 port 47235 connected to 10.200.6.10 port 5201
[ ID] Interval           Transfer     Bitrate         Retr
[  6]   0.00-10.01  sec  1.29 GBytes  1.11 Gbits/sec  505             sender
[  6]   0.00-10.01  sec  1.29 GBytes  1.11 Gbits/sec                  receiver
```

Server result:

```text
Accepted connection from 10.63.0.1
[  5] local 10.200.6.10 port 5201 connected to 10.63.0.1
[  5]   0.00-10.01  sec  1.29 GBytes  1.11 Gbits/sec                  receiver
```

Result status: passed. PacketRusher successfully generated UE-plane TCP traffic from `10.63.0.1` through UPF to the persistent N6 iperf3 server at `10.200.6.10`.
