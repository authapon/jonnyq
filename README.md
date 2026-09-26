# jonnyq

Coding agent CLI แบบ REPL เขียนด้วย Go (stdlib-first, ไม่มี third-party Go dependency) รองรับ provider แบบ Ollama (native API) และ OpenAI-compatible (SSE) พร้อม tool-calling loop, การ compact conversation history อัตโนมัติ, และชุดคำสั่ง `/plan` `/coding` `/autocoding` สำหรับ automate การเขียนโค้ดจาก `requirements.md`

## Features

- REPL loop: รับ prompt ต่อเนื่องจนกว่าจะสั่ง `/exit`
- รองรับ 2 provider: `ollama` (native `/api/chat`, ได้ metric เวลาจริงจาก provider) และ `openai` (OpenAI-compatible `/v1/chat/completions` แบบ SSE)
- Tools ให้ model เรียกใช้: `read_file`, `write_file`, `edit_file`, `create_folder`, `web_search`, `web_fetch`, `read_pdf`, `read_pic`, `run_command`, `read_skill`
- แสดงผลแบบมีสี: ขาว=prompt, เขียว=thinking, แดง=tool call, เหลือง=answer, เทา=metric ท้ายรอบ — แต่ละ section (thinking/tool call/answer) มีบรรทัดว่างคั่นและหัวข้อตัวหนาสีสดกำกับไว้พร้อมวันเวลาที่ section นั้นเริ่ม เช่น `Thinking (2026-09-17 10:23:45)`, `Tool call (...)`, `Answer (...)` พร้อม log ไฟล์แบบ plain text (`output.txt`)
- prompt ที่รับคำสั่ง (`>`) แสดงชื่อ model และ context size ที่ใช้อยู่กำกับไว้ เช่น `ornith-1.5-35b-a3b (100000 token) >` (ก่อนตั้ง model จะเป็น `>` เฉยๆ)
- **Conversation history ในหน่วยความจำ** ที่ส่งให้ model จริงทุกรอบ (ตัวกำหนดความเร็วในการประมวลผล prompt) จะถูก compact (สรุปเหลือเป็นข้อความเดียว) อัตโนมัติทันทีที่ขนาดโดยประมาณเกิน ~60% ของ context size ที่ตั้งไว้ (ประเมินแบบหยาบที่ ~4 ตัวอักษร/token เพราะไม่มี tokenizer จริง) โดยเช็คทั้งระหว่างรันหนึ่ง turn (หลังทุกรอบ tool call ไม่ใช่รอจบ turn) และหลังจบ turn — ทำให้ prompt ที่ส่งให้ model กระชับอยู่เสมอแม้ระหว่างรันงานยาว ๆ เช่น `/autocoding` ที่เรียก tool ต่อเนื่องจำนวนมากภายใน turn เดียว — ตัว summary ที่ได้ถูกบังคับให้เหลือไม่เกิน **~25% ของ budget เดิม** เสมอ (สั่ง model ให้สรุปสั้น ๆ ก่อน แล้วตัดท้ายบังคับอีกชั้นถ้า model ไม่ทำตาม กันไม่ให้ summary ที่ยืดยาวทำให้ compact รอบถัดไปเกิดขึ้นถี่จนแทบไม่ได้ลดขนาดจริง) รายละเอียดที่ตัดออกไป (เนื้อไฟล์/ output คำสั่งแบบเต็ม ๆ) model สามารถ `read_file`/`run_command` ซ้ำเพื่อดึงกลับมาเองได้เมื่อต้องการ (ไม่มีไฟล์ transcript แยกต่างหากเก็บไว้บนดิสก์ — ใช้ `output.txt` สำหรับดู log การทำงานย้อนหลัง)
- แยก planning ออกจากการเขียนโค้ดเป็น 3 คำสั่ง (ดูรายละเอียดที่หัวข้อ Slash commands): `/plan` สร้าง/reconcile `.progress` อย่างเดียว, `/coding` ทำทีละ task แล้วหยุด, `/autocoding` plan แล้วไล่ทำทุก task รวดเดียว (พฤติกรรมเดิมของ `/coding` ก่อนแยก)
- `/autocoding` **ล้าง conversation history ในหน่วยความจำก่อนเริ่มทุก task** (ไม่ต่อเนื่องจากขั้นตอน plan หรือ task ก่อนหน้าเลย) เพื่อให้ prompt ของแต่ละ task เล็กและเร็วที่สุดเท่าที่จะทำได้ แทนที่จะโตขึ้นเรื่อย ๆ ตลอดการรัน (แม้จะมี compaction คอยจำกัดขนาดไว้แล้วก็ตาม) — ให้ model ไปสืบหาข้อมูลที่ต้องใช้เอง (`read_file`, `run_command` ฯลฯ) ใหม่ทุกครั้งแทน เนื่องจาก state จริงของงานอยู่ในไฟล์ (`.progress`, `requirements.md`, โค้ด) อยู่แล้วไม่ได้พึ่ง history เป็นแหล่งความจริง — trade-off คือแต่ละ task อาจต้องเรียก tool ซ้ำเพื่อ "ค้นพบ" สิ่งที่เพิ่งอ่านไปในรอบก่อนหน้า และอาจสูญเสีย continuity ของการตัดสินใจที่ไม่ได้เขียนลงไฟล์ระหว่าง task; `/coding` และ `/plan` ยังคงพฤติกรรมเดิม (history ต่อเนื่องข้าม invocation ภายใน session เดียวกัน)
- **Incremental planning**: `/plan` (และ `/autocoding` ซึ่งเรียก `/plan` ให้อัตโนมัติ) ตรวจ hash ของ `requirements.md` เทียบกับที่เก็บไว้ใน `.progress.hash` ถ้าแก้/เพิ่ม requirement มา จะให้ model reconcile `.progress` ต่อยอด (เพิ่ม task ใหม่/uncheck task เดิมที่ไม่ตรงกับ requirement หรือโค้ดปัจจุบันแล้ว) โดยไม่ต้องเริ่ม plan ใหม่ทั้งหมด และเช็คความสอดคล้องกับโค้ดที่มีอยู่แล้วเสมอทั้งตอนสร้างครั้งแรกและตอน reconcile
- **`.progress` ละเอียดและมีโครงสร้างเป็น phase**: prompt ตอนสร้าง/reconcile `.progress` กำชับให้ model แตกงานให้ละเอียดที่สุดเท่าที่จะทำได้ (task เล็ก ตรวจสอบได้อิสระทีละอัน มากกว่า task ใหญ่กว้าง ๆ) และจัดรูปแบบเป็น `# <Project Title>` ตามด้วยกลุ่ม `## Phase N: <ชื่อ phase>` (เช่น scaffolding/config → data layer → core logic → API → UI → testing/verification ปรับตาม project จริง) โดยแต่ละ phase มี task ย่อยเป็น `- [ ] ...` อ้างอิง requirement ID ในวงเล็บถ้า `requirements.md` มีการติด ID ไว้ (เช่น `(REQ-012)`) — หัวข้อ phase/title ไม่ใช่ checkbox จึงไม่ถูกนับเป็น task (parser มองข้ามบรรทัดที่ไม่ใช่ `- [ ]`/`- [x]` อยู่แล้ว ไม่ต้องแก้โค้ด) ตอน reconcile จะพยายามจัดกลุ่ม task ใหม่เข้า phase เดิมที่เหมาะสมก่อนจะเพิ่ม phase ใหม่
- ระหว่าง `/coding`/`/autocoding` ทำงาน ตัว system prompt จะเข้มงวดขึ้น (`AUTONOMOUS CODING MODE`) กำชับว่าห้าม mark task ว่าเสร็จโดยไม่ได้รัน build/test จริงในรอบนั้นแล้วเห็นผลผ่านจริง — เป็นการกำกับผ่าน prompt เท่านั้น ไม่มีการตรวจสอบซ้ำจากฝั่งโปรแกรมเอง จึงยังขึ้นกับความสามารถ/ความซื่อสัตย์ของ model ที่ใช้อยู่
- prompt ที่สั่งให้เขียน/แก้ `.progress` กำชับให้เขียนคำอธิบาย task เป็น**ภาษาอังกฤษเสมอ** แม้ `requirements.md` จะเป็นภาษาอื่น (เก็บชื่อ/label เฉพาะจาก requirement ไว้เป็นภาษาเดิมได้เพื่ออ้างอิง) เนื่องจากบาง model เขียนภาษาอื่นได้ไม่ดีเท่าภาษาอังกฤษ
- ตรวจจับ**การคิดวนซ้ำ** (บาง local model ผ่าน backend อย่าง llama.cpp บางครั้งจะวนพูดประโยคเดิม ๆ ซ้ำ ๆ ในคำตอบโดยไม่เรียก tool หรือสรุปจบ) ถ้าพบบรรทัด/ย่อหน้าเดิมซ้ำเกิน 3 ครั้งในช่วงสั้น ๆ จะตัด response นั้นทิ้งทันที (ยกเลิก request ที่ค้างอยู่) แล้วบันทึกลง history เป็นข้อความสั้น ๆ แทนขยะที่วนซ้ำ พร้อมแจ้งเตือนในหน้าจอ — ทำให้ turn จบแบบปกติ (ไม่ error) แล้วปล่อยให้กลไก retry/stall เดิมของ `/coding`, `/autocoding` จัดการลองใหม่ต่อไป
- system prompt (ทุกโหมด ไม่ใช่แค่ coding) กำชับให้ model **คิดแบบเด็ดขาดและกระชับ**: ตัดสินใจแล้วลงมือทำ ไม่วนคิดเรื่องเดิมหรือพูดแผนซ้ำ ๆ หลีกเลี่ยงการให้เหตุผลที่ยืดเยื้อ แต่ต้องไม่แลกความถูกต้องกับความเร็ว — ยังต้องตรวจสอบสิ่งที่ไม่แน่ใจด้วย tool ก่อนสรุปเป็นข้อเท็จจริงเสมอ (เสริมกับกลไกตัดจบ loop ด้านบน คนละจุดกัน: อันนี้ลดโอกาสเกิด loop ตั้งแต่ต้น ส่วนตัวตรวจจับ loop จัดการตอนมันเกิดขึ้นแล้ว)
- ถ้าเจอ task เดิมค้างซ้ำ (ยัง `- [ ]` อยู่ใน `.progress` ทั้งที่ทำมาแล้วรอบหนึ่ง) ตั้งแต่รอบที่ 2 เป็นต้นไป prompt จะแนบข้อความเตือนที่ต่างกัน 2 แบบตามสิ่งที่ตรวจพบจริงจากไฟล์ (เทียบเนื้อหา `.progress` ก่อน/หลังรอบก่อนหน้า): (1) ถ้ารอบก่อนหน้า model **ไม่เรียก `edit_file`/`write_file` แตะไฟล์เลย** (เช่น แค่พิมพ์ยืนยันความสำเร็จในคำตอบ หรือ echo ข้อความ "✓ passed" ปลอม ๆ ผ่าน `run_command` โดยไม่ได้รันการ verify จริง) จะเตือนตรง ๆ ว่าการบรรยายว่าทำเสร็จไม่ได้แก้ไฟล์จริง ต้องเรียก tool จริงเท่านั้น หรือ (2) ถ้าแตะไฟล์แล้วแต่ task ยังไม่ถูกติ๊ก (เช่น `edit_file` ไม่ match exact text หรือ `write_file` เขียนทับทั้งไฟล์จาก context เก่า) จะให้ไป `read_file` เช็คของจริงก่อนแก้ต่อ — ทั้งสองกรณี `base prompt` (ไม่ใช่แค่ตอน retry) ก็เน้นย้ำอยู่แล้วว่าการติ๊ก checkbox ต้องเกิดจากการเรียก `edit_file`/`write_file` จริง การบอกในคำตอบเฉย ๆ ไม่นับ — `/coding` เก็บตัวนับและสถานะนี้ไว้ในไฟล์ `.progress.retry` เพราะแต่ละครั้งที่เรียกเป็นคนละ process/turn กัน ไม่มี loop ในหน่วยความจำให้จำต่อกันแบบ `/autocoding`
- **Safety valve อีกชั้นสำหรับ `/autocoding`**: ตัวนับด้านบนดักได้เฉพาะกรณี "task เดิมค้างด้วยข้อความเดียวกันซ้ำ ๆ" เท่านั้น ถ้า model แก้ `.progress` ไปเรื่อย ๆ แบบไม่มีความหมาย (เช่น สลับลำดับ task ไปมา ทำให้ task ที่ถูกหยิบมาทำงานตัวถัดไป (`next`) มีข้อความเปลี่ยนไปทุกรอบ) โดยไม่มี task ไหนถูกติ๊กสำเร็จจริงเลย ตัวนับแบบข้อความเดิมจะไม่มีทางถูก trigger เพราะข้อความไม่เคยซ้ำกัน 2 รอบติด — จึงเพิ่มตัวนับอิสระอีกตัวที่ดูจาก **จำนวน task ที่ติ๊กสำเร็จโดยรวมทั้งไฟล์** แทน ถ้าผ่านไป 15 รอบติดต่อกันแล้วไม่มี task ไหนถูกติ๊กเพิ่มขึ้นเลย (ไม่ว่า `next` จะเปลี่ยนข้อความกี่ครั้งก็ตาม) จะหยุดทำงานทันทีพร้อม error บอกให้ผู้ใช้ไปตรวจสอบ `.progress`/`requirements.md` เอง แทนที่จะวนต่อไปได้เรื่อย ๆ ไม่มีที่สิ้นสุด
- **Pair programming ผ่าน `/watchfile`**: `/watchfile [<magic word>|off]` (default magic word `AI!`, ตั้งค่าเริ่มต้นได้ผ่าน `-watch-magic-word`/`JONNYQ_WATCH_MAGIC_WORD`) เปิดการ poll working directory เป็นระยะ (`-watch-poll-interval-sec`/`JONNYQ_WATCH_POLL_INTERVAL_SEC`, default 1 วินาที, stdlib-only ไม่ใช้ fsnotify) เมื่อไฟล์ถูกแก้ไขแล้วมีบรรทัดที่มี magic word (เช่น `// AI! เพิ่ม error handling ตรงนี้`) จะส่งบรรทัดนั้นเป็น prompt ให้ agent ไปอ่านโค้ดรอบ ๆ แล้วดำเนินการตามคำสั่ง โดยจะข้าม dotfile/dot-directory ทั้งหมด (`.git`, `.progress*` ฯลฯ) รวมถึง `node_modules`/`vendor` และ**ไฟล์ log ของตัวเอง (`-output`)** เสมอ เพื่อไม่ให้ transcript ที่สะท้อน trigger กลับเข้ามาใน working directory ย้อนมา trigger ตัวเองซ้ำไม่รู้จบ — บรรทัดที่เคย trigger แล้วจะไม่ trigger ซ้ำจนกว่าเนื้อหาบรรทัดนั้นจะเปลี่ยน แก้ magic word ระหว่างที่กำลัง watch อยู่ได้ทันทีโดยไม่ต้อง restart การ watch
- **แจ้งเตือนผ่าน [ntfy.sh](https://ntfy.sh)** (หรือ self-hosted ntfy server ก็ได้): ตั้งค่า topic URL เต็ม ๆ ได้ผ่าน `-ntfy-url`/`JONNYQ_NTFY_URL`/`/ntfy [<topic url>|off]` (เช่น `https://ntfy.sh/my-topic`) เมื่อตั้งไว้แล้ว `/plan` และ `/coding` จะยิง push notification ไปที่ topic นั้นทุกครั้งที่ทำงานจบ (ทั้งสำเร็จและตอน error/stall) พร้อมสรุปผลลัพธ์จริงที่คำนวณจาก `.progress` โดยตรง (ไม่ได้เรียก model มาสรุปเพิ่ม เพื่อความเร็วและไม่เสี่ยง hallucinate) เช่น "Generated .progress from requirements.md: 5/12 tasks done", "Completed: \"add error handling\"\n6/12 tasks done", หรือ "Task ... made no progress after 5 attempts ..." ตอน stall — ต่อท้ายด้วย**เวลาเริ่ม/เวลาจบ/ระยะเวลารวมของคำสั่งนั้น และค่าสถิติจาก LLM รอบล่าสุด** (preload/prompt_eval/thinking duration, token in/out/total, tok/s — ชุดเดียวกับที่โชว์ท้าย turn ในหน้าจอ terminal) เช่น
  ```
  Completed: "add error handling"
  6/12 tasks done

  Started: 2026-09-22 10:15:03 UTC
  Finished: 2026-09-22 10:17:41 UTC
  Duration: 2m38s
  Stats: preload=1.2s prompt_eval=340ms thinking=95.4s token_in=4521 token_out=812 total_token=5333 tok/s=8.51
  ```
  กรณีที่ไม่มีการเรียก model จริง (เช่น task ทำครบหมดแล้วไม่มีอะไรต้องทำ) ค่าสถิติจะเป็น `n/a` ทั้งหมดแทนที่จะโชว์ตัวเลขค้างจาก turn ก่อนหน้าที่ไม่เกี่ยวข้อง — `/autocoding` **ไม่แจ้งเตือนเอง** (ตามที่ตั้งใจไว้ เพราะมันเรียก `/plan` ภายในให้อัตโนมัติ ถ้าแจ้งด้วยจะรัวข้อความทุก task) หากส่ง notification ไม่สำเร็จ (เช่น network error) จะขึ้น warning เฉย ๆ ไม่ทำให้ `/plan`/`/coding` ล้มเหลวไปด้วย
- **`/toolmode [native|prompt]`** (ตั้งค่าเริ่มต้นได้ผ่าน `-tool-mode`/`JONNYQ_TOOL_MODE` ด้วย ไม่ใช่แค่ slash command) — สำหรับ model/backend ที่เรียก native tool-calling ไม่เสถียร (เช่น ส่ง argument ผิด/ขาด field ที่จำเป็นซ้ำ ๆ) เปลี่ยนวิธีเรียก tool มาใช้ plain text แทนได้ โดย default เป็น `native` (พฤติกรรมเดิมทุกอย่าง) เปลี่ยนเป็น `prompt` แล้ว jonnyq จะ:
  - **ไม่ส่ง native tools spec ให้ provider เลย** (เพราะการส่งพร้อมกับให้ model พิมพ์ call เป็น text มักไปกระตุ้น grammar-constraint ของ backend ที่พังอยู่แล้ว — ตรงข้ามกับที่โหมดนี้ต้องการหลีกเลี่ยง) แล้วอธิบายรายชื่อ tool + JSON schema ของแต่ละตัวลงใน system prompt แทน
  - รอให้ model ตอบกลับด้วย fenced block รูปแบบ:
    ````
    ```tool
    {"name": "read_file", "arguments": {"path": "data.txt"}}
    ```
    ````
    แล้ว parse ออกมาเรียก tool จริงตาม pipeline เดิมทุกอย่าง (log, history, error handling เหมือนโหมด native)
  - ส่งผลลัพธ์ tool กลับเป็น **plain user message** (`"Tool result (<tool>): <output>"`) แทนที่จะใช้ `tool` role/`tool_call_id` แบบ native API เพราะ model ที่ต้องพึ่งโหมดนี้ (native ไม่เสถียรอยู่แล้ว) มักไม่เข้าใจ convention นั้น และบาง backend อาจปฏิเสธ request ที่มี tool role โดยไม่เคยประกาศ tools ไว้
  - ถ้า model ตอบ JSON ผิดรูปแบบหรือขาด `"name"` จะได้ error message ที่ชัดเจนกลับไปทันที (ไม่ผ่าน `Tools.Call` ซึ่งจะแค่บอก "unknown tool" เฉย ๆ) ให้ model แก้ไขแล้วลองใหม่รอบถัดไป

  หมายเหตุ UX: เนื่องจากข้อความ answer ถูก stream ออกจอทีละตัวอักษรแบบ real-time อยู่แล้ว fenced block ดิบ ๆ ที่ model พิมพ์จะโผล่ปนอยู่ใน section "Answer" ตามปกติ (ไม่ได้ถูกซ่อน/ย้ายไปโชว์เฉพาะใน "Tool call" เหมือน native เพราะต้องรู้ล่วงหน้าว่าจะมี block มา ซึ่งขัดกับการ stream สด) แล้วหลังจากนั้นจะมี "Tool call" section ตามมาแสดงผลการ parse/เรียกจริงอีกที ถือเป็น trade-off ที่ยอมรับได้เพื่อคง real-time streaming ไว้เหมือนเดิมทุกโหมด

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
| `-max-tool-calls-per-turn` | `JONNYQ_MAX_TOOL_CALLS_PER_TURN` | `50` | จำนวน tool call สูงสุดที่ยอมให้เรียกภายใน 1 รอบ prompt (safety valve กัน tool-call loop วนไม่รู้จบ) |
| `-skill-path` | `JONNYQ_SKILL_PATH` | (ว่าง) | path ของ skill คั่นด้วย `;` ได้หลายอัน |
| `-thinking` | `JONNYQ_THINKING` | `true` | เปิด/ปิด model thinking |
| `-watch-magic-word` | `JONNYQ_WATCH_MAGIC_WORD` | `AI!` | magic word ที่ `/watchfile` (ไม่ใส่ argument) ใช้ scan หา |
| `-watch-poll-interval-sec` | `JONNYQ_WATCH_POLL_INTERVAL_SEC` | `1` | ความถี่ในการ poll working directory ของ `/watchfile` (วินาที) |
| `-ntfy-url` | `JONNYQ_NTFY_URL` | (ว่าง — ปิดการแจ้งเตือน) | ntfy.sh (หรือ self-hosted) topic URL เต็ม ๆ สำหรับแจ้งเตือนตอน `/plan`/`/coding` ทำงานจบ เช่น `https://ntfy.sh/my-topic` |
| `-tool-mode` | `JONNYQ_TOOL_MODE` | `native` | โหมดเรียก tool เริ่มต้น: `native` หรือ `prompt` (ปรับทีหลังได้ด้วย `/toolmode`) — ค่าอื่นนอกจากสองค่านี้จะ error ตอนเริ่มโปรแกรม |

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
| `/prompt <file>` | อ่านเนื้อหาไฟล์มาเป็น prompt แล้วส่งเลย เหมือนพิมพ์เอง |
| `/plan` | สร้าง `.progress` จาก `requirements.md` ถ้ายังไม่มี หรือ reconcile ถ้า `requirements.md` เปลี่ยนไปแล้ว (เช็คความสอดคล้องกับโค้ดที่มีด้วย) — ไม่เขียนโค้ดใด ๆ |
| `/coding` | ทำ task แรกที่ยังไม่เสร็จใน `.progress` **แค่ 1 ข้อแล้วหยุด** (ต้องมี `.progress` อยู่แล้ว ถ้ายังไม่มีให้สั่ง `/plan` ก่อน) |
| `/autocoding` | พฤติกรรมเดิมของ `/coding` ก่อนแยกคำสั่ง: เรียก `/plan` ให้อัตโนมัติถ้าจำเป็น แล้วไล่ทำทุก task ใน `.progress` รวดเดียวจนครบ |
| `/watchfile [<word>\|off]` | ไม่มี argument: เริ่ม/หยุด watch สลับกัน (toggle) ด้วย magic word ปัจจุบัน; ใส่ magic word: ตั้ง/เริ่ม watch ด้วยคำนั้น (ถ้ากำลัง watch อยู่แล้วจะเปลี่ยนคำแบบ live ไม่ restart); `off`/`stop`: หยุด watch ชัดเจน |
| `/ntfy [<topic url>\|off]` | ไม่มี argument: แสดงค่าปัจจุบัน; ใส่ URL: ตั้ง/เปิดการแจ้งเตือนด้วย topic นั้น; `off`: ปิดการแจ้งเตือน — มีผลกับ `/plan`/`/coding` ในครั้งถัดไปทันที |
| `/toolmode [native\|prompt]` | ไม่มี argument: แสดงโหมดปัจจุบัน; `native`: ใช้ native tool-calling ของ provider (default); `prompt`: ให้ model เรียก tool ผ่านข้อความ fenced JSON block แทน — มีผลกับรอบถัดไปทันที |
| `/exit`, `/bye` | ออกจากโปรแกรม |

ถ้ายังไม่ได้ตั้ง `-model`/`JONNYQ_MODEL` โปรแกรมจะแจ้งเตือนก่อนแสดง prompt และรับได้เฉพาะ slash command เท่านั้น จนกว่าจะสั่ง `/model <name>`

### Multi-line prompt

พิมพ์ prompt หลายบรรทัดที่ prompt (`>`) ได้ 2 แบบ:

- **ต่อบรรทัดด้วย `\`**: จบแต่ละบรรทัดที่ต้องการต่อด้วย backslash แล้ว Enter บรรทัดสุดท้ายไม่ต้องมี `\`
  ```
  > แก้บั๊กใน main.go \
  ให้ handle error ตอนเปิดไฟล์ไม่เจอด้วย
  ```
- **บล็อกด้วย `"""`**: พิมพ์ `"""` บรรทัดเดียวเพื่อเริ่มบล็อก พิมพ์เนื้อหาได้หลายบรรทัดตามต้องการ แล้วพิมพ์ `"""` อีกครั้งเพื่อจบบล็อก
  ```
  > """
  ช่วยรีวิวโค้ดต่อไปนี้:

  func foo() {
      ...
  }
  """
  ```

หรือใช้ `/prompt <file>` เพื่ออ่าน prompt ยาว ๆ จากไฟล์แทนการพิมพ์ก็ได้เช่นกัน

กด **Ctrl-C** เพื่อยกเลิกรอบการทำงานปัจจุบันได้โดยไม่ต้องปิดโปรแกรม รวมถึงระหว่างที่ `/plan`, `/coding`, หรือ `/autocoding` กำลังทำงานอยู่ด้วย (ยกเลิกได้ทั้ง turn ปัจจุบันของ agent และ process ของ `run_command` ที่กำลังรันอยู่)

## โครงสร้างโปรเจกต์

```
cmd/jonnyq/        entry point
internal/config/   flag + JONNYQ_* env parsing
internal/llm/      provider interface + ollama.go, openai.go
internal/tools/    read_file, write_file, edit_file, create_folder,
                    web_search, web_fetch, read_pdf, read_pic,
                    run_command, read_skill
internal/agent/    tool-calling loop, context compaction, metrics, /toolmode (native + prompt-based)
internal/repl/     prompt loop + slash commands
internal/skill/    skill discovery
internal/coding/   /plan, /coding, /autocoding automation + .progress
internal/watch/    /watchfile: poll working directory หา magic word (stdlib-only, ไม่ใช้ fsnotify)
internal/notify/   /ntfy: ส่ง push notification ไปยัง ntfy.sh/self-hosted server
internal/ui/       สีของ terminal + log แบบ plain text
```
