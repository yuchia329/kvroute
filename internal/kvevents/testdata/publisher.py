"""A real libzmq publisher speaking vLLM 0.28.0's KV event protocol.

zmq_test.go runs it through uv, when KVROUTE_LIBZMQ=1:

    uv run --no-project --with pyzmq==27.2.0 --with msgspec==0.21.1 \
        python internal/kvevents/testdata/publisher.py

pyzmq 27.2.0 bundles libzmq 4.3.5, and both are the versions installed beside
vLLM 0.28.0 in the GPU box's engine venv. The sockets, the multipart framing and
the replay service below are ZmqEventPublisher's, from
vllm/distributed/kv_events.py at that tag: a PUB socket sending
(topic, seq as 8 big-endian bytes, payload), a ROUTER socket answering a replay
request of (identity, empty, start seq) with every buffered batch from that
sequence number on, and then an end marker whose sequence number is -1.

What is not the engine's is the driving. The engine publishes one batch per
scheduler step; this publishes when told to, so that a test decides when a
subscriber falls behind. It prints one line of JSON once its sockets are bound,
then reads one command per line from stdin and acknowledges each:

    publish N   publish the next N batches
    drop N      buffer the next N batches for replay without sending them live,
                which is what the PUB socket's high-water mark does to a
                subscriber that has fallen behind
    quit

Batch n carries one BlockStored event whose one block is named n + 1 and whose
sixteen tokens are all n, so a subscriber can check it received the batch it
thinks it did.
"""

import json
import sys

import msgspec
import zmq

# gen.py sits beside this file and holds the engine's struct definitions.
from gen import BlockStored, KVEventBatch

END_SEQ = (-1).to_bytes(8, "big", signed=True)
TOPIC = b""


def batch(seq):
    event = BlockStored(
        block_hashes=[seq + 1],
        parent_block_hash=None,
        token_ids=[seq] * 16,
        block_size=16,
        lora_id=None,
        medium="GPU",
        lora_name=None,
        group_idx=0,
    )
    return KVEventBatch(ts=float(seq), events=[event], data_parallel_rank=0)


def main():
    ctx = zmq.Context.instance()
    pub = ctx.socket(zmq.PUB)
    pub.set_hwm(100_000)
    pub_port = pub.bind_to_random_port("tcp://127.0.0.1")
    replay = ctx.socket(zmq.ROUTER)
    replay_port = replay.bind_to_random_port("tcp://127.0.0.1")

    encoder = msgspec.msgpack.Encoder()
    buffer = []  # (seq, payload), as the engine's deque(maxlen=buffer_steps)
    seq = 0

    print(json.dumps({
        "endpoint": f"tcp://127.0.0.1:{pub_port}",
        "replay": f"tcp://127.0.0.1:{replay_port}",
    }), flush=True)

    poller = zmq.Poller()
    poller.register(replay, zmq.POLLIN)
    poller.register(sys.stdin, zmq.POLLIN)
    while True:
        for sock, _ in poller.poll():
            if sock is replay:
                # ZmqEventPublisher._service_replay, verbatim in effect.
                frame = replay.recv_multipart()
                if len(frame) != 3:
                    continue
                client_id, _, start_seq_bytes = frame
                start_seq = int.from_bytes(start_seq_bytes, "big")
                for s, buf in buffer:
                    if s >= start_seq:
                        replay.send_multipart((client_id, b"", TOPIC, s.to_bytes(8, "big"), buf))
                replay.send_multipart((client_id, b"", b"", END_SEQ, b""))
                continue

            line = sys.stdin.readline()
            if not line or line.strip() == "quit":
                return
            command, count = line.split()
            for _ in range(int(count)):
                payload = encoder.encode(batch(seq))
                if command == "publish":
                    pub.send_multipart((TOPIC, seq.to_bytes(8, "big"), payload))
                buffer.append((seq, payload))
                seq += 1
            print(json.dumps({"next": seq}), flush=True)


if __name__ == "__main__":
    main()
