# ai-cli-accounts (`am`)

<p align="center">
  <b>AI CLI Account Manager & Local AI Proxy Gateway</b><br>
  Snapshot, tự động xoay vòng tài khoản Claude Code, và cổng OpenAI Gateway kết nối Multi-Provider LLMs.
</p>

---

**ai-cli-accounts** (lệnh CLI: `am`) là công cụ quản lý tài khoản CLI và Local AI Proxy Gateway viết bằng Golang. 

Hệ thống cho phép bạn snapshot và chuyển đổi trạng thái đăng nhập cục bộ của **Claude Code**, **Codex**, và **Gemini CLI** mà không cần đăng xuất / đăng nhập lại. Proxy daemon chạy nền tự động xoay vòng tài khoản Claude Code **trước khi** bị gián đoạn bởi Rate Limit, đồng thời cung cấp cổng **OpenAI-compatible AI Gateway** với cơ chế tự động chuyển mạch (failover) giữa nhiều nhà cung cấp LLM khác nhau.

> 📖 **Kiến trúc hệ thống:** Xem chi tiết thiết kế DDD và sơ đồ dữ liệu tại [STRUCT.md](STRUCT.md).

---

## Tính Năng Nổi Bật

1. **Auto-Rotate Claude Accounts:** Tự động xoay vòng qua nhiều tài khoản Claude Pro / Max trước khi chạm rate limit (dựa trên header `anthropic-ratelimit-*`), tự động gia hạn OAuth token khi sắp hết hạn.
2. **Local AI Gateway (`/v1/chat/completions`):** Cung cấp endpoint chuẩn OpenAI (`http://127.0.0.1:8787/v1`) cho bất kỳ ứng dụng nào (Python, Node.js, Cursor, Continue, LangChain...).
3. **Multi-Provider Failover Pool:** Tự động chuyển mạch dự phòng (failover) giữa các nhà cung cấp (GitHub Models, Google AI Studio, Groq, DuckDuckGo AI, Claude Web, ChatGPT Web) khi gặp HTTP 429 với thời gian cách ly Cooldown 30 phút.
4. **Context Injection & Stateless Retention:** Giữ trọn vẹn lịch sử hội thoại khi chuyển đổi giữa các tài khoản hoặc failover giữa các provider.
5. **Token Usage Analytics:** Đo lường chi tiết lượng token vào/ra theo từng ngày, từng model, project (qua TCP socket resolution) và session.
6. **Bảo Mật Cao:** Sử dụng macOS Keychain kết hợp mã hoá AES-256-GCM / Scrypt để bảo vệ thông tin đăng nhập và token.

---

## Cài Đặt & Khởi Tạo Nhanh

### 1. Cài đặt tự động qua script:
```sh
curl -fsSL https://raw.githubusercontent.com/ninhlee99/ai-cli-accounts/main/install.sh | sh
```
Script sẽ tự động clone, biên dịch bằng Go 1.22+, cài đặt binary vào `/usr/local/bin/am`, thiết lập hook Claude Code (`am setup`) và dọn dẹp thư mục tạm.

### 2. Bắt đầu sử dụng:
```sh
claude          # tài khoản đầu tiên sẽ được tự động snapshot vào am
```

Để thêm tài khoản thứ 2: gõ `/login` trong Claude Code, sau đó mở tab terminal mới — tài khoản mới sẽ tự động được phát hiện và đưa vào danh sách xoay vòng.

* **Cập nhật:** Chạy lại lệnh cài đặt bên trên.
* **Gỡ cài đặt:** 
  ```sh
  am hook uninstall
  rm -f ~/.claude/commands/am/feedback.md
  rm -f /usr/local/bin/am
  # Tuỳ chọn: rm -rf ~/.am (để xoá sạch toàn bộ cấu hình và dữ liệu đã lưu)
  ```

---

## Danh Sách Lệnh CLI (`am`)

| Nhóm Lệnh | Cú Pháp | Chức Năng |
| :--- | :--- | :--- |
| **Cài đặt** | `am setup` | Cài đặt hook Claude Code + slash command `/am:feedback` |
| **Hồ sơ (Profile)** | `am add [tool] [name]` | Lưu tài khoản hiện tại vào profile (mặc định: `claude`) |
| | `am ls [tool]` | Liệt kê danh sách profile — ID (`claude1`, ...), tên, email, trạng thái active |
| | `am sw` / `am sw <id\|name>` | Menu mũi tên tương tác đổi profile hoặc provider |
| | `am current [tool]` | Xem tài khoản đang đăng nhập trên hệ thống |
| | `am rename <id\|name> <new>` | Đổi tên profile |
| | `am rm <id\|name>` | Chuyển profile vào thùng rác |
| | `am restore <id\|name>` | Khôi phục profile từ thùng rác |
| | `am restore --backup` | Khôi phục lại từ bản auto-backup gần nhất |
| **Provider & Gateway** | `am accounts` | Xem danh sách các nhà cung cấp AI trong pool, độ ưu tiên và model |
| | `am login [provider]` | Wizard đăng nhập Web / API (`chatgpt`, `claude`, `gemini`, `github`, `groq`) |
| | `am api add <name> --endpoint <url> --api-key <key>` | Thêm nhà cung cấp chuẩn OpenAI vào pool |
| | `am api rm <name>` / `am api ls` | Xoá hoặc xem danh sách provider API |
| | `am chat [prompt]` | REPL chat trực tiếp trên terminal với cơ chế failover |
| **Giám sát & Quản trị** | `am status` | Xem trạng thái proxy daemon, phiên tab kết nối, quota 5h/7d và pool |
| | `am usage [day\|week\|month\|all]` | Báo cáo thống kê token sử dụng (mặc định theo ngày) |
| | `am usage -D [-d YYYY-MM-DD] [-p PROJECT]` | Báo cáo chi tiết theo từng session, project, model |
| | `am proxy [up\|down]` | Bật/tắt daemon proxy chạy nền (mặc định cổng `:8787`) |
| | `am env` | Xuất lệnh `export ANTHROPIC_BASE_URL=...` dùng cho `eval "$(am env)"` |
| | `am hook [install\|uninstall\|status]` | Quản trị hook trong `~/.claude/settings.json` |
| | `am export [tool] [name..]` | Xuất profile ra file mã hoá di động (.amexp) |
| | `am import [-f file]` | Nhập profile từ file mã hoá di động |
| | `am feedback` | Tạo nhanh issue báo lỗi / đóng góp ý tưởng lên GitHub |

---

## Sử Dụng Làm Local AI Gateway (`http://127.0.0.1:8787`)

Cổng proxy `127.0.0.1:8787` hoạt động như một OpenAI-compatible API Gateway. Mọi ứng dụng có thể trỏ về địa chỉ này để tận dụng cơ chế Auto-Failover:

### 1. Python (`openai` SDK chính thức)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8787/v1",
    api_key="am-proxy"  # Bất kỳ chuỗi nào
)

# Hỗ trợ đầy đủ SSE Streaming
stream = client.chat.completions.create(
    model="default",
    messages=[{"role": "user", "content": "Viết thuật toán QuickSort trong Go"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

### 2. Node.js / TypeScript
```typescript
import OpenAI from "openai";

const openai = new OpenAI({
  baseURL: "http://127.0.0.1:8787/v1",
  apiKey: "am-proxy",
});

const completion = await openai.chat.completions.create({
  model: "default",
  messages: [{ role: "user", content: "Xin chào từ TypeScript!" }],
});
console.log(completion.choices[0].message.content);
```

### 3. Cursor / Continue / Cline / LangChain
Chỉ cần cấu hình trong file settings hoặc biến môi trường:
```bash
OPENAI_BASE_URL="http://127.0.0.1:8787/v1"
OPENAI_API_KEY="am-proxy"
```

---

## Cấu Hình Multi-Provider Pool

Các nhà cung cấp được cấu hình trong `~/.am/accounts.json` (xem mẫu tại [`accounts.example.json`](accounts.example.json)). Bạn có thể sử dụng cú pháp `env:TEN_BIEN` để nạp khoá bí mật từ biến môi trường:

```json
{
  "providers": [
    {
      "id": "github-models",
      "type": "openai_compatible",
      "priority": 1,
      "baseUrl": "https://models.github.ai/inference",
      "apiKey": "env:GITHUB_MODELS_TOKEN",
      "model": "gpt-4o"
    },
    {
      "id": "google-ai-studio",
      "type": "openai_compatible",
      "priority": 2,
      "baseUrl": "https://generativelanguage.googleapis.com/v1beta/openai",
      "apiKey": "env:GOOGLE_AI_STUDIO_KEY",
      "model": "gemini-2.0-flash"
    },
    {
      "id": "groq",
      "type": "openai_compatible",
      "priority": 3,
      "baseUrl": "https://api.groq.com/openai/v1",
      "apiKey": "env:GROQ_API_KEY",
      "model": "llama-3.3-70b-versatile"
    },
    {
      "id": "duckduckgo",
      "type": "duckduckgo",
      "priority": 4,
      "model": "claude-3-haiku-20240307"
    }
  ]
}
```

---

## Bản Quyền & Đóng Góp

Phát hành theo giấy phép MIT. Mọi đóng góp ý kiến hoặc báo lỗi vui lòng gửi qua lệnh `am feedback` hoặc GitHub Pull Requests!
