"""Synchronous client for a private mobile-use endpoint."""

import ipaddress
import json
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class APIError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status = status
        self.code = code


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, origin: str, token: str):
        parsed = urlsplit(origin)
        host = parsed.hostname
        if not host or parsed.username or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
            raise ValueError("Use a bare mobile-use origin.")
        try:
            loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback = host == "localhost"
        if parsed.scheme != "https" and not (parsed.scheme == "http" and loopback):
            raise ValueError("Remote origins require HTTPS.")
        self._origin = origin.rstrip("/")
        self._token = token
        self._opener = build_opener(_NoRedirect)

    def devices(self) -> list[dict]:
        return self._request("GET", "/v1/devices")

    def start(self, device_id: str, purpose: str, capabilities: list[str]) -> dict:
        return self._request("POST", "/v1/sessions", {"device_id": device_id, "purpose": purpose, "capabilities": capabilities})

    def session(self, session_id: str) -> dict:
        return self._request("GET", self._session_path(session_id))

    def operate(self, session_id: str, operation: dict) -> dict:
        timeout = 24 if operation.get("kind") in ("app_install", "app_reset") else 15
        return self._request("POST", self._session_path(session_id) + "/operations", operation, timeout=timeout)

    def end(self, session_id: str) -> dict:
        return self._request("POST", self._session_path(session_id) + "/end")

    @staticmethod
    def _session_path(session_id: str) -> str:
        return "/v1/sessions/" + quote(session_id, safe="")

    def _request(self, method, path, value=None, *, timeout=15):
        headers = {"Authorization": f"Bearer {self._token}"}
        if value is not None:
            headers["Content-Type"] = "application/json"
        body = json.dumps(value).encode() if value is not None else None
        request = Request(self._origin + path, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=timeout) as response:
                return json.loads(response.read(13 << 20))
        except HTTPError as error:
            with error:
                try:
                    fault = json.loads(error.read(32 << 10)).get("error", {})
                except (ValueError, AttributeError):
                    fault = {}
            raise APIError(error.code, fault.get("code", "HTTP_ERROR"), fault.get("message", str(error))) from None
