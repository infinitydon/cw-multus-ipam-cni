## free5gc-multiupf helm package
. This Helm package installs each of 5G (UDM, UDR, NRF, NSSF, AUSF, AMF, SMF, PCF, and UPF). 

. **Note** For traffic steering demo purpose, it creates 2 UPFs (UPF1 and UPF2).

## iperf3 UE-plane validation

This chart can deploy a persistent iperf3 server on the N6 cw-multinet network:

```yaml
iperf3:
  enabled: true
  port: 5201
  n6:
    multusIP: "10.200.6.10"
    multusNetworkMask: "24"
```

The server keeps running as a Deployment named `<release>-iperf3-server`. It adds a route back to the UE pool through the UPF N6 address, then listens on the N6 address.

## gtp5g compatibility

The chart defaults to `gtp5g.version=v0.9.14`. Keep this aligned with the deployed free5GC UPF image. If the UPF logs show `CreatePDR`, `CreateFAR`, or `CreateQER` errors, stop PacketRusher/UPF traffic, upgrade the module version, restart UPF/SMF/AMF, and then start PacketRusher again so stale PDU session state is cleared.

## Subscriber AMBR

The provisioned UE AMBR is configurable:

```yaml
subprov:
  subscribedUeAmbr:
    downlink: "20 Gbps"
    uplink: "20 Gbps"
```

The subscriber provisioner creates new subscribers and updates existing subscribers on reruns. It uses `POST` first and falls back to `PUT` when the WebUI reports that the subscriber already exists.

Run the PacketRusher client from the repository root:

```sh
export KUBECONFIG=/path/to/kubeconfig
./scripts/run-packetrusher-iperf3.sh
```

See `docs/iperf3-validation.md` for the live CoreWeave validation report.
