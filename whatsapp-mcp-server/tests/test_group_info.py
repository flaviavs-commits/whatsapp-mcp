import requests

import main
import whatsapp

GROUP = "120363000000000001@g.us"
GROUP_PAYLOAD = {
    "jid": GROUP,
    "name": "Clientes VIP",
    "participant_count": 57,
    "owner": {"jid": "5531977776666@s.whatsapp.net", "phone": "5531977776666", "in_group": True},
    "admins": [{"jid": "5511988887777@s.whatsapp.net", "phone": "5511988887777", "is_admin": True}],
}


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text=""):
        self.status_code = status_code
        self._payload = payload
        self.text = text

    def json(self):
        if self._payload is None:
            raise ValueError("no JSON")
        return self._payload


def _fake_post(monkeypatch, calls, response):
    def fake_post(url, json, headers=None, timeout=None):
        calls.append({"url": url, "json": json, "headers": headers, "timeout": timeout})
        if isinstance(response, Exception):
            raise response
        return response

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)


def test_get_group_info_asks_the_bridge(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    _fake_post(monkeypatch, calls, DummyResponse(200, {"success": True, "group": GROUP_PAYLOAD}))

    result = whatsapp.get_group_info(GROUP)

    assert result == {"success": True, "group": GROUP_PAYLOAD}
    assert len(calls) == 1
    assert calls[0]["url"].endswith("/group-info")
    assert calls[0]["json"] == {"chat_jid": GROUP}
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}
    assert calls[0]["timeout"]  # WhatsApp can be slow, but never wait forever


def test_get_group_info_rejects_non_group_jid_without_calling_bridge(monkeypatch):
    calls = []
    _fake_post(monkeypatch, calls, DummyResponse(200, {"success": True, "group": GROUP_PAYLOAD}))

    result = whatsapp.get_group_info("5531977776666@s.whatsapp.net")

    assert result["success"] is False
    assert "@g.us" in result["message"]
    assert calls == []


def test_get_group_info_passes_bridge_error_message(monkeypatch):
    calls = []
    message = "This number is not in that group (or the group no longer exists)"
    _fake_post(monkeypatch, calls, DummyResponse(403, {"success": False, "message": message}))

    assert whatsapp.get_group_info(GROUP) == {"success": False, "message": message}


def test_get_group_info_explains_old_bridge(monkeypatch):
    calls = []
    _fake_post(monkeypatch, calls, DummyResponse(404, None, "404 page not found"))

    result = whatsapp.get_group_info(GROUP)

    assert result["success"] is False
    assert "update the bridge" in result["message"]


def test_get_group_info_reports_unreachable_bridge(monkeypatch):
    calls = []
    _fake_post(monkeypatch, calls, requests.ConnectionError("refused"))

    result = whatsapp.get_group_info(GROUP)

    assert result["success"] is False
    assert "refused" in result["message"]


def test_get_group_info_tool_returns_bridge_answer(monkeypatch):
    seen = []
    monkeypatch.setattr(
        main,
        "whatsapp_get_group_info",
        lambda chat_jid: seen.append(chat_jid) or {"success": True, "group": GROUP_PAYLOAD},
    )

    assert main.get_group_info(GROUP) == {"success": True, "group": GROUP_PAYLOAD}
    assert seen == [GROUP]
