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
export KUBECONFIG=/path/to/kubeconfig
NAMESPACE=core5g \
  SERVER_IP=10.200.6.10 \
  UE_IP=10.63.0.1 \
  VRF=vrf0000000003 \
  DURATION=10 \
  PARALLEL=1 \
  ./scripts/run-packetrusher-iperf3.sh
```

## Live Test Report

Cluster: CoreWeave test cluster

Namespace: `core5g`

Test date: 2026-06-15

Components:

- CNI: `cw-multinet`
- free5GC UPF image: `free5gc/upf:v4.1.0`
- `gtp5g` module: `v0.9.14`
- N6 VNI: `5206`
- PacketRusher N3: `10.200.3.3/24`
- UPF N3: `10.200.3.2/24`
- UPF N6: `10.200.6.2/24`
- iperf3 server N6: `10.200.6.10/24`
- UE VRF: `vrf0000000003`
- UE IP: `10.63.0.1/32`

Chart deployment:

- Helm release: `f5g-core`
- Chart: `free5gc-cw-multinet-0.1.2`
- PacketRusher chart: `packetrusher-cw-multinet-0.1.1`
- iperf3 server Deployment: `f5g-core-iperf3-server`
- Server pod state: `1/1 Running`

Root cause found:

```text
The cw-multinet N2/N3/N6 dataplane was healthy, but the UE path failed because
UPF could not program the kernel GTP-U rules:

Est CreateFAR error: no such file or directory
Est CreateQER error: no such file or directory
Est CreatePDR error: no such file or directory
```

Fix applied:

```text
gtp5g.version changed from v0.9.5 to v0.9.14.
The gtp5g installer DaemonSet rebuilt and loaded the module on both worker nodes.
AMF, SMF, and UPF were restarted to clear stale PDU session state before starting PacketRusher.
PacketRusher now references the deployed free5GC release NADs with coreDeploymentName=f5g-core.
```

Server validation:

```text
n6               UP             10.200.6.10/24
10.63.0.0/16 via 10.200.6.2 dev n6
```

Client command used:

```sh
export KUBECONFIG=/path/to/kubeconfig
./scripts/run-packetrusher-iperf3.sh
```

Client result:

```text
PacketRusher N3 to UPF N3:
3 packets transmitted, 3 received, 0% packet loss, rtt avg 0.091 ms

UE VRF to N6 iperf3 server:
3 packets transmitted, 3 received, 0% packet loss, rtt avg 0.137 ms

UE VRF to internet:
3 packets transmitted, 3 received, 0% packet loss, rtt avg 4.131 ms

iperf3 TCP from UE VRF to N6 server:
local 10.63.0.1 connected to 10.200.6.10 port 5201
0.00-5.00 sec 749 MBytes 1.25 Gbits/sec sender, 1376 retransmits
0.00-5.01 sec 749 MBytes 1.25 Gbits/sec receiver
```

Server result:

```text
Accepted connection from 10.63.0.1
[  5] local 10.200.6.10 port 5201 connected to 10.63.0.1 port 43810
[  5]   0.00-5.01   sec   749 MBytes  1.25 Gbits/sec                  receiver
```

Helper script verification:

```text
KUBECONFIG=/path/to/kubeconfig DURATION=5 ./scripts/run-packetrusher-iperf3.sh
[  6]   0.00-5.01   sec   713 MBytes  1.19 Gbits/sec   89             sender
[  6]   0.00-5.01   sec   713 MBytes  1.19 Gbits/sec                  receiver
```

## AMBR Retest

The original subscriber data provisioned UE AMBR as:

```json
"subscribedUeAmbr": {
  "downlink": "2 Gbps",
  "uplink": "1 Gbps"
}
```

The chart now exposes this as:

```yaml
subprov:
  subscribedUeAmbr:
    downlink: "20 Gbps"
    uplink: "20 Gbps"
```

The live subscriber records in MongoDB were updated and verified:

```json
{
  "ueId": "imsi-208930000000003",
  "subscribedUeAmbr": {
    "downlink": "20 Gbps",
    "uplink": "20 Gbps"
  }
}
```

After reprovisioning, AMF, SMF, UPF, UDM, UDR, and PCF were restarted, and PacketRusher established a fresh PDU session.

Retest results:

```text
ip vrf exec vrf0000000003 iperf3 -c 10.200.6.10 -p 5201 -P 4 -t 10
[SUM]   0.00-10.03  sec  1.27 GBytes  1.09 Gbits/sec  120             sender
[SUM]   0.00-10.03  sec  1.27 GBytes  1.09 Gbits/sec                  receiver

ip vrf exec vrf0000000003 iperf3 -c 10.200.6.10 -p 5201 -P 8 -t 10
[SUM]   0.00-10.00  sec  1.33 GBytes  1.14 Gbits/sec  133             sender
[SUM]   0.00-10.01  sec  1.33 GBytes  1.14 Gbits/sec                  receiver
```

Conclusion: raising UE AMBR from `1 Gbps` uplink to `20 Gbps` uplink did not increase the PacketRusher UE-plane iperf result. The observed limit is therefore likely elsewhere in the UE/PacketRusher/UPF path, not the provisioned UE AMBR.

Result status: passed. PacketRusher successfully generated UE-plane TCP traffic from `10.63.0.1` through UPF to the persistent N6 iperf3 server at `10.200.6.10`.
