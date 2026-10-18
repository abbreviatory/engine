"""A pytest sample for Abbreviatory's fixtures. Some tests fail on purpose."""
import json
import warnings

import pytest


def parse_price(s):
    amount, currency = s.split(" ")
    return round(float(amount), 2), currency


@pytest.mark.parametrize("i", range(900))
def test_parse_price_table(i):
    assert parse_price(f"{i}.50 EUR") == (i + 0.5, "EUR")


def test_parse_price_comma():
    # Deliberate failure: a decimal comma.
    assert parse_price("12,50 EUR") == (12.5, "EUR")


def test_parse_price_no_currency():
    # Deliberate failure: no currency.
    assert parse_price("12.50") == (12.5, "")


class TestRounding:
    def test_half_up(self):
        # Deliberate failure: float rounding.
        assert parse_price("2.675 USD") == (2.68, "USD")

    def test_whole(self):
        assert parse_price("3 USD") == (3.0, "USD")


@pytest.mark.parametrize("raw", ['{"a": 1}', '{"a": 2,}', "{'a': 3}"])
def test_json_inputs(raw):
    # Two of the three fail on purpose.
    assert json.loads(raw)["a"] > 0


@pytest.fixture
def broken_db():
    raise ConnectionError("database at 10.0.0.12:5432 refused the connection")


def test_needs_db(broken_db):
    assert broken_db


@pytest.mark.skip(reason="needs a network")
def test_remote_rates():
    pass


def test_deprecated_api():
    warnings.warn("parse_price will require a locale in 2.0", DeprecationWarning)
    assert parse_price("1 EUR") == (1.0, "EUR")
