# The spill rule's two signals

4022 routing decisions.

| signal | observed range | correlation with inflight |
|---|---|---|
| prefix cache hit rate (residency branch) | 3955 read, 67 unread: min 0.014, p10 0.560, p50 0.662, p90 0.735, max 0.908 | r = 0.138 over 3955 pairs |
| honoured rate (measured, not routed on) | 3927 read, 95 unread: min 0.948, p10 0.982, p50 1.000, p90 1.000, max 1.000 | r = -0.581 over 3927 pairs |
| batch KV occupancy (not routed on) | 4022 read, 0 unread: min 0.000, p10 0.009, p50 0.066, p90 0.583, max 0.848 | r = 0.979 over 4022 pairs |

The residency signal moves with inflight at r = 0.138. The gauge it replaced moved
with inflight at r = 0.973 over #16's rows, and at r = 0.979 over these.

Levels reachable at this rung are those above 0.014, the lowest rate observed.
A level at or below it cannot fire, which is what two of #16's three were.
