# ai-cli-accounts

AI CLI Account Manager & Local AI Proxy Gateway. Command: `am`.

Snapshot and swap local login states of **Claude Code**, **Codex**, and **Gemini CLI** without logging out and back in. A local daemon proxy also rotates Claude accounts automatically **before** a rate limit interrupts you, and provides a standard **OpenAI-compatible AI Gateway** with auto-failover across multiple providers.

> 📖 **Kiến trúc hệ thống:** Xem chi tiết cấu trúc thư mục và thiết kế các package tại [STRUCT.md](STRUCT.md).

---

## Tính Năng Nổi Bật

1. **Auto-Rotate Claude Accounts:** Tự động xoay vòng qua nhiều tài khoản Claude Pro / Max trước khi chạm rate limit (dựa trên header `anthropic-ratelimit-*`), tự động gia hạn OAuth token khi sắp hết hạn.
2. **Local AI Gateway (`/v1/chat/completions`):** Cung cấp endpoint chuẩn OpenAI (`http://127.0.0.1:8787/v1`) cho bất kỳ ứng dụng nào (Python, Node.js, Cursor, Continue, LangChain...).
3. **Multi-Provider Failover Pool:** Tự động chuyển tiếp (failover) giữa các nhà cung cấp (Google Gemini AI Studio, GitHub Models, Groq, DuckDuckGo AI, Claude Web, ChatGPT Web) khi gặp HTTP 429 với cơ chế Cooldown 30 phút.
4. **Stateless Session Retention:** Giữ trọn vẹn ngữ cảnh hội thoại khi chuyển đổi giữa các tài khoản hoặc provider.
5. **Token Usage Analytics:** Đo lường chi tiết lượng token vào/ra theo từng ngày, từng model, project và session.

---

## Cài Đặt & Khởi Tạo

```sh
curl -fsSL https://raw.githubusercontent.com/ninhlee99/ai-cli-accounts/main/install.sh | sh
```

Lệnh trên sẽ clone vào thư mục tạm, biên dịch bằng Go 1.22+, cài đặt vào `/usr/local/bin/am`, thiết lập hook Claude Code (`am setup`) và dọn dẹp thư mục tạm.

Sau đó mở terminal:
```sh
claude          # tài khoản đầu tiên sẽ được tự động snapshot vào am
```

Để thêm tài khoản thứ 2: gõ `/login` trong Claude Code, sau đó mở tab mới — tài khoản mới sẽ tự động được đưa vào danh sách xoay vòng.

* **Cập nhật:** Chạy lại lệnh cài đặt bên trên.
* **Gỡ cài đặt:** `am hook uninstall && rm -f ~/.claude/commands/am/feedback.md && rm -f /usr/local/bin/am` (thêm `rm -rf ~/.am` để xoá toàn bộ dữ liệu lưu trữ).

---

## Danh Sách Lệnh CLI (`am`)

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `am setup` | Cài đặt hook Claude Code + slash command `/am:feedback` |
| `am add [tool] [name]` | Lưu tài khoản hiện đang đăng nhập (mặc định: `claude`, tên: email) |
| `am ls [tool]` | Liệt kê danh sách profile — ID (`claude1`, ...), tên, email, active |
| `am sw` / `am sw <id\|name>` | Đổi tài khoản (menu mũi tên hoặc gõ trực tiếp); hỗ trợ chọn cả Provider AI |
| `am status` | Xem trạng thái proxy daemon, phiên kết nối, quota 5h/7d và danh sách pool |
| `am current [tool]` | Kiểm tra tài khoản hiện đang đăng nhập trên hệ thống |
| `am rename <id\|name> <new>` | Đổi tên profile |
| `am rm <id\|name>` | Chuyển profile vào thùng rác |
| `am restore <id\|name>` / `--backup` | Khôi phục profile từ thùng rác hoặc bản sao lưu |
| `am usage [day\|week\|month\|all]` | Bảng thống kê token sử dụng qua proxy theo ngày (mặc định: tuần) |
| `am usage -D [-d YYYY-MM-DD] [-p PROJECT]` | Báo cáo chi tiết theo từng session, project, model |
| `am login [provider]` | Đăng nhập tài khoản Web / API (`chatgpt`, `claude`, `gemini`, `github`, `groq`) |
| `am accounts` | Xem danh sách các provider trong pool, độ ưu tiên và model |
| `am api add <name> --endpoint <url> --api-key <key>` | Thêm nhà cung cấp chuẩn OpenAI vào pool |
| `am api rm <name>` / `am api ls` | Xoá hoặc xem danh sách nhà cung cấp |
| `am chat [prompt]` | Chat trực tiếp trên terminal với cơ chế tự động xoay vòng provider |
| `am proxy [up\|down]` | Quản lý tiến trình proxy daemon chạy nền (mặc định cổng `8787`) |
| `am env` | In lệnh `export ANTHROPIC_BASE_URL=...` dùng cho `eval "$(am env)"` |
| `am hook [install\|uninstall\|status]` | Quản trị hook trong `~/.claude/settings.json` |
| `am export [tool] [name..]` | Xuất profile ra file mã hoá (.amexp) kèm mật khẩu |
| `am import [-f file]` | Nhập profile từ file mã hoá di động |
| `am feedback` | Tạo nhanh issue báo lỗi / đóng góp ý tưởng lên GitHub |

---

## Sử Dụng Làm Local AI Gateway (`http://127.0.0.1:8787`)

Cổng proxy `127.0.0.1:8787` hoạt động như một AI Gateway chuẩn hoá, sẵn sàng phục vụ các ứng dụng của bạn:

### 1. Python (dùng thư viện chính thức `openai`)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8787/v1",
    api_key="am-proxy"  # Chuỗi bất kỳ
)

# Streaming response
stream = client.chat.completions.create(
    model="default",
    messages=[{"role": "user", "content": "Xin chào!"}],
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
  messages: [{ role: "user", content: "Viết hàm fibonacci trong Go" }],
});
console.log(completion.choices[0].message.content);
```

### 3. Cursor / Continue / Cline / LangChain
Chỉ cần cấu hình:
```bash
OPENAI_BASE_URL="http://127.0.0.1:8787/v1"
OPENAI_API_KEY="am-proxy"
```

---

## Cấu Hình Multi-Provider Pool

Các nhà cung cấp được cấu hình trong `~/.am/accounts.json` (xem mẫu tại [`accounts.example.json`](accounts.example.json)). Bạn có thể sử dụng biến môi trường dạng `env:VAR_NAME` để không phải lưu khoá bí mật ở dạng văn bản rõ:

* `github-models` (GPT-4o từ GitHub Models)
* `google-ai-studio` (Gemini 2.0 Flash / Pro)
* `groq` (Llama 3.3 70B, Mixtral)
* `duckduckgo` (Claude 3 Haiku ẩn danh, miễn phí)
* `chatgpt-web` & `claude-web` (Phiên làm việc web qua Cookie/Session)

Xem chi tiết kiến trúc và cơ chế Auto-Rotation tại [STRUCT.md](STRUCT.md).
