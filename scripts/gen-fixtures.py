#!/usr/bin/env python3
"""Writes the generated fixtures: an application log and an API response.

The test-runner fixtures (go test, pytest, JUnit) are real output from the
sample projects in test/fixtures/src; see scripts/record-fixtures.sh. These
two are generated because a realistic log and API response need volume and
variety a sample project can't give. A fixed seed makes them the same on
every run.

    python3 scripts/gen-fixtures.py
"""
import json
import os
import random

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "..", "test", "fixtures")

JAVA_TRACE = """\tat com.example.orders.PaymentClient.charge(PaymentClient.java:{a})
\tat com.example.orders.CheckoutService.complete(CheckoutService.java:{b})
\tat com.example.orders.CheckoutController.post(CheckoutController.java:88)
\tat java.base/java.lang.Thread.run(Thread.java:1583)
Caused by: java.net.SocketTimeoutException: Read timed out
\tat java.base/sun.nio.ch.NioSocketImpl.timedRead(NioSocketImpl.java:278)
\t... 4 more"""

PY_TRACE = """Traceback (most recent call last):
  File "/srv/app/workers/thumbs.py", line 41, in run
    img = load(path)
  File "/srv/app/workers/thumbs.py", line 17, in load
    return Image.open(path)
FileNotFoundError: [Errno 2] No such file or directory: '/data/uploads/{n}.jpg'"""

GO_PANIC = """panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x6b4c2e]

goroutine 412 [running]:
example.com/ingest/queue.(*Batch).Flush(0x0)
\t/src/ingest/queue/batch.go:88 +0x2e
example.com/ingest/queue.(*Worker).loop(0xc0001a2000)
\t/src/ingest/queue/worker.go:131 +0x1c5
created by example.com/ingest/queue.Start in goroutine 1
\t/src/ingest/queue/worker.go:52 +0x9d"""


def ts(i):
    s = 8 * 3600 + i // 4
    return "2026-10-18T%02d:%02d:%02d.%03dZ" % (s // 3600, s // 60 % 60, s % 60, (i * 37) % 1000)


def log(rnd):
    lines = []
    paths = ["/api/orders", "/api/orders/{id}", "/api/cart", "/api/users/{id}", "/health"]
    for i in range(60000):
        t = ts(i)
        r = rnd.random()
        rid = "%08x" % rnd.getrandbits(32)
        if r < 0.55:
            p = rnd.choice(paths).replace("{id}", str(rnd.randint(1, 99999)))
            lines.append(f"{t} INFO  http request_id={rid} method=GET path={p} status=200 duration_ms={rnd.randint(2, 180)}")
        elif r < 0.80:
            lines.append(f"{t} DEBUG cache hit key=user:{rnd.randint(1, 99999)} ttl={rnd.randint(1, 600)}s")
        elif r < 0.93:
            lines.append(f"{t} INFO  queue depth={rnd.randint(0, 40)} workers=8")
        elif r < 0.985:
            w = rnd.random()
            if w < 0.6:
                lines.append(f"{t} WARN  config key \"timeout\" is deprecated; use \"read_timeout\"")
            elif w < 0.9:
                lines.append(f"{t} WARN  slow query took {rnd.randint(500, 4000)}ms: SELECT * FROM orders WHERE user_id = {rnd.randint(1, 99999)}")
            else:
                lines.append(f"{t} WARN  retrying payment request_id={rid} attempt={rnd.randint(2, 4)}")
        elif r < 0.993:
            lines.append(f"{t} ERROR db: connection to 10.0.4.{rnd.randint(2, 9)}:5432 refused (request_id={rid})")
        elif r < 0.997:
            lines.append(f"{t} ERROR checkout failed for order {rnd.randint(10000, 99999)}")
            lines.append("java.lang.IllegalStateException: payment gateway did not answer")
            lines.extend(JAVA_TRACE.format(a=rnd.choice([112, 112, 118]), b=64).splitlines())
        elif r < 0.9995:
            lines.append(f"{t} ERROR thumbnail worker crashed")
            lines.extend(PY_TRACE.format(n=rnd.randint(1000, 9999)).splitlines())
        else:
            lines.extend(GO_PANIC.splitlines())
    return "\n".join(lines) + "\n"


def api(rnd):
    items = []
    for i in range(2500):
        it = {
            "id": 100000 + i,
            "sku": "SKU-%05d" % rnd.randint(0, 99999),
            "name": rnd.choice(["Desk lamp", "Notebook", "USB-C cable", "Monitor arm", "Pen set"]),
            "price": {"amount": round(rnd.uniform(3, 300), 2), "currency": "EUR"},
            "tags": rnd.sample(["office", "sale", "new", "eco", "bundle"], 2),
            "stock": rnd.randint(0, 500),
            "updated_at": ts(i * 13),
        }
        if i in (311, 1407, 2210):
            it["error"] = {"code": "PRICE_STALE", "message": "price feed for %s is older than 24h" % it["sku"]}
        items.append(it)
    doc = {
        "request_id": "4f9c2b1e-8d7a-4e21-9b3c-5a6d7e8f9012",
        "status": "partial",
        "page": {"number": 1, "size": 2500, "total": 9812},
        "items": items,
        "warnings": ["3 items have stale prices"],
        "description": "Catalogue export for the spring range. " * 12,
    }
    return json.dumps(doc, indent=2) + "\n"


def main():
    rnd = random.Random(20261018)
    with open(os.path.join(OUT, "app.log"), "w") as f:
        f.write(log(rnd))
    with open(os.path.join(OUT, "api-response.json"), "w") as f:
        f.write(api(rnd))


if __name__ == "__main__":
    main()
