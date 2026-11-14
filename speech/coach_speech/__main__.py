"""Arranque: python -m coach_speech (escucha en 127.0.0.1 salvo SPEECH_HOST explícito)."""

from __future__ import annotations

import os


def main() -> None:
    import uvicorn

    from .app import create_app

    uvicorn.run(
        create_app(),
        host=os.environ.get("SPEECH_HOST", "127.0.0.1"),
        port=int(os.environ.get("SPEECH_PORT", "8090")),
        timeout_keep_alive=5,
        log_level=os.environ.get("SPEECH_LOG_LEVEL", "info"),
        access_log=False,  # las URLs llevan el prompt: no se loguean
    )


if __name__ == "__main__":
    main()
