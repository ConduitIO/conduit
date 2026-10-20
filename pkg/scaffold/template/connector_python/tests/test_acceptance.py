"""Run the SDK acceptance suite against this connector, in-process.

The acceptance suite (``conduit.connector.testing.acceptance``) is the shared
contract a Conduit connector must pass — versioned via ``CONTRACT_VERSION`` so
your CI output states exactly which contract it passed. It exercises the
connector's ``Source``/``Destination`` through the same servicer adapters
``conduit.connector.serve`` uses, without a real gRPC socket or a real Conduit
binary.

Wire your own connector in by returning its config class and source/destination
classes from the driver below; the test methods come from
``AcceptanceTestSuite`` unchanged.
"""

from __future__ import annotations

from conduit.connector import Specification
from conduit.connector.testing.acceptance import (
    AcceptanceTestSuite,
    ConfigurableAcceptanceTestDriver,
)

from main import Config, GeneratorSource


class TestAcceptance(AcceptanceTestSuite):
    def driver(self) -> ConfigurableAcceptanceTestDriver:
        return ConfigurableAcceptanceTestDriver(
            spec=Specification(name="connectorname", version="0.1.0", author="you"),
            source_cls=GeneratorSource,
            source_cfg={"count": "10"},
        )
