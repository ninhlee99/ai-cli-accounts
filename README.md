<p align="center">
  <img src="assets/logo.svg" alt="amux logo" width="160" />
</p>

<h1 align="center">amux (<code>am</code>)</h1>

<p align="center">
  <b>AI Account Multiplexer & Local Failover Gateway</b><br>
  Tự động xoay vòng tài khoản Claude Code chống Rate Limit & Cổng AI Gateway chuẩn OpenAI / Anthropic / Gemini đa nhà cung cấp.
</p>

<p align="center">
  <a href="#-tính-năng-cốt-lõi">Tính Năng</a> •
  <a href="#-cài-đặt-nhanh">Cài Đặt</a> •
  <a href="#-danh-sách-lệnh-cli-amux--am">Lệnh CLI</a> •
  <a href="#-local-ai-gateway-http1270018787">AI Gateway</a> •
  <a href="#-luồng-claude-code--proxy--tools">Claude Code & Tools</a> •
  <a href="#-antigravity--agy-google-genai">Antigravity (AGY)</a> •
  <a href="#-amux-watch--dashboard-realtime">Watch</a> •
  <a href="#️-cấu-hình-provider-pool-amaccountsjson">Provider Pool</a> •
  <a href="STRUCT.md">Kiến Trúc</a>
</p>

---

## ⚡ Tính Năng Cốt Lõi

- 🔄 **Auto-Rotate Claude Accounts:** Tự động phát hiện và xoay vòng qua nhiều tài khoản Claude Pro / Max trước khi chạm rate limit (dựa vào header `anthropic-ratelimit-*`, ngưỡng mặc định 95%), tự động refresh token OAuth.
- 🌐 **Local AI Gateway (`:8787`):** Hỗ trợ đầy đủ 3 giao thức chuẩn:
  - **OpenAI:** `/v1/chat/completions`, `/v1/models` (cho Cursor, Continue, Cline, LangChain, SDK)
  - **Anthropic:** `/v1/messages` (cho Claude Code)
  - **Google Gemini:** `/v1beta/models/...`, `:generateContent`, `:streamGenerateContent`, `:countTokens` (cho Antigravity / AGY CLI & IDE)
- 🧰 **Tool Mid-Layer (`pkg/tools`):** Chuyển đổi tool schema và tool call giữa Claude Code (`tool_use`), Cursor / OpenAI (`tool_calls`), Codex và Antigravity (`functionDeclarations`).
- 🤖 **WebLoop Tool Emulation (`webloop.go`):** Giả lập giao thức gọi công cụ `<tool_call>` cho các Web Session (ChatGPT Web, Claude Web, Gemini Web), cho phép backend trình duyệt vẫn tham gia vào vòng lặp agent loop đầy đủ.
- 🧠 **Task Classifier & Smart Escalation (`classifier.go`):** Tự động phân loại ngữ cảnh tác vụ (kiến trúc hệ thống, đánh giá an ninh bảo mật, phân tích crash dump, tối ưu hiệu năng) để tự động kích hoạt chế độ suy luận chuyên sâu (*extended thinking*) hoặc điều hướng lên các model Pro (như `gemini-3.1-pro`, `o3-mini`).
- 🛡️ **Multi-Provider Failover:** Chuyển mạch thông minh khi gặp 429 hoặc lỗi xác thực giữa GitHub Models, Google Gemini API, Groq, OpenRouter, Codex CLI và Web Sessions (ChatGPT / Claude / Gemini) với thời gian chờ (cooldown) 30 phút.
- 🔀 **Quản lý Pool linh hoạt + `X-Provider`:** `am off` / `am pool remove` đưa tài khoản ra khỏi vòng xoay; `am on` / `am pool add` đưa trở lại. Gọi trực tiếp bằng header `X-Provider: <id>` (+ optional `X-Model`).
- 🧠 **Context & Session Retention:** Duy trì lịch sử hội thoại xuyên suốt khi chuyển đổi giữa các tài khoản hoặc khi failover sang provider khác.
- 📊 **Token Usage Analytics:** Thống kê token trực quan theo ngày / tuần / tháng / all, phân tích chi tiết theo từng project, model và session.
- 🪵 **Error Diagnostics & 7-Day Log Retention (`am logs`):** Lưu trữ chẩn đoán chi tiết lỗi turn (`errors.log`), tự động dọn dẹp nhật ký sau 7 ngày; hỗ trợ tạo issue báo lỗi an toàn với `am feedback`.
- 🧼 **Privacy Redact (`pkg/privacy`):** Tự động che mờ email, API key, webhook, token, thẻ tín dụng trên payload outbound trước khi gửi lên upstream.
- 🚇 **HTTP CONNECT Tunneling:** Đóng vai trò forward proxy hỗ trợ các công cụ hoặc SDK cấu hình qua biến `HTTP_PROXY` / `HTTPS_PROXY`.
- 🔐 **Bảo Mật Cao Cấp:** Lưu trữ chứng thực trong macOS Keychain; mã hóa tệp `accounts.json` bằng AES-256-GCM (`AMENC1:`) với Scrypt master key; khi bind `--public` tự động phát hành API Key tạm thời `amux-<auth-token>` (`am proxy token`).
- 🔁 **Đồng Bộ Môi Trường Toàn Diện:** Tự động đồng bộ biến môi trường giữa `~/.claude/settings.json`, macOS GUI session qua `launchctl`, và shell qua `eval "$(am env)"` (tự động `unset` khi proxy tắt để trả về upstream gốc).

---

## 🚀 Cài Đặt Nhanh

### 1. Cài đặt tự động (macOS):
```sh
curl -fsSL https://raw.githubusercontent.com/ninhlee99/amux/main/install.sh | sh
```
> *Yêu cầu: **macOS**, **Go 1.26+**, Git. Script biên dịch từ source, cài đặt song song hai lệnh **`amux`** và **`am`** vào `~/.local/bin` (hoặc `/usr/local/bin`), sau đó tự động chạy `am setup` để tích hợp hooks và lệnh slash `/am:feedback`.*

### 2. Sử dụng ngay với Claude Code:
1. Mở `claude` — hook `SessionStart` sẽ tự động khởi động `am proxy up`. Proxy tự động snapshot tài khoản Claude hiện tại vào `~/.am/` (hoặc bạn có thể dùng `am add` thủ công).
2. Thêm tài khoản phụ: trong Claude Code gõ `/login` với tài khoản mới, sau đó chạy `am add` (hoặc mở một tab mới — proxy sẽ tự động nhận diện và snapshot).
3. Thêm các provider dự phòng vào pool: `am login chatgpt|claude|gemini|gemini-web|github|groq` hoặc `am api add`.

* **Tự động cập nhật:** `am setup --auto-update` (Cài đặt LaunchAgent kiểm tra bản mới định kỳ).
* **Nâng cấp thủ công:** `am update` (hoặc `am update --force` để build lại từ mã nguồn mới nhất; giữ nguyên toàn bộ dữ liệu tại `~/.am/`).
* **Gỡ cài đặt:** `am hook uninstall && rm -f /usr/local/bin/am /usr/local/bin/amux ~/.local/bin/am ~/.local/bin/amux`

---

## 📋 Danh Sách Lệnh CLI (`amux` / `am`)

> 💡 **Mẹo:** Bạn có thể dùng `amux` hoặc `am` thay thế cho nhau (ví dụ: `amux sw` hoàn toàn tương đương với `am sw`).

### Quản Lý Tài Khoản
| Lệnh | Mô Tả |
| :--- | :--- |
| `am accounts` | Hiển thị **tất cả** tài khoản: Claude Code + Web Sessions + API (`POOL=IN/OUT`) |
| `am off <id>` / `am on <id>` | Đưa tài khoản ra ngoài / trở lại vòng xoay (vẫn lưu trong danh sách `accounts`) |
| `am add [tool] [tên]` | Lưu thông tin đăng nhập CLI hiện tại (`claude` / `codex` / `gemini` / `antigravity`) |
| `am rm <id\|name>` | Xoá tài khoản (profile CLI chuyển vào thùng rác, provider bị gỡ bỏ) |
| `am restore <id>` | Khôi phục tài khoản từ thùng rác (`am restore --backup` khôi phục bản sao lưu gần nhất) |
| `am rename <cũ> <mới>` | Đổi tên profile Claude |
| `am sw` / `am sw <id>` | Mở picker chọn nhanh hoặc ghim cố định một profile Claude / Provider |
| `am ls [tool]` | Liệt kê danh sách tài khoản (alias của `am accounts`) |
| `am current [tool]` | Kiểm tra tài khoản đang đăng nhập trên máy (`claude` / `codex` / `antigravity`) |

### Quản Lý Rotation Pool & Failover
| Lệnh | Mô Tả |
| :--- | :--- |
| `am pool` | Xem danh sách các provider đang tham gia vòng xoay (**IN**) |
| `am pool add <id>` | Kích hoạt provider tham gia vòng xoay (tương đương `am on <id>`) |
| `am pool remove <id>` | Tạm dừng provider khỏi vòng xoay mà không xoá cấu hình (tương đương `am off <id>`) |
| `am pool priority <id> <N>` | Đặt độ ưu tiên (số nhỏ ưu tiên gọi trước, tự động nạp lại không cần restart) |
| `am pool model <id> <model>` | Thay đổi model mặc định của provider (hot-reload thời gian thực) |
| `am pool set <id> [flags]` | Cấu hình nhanh provider: `--priority N`, `--model M`, `--on`, `--off` |
| `am login <provider>` | Đăng nhập tương tác: `chatgpt`, `claude`, `gemini`, `gemini-web`, `github`, `groq` |
| `am api add <tên> [flags]` | Thêm OpenAI-compatible endpoint (`--endpoint <url> --api-key <key> [--model M] [--priority N]`) |
| `am accounts rm <id>` | Xoá hoàn toàn provider khỏi cấu hình |
| `am doctor providers` | Gửi truy vấn thử nghiệm (1-turn probe) kiểm tra tình trạng kết nối từng adapter |
| `am chat [--provider <id>]` | Trò chuyện trực tiếp trên terminal kèm khả năng tự động failover |

> **Codex CLI:** Sau khi chạy `am add codex`, token đăng ký của ChatGPT sẽ tự động được chuyển thành adapter `codex:NN` (`type: codex_cli`) — không cần thực hiện `am login` riêng biệt.

### Giám Sát, Proxy & Tiện Ích Hệ Thống
| Lệnh | Mô Tả |
| :--- | :--- |
| `am setup [--auto-update]` | Cài đặt hooks, lệnh `/am:feedback`, cấu hình auto-update |
| `am update [--force] [--quiet]` | Nâng cấp amux lên phiên bản mới nhất từ GitHub (giữ nguyên `~/.am/`) |
| `am status` (hoặc `am st`) | Kiểm tra trạng thái proxy daemon, hạn mức 5h/7d, số phiên hoạt động |
| `am watch` (hoặc `am dash`) | Mở Dashboard TUI realtime: Dash · Accounts · Activity · Usage |
| `am usage [day\|week\|month\|all]` | Thống kê token (`-D` chi tiết, `-d YYYY-MM-DD`, `-p PROJECT`) |
| `am logs [flags]` | Quản lý nhật ký hoạt động: `--count` (thống kê), `--errors` (xem lỗi), `--clean` (dọn dẹp > 7 ngày) |
| `am proxy [up\|down\|token]` | Điều khiển daemon `:8787`. Hỗ trợ: `--public`, `-b/--addr`, `-p/--port`, `--threshold N` |
| `am run <tool> [args...]` | Khởi chạy công cụ (`claude`, `agy`, `antigravity`) kèm tự động kết nối và nạp môi trường proxy |
| `am env [--public]` | Xuất biến môi trường cho lệnh `eval "$(am env)"` (`--public` sử dụng IP mạng LAN) |
| `am env [set\|get\|rm\|list]` | Quản lý và lưu trữ cố định các biến môi trường tùy chỉnh cho proxy/công cụ |
| `am hook [claude\|agy\|codex\|cursor]` | Điều khiển hook vòng đời cho từng công cụ (`start` / `stop`) |
| `am hook [install\|uninstall\|status]` | Cài đặt, gỡ bỏ hoặc kiểm tra trạng thái hooks tự động trên toàn hệ thống |
| `am export` / `am import` | Đóng gói xuất / nhập bộ hồ sơ tài khoản mã hóa (`.amexp`) qua máy khác |
| `am feedback [--error]` | Mở issue báo lỗi GitHub, tự động thu thập thông tin chẩn đoán đã khử dữ liệu nhạy cảm |

---

## 🔌 Local AI Gateway (`http://127.0.0.1:8787`)

Cổng gateway amux đóng vai trò máy chủ trung gian thống nhất:
- **OpenAI Endpoint:** `/v1/chat/completions`, `/v1/models`
- **Anthropic Endpoint:** `/v1/messages`
- **Gemini Endpoint:** `/v1beta/models/...`, `:generateContent`, `:streamGenerateContent`, `:countTokens`
- **Forward Proxy:** Hỗ trợ HTTP CONNECT tunneling qua biến `HTTP_PROXY` / `HTTPS_PROXY`.

Khi chạy trên loopback (`127.0.0.1`), proxy **không** bắt buộc API key (chấp nhận bất kỳ token mẫu/dummy). Khi gắn cờ `--public` (lắng nghe `0.0.0.0`), hệ thống sẽ kích hoạt bảo mật nghiêm ngặt và tự sinh API Key tạm thời dạng `amux-<auth-token>`. Các client ngoài mạng bắt buộc phải truyền key này qua header `Authorization: Bearer <key>`, `X-Api-Key` hoặc `X-Am-Token`. Bạn có thể in key này bằng lệnh `am proxy token`.

### 1. Claude Code
```sh
am proxy up
eval "$(am env)"    # Thiết lập ANTHROPIC_BASE_URL=http://127.0.0.1:8787 và ANTHROPIC_AUTH_TOKEN=am-proxy
claude              # Hoặc chạy nhanh: am run claude
```

### 2. Cursor / Continue / Cline / LangChain
Cấu hình trong file môi trường `.env` hoặc settings của extension:
```env
OPENAI_BASE_URL="http://127.0.0.1:8787/v1"
OPENAI_API_KEY="am-proxy"
```

### 3. Antigravity / Gemini CLI / Google Gen AI SDK
```env
GEMINI_API_BASE="http://127.0.0.1:8787"
GOOGLE_GENAI_BASE_URL="http://127.0.0.1:8787"
```

### 4. Chỉ định trực tiếp Provider hoặc Model
Bạn có thể bỏ qua cơ chế xoay vòng pool và yêu cầu xử lý từ một provider cụ thể bằng HTTP Headers:
```http
X-Provider: gemini:api:01
X-Model: gemini-3.1-pro
```

### 5. Lập trình với Python SDK
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8787/v1",
    api_key="am-proxy",
)

# Hỗ trợ đầy đủ Server-Sent Events (SSE) Streaming
stream = client.chat.completions.create(
    model="default",
    messages=[{"role": "user", "content": "Viết thuật toán QuickSort trong Go"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

---

## 🧰 Luồng Claude Code ↔ Proxy ↔ Tools

Claude Code, Cursor và Antigravity **không** thực thi lệnh trên server amux. Client hoàn toàn sở hữu các công cụ hệ thống cục bộ (`Bash`, `Read`, `Edit`, `Write`...); nhiệm vụ của proxy là chuyển ngữ tương thích giữa các khối `tool_use` (Anthropic), `tool_calls` (OpenAI) hoặc `functionCall` (Gemini) để vòng lặp tác tử hoạt động mượt mà.

### Chuẩn hóa giao tiếp công cụ

| Client | Endpoint Proxy tiếp nhận | Định dạng Tool Wire Format |
| :--- | :--- | :--- |
| **Claude Code** | `POST /v1/messages` | Anthropic `tools[]` + `tool_use` / `tool_result` |
| **Cursor / Codex** | `POST /v1/chat/completions` | OpenAI `tools[].function` + `tool_calls` |
| **Antigravity (AGY)** | `POST /v1beta/models/...` | Gemini `functionDeclarations` / `functionCall` |

Tầng giữa `pkg/tools` chuyển đổi tất cả về cấu trúc canonical `types.ChatRequest`, sau đó các adapter sẽ phát sinh định dạng wire chuẩn tương ứng cho upstream.

### Cơ Chế WebLoop Emulation cho Web Sessions (`webloop.go`)
Đối với các tài khoản Web Session (như ChatGPT Web, Claude Web, Gemini Web) vốn không hỗ trợ gọi function gốc qua API:
- amux đóng gói danh mục công cụ và quy tắc thực thi vào system prompt.
- Phân tích cú pháp phản hồi văn bản của web model để bóc tách các thẻ `<tool_call>`, cú pháp XML hoặc bash markdown fences.
- Chuyển đổi các phát hiện này thành `types.ToolCall` hợp lệ để gửi về client.
- Xử lý các tình huống từ chối hỗ trợ tool hoặc yêu cầu paste mã nguồn để buộc model phát sinh tool call tự động.

---

## 🚀 Antigravity / AGY (Google GenAI)

Antigravity (AGY CLI & IDE) tương tác qua giao thức chuẩn Google Gemini:
- `POST /v1beta/models/...:generateContent` & `:streamGenerateContent`
- `POST /v1beta/models/...:countTokens`
- `GET /v1beta/models`

### Tự Động Khởi Chạy & Hooks
1. **Đăng ký Hooks tự động:**
   Lệnh `am hook install` hoặc `am setup` sẽ cấu hình các file lifecycle hooks:
   - **Antigravity (AGY):** `~/.gemini/config/hooks.json` → gọi `am hook agy start` / `stop`
   - **Claude Code:** `~/.claude/settings.json` → gọi `am hook claude start` / `stop`
   - **Codex:** `~/.codex/hooks.json` → gọi `am hook codex start`
   - **Cursor:** `~/.cursor/hooks.json` → gọi `am hook cursor start`

2. **Cách chạy AGY với proxy amux:**
   ```sh
   # Cách 1: Chạy trực tiếp (tự bật proxy ngầm và inject biến môi trường)
   am run agy
   am run antigravity

   # Cách 2: Export môi trường vào shell (bao gồm alias agy='am run agy')
   eval "$(am env)"
   agy
   ```

3. **Quản lý Profile AGY:**
   ```sh
   am current antigravity         # Xem email Antigravity đang login từ macOS Keychain
   am add antigravity <tên>       # Lưu profile AGY vào pool amux
   ```

---

## 📺 `amux watch` — Dashboard Realtime

Giao diện dòng lệnh TUI xây dựng bằng Bubble Tea + Lip Gloss (bảng màu Tokyo Night với viền bo tròn):

```sh
am proxy up          # Chạy proxy daemon (nếu chưa bật)
am watch             # Khởi chạy dashboard quan sát trực tiếp
```

Bảng điều khiển tổ chức theo 4 tab chuyên biệt (**chuyển tab bằng phím `1`–`4` hoặc `Tab`**):

| Tab | Tên Tab | Chức Năng Chính |
| :---: | :--- | :--- |
| **1** | **Dash** | Tổng quan KPI proxy, tình trạng xoay vòng pool, biểu đồ token 7 ngày, nhật ký hoạt động gần nhất |
| **2** | **Accounts** | Phân nhóm tài khoản (CLAUDE CODE, WEB, CODEX, CHATGPT, GEMINI, API), hạn mức 5h/7d, trạng thái `POOL` / `OUT` |
| **3** | **Activity** | Nhật ký chi tiết gộp (Sự kiện + Yêu cầu mạng I/O), hỗ trợ tìm kiếm lọc nhanh bằng phím `/` |
| **4** | **Usage** | Thống kê tiêu thụ token theo ngày/tuần/tháng/toàn bộ; lọc theo account/model hoặc dự án (`p`) |

**Phím tắt hữu ích trong Watch:**
- `1` – `4` / `Tab`: Chuyển đổi qua lại giữa các tab
- `/`: Kích hoạt bộ lọc tìm kiếm văn bản trong danh sách
- `p`: Lọc theo dự án (trong tab Usage)
- `d` / `w` / `m` / `a`: Xem thống kê theo Day / Week / Month / All (trong tab Usage)
- `c`: Xoá bộ lọc tìm kiếm
- `r`: Buộc làm mới dữ liệu
- `q` / `Ctrl+C`: Thoát dashboard

---

## ⚙️ Cấu Hình Provider Pool (`~/.am/accounts.json`)

Mỗi provider được gán một độ ưu tiên (`priority`), số càng nhỏ sẽ được ưu tiên điều hướng trước. Giá trị khóa API có thể liên kết trực tiếp với biến môi trường bằng cú pháp `env:TEN_BIEN`.

Quy chuẩn ID tài khoản: `brand[:method]:NN` (ví dụ: `github:api:01`, `gemini:api:01`, `claude:web:01`). Các ID cũ được hệ thống tự động nhận diện và nâng cấp.

> ⚠️ **Lưu ý:** File `~/.am/accounts.json` trên ổ đĩa được mã hóa tự động bằng thuật toán **AES-256-GCM** (định dạng `AMENC1:`) thông qua khóa chủ trong macOS Keychain. Bạn không nên chỉnh sửa file này thủ công mà hãy dùng các lệnh CLI như `am login`, `am api add`, `am pool priority`, `am pool model`, `am off`, `am on`.

Bạn có thể tham khảo file mẫu chưa mã hóa tại [`accounts.example.json`](accounts.example.json):

```json
{
  "providers": [
    {
      "id": "github:api:01",
      "type": "openai_compatible",
      "priority": 1,
      "baseUrl": "https://models.github.ai/inference",
      "apiKey": "env:GITHUB_MODELS_TOKEN",
      "model": "gpt-4o"
    },
    {
      "id": "gemini:api:01",
      "type": "openai_compatible",
      "priority": 2,
      "baseUrl": "https://generativelanguage.googleapis.com/v1beta/openai",
      "apiKey": "env:GOOGLE_AI_STUDIO_KEY",
      "model": "gemini-3.6-flash"
    },
    {
      "id": "groq:api:01",
      "type": "openai_compatible",
      "priority": 3,
      "baseUrl": "https://api.groq.com/openai/v1",
      "apiKey": "env:GROQ_API_KEY",
      "model": "llama-3.3-70b-versatile"
    },
    {
      "id": "chatgpt:01",
      "type": "chatgpt_web",
      "priority": 5,
      "sessionToken": "env:CHATGPT_SESSION_TOKEN"
    },
    {
      "id": "claude:web:01",
      "type": "claude_web",
      "priority": 6,
      "sessionKey": "env:CLAUDE_SESSION_KEY",
      "model": "claude-3-5-sonnet-20241022"
    }
  ]
}
```

### Mở Rộng Ra Mạng Cục Bộ (`--public`)
Khi cần chia sẻ gateway cho máy khác trong mạng nội bộ:
```sh
am proxy up --public            # Lắng nghe trên 0.0.0.0:8787
am proxy up --public -p 9000    # Đổi sang cổng tuỳ chọn 9000
am proxy token                  # In API key tạm thời (amux-<token>) cấp phát cho client ngoài
eval "$(am env --public)"       # Xuất biến môi trường trỏ trực tiếp đến địa chỉ IP mạng LAN
```

---

## 🏛️ Kiến Trúc Hệ Thống & Tài Liệu Kỹ Thuật

- 📖 Xem chi tiết thiết kế Domain-Driven Design (DDD), phân tầng các package và sơ đồ luồng dữ liệu tại [STRUCT.md](STRUCT.md).
- 🧠 Xem tài liệu thiết kế lớp nén ngữ cảnh token tại [docs/token-compression.md](docs/token-compression.md).
- 📊 Báo cáo đối chiếu công cụ và inventory tại [docs/reports/index.html](docs/reports/index.html).

## 📄 Bản Quyền

Dự án được phát hành theo giấy phép [MIT](LICENSE).
