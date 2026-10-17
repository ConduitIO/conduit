"""A scaffolded Conduit source connector.

Replace ``GeneratorSource`` with your own source (or add a ``Destination``),
then package it into a standalone plugin Conduit can launch:

    conduit-connector-sdk build . -o connectorname.pyz

Conduit launches a standalone connector with a clean environment (no inherited
``PATH``), so the built artifact — a directly-executable file with an absolute
interpreter shebang and every third-party dependency vendored — is the
deployment unit, not ``python main.py``.
"""

from __future__ import annotations

from conduit.connector import BackoffRetry, Change, Operation, Record, Source, serve
from conduit.connector.config import BaseConfig, Field, Specification


class Config(BaseConfig):
    """Configuration for :class:`GeneratorSource`."""

    count: int = Field(default=10, ge=1, description="Number of records to emit before backoff.")


class GeneratorSource(Source[Config]):
    """Emits a finite sequence of synthetic records, resuming from a position.

    A minimal, dependency-free source that passes the SDK acceptance suite
    in-process. Replace ``read``/``open`` with your real reads; keep the
    position semantics (invariant 2): ``open`` resumes strictly *after* the
    last position it emitted, and ``read`` returns monotonic positions.
    """

    _next: int = 0

    async def open(self, position: bytes | None) -> None:
        """Resume after ``position`` (the last successfully emitted value)."""
        self._next = int(position.decode()) + 1 if position else 0

    async def read(self) -> Record:
        """Emit the next record, or signal there is nothing yet.

        Raises:
            BackoffRetry: the sequence is exhausted; the SDK's read loop paces
                retries (do not sleep here yourself).
        """
        if self._next >= self.config.count:
            raise BackoffRetry()

        value = self._next
        self._next += 1
        return Record(
            position=str(value).encode(),
            operation=Operation.CREATE,
            key={"id": value},
            payload=Change(after={"value": value}),
        )

    async def teardown(self) -> None:
        """Release any resources opened in :meth:`open`."""


if __name__ == "__main__":
    serve(Specification(name="connectorname", version="0.1.0", author="you"), source=GeneratorSource)
