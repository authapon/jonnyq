# jonnyq

Coding agent CLI แบบ REPL เขียนด้วย Go (stdlib-first, ไม่มี third-party Go dependency) รองรับ provider แบบ Ollama (native API) และ OpenAI-compatible (SSE) พร้อม tool-calling loop, context persistence, และคำสั่ง `/coding` สำหรับ automate การเขียนโค้ดจาก `requirements.md`

## Features

- REPL loop: รับ prompt ต่อเนื่องจนกว่าจะสั่ง `/exit`
- รองรับ 2 provider: `ollama` (native `/api/chat`, ได้ metric เวลาจริงจาก provider) และ `openai` (OpenAI-compatible `/v1/chat/completions` แบบ SSE)
- Tools ให้ model เรียกใช้: `read_file`, `write_file`, `edit_file`, `create_folder`, `web_search`, `web_fetch`, `read_pdf`, `read_pic`, `run_command`, `read_skill`
- แสดงผลแบบมีสี: ขาว=prompt, เขียว=thinking, แดง=tool call, เหลือง=answer, เทา=metric ท้ายรอบ พร้อม log ไฟล์แบบ plain text (`output.txt`)
- เก็บ context การทำงานลงไฟล์ `.context` และ compact อัตโนมัติทุก 20 รอบ
- คำสั่ง `/coding` แตก `requirements.md` เป็น task checklist ในไฟล์ `.progress` แล้วไล่ทำทีละ task พร้อม verify

## ความปลอดภัย (โดยตั้งใจ)

- `read_file` / `write_file` / `edit_file` / `create_folder` ถูกจำกัดให้ทำงานได้เฉพาะภายใน working directory เท่านั้น (กัน path traversal และ symlink escape)
- `run_command` รันได้ทันทีโดยไม่มี confirmation prompt แต่มี denylist บล็อกคำสั่งทำลายล้าง (เช่น `rm -rf /`, fork bomb, `mkfs`, `dd of=/dev/...`, `shutdown`, `curl | bash` ฯลฯ)
- `read_pdf` เรียก `pdftotext`/`pdftoppm` จาก **poppler-utils** ผ่าน `os/exec` (ไม่ได้ผูก PDF library เข้า binary) — ต้องติดตั้ง poppler-utils แยกในเครื่องที่รัน

## ความต้องการของระบบ

- Go 1.24+ สำหรับ build
- (ถ้าจะใช้ `read_pdf`) ติดตั้ง poppler-utils:
  ```bash
  # Debian/Ubuntu
  sudo apt install poppler-utils
  # macOS
  brew install poppler
  ```

## Build เป็น executable file

โปรเจกต์นี้เป็น pure Go ไม่มี cgo และไม่มี third-party dependency เลย จึง build เป็น binary เดียวได้ตรงไปตรงมา

### Build สำหรับเครื่องปัจจุบัน

```bash
go build -o jonnyq ./cmd/jonnyq
./jonnyq -h
```

### Build แบบ optimize (ตัด debug symbol ให้ไฟล์เล็กลง)

```bash
go build -ldflags="-s -w" -o jonnyq ./cmd/jonnyq
```

### Cross-compile ข้าม OS/สถาปัตยกรรม

เพราะไม่มี cgo จึงตั้ง `CGO_ENABLED=0` แล้ว cross-compile ข้ามแพลตฟอร์มได้โดยไม่ต้องมี toolchain ของ target:

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o jonnyq-linux-amd64 ./cmd/jonnyq

# Linux arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o jonnyq-linux-arm64 ./cmd/jonnyq

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o jonnyq-darwin-arm64 ./cmd/jonnyq

# macOS Intel
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o jonnyq-darwin-amd64 ./cmd/jonnyq

# Windows amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o jonnyq.exe ./cmd/jonnyq
```

> หมายเหตุ: `read_pdf` ยังต้องพึ่ง `pdftotext`/`pdftoppm` ที่ติดตั้งแยกบนเครื่องปลายทางเสมอ ไม่ว่าจะ cross-compile ไปแพลตฟอร์มไหนก็ตาม เพราะเป็นการเรียก external tool ผ่าน `os/exec` ไม่ใช่โค้ดที่ compile ติดไปกับ binary

### รัน test / vet ก่อน build (แนะนำ)

```bash
go vet ./...
go test ./...
```

## การตั้งค่า (Config)

ตั้งค่าได้ทั้งผ่าน CLI flag และ environment variable ที่ขึ้นต้นด้วย `JONNYQ_` โดยลำดับความสำคัญ: **CLI flag > env var > ค่า default**

| Flag | Env var | Default | ความหมาย |
|---|---|---|---|
| `-provider` | `JONNYQ_PROVIDER` | `ollama:http://localhost:11434` | provider ในรูปแบบ `name:url` (`ollama` หรือ `openai`) |
| `-key` | `JONNYQ_KEY` | (ว่าง) | provider API key |
| `-model` | `JONNYQ_MODEL` | (ว่าง — ต้องตั้งก่อนถึงจะสั่งงานได้) | ชื่อ model |
| `-context` | `JONNYQ_CONTEXT_SIZE` | `16348` | ขนาด context |
| `-output` | `JONNYQ_OUTPUT_FILE` | `output.txt` | ไฟล์ log แบบ append |
| `-searxng-url` | `JONNYQ_SEARXNG_URL` | (ว่าง — ปิดการใช้ `web_search`) | URL ของ searxng |
| `-searxng-max-results` | `JONNYQ_SEARXNG_MAX_RESULTS` | `10` | จำนวนผลลัพธ์สูงสุด |
| `-searxng-concurrency` | `JONNYQ_SEARXNG_CONCURRENCY` | `4` | จำนวนการค้นหาพร้อมกันสูงสุด |
| `-searxng-timeout-sec` | `JONNYQ_SEARXNG_TIMEOUT_SEC` | `30` | timeout วินาที |
| `-fetch-concurrency` | `JONNYQ_FETCH_CONCURRENCY` | `5` | จำนวนการโหลดเว็บพร้อมกันสูงสุด |
| `-fetch-timeout-sec` | `JONNYQ_FETCH_TIMEOUT_SEC` | `30` | timeout วินาที |
| `-pdf-max-pages` | `JONNYQ_PDF_MAX_PAGES` | `50` | จำนวนหน้า PDF สูงสุด |
| `-pdf-dpi` | `JONNYQ_PDF_DPI` | `150` | DPI ตอน render PDF เป็นภาพ |
| `-run-command-timeout-sec` | `JONNYQ_RUN_COMMAND_TIMEOUT_SEC` | `300` | timeout ของ `run_command` วินาที (ปรับตอนรันได้ด้วย `/run_command_timeout`) |
| `-skill-path` | `JONNYQ_SKILL_PATH` | (ว่าง) | path ของ skill คั่นด้วย `;` ได้หลายอัน |
| `-thinking` | `JONNYQ_THINKING` | `true` | เปิด/ปิด model thinking |

ตัวอย่างการรัน:

```bash
JONNYQ_MODEL=llama3 ./jonnyq
# หรือ
./jonnyq -provider openai:https://api.openai.com/v1 -key sk-... -model gpt-4o-mini
```

## Slash commands

| คำสั่ง | ความหมาย |
|---|---|
| `/?`, `/help` | แสดงคำสั่งทั้งหมด |
| `/provider <name>:<url>` | ตั้ง provider |
| `/key <key>` | ตั้ง provider API key |
| `/model <model>` | ตั้ง model |
| `/list_model` | แสดงรายการ model ที่มีจาก provider ปัจจุบัน |
| `/context <size>` | ตั้ง context size |
| `/think <true\|false>` | เปิด/ปิด thinking |
| `/run_command_timeout <seconds>` | ตั้ง timeout ของ `run_command` (วินาที) ที่กำลังรันอยู่ |
| `/coding` | automate เขียนโค้ดจาก `requirements.md` ทั้งหมด พร้อม compile/test/verify ทีละ task ใน `.progress` |
| `/exit` | ออกจากโปรแกรม |

ถ้ายังไม่ได้ตั้ง `-model`/`JONNYQ_MODEL` โปรแกรมจะแจ้งเตือนก่อนแสดง prompt และรับได้เฉพาะ slash command เท่านั้น จนกว่าจะสั่ง `/model <name>`

กด **Ctrl-C** เพื่อยกเลิกรอบการทำงานปัจจุบันได้โดยไม่ต้องปิดโปรแกรม รวมถึงระหว่างที่ `/coding` กำลังทำงานอยู่ด้วย (ยกเลิกได้ทั้ง turn ปัจจุบันของ agent และ process ของ `run_command` ที่กำลังรันอยู่)

## โครงสร้างโปรเจกต์

```
cmd/jonnyq/        entry point
internal/config/   flag + JONNYQ_* env parsing
internal/llm/      provider interface + ollama.go, openai.go
internal/tools/    read_file, write_file, edit_file, create_folder,
                    web_search, web_fetch, read_pdf, read_pic,
                    run_command, read_skill
internal/agent/    tool-calling loop, .context, compaction, metrics
internal/repl/     prompt loop + slash commands
internal/skill/    skill discovery
internal/coding/   /coding automation + .progress
internal/ui/       สีของ terminal + log แบบ plain text
```
