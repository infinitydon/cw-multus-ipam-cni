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

Run the PacketRusher client from the repository root:

```sh
KUBECONFIG=/Users/cadigun/Downloads/CWKubeconfig_eben-cluster04 ./scripts/run-packetrusher-iperf3.sh
```

See `docs/iperf3-validation.md` for the live CoreWeave validation report.
