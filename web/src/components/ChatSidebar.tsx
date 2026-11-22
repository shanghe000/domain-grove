import { useEffect, useId, useRef, useState } from "react";
import type { ChangeEvent, DragEvent, FormEvent, KeyboardEvent } from "react";
import type { ChatMessage, Identity, Progress } from "../types";
import "./panels.css";

interface Props {
  mock: boolean;
  identity: Identity;
  messages: ChatMessage[];
  progress: Progress[];
  busy: boolean;
  onSend: (message: string, files: File[]) => Promise<void>;
}

const MAX_FILE_BYTES = 8 * 1024 * 1024;
const FEISHU_SAMPLE = "https://example.feishu.cn/docx/ReadingClubExample";
const CONFIG_SAMPLE = "https://sample.invalid/Reward.xlsx https://sample.invalid/Activity.xlsx";

export function PanelIcon({ name }: { name: "add" | "close" | "file" | "send" | "open" | "download" | "check" | "signal" }) {
  const paths = {
    add: "M8 3v10M3 8h10",
    close: "m4 4 8 8m0-8-8 8",
    file: "M4 1.75h5l3 3V14H4zM9 1.75v3h3M6 8h4m-4 2.5h4",
    send: "M8 13V3m-4 4 4-4 4 4",
    open: "M6 3H3v10h10v-3m-5-2 5-5m-4 0h4v4",
    download: "M8 2v8m-3-3 3 3 3-3M3 12v2h10v-2",
    check: "m3.25 8 3.25 3.25L13 5",
    signal: "M2 9h2l2-4 3 7 2-4h3",
  };
  return (
    <svg className="panel-icon" width="16" height="16" viewBox="0 0 16 16"
      fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={paths[name]} />
    </svg>
  );
}

function sameFile(first: File, second: File): boolean {
  return first.name === second.name && first.size === second.size && first.lastModified === second.lastModified;
}

export function ChatSidebar({ mock, identity, messages, progress, busy, onSend }: Props) {
  const [message, setMessage] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [fileError, setFileError] = useState("");
  const [dragging, setDragging] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const composer = useRef<HTMLTextAreaElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const stream = useRef<HTMLDivElement>(null);
  const nearEnd = useRef(true);
  const dragDepth = useRef(0);
  const id = useId();
  const waiting = busy || submitting;
  const ready = message.trim().length > 0 || files.length > 0;

  useEffect(() => {
    if (stream.current && nearEnd.current) stream.current.scrollTop = stream.current.scrollHeight;
  }, [messages.length, progress, waiting]);

  function addFiles(incoming: File[]) {
    const allowed: File[] = [];
    let error = "";
    for (const file of incoming) {
      if (!file.name.toLowerCase().endsWith(".xlsx")) {
        error = "请选择 .xlsx 工作簿。";
      } else if (!file.size || file.size > MAX_FILE_BYTES) {
        error = "工作簿须为非空文件，单个不超过 8 MiB。";
      } else {
        const inputs = [...files, ...allowed];
        if (inputs.some(current => sameFile(current, file))) continue;
        if (inputs.some(current => current.name === file.name)) {
          error = "同名工作簿请分开投喂。";
        } else if (inputs.length === 2) {
          error = "每次最多添加两份工作簿。";
          break;
        } else {
          allowed.push(file);
        }
      }
    }
    if (allowed.length) setFiles(current => [...current, ...allowed]);
    setFileError(error);
  }

  function onInput(event: ChangeEvent<HTMLInputElement>) {
    addFiles(Array.from(event.currentTarget.files ?? []));
    event.currentTarget.value = "";
    composer.current?.focus();
  }

  function onDragEnter(event: DragEvent<HTMLElement>) {
    if (!Array.from(event.dataTransfer.types).includes("Files")) return;
    event.preventDefault();
    dragDepth.current++;
    setDragging(true);
  }

  function onDragLeave(event: DragEvent<HTMLElement>) {
    if (!Array.from(event.dataTransfer.types).includes("Files")) return;
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (!dragDepth.current) setDragging(false);
  }

  function onDrop(event: DragEvent<HTMLElement>) {
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    addFiles(Array.from(event.dataTransfer.files));
    composer.current?.focus();
  }

  async function submit(text = message, attached = files) {
    if (waiting || (!text.trim() && !attached.length)) return;
    setSubmitting(true);
    setFileError("");
    nearEnd.current = true;
    try {
      await onSend(text.trim(), attached);
      if (text === message) setMessage(current => current === text ? "" : current);
      if (attached === files) setFiles(current => current === attached ? [] : current);
    } catch {
      // The parent shows the request failure. Keep the input available for retry.
    } finally {
      setSubmitting(false);
    }
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    void submit();
  }

  function keyboardSend(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.altKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void submit();
  }

  function trackingPrompt() {
    setMessage(current => current.trim() ? current.trimEnd() + "\n同步埋点版本 " : "同步埋点版本 ");
    requestAnimationFrame(() => composer.current?.focus());
  }

  return (
    <section className={"governance-panel" + (dragging ? " governance-panel--dragging" : "")}
      aria-label="治理对话" onDragEnter={onDragEnter} onDragLeave={onDragLeave}
      onDragOver={event => { if (Array.from(event.dataTransfer.types).includes("Files")) event.preventDefault(); }}
      onDrop={onDrop}>
      <header className="governance-header">
        <span className="governance-mark" aria-hidden="true"><PanelIcon name="signal" /></span>
        <h2>治理对话</h2>
      </header>
      <div className="governance-scroll" ref={stream}
        onScroll={event => {
          const target = event.currentTarget;
          nearEnd.current = target.scrollHeight - target.scrollTop - target.clientHeight < 56;
        }}>
        {messages.length === 0 && (
          <div className="governance-start">
            <h3>从一份资料开始</h3>
            <p>粘贴飞书文档或配置下载链接，也可以拖入工作簿。生成草案后，在画布上核对再入库。</p>
            {mock && (
              <div className="governance-examples" aria-label="试用样例">
                <button type="button" disabled={waiting} onClick={() => void submit(FEISHU_SAMPLE, [])}>
                  <PanelIcon name="file" /><span>读取阅读挑战文档示例</span><PanelIcon name="open" />
                </button>
                <button type="button" disabled={waiting} onClick={() => void submit(CONFIG_SAMPLE, [])}>
                  <PanelIcon name="file" /><span>读取活动与奖励配置样例</span><PanelIcon name="open" />
                </button>
              </div>
            )}
          </div>
        )}
        <div className="governance-log" role="log" aria-label="治理消息" aria-relevant="additions text">
          {messages.map((entry, index) => (
            <article className={"governance-message governance-message--" + entry.role} key={index}>
              <strong className="governance-sender" title={entry.role === "user" ? identity.username : undefined}>
                {entry.role === "user" ? "你" : entry.role === "error" ? "处理失败" : "治理助手"}
              </strong>
              <p>{entry.text}</p>
            </article>
          ))}
        </div>
        {(waiting || progress.length > 0) && (
          <div className="governance-progress" aria-label="资料处理进度" aria-live="polite">
            {progress.length === 0 ? (
              <p><span className="panel-progress-dot" data-status="running" />正在准备处理资料</p>
            ) : progress.map(item => (
              <p key={item.tool}>
                {item.status === "done" ? <PanelIcon name="check" /> : <span className="panel-progress-dot" data-status={item.status} />}
                <span>{item.text}</span>
              </p>
            ))}
          </div>
        )}
      </div>
      <form className="governance-compose" aria-label="添加资料和编辑指令" onSubmit={onSubmit}>
        {files.length > 0 && (
          <ul className="governance-files" aria-label="待发送文件">
            {files.map(file => (
              <li key={file.name + file.lastModified}>
                <PanelIcon name="file" />
                <span title={file.name}>{file.name}</span>
                <button className="panel-icon-button" type="button" aria-label={"移除 " + file.name}
                  onClick={() => setFiles(current => current.filter(value => value !== file))}><PanelIcon name="close" /></button>
              </li>
            ))}
          </ul>
        )}
        <label className="panel-sr-only" htmlFor={id}>治理消息</label>
        <textarea id={id} ref={composer} value={message} rows={3} maxLength={12000}
          placeholder="粘贴资料链接或描述图谱修改…" onChange={event => setMessage(event.target.value)} onKeyDown={keyboardSend}
          aria-describedby={fileError ? id + "-error" : undefined} />
        {fileError && <p id={id + "-error"} className="panel-error" role="alert">{fileError}</p>}
        <div className="governance-tools">
          <input ref={input} className="panel-sr-only" tabIndex={-1} aria-label="选择 Excel 工作簿" type="file"
            accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" multiple onChange={onInput} />
          <button type="button" className="panel-text-button governance-tool" onClick={() => input.current?.click()}
            title="添加 Excel 工作簿">
            <PanelIcon name="add" /><span>工作簿</span>
          </button>
          <button type="button" className="panel-text-button governance-tool" onClick={trackingPrompt}>
            <PanelIcon name="signal" /><span>同步埋点版本</span>
          </button>
          <button type="submit" className="governance-send" disabled={waiting || !ready}
            aria-label={waiting ? "正在处理资料" : "发送治理消息"} title={waiting ? "正在处理资料" : "发送"}>
            <PanelIcon name="send" />
          </button>
        </div>
      </form>
      {dragging && <div className="governance-drop" aria-hidden="true"><PanelIcon name="add" />释放后添加 Excel 工作簿</div>}
    </section>
  );
}
