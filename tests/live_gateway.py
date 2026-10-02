#!/usr/bin/env python3
# live_gateway.py — 實機回歸：對執行中的 litellm 跑 DLP 與分流情境。
# 不是離線測試：要先部署 litellm，由 task litellm:check 呼叫，不進 task test。
# 檔名刻意不以 test_ 開頭，python3 -m pytest tests/ 不會收進來，也就不會打到正式服務。
# key 從機密檔讀，不印出。每則印 [OK]／[FAIL]，全部跑完才決定退出碼，全部通過時是 0。
import base64
import json
import os
import struct
import sys
import urllib.error
import urllib.request
import zlib
from typing import Callable, NamedTuple

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.join(HERE, "..")
LITELLM_SECRET = os.path.join(ROOT, "services", "litellm", "secret", "secret.env")
OPENWEBUI_SECRET = os.path.join(ROOT, "services", "openwebui", "secret", "secret.env")

# 預設打主機本機的 litellm；LITELLM_URL 供指到別的位址，也用來驗連不上時會失敗
BASE_URL = os.environ.get("LITELLM_URL", "http://127.0.0.1:4000").rstrip("/")
# 地端模型與判斷模型都在同一張卡上，單則最長等 5 分鐘
TIMEOUT_SECONDS = 300
# 只看分流與 DLP 的結果，不需要完整回答
MAX_TOKENS = 16
CHAT = "/v1/chat/completions"
# 地端模型都經 vllm，model_list 裡的 model 都以這個開頭；其餘一律算雲端
LOCAL_PREFIX = "hosted_vllm/"
# 請求的去向
LOCAL, CLOUD, UNKNOWN = "local", "cloud", "unknown"

# 內建 guardrail 阻擋時的訊息開頭。custom_code 在送出前阻擋時回 200 加一則合成回答，內容就是這段訊息
BLOCK_MARK = "DLP："
# 撐爆 chat-local 上下文的填充文字，約 6 萬 7 千字；用來逼出 fallback
FILLER = "這是一段用來撐滿上下文長度的填充文字，內容沒有意義。" * 2600

# 「帶個資」指的就是這一段
PII = "幫我檢查這筆資料：王小明 身分證A123456789"
PII_FULLWIDTH = "幫我檢查：身分證Ａ１２３４５６７８９"
EASY = "早安！今天天氣不錯"
HARD = "請證明根號 2 是無理數，並寫一個二分法求根號 2 的 Python 程式"
# 困難題帶個資：分流會想送雲端，驗 DLP 在這條路上有沒有漏
HARD_PII = "我的身分證字號是A123456789，請推導二次方程式的公式解，並寫一個 Python 函式處理判別式小於零的複數根，附上三組測試"

# OpenWebUI v0.11.0 的 DEFAULT_RAG_TEMPLATE。知識庫聊天會把它接在使用者問題前面，問題在 </context> 之後
RAG_TEMPLATE = """### Task:
Respond to the user query using the provided context, incorporating inline citations in the format [id] **only when the <source> tag includes an explicit id attribute** (e.g., <source id="1">).

### Guidelines:
- If you don't know the answer, clearly state that.
- If uncertain, ask the user for clarification.
- Respond in the same language as the user's query.
- If the context is unreadable or of poor quality, inform the user and provide the best possible answer.
- If the answer isn't present in the context but you possess the knowledge, explain this to the user and provide the answer using your own understanding.
- **Only include inline citations using [id] (e.g., [1], [2]) when the <source> tag includes an id attribute.**
- Do not cite if the <source> tag does not contain an id attribute.
- Do not use XML tags in your response.
- Ensure citations are concise and directly related to the information provided.

### Example of Citation:
If the user asks about a specific topic and the information is found in a source with a provided id attribute, the response should include the citation like in the following example:
* "According to the study, the proposed method increases efficiency by 20% [1]."

### Output:
Provide a clear and direct response to the user's query, including inline citations in the format [id] only when the <source> tag with id attribute is present in the context.

<context>
{context}
</context>
"""
TRAVEL_RULES = (
    '<source id="1">國內出差膳雜費每日新台幣 480 元，當日往返且出差未滿四小時者不發給膳雜費。'
    "住宿費每日上限：甲級地區 2,400 元、乙級 2,000 元、丙級 1,600 元。"
    "國外出差依行政院公告的日支數額，以出發當日臺灣銀行美元賣出匯率換算新台幣。</source>"
)
RAG_EASY = RAG_TEMPLATE.format(context=TRAVEL_RULES) + "\n膳雜費每日多少？"
RAG_HARD = RAG_TEMPLATE.format(context=TRAVEL_RULES) + (
    "\n依據這份規定，寫一個 Python 函式：輸入出差地點與天數，算出住宿與膳雜費的報支上限，"
    "國外要換算成新台幣並處理甲乙丙三級，附三組測試"
)


class Result(NamedTuple):
    status: int
    model_name: str | None  # 回應標頭 x-litellm-model-name：實際送到的後端模型。分流時別名標頭寫 chat-auto，看不出去向
    error: str | None
    text: str  # 回答的文字；串流時是全部片段接起來。guardrail 的合成回答也在這裡


class Case(NamedTuple):
    name: str
    key: str  # master 是 master key，web 是網頁聊天用的 key
    path: str
    body: dict
    check: Callable[[Result], str | None]


def png_data_url() -> str:
    """產生一張 16×16 的灰色 PNG，給「含圖片」的情境用；不依賴外部檔案。"""
    size = 16
    raw = b"".join(b"\x00" + b"\x80\x80\x80" * size for _ in range(size))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
    return "data:image/png;base64," + base64.b64encode(png).decode()


def read_secret(path: str, name: str) -> str | None:
    """讀 KEY=value 形式的機密檔；檔案或鍵不存在回 None。值不印出。"""
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                if line.startswith(name + "="):
                    return line.split("=", 1)[1].strip()
    except FileNotFoundError:
        return None
    return None


def chat_body(model: str, content, **extra) -> dict:
    """組 chat completions 的請求本文。"""
    body = {"model": model, "max_tokens": MAX_TOKENS, "messages": [{"role": "user", "content": content}]}
    body.update(extra)
    return body


def call(key: str, path: str, body: dict) -> Result:
    """送出一則請求。HTTP 錯誤不丟例外，轉成 Result；連線失敗才丟例外。"""
    req = urllib.request.Request(
        BASE_URL + path,
        data=json.dumps(body).encode(),
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_SECONDS) as resp:
            raw = resp.read().decode(errors="replace")
            return Result(resp.status, resp.headers.get("x-litellm-model-name"), None, answer_text(raw, bool(body.get("stream"))))
    except urllib.error.HTTPError as e:
        return Result(e.code, None, error_message(e.read()), "")


def answer_text(raw: str, stream: bool) -> str:
    """取出回答文字：chat 的 message.content、串流的每段 delta、responses 的 output 文字。"""
    if stream:
        parts = []
        for line in raw.splitlines():
            if line.startswith("data:") and line != "data: [DONE]":
                try:
                    chunk = json.loads(line[len("data:"):])
                except ValueError:
                    continue
                parts.extend((c.get("delta") or {}).get("content") or "" for c in chunk.get("choices") or [])
        return "".join(parts)
    try:
        data = json.loads(raw)
    except ValueError:
        return ""
    if data.get("choices"):
        return (data["choices"][0].get("message") or {}).get("content") or ""
    for item in data.get("output") or []:
        for part in item.get("content") or []:
            if part.get("text"):
                return part["text"]
    return ""


def error_message(raw: bytes) -> str:
    try:
        err = json.loads(raw).get("error")
    except ValueError:
        return raw.decode(errors="replace")[:200]
    if isinstance(err, dict):
        return str(err.get("message", err))
    return str(err)


def expect_block(mark: str) -> Callable[[Result], str | None]:
    """阻擋：custom_code 在送出前阻擋時回 200 加一則合成回答，回答以 mark 開頭。"""

    def check(result: Result) -> str | None:
        if result.status == 200 and result.text.startswith(mark):
            return None
        return f"預期阻擋（訊息開頭「{mark}」），實際 HTTP {result.status}：{(result.error or result.text)[:60]}"

    return check


def destination(model_name: str | None) -> str:
    """後端模型名稱換成去向：LOCAL 或 CLOUD。沒有標頭時回 UNKNOWN。"""
    if not model_name:
        return UNKNOWN
    return LOCAL if model_name.startswith(LOCAL_PREFIX) else CLOUD


def expect_pass(expected: str) -> Callable[[Result], str | None]:
    """放行：比對狀態碼與去向。expected 是 LOCAL 或 CLOUD。"""

    def check(result: Result) -> str | None:
        if result.status != 200:
            return f"預期放行，實際 HTTP {result.status}：{(result.error or '')[:60]}"
        if destination(result.model_name) != expected:
            return f"預期送到 {expected}，實際送到 {result.model_name}"
        return None

    return check


def expect_not_cloud(result: Result) -> str | None:
    """沒有送到雲端：出錯或送地端都算，只有 200 且去向是雲端才失敗。"""
    if result.status == 200 and destination(result.model_name) == CLOUD:
        return f"送到了雲端：{result.model_name}"
    return None


def cases(image: str) -> list[Case]:
    """D1～D12 與 R1～R7 照抄工單 01 的表，D13～D18 與 R8 是工單 03 加的探測。

    工單不進版控，只存本機的 docs/tickets/2026-09-19-LiteLLM-內建功能取代自寫-DLP-與分流/。
    """
    hard_with_image = [{"type": "text", "text": HARD}, {"type": "image_url", "image_url": {"url": image}}]
    block = expect_block(BLOCK_MARK)
    return [
        Case("D1 master key 帶個資送 chat-cloud", "master", CHAT, chat_body("chat-cloud", PII), block),
        Case("D2 送 chat-local，但夾帶雲端 fallback", "master", CHAT,
             chat_body("chat-local", PII, fallbacks=["chat-cloud"]), expect_pass(LOCAL)),
        Case("D3 送 chat-local，不帶 fallback", "master", CHAT, chat_body("chat-local", PII), expect_pass(LOCAL)),
        Case("D4 master key 選 chat-auto 帶個資", "master", CHAT, chat_body("chat-auto", PII), expect_pass(LOCAL)),
        Case("D5 全形身分證送 chat-cloud", "master", CHAT, chat_body("chat-cloud", PII_FULLWIDTH), block),
        Case("D6 公司關鍵字「德安機密」送 chat-cloud", "master", CHAT,
             chat_body("chat-cloud", "這是德安機密專案的進度"), block),
        Case("D7 /v1/responses 的 instructions 帶手機號碼", "master", "/v1/responses",
             {"model": "chat-cloud", "instructions": "使用者手機 0912345678", "input": "用一句話介紹台北"}, block),
        Case("D8 embedding 帶個資", "master", "/v1/embeddings", {"model": "embed", "input": [PII]}, expect_pass(LOCAL)),
        Case("D9 網頁 key 選 chat-auto 帶個資", "web", CHAT, chat_body("chat-auto", PII), expect_pass(LOCAL)),
        Case("D10 不含個資送 chat-cloud", "master", CHAT, chat_body("chat-cloud", "用一句話介紹台北"),
             expect_pass(CLOUD)),
        Case("D11 同 D9，但用串流", "web", CHAT, chat_body("chat-auto", PII, stream=True), expect_pass(LOCAL)),
        Case("D12 網頁 key 選 chat-auto 問困難題並帶個資", "web", CHAT, chat_body("chat-auto", HARD_PII),
             expect_pass(LOCAL)),
        Case("D13 送 chat-local 夾帶雲端 fallback，提示詞超過地端上下文", "master", CHAT,
             chat_body("chat-local", PII + "\n" + FILLER, fallbacks=["chat-cloud"]), expect_not_cloud),
        Case("D14 統一編號檢查碼對，送 chat-cloud", "master", CHAT, chat_body("chat-cloud", "公司統編 12345675"), block),
        Case("D15 統一編號檢查碼不對，送 chat-cloud", "master", CHAT, chat_body("chat-cloud", "統編 12345678"),
             expect_pass(CLOUD)),
        Case("D16 身分證檢查碼不對，送 chat-cloud", "master", CHAT, chat_body("chat-cloud", "訂單編號A123456780 已出貨"),
             expect_pass(CLOUD)),
        Case("D17 email 送 chat-cloud", "master", CHAT, chat_body("chat-cloud", "請寄到 dev01@example.com"), block),
        Case("D18 直接指定 chat-cloud 帶圖片", "master", CHAT, chat_body("chat-cloud", hard_with_image), block),
        Case("R1 chat-auto 簡單題", "master", CHAT, chat_body("chat-auto", EASY), expect_pass(LOCAL)),
        Case("R2 chat-auto 困難題", "master", CHAT, chat_body("chat-auto", HARD), expect_pass(CLOUD)),
        Case("R3 chat-auto 含圖片的困難題", "master", CHAT, chat_body("chat-auto", hard_with_image),
             expect_pass(LOCAL)),
        Case("R4 直接指定 chat-cloud", "master", CHAT, chat_body("chat-cloud", EASY), expect_pass(CLOUD)),
        Case("R5 直接指定 chat-local 問困難題", "master", CHAT, chat_body("chat-local", HARD), expect_pass(LOCAL)),
        Case("R6 知識庫的簡單題", "master", CHAT, chat_body("chat-auto", RAG_EASY), expect_pass(LOCAL)),
        Case("R7 知識庫的困難題", "master", CHAT, chat_body("chat-auto", RAG_HARD), expect_pass(CLOUD)),
        Case("R8 送 chat-local 帶圖片、夾帶 chat-auto fallback，提示詞超過地端上下文", "master", CHAT,
             chat_body("chat-local", [{"type": "text", "text": HARD + "\n" + FILLER}, {"type": "image_url", "image_url": {"url": image}}],
                       fallbacks=["chat-auto"]),
             expect_not_cloud),
    ]


def reachable() -> str | None:
    """連得上回 None，連不上回原因。"""
    try:
        with urllib.request.urlopen(BASE_URL + "/health/liveliness", timeout=10):
            return None
    except (urllib.error.URLError, OSError) as e:
        return str(getattr(e, "reason", e))


def main() -> int:
    keys = {
        "master": read_secret(LITELLM_SECRET, "LITELLM_MASTER_KEY"),
        # OPENAI_API_KEYS 可以用分號隔開多把，網頁聊天用第一把
        "web": (read_secret(OPENWEBUI_SECRET, "OPENAI_API_KEYS") or "").split(";")[0] or None,
    }
    for kind, path in (("master", LITELLM_SECRET), ("web", OPENWEBUI_SECRET)):
        if not keys[kind]:
            print(f"[FAIL] 讀不到 {kind} key：{path}", file=sys.stderr)
            return 1
    problem = reachable()
    if problem:
        print(f"[FAIL] 連不上 {BASE_URL}：{problem}", file=sys.stderr)
        return 1
    table = cases(png_data_url())
    failed = 0
    for case in table:
        try:
            reason = case.check(call(keys[case.key], case.path, case.body))
        except (urllib.error.URLError, OSError) as e:
            reason = f"請求失敗：{getattr(e, 'reason', e)}"
        if reason:
            failed += 1
            print(f"[FAIL] {case.name}：{reason}", file=sys.stderr)
        else:
            print(f"[OK] {case.name}")
    if failed:
        print(f"[FAIL] {failed}/{len(table)} 則沒過", file=sys.stderr)
        return 1
    print(f"ALL {len(table)} PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
