"""Límite de tamaño del cuerpo HTTP a nivel ASGI.

Corta antes de que el parser multipart guarde el archivo completo, tanto con
Content-Length como con transferencia chunked.
"""

from __future__ import annotations

import json
import re
import uuid
from collections.abc import Callable

_RID = re.compile(rb"^[A-Za-z0-9-]{8,64}$")


class _TooLarge(Exception):
    pass


class BodySizeLimitMiddleware:
    def __init__(self, app, max_bytes: int, on_reject: Callable[[], None] | None = None):
        self.app, self.max_bytes, self.on_reject = app, max_bytes, on_reject

    async def _reject(self, scope, send) -> None:
        if self.on_reject:
            self.on_reject()
        rid = dict(scope.get("headers", [])).get(b"x-request-id", b"")
        rid = rid if _RID.match(rid) else str(uuid.uuid4()).encode()
        body = json.dumps(
            {
                "type": "urn:cx9:problem:payload-too-large",
                "title": "Payload Too Large",
                "status": 413,
                "code": "PAYLOAD_TOO_LARGE",
                "detail": f"cuerpo supera {self.max_bytes} bytes",
                "retryable": False,
                "instance": scope.get("path", ""),
                "request_id": rid.decode(),
            }
        ).encode()
        await send(
            {
                "type": "http.response.start",
                "status": 413,
                "headers": [
                    (b"content-type", b"application/problem+json"),
                    (b"content-length", str(len(body)).encode()),
                    (b"connection", b"close"),
                    (b"x-request-id", rid),
                ],
            }
        )
        await send({"type": "http.response.body", "body": body})

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        for k, v in scope.get("headers", []):
            if k == b"content-length":
                try:
                    if int(v) > self.max_bytes:
                        return await self._reject(scope, send)
                except ValueError:
                    return await self._reject(scope, send)
        seen = 0
        exceeded = False
        replaced = False

        async def limited_receive():
            nonlocal seen, exceeded
            msg = await receive()
            if msg["type"] == "http.request":
                seen += len(msg.get("body", b""))
                if seen > self.max_bytes:
                    exceeded = True
                    raise _TooLarge
            return msg

        async def guarded_send(msg):
            # Algunos parsers (p. ej. el de JSON de FastAPI) atrapan la excepción y
            # responden 400: si el límite se superó, se reemplaza por 413.
            nonlocal replaced
            if exceeded:
                if msg["type"] == "http.response.start" and not replaced:
                    replaced = True
                    await self._reject(scope, send)
                return
            await send(msg)

        try:
            await self.app(scope, limited_receive, guarded_send)
        except _TooLarge:
            if not replaced:
                replaced = True
                await self._reject(scope, send)
