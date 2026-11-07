#!/usr/bin/env python3
"""FOFA MCP server — 多 key 池 + 失败轮询。

为什么是独立进程：ARTEX 通过 MCP stdio 调用外部工具，进程隔离意味着某个
key 被限流/封停时不会把错误状态带进 agent 进程；池的健康度也由本进程独立维护。

key 池语义（关键设计，不是简单的 for 循环重试）：
  - 轮询起点按 round-robin 前进，避免所有请求都先打第一个 key（那是限流最快的方式）。
  - 每个 key 有独立的熔断状态：连续失败到阈值后进入冷却期，冷却期内直接跳过。
  - 账号类错误（余额不足/无权限/过期）立即长冷却；网络类错误用指数退避。
  - 一次请求内最多尝试 N 个 key；全部失败才向调用方报错，并说明每个 key 的原因。

配置通过环境变量提供：
  FOFA_KEYS           逗号分隔的 key 列表（必填）；也接受 FOFA_KEY / FOFA_API_KEY 单 key
  FOFA_BASE_URL       默认 https://fofa.info
  FOFA_POOL_COOLDOWN  账号类错误的冷却秒数（默认 1800）
  FOFA_NET_COOLDOWN   网络类错误的首次冷却秒数（默认 30，指数增长）
  FOFA_MAX_ATTEMPTS   单次请求最多尝试的 key 数（默认 3）

运行：python3 fofa_mcp.py            # stdio MCP server
      python3 fofa_mcp.py --selftest # 本地自检，不进入 MCP 循环
"""

from __future__ import annotations

import base64
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

SERVER_NAME = "fofa"
PROTOCOL_VERSION = "2025-06-18"

# 账号类错误：这个 key 本身不可用，重试没有意义，直接长冷却。
ACCOUNT_ERROR_MARKERS = (
    "820000", "820001", "-700", "账号无效", "账号已过期", "账号过期",
    "无效的fofa", "无效的 fofa", "f点不足", "f币不足", "余额不足", "配额",
    "权限不足", "没有权限", "会员", "account invalid", "invalid key",
    "expired", "insufficient", "quota", "permission", "unauthorized", "forbidden",
)


class KeyState:
    """一个 key 的健康状态。所有字段由 pool 在锁内读写。"""

    __slots__ = ("key", "fails", "cooldown_until", "last_error", "ok_count", "fail_count")

    def __init__(self, key: str):
        self.key = key
        self.fails = 0                 # 连续失败次数；成功即清零
        self.cooldown_until = 0.0      # unix 时间；在此之前跳过
        self.last_error = ""
        self.ok_count = 0
        self.fail_count = 0

    def masked(self) -> str:
        k = self.key
        return k[:4] + "***" + k[-4:] if len(k) > 8 else "***"

    def open(self, now: float) -> bool:
        return now >= self.cooldown_until


class KeyPool:
    """多 key 轮询池。

    线程安全：MCP server 可能并发处理 tools/call，池状态必须在锁内更新，
    否则两个请求会同时把同一个 key 判成可用。
    """

    def __init__(self, keys: list[str], account_cooldown: float, net_cooldown: float, max_attempts: int):
        self.keys = [KeyState(k) for k in keys]
        self.account_cooldown = account_cooldown
        self.net_cooldown = net_cooldown
        self.max_attempts = max(1, max_attempts)
        self.mu = threading.Lock()
        self.cursor = 0

    def order(self) -> list[KeyState]:
        """返回本轮尝试顺序：从 cursor 开始轮转，跳过冷却中的 key。"""
        with self.mu:
            n = len(self.keys)
            if n == 0:
                return []
            start = self.cursor % n
            self.cursor = (self.cursor + 1) % n
            rotated = self.keys[start:] + self.keys[:start]
            now = time.time()
            usable = [s for s in rotated if s.open(now)]
            # 全部冷却中时仍按轮转顺序尝试，否则一次全冷却会让服务彻底不可用；
            # 让调用方看到真实错误比静默失败好。
            return usable if usable else rotated

    def record_ok(self, st: KeyState) -> None:
        with self.mu:
            st.fails = 0
            st.ok_count += 1

    def record_fail(self, st: KeyState, account: bool, err: str) -> None:
        with self.mu:
            st.fails += 1
            st.fail_count += 1
            st.last_error = err[:300]
            if account:
                wait = self.account_cooldown
            else:
                # 网络类：指数退避，封顶 8 倍基础值。
                wait = min(self.net_cooldown * (2 ** (st.fails - 1)), self.net_cooldown * 8)
            st.cooldown_until = time.time() + wait

    def snapshot(self) -> list[dict]:
        with self.mu:
            now = time.time()
            return [
                {
                    "key": s.masked(),
                    "ok": s.ok_count,
                    "fail": s.fail_count,
                    "consecutive_fails": s.fails,
                    "cooldown_seconds": max(0, int(s.cooldown_until - now)),
                    "last_error": s.last_error,
                }
                for s in self.keys
            ]


def _is_account_error(msg: str) -> bool:
    text = str(msg or "").lower()
    return any(m in text for m in ACCOUNT_ERROR_MARKERS)


class FofaClient:
    def __init__(self, pool: KeyPool, base_url: str, timeout: float = 30.0):
        self.pool = pool
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def _call(self, key: str, params: dict) -> dict:
        q = urllib.parse.urlencode({**params, "key": key})
        url = f"{self.base_url}/api/v1/search/all?{q}"
        req = urllib.request.Request(url, headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            raw = resp.read()
        try:
            return json.loads(raw)
        except Exception:
            raise RuntimeError(f"FOFA 返回非 JSON: {raw[:200]!r}")

    def search(self, query: str, page: int, size: int, fields: str) -> dict:
        """按 key 池顺序尝试；成功即返回，并附带本次实际使用的 key（脱敏）。"""
        qb64 = base64.b64encode(query.encode("utf-8")).decode("ascii")
        params = {
            "qbase64": qb64,
            "fields": fields,
            "page": str(page),
            "size": str(size),
            "full": "false",
        }
        attempts: list[dict] = []
        order = self.pool.order()
        if not order:
            raise RuntimeError("FOFA key 池为空：请设置 FOFA_KEYS")

        for st in order[: self.pool.max_attempts]:
            try:
                data = self._call(st.key, params)
            except urllib.error.HTTPError as e:
                body = ""
                try:
                    body = e.read()[:300].decode("utf-8", "replace")
                except Exception:
                    pass
                msg = f"HTTP {e.code}: {body}"
                account = e.code in (401, 403) or _is_account_error(msg)
                self.pool.record_fail(st, account, msg)
                attempts.append({"key": st.masked(), "error": msg})
                continue
            except Exception as e:
                msg = f"{type(e).__name__}: {e}"
                self.pool.record_fail(st, False, msg)
                attempts.append({"key": st.masked(), "error": msg})
                continue

            # FOFA 用 {"error": true, "errmsg": "..."} 表达业务错误
            if data.get("error"):
                msg = str(data.get("errmsg") or data)
                account = _is_account_error(msg)
                self.pool.record_fail(st, account, msg)
                attempts.append({"key": st.masked(), "error": msg})
                continue

            self.pool.record_ok(st)
            return {
                "engine": "fofa",
                "key": st.masked(),
                "page": page,
                "size": data.get("size", 0),
                "results": data.get("results", []),
                "fields": fields.split(","),
                "attempts": attempts,
            }

        raise RuntimeError("FOFA 全部 key 均失败：" + "; ".join(f"{a['key']}={a['error']}" for a in attempts))


# --- MCP 协议层 ---

def _schema(**props) -> dict:
    return {"type": "object", "properties": props, "required": []}


TOOLS = [
    {
        "name": "fofa_search",
        "description": (
            "FOFA 网络空间测绘检索（多 key 池，失败自动切换下一个 key）。"
            "query 用 FOFA 原生语法，如 domain=\"example.com\" && port=\"443\"。"
            "返回 {size, results, fields, key, attempts}：results 是二维数组，列顺序见 fields。"
            "分页用 page 从 1 开始；size 上限 100。仅做只读测绘，不接触目标。"
        ),
        "inputSchema": _schema(
            query={"type": "string", "description": "FOFA 查询语法（原文，不要 base64）"},
            page={"type": "integer", "description": "页码，从 1 开始，默认 1"},
            size={"type": "integer", "description": "每页条数，默认 20，上限 100"},
            fields={"type": "string", "description": "返回字段，逗号分隔；默认 host,ip,port,title,domain,org"},
        ),
    },
    {
        "name": "fofa_key_status",
        "description": (
            "查看 FOFA key 池的健康状态：每个 key 的成功/失败次数、连续失败数、"
            "剩余冷却秒数与最近一次错误。用于判断是限流、余额不足还是网络问题。不发起检索。"
        ),
        "inputSchema": _schema(),
    },
]


def _tool_result(payload: object, is_error: bool = False) -> dict:
    return {
        "content": [{"type": "text", "text": json.dumps(payload, ensure_ascii=False, indent=2)}],
        "isError": is_error,
    }


def handle_tool_call(name: str, args: dict, client: FofaClient) -> dict:
    if name == "fofa_key_status":
        return _tool_result({"pool": client.pool.snapshot()})

    if name != "fofa_search":
        return _tool_result({"error": f"未知工具 {name}"}, is_error=True)

    query = str(args.get("query") or "").strip()
    if not query:
        return _tool_result({"error": "query 必填（FOFA 原生语法）"}, is_error=True)
    try:
        page = max(1, int(args.get("page") or 1))
        size = int(args.get("size") or 20)
    except (TypeError, ValueError):
        return _tool_result({"error": "page/size 必须是整数"}, is_error=True)
    size = max(1, min(size, 100))
    fields = str(args.get("fields") or "host,ip,port,title,domain,org")
    try:
        return _tool_result(client.search(query, page, size, fields))
    except Exception as e:
        return _tool_result({"error": str(e)}, is_error=True)


def serve(client: FofaClient) -> None:
    """stdio JSON-RPC 循环：一行一条消息（MCP stdio 传输约定）。"""
    out = sys.stdout
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        mid = msg.get("id")
        method = msg.get("method", "")

        # 通知（无 id）不需要回包
        if mid is None:
            continue

        if method == "initialize":
            result = {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {"tools": {}},
                "serverInfo": {"name": SERVER_NAME, "version": "1.0.0"},
            }
        elif method == "tools/list":
            result = {"tools": TOOLS}
        elif method == "tools/call":
            params = msg.get("params") or {}
            result = handle_tool_call(str(params.get("name", "")), params.get("arguments") or {}, client)
        elif method == "ping":
            result = {}
        else:
            out.write(json.dumps({
                "jsonrpc": "2.0", "id": mid,
                "error": {"code": -32601, "message": f"method not found: {method}"},
            }, ensure_ascii=False) + "\n")
            out.flush()
            continue

        out.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": result}, ensure_ascii=False) + "\n")
        out.flush()


def build_client() -> FofaClient:
    raw = ",".join(filter(None, [
        os.environ.get("FOFA_KEYS", ""),
        os.environ.get("FOFA_KEY", ""),
        os.environ.get("FOFA_API_KEY", ""),
    ]))
    keys = [k.strip() for k in raw.split(",") if k.strip()]
    pool = KeyPool(
        keys,
        account_cooldown=float(os.environ.get("FOFA_POOL_COOLDOWN", "1800")),
        net_cooldown=float(os.environ.get("FOFA_NET_COOLDOWN", "30")),
        max_attempts=int(os.environ.get("FOFA_MAX_ATTEMPTS", "3")),
    )
    base = os.environ.get("FOFA_BASE_URL", "https://fofa.info")
    return FofaClient(pool, base)


def main() -> int:
    client = build_client()
    if "--selftest" in sys.argv:
        # 自检只验证池与协议装配，不消耗 FOFA 配额。
        print("keys:", len(client.pool.keys), [s.masked() for s in client.pool.keys])
        print("order:", [s.masked() for s in client.pool.order()][:3])
        print("tools:", [t["name"] for t in TOOLS])
        r = handle_tool_call("fofa_key_status", {}, client)
        print("status tool ok:", not r.get("isError"))
        return 0
    if not client.pool.keys:
        sys.stderr.write("FOFA MCP: FOFA_KEYS 未配置，工具会返回明确错误而不是崩溃\n")
    serve(client)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
