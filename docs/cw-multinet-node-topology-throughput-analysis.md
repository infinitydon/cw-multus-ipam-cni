# cw-multinet Node Topology Throughput Analysis

Date: 2026-06-16

This note explains the node placement labels observed during the June 16 `cw-multinet` throughput retest and how they relate to the throughput results in `docs/cw-multinet-iperf3-performance.md`.

## Node Topology Labels

The cluster contained two GPU worker nodes and one CPU worker node:

| Node | Class | Type / Pool | Region | Zone | Datahall | Rack | Rack unit | Grid group | Rack label | Ethernet |
| --- | --- | --- | --- | ---: | --- | ---: | ---: | --- | --- | --- |
| `g46cd14` | `gpu` | `rtxp6000-8x` | `US-EAST-04` | `184` | `DH2` | `184` | `19` | `G` | `dh2-r184-us-east-04a` | `100G mlx5_core` |
| `g55e620` | `gpu` | `rtxp6000-8x` | `US-EAST-04` | `193` | `DH2` | `193` | `37` | `G` | `dh2-r193-us-east-04a` | `100G mlx5_core` |
| `g80b396` | `cpu` | `cd-hp-a96-genoa` | `US-EAST-04` | `238` | `DH1` | `238` | `1` | `A` | `dh1-r238-us-east-04a` | `100G mlx5_core` |

The explicit fabric and leafgroup labels exist, but are not populated on these nodes:

```text
backend.coreweave.cloud/fabric=
backend.coreweave.cloud/leafgroup=
backend.coreweave.cloud/superpod=
ib.coreweave.cloud/fabric=
ib.coreweave.cloud/leafgroup=
ib.coreweave.cloud/superpod=
```

So there is no direct Kubernetes label that names a fabric. However, the populated topology labels are still useful:

- The two GPU nodes are both in `DH2` and grid group `G`.
- The CPU node is in `DH1` and grid group `A`.
- All three nodes advertise `ethernet.coreweave.cloud/speed=100G` with `mlx5_core`.

This means the CPU node is likely on a different placement path from the two GPU nodes, even though all three expose 100G Ethernet.

## Throughput Reference

Detailed raw results are in:

```text
docs/cw-multinet-iperf3-performance.md
```

The most relevant June 16 `iperf3` results were:

| Pair / Direction | TCP `-P4` | TCP `-P8` | Bidirectional `-P4` |
| --- | ---: | ---: | ---: |
| `g46cd14 -> g80b396` | `50.9 Gbits/sec` | `79.5 Gbits/sec` | `42.5 Gbits/sec` |
| `g80b396 -> g46cd14` | `64.6 Gbits/sec` | not run separately | `65.0 Gbits/sec` |
| `g55e620 -> g80b396` | `50.4 Gbits/sec` | `96.2 Gbits/sec` | `42.2 Gbits/sec` |
| `g80b396 -> g55e620` | `61.4 Gbits/sec` | not run separately | `64.6 Gbits/sec` |
| `g46cd14 -> g55e620` | `71.9 Gbits/sec` | `109 Gbits/sec` | `55.5 Gbits/sec` |
| `g55e620 -> g46cd14` | `64.2 Gbits/sec` | not run separately | `54.8 Gbits/sec` |

The best result was the GPU-to-GPU path:

```text
g46cd14 -> g55e620, TCP -P8: 109 Gbits/sec
```

Paths involving the CPU node were still strong, but more variable:

```text
g46cd14 -> g80b396, TCP -P8: 79.5 Gbits/sec
g55e620 -> g80b396, TCP -P8: 96.2 Gbits/sec
g80b396 -> g46cd14, TCP -P4: 64.6 Gbits/sec
g80b396 -> g55e620, TCP -P4: 61.4 Gbits/sec
```

## Interpretation

The results are consistent with topology and node-class differences rather than a `cw-multinet` lifecycle or overlay failure:

- `g55e620` was added roughly 9 hours before the lifecycle test and joined the overlay correctly.
- `cw-multinet-cni` and Whereabouts were running on the new node.
- New-node secondary L2 connectivity passed with `0%` packet loss.
- The new node participated in high-throughput tests, including `96.2 Gbits/sec` to the CPU node and `109 Gbits/sec` received from the other GPU node.
- The CPU node is in a different datahall/grid/rack placement (`DH1`, grid `A`, rack `238`) from the GPU nodes (`DH2`, grid `G`, racks `184` and `193`).

The observed directional variance is therefore expected for a VXLAN overlay running across different host classes and likely different underlying fabric paths. `cw-multinet` is able to exceed 100 Gbit/s when the node pair and stream count allow it.

## Testing Guidance

For future throughput validation:

- Use `iperf3` as the primary tool.
- Use `-P8` or higher when trying to estimate the overlay ceiling.
- Always test both directions.
- Record node class, node pool, datahall, rack, grid group, and Ethernet speed alongside throughput.
- Do not treat UDP results from the current `netshoot` pod as the overlay ceiling; both `iperf3` UDP and `nuttcp` UDP underdrove the path in these tests.
- Treat `nuttcp` as a sanity check only in this environment; it produced much lower TCP throughput than `iperf3`.

