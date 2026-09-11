# Token compression layer (amux)

Mục tiêu: giảm **input** + **output** token qua proxy mà không phá agent tool loop (Claude / Cursor / Codex / Antigravity).

## Vị trí trong amux

```
Client IDE ──► amux proxy ──► [Context Builder] ──► Provider pool / Anthropic RP
                     │
                     ├─ Raw log     (~/.am/requests.log, usage)
                     ├─ Compress    (local small model OR rules)
                     └─ Retrieve    (optional embeddings later)
```

Layer nằm **sau** bridge parse (đã có tools canonical), **trước** `pool.Send` — để:

- không đụng wire format tool_use / tool_calls
- chỉ nén phần **text history**, không nén `role=tool` / `tool_use` args (tránh mất lệnh)

## 4 hướng giảm chi phí

| Hướng | Cách | Ước giảm | Rủi ro |
|---|---|---|---|
| Input compress | summary + structured memory + retrieval | 50–90% | context drift |
| Output control | prompt cap / strip filler | 30–80% | mất chi tiết |
| Mid-layer local | 3B–7B GGUF (Ollama/llama.cpp) | rẻ hơn Claude | CPU Mac cũ chậm |
| Token-level lossy | bullets, drop stopwords | 40–70% | mất sắc thái |

## Quy tắc an toàn (bắt buộc)

1. **Không nén** message `role=tool`, `tool_calls`, `tool_use` blocks.
2. **Giữ** system prompt gốc (hoặc summary có version hash).
3. **Giữ** N turn gần nhất raw (vd. 2–4); chỉ nén phần cũ hơn.
4. Structured memory JSON schema cố định:

```json
{
  "facts": [],
  "goals": "",
  "constraints": "",
  "history_summary": "",
  "pending_tasks": [],
  "files_touched": []
}
```

5. Mỗi compress gắn `source_hash` + `model` để debug drift.

## Phase triển khai đề xuất

**P0** — rule-based (không cần LLM local)

- Truncate long tool outputs (đã có truncate ở monitor)
- Collapse consecutive assistant/user prose > K chars → bullets
- Drop repeated system boilerplate

**P1** — structured memory file `~/.am/memory/<session>.json`

- Update sau mỗi turn (merge facts)
- Inject 1 block memory thay vì full history khi `FullContext` quá dài

**P2** — local compressor (Ollama)

- Endpoint nội bộ `amux compress` / env `AM_COMPRESS_URL`
- Model mặc định: `qwen2.5:3b` hoặc `llama3.2:3b` (Mac cũ)
- Chỉ gọi khi `estimateTokens(messages) > threshold`

**P3** — retrieval

- Embeddings local (nomic / mini) + top-k chunks
- Kết hợp summary + k snippets

## Output token

- Bridge có thể gắn `max_tokens` / system suffix “be concise” theo client dialect (opt-in flag).
- Không auto-cắt stream giữa `tool_use` (sẽ phá agent).

## Khó khăn chính

- Nén code/tables mà không mất logic
- Đồng bộ hiểu biết giữa Claude vs Codex vs Gemini
- Tránh drift: luôn giữ recent raw + memory checksum

## Không làm ngay

- Không chạy LLM lớn local trên proxy path mặc định
- Không thay thế Anthropic reverse-proxy tool path bằng compress (tools phải nguyên văn)
