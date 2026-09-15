"""Golden payloads for the KV cache event decoder.

Regenerate from the repository root with:

    uv run --no-project --with msgspec==0.21.1 python internal/kvevents/testdata/gen.py

msgspec 0.21.1 is the version installed beside vLLM 0.28.0 in the GPU box's
engine venv. The struct definitions below carry the fields of
vllm/distributed/kv_events.py at that tag, read off the box on 2026-09-10, and
each payload is encoded the way ZmqEventPublisher encodes one: publish() stamps
the batch with the publisher's data-parallel rank, and the publisher thread then
calls msgspec.msgpack.Encoder().encode(batch).

So the decoder is tested against the engine's own encoder rather than against an
imitation of it written by the same hand as the decoder, which could only ever
agree with itself.

The expected values are written out as literals in decode_test.go rather than
read from here, for the same reason.
"""

import pathlib
from typing import Any

import msgspec


# --- vllm/distributed/kv_events.py @ v0.28.0 -------------------------------
# Fields and struct options as the engine declares them. The __hash__ methods
# the engine adds for its multi-worker aggregator are left out: they play no
# part in encoding.


class EventBatch(
    msgspec.Struct,
    array_like=True,
    omit_defaults=True,
    gc=False,
):
    ts: float
    events: list[Any]
    data_parallel_rank: int | None = None


class KVCacheEvent(
    msgspec.Struct,
    omit_defaults=True,
    gc=False,
    tag=True,
):
    """Base class for all KV cache-related events"""


class BlockStored(KVCacheEvent):
    block_hashes: list[bytes | int]
    parent_block_hash: bytes | int | None
    token_ids: list[int]
    block_size: int
    lora_id: int | None
    medium: str | None
    lora_name: str | None
    extra_keys: list[tuple[Any, ...] | None] | None = None
    group_idx: int | None = None
    kv_cache_spec_kind: str | None = None
    kv_cache_spec_sliding_window: int | None = None
    locality: str | None = None


class BlockRemoved(KVCacheEvent):
    block_hashes: list[bytes | int]
    medium: str | None
    group_idx: int | None = None
    locality: str | None = None


class AllBlocksCleared(KVCacheEvent):
    pass


class KVEventBatch(EventBatch):
    events: list[BlockStored | BlockRemoved | AllBlocksCleared]


# --- The cases ---------------------------------------------------------------

TS = 1757500000.25

# ZmqEventPublisher.publish sets this before encoding, so every batch the
# engine actually sends carries it. It is 0 on this fleet: one replica per GPU,
# no data parallelism.
RANK = 0

# Sixteen tokens spanning every msgpack integer width Llama 3's 128,256-entry
# vocabulary reaches: positive fixint (0, 1, 2, 42, 127), uint8 (128, 255),
# uint16 (256, 271, 9125, 65535) and uint32 (65536 and up).
FIRST_BLOCK = [128000, 128006, 9125, 128007, 271, 0, 1, 127, 128, 255, 256, 65535, 65536, 128255, 42, 2]
SECOND_BLOCK = list(range(100, 116))

# By default the engine sends each block hash as the low 64 bits of its digest,
# as a Python int (VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=1). Both of these need
# msgpack's uint64 form, and the second is the largest value it can hold.
ROOT_HASH = 0xDEADBEEF12345678
TOP_HASH = 0xFFFFFFFFFFFFFFFF


def stored(hashes, parent, tokens, **overrides):
    fields = dict(
        block_hashes=hashes,
        parent_block_hash=parent,
        token_ids=tokens,
        block_size=16,
        lora_id=None,
        medium="GPU",
        lora_name=None,
        # The engine always names the KV cache group; a single-group model
        # like this one is group 0.
        group_idx=0,
    )
    fields.update(overrides)
    return BlockStored(**fields)


def batch(*events, rank=RANK):
    return KVEventBatch(ts=TS, events=list(events), data_parallel_rank=rank)


CASES = {
    # Two full blocks from the start of a prompt: no parent.
    "stored.msgpack": batch(stored([ROOT_HASH, TOP_HASH], None, FIRST_BLOCK + SECOND_BLOCK)),
    # One block continuing a run whose last block the engine already holds.
    "stored_child.msgpack": batch(stored([7], TOP_HASH, SECOND_BLOCK)),
    "removed.msgpack": batch(BlockRemoved(block_hashes=[0x8000000000000001], medium="GPU", group_idx=0)),
    "cleared.msgpack": batch(AllBlocksCleared()),
    # A batch with no rank. The engine always stamps one before encoding, but
    # the field is optional in its schema, and msgspec 0.21.1 encodes the
    # absent value as a trailing nil rather than dropping it: omit_defaults
    # does not shorten an array_like envelope.
    "unranked.msgpack": batch(AllBlocksCleared(), rank=None),
    # VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=0: the full digest as bytes.
    "bytes_hashes.msgpack": batch(stored([bytes(range(32))], bytes(range(100, 132)), FIRST_BLOCK)),
    # Blocks whose hashes cover more than their tokens: a LoRA adapter, and a
    # cache salt on the first block only, as the engine attaches one.
    "keyed.msgpack": batch(
        stored(
            [11, 12],
            None,
            FIRST_BLOCK + SECOND_BLOCK,
            lora_id=3,
            lora_name="adapter",
            extra_keys=[("salt-1",), None],
        )
    ),
    # Several events in one engine step, in the order the engine queued them,
    # including a block stored to the CPU tier rather than the GPU.
    "mixed.msgpack": batch(
        stored([7], TOP_HASH, SECOND_BLOCK),
        stored([21], None, FIRST_BLOCK, medium="CPU"),
        BlockRemoved(block_hashes=[7], medium="GPU", group_idx=0),
        AllBlocksCleared(),
    ),
}


def main():
    here = pathlib.Path(__file__).parent
    encoder = msgspec.msgpack.Encoder()
    for name, value in CASES.items():
        (here / name).write_bytes(encoder.encode(value))
        print(f"wrote {name}")


if __name__ == "__main__":
    main()
