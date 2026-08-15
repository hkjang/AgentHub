import { FormEvent, useEffect, useState } from "react";
import {
  Braces,
  Copy,
  EyeOff,
  ExternalLink,
  KeyRound,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api } from "../api";
import {
  Drawer,
  Empty,
  ErrorBanner,
  PageHeader,
  SuccessBanner,
} from "../components/UI";
import type { APIKey, PersonalSecret } from "../types";

export function Developer() {
  const [tab, setTab] = useState<"secrets" | "api">("secrets"),
    [secrets, setSecrets] = useState<PersonalSecret[]>([]),
    [keys, setKeys] = useState<APIKey[]>([]),
    [drawer, setDrawer] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [token, setToken] = useState("");
  const load = () =>
    Promise.all([
      api
        .get<{ items: PersonalSecret[] }>("/api/v1/secrets")
        .then((v) => setSecrets(v.items)),
      api
        .get<{ items: APIKey[] }>("/api/v1/api-keys")
        .then((v) => setKeys(v.items)),
    ]);
  useEffect(() => {
    void load();
  }, []);
  const rotate = async () => {
    if (
      !confirm(
        "개인 데이터 키를 회전하면 모든 Secret이 새 키로 다시 암호화됩니다. 계속할까요?",
      )
    )
      return;
    try {
      const result = await api.post<{ version: number }>("/api/v1/keys/rotate");
      setNotice(`개인 키를 v${result.version}으로 안전하게 회전했습니다.`);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "키 회전에 실패했습니다.");
    }
  };
  const remove = async (kind: "secret" | "key", id: string) => {
    try {
      await api.delete(
        kind === "secret" ? `/api/v1/secrets/${id}` : `/api/v1/api-keys/${id}`,
      );
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "삭제하지 못했습니다.");
    }
  };
  return (
    <div className="page">
      <PageHeader
        eyebrow="개인 보안"
        title="시크릿 · API 키"
        description="서비스 관리자 설정과 분리된 개인 Credential 및 개발자 접근 권한입니다."
        actions={
          <>
            <button className="button ghost" onClick={() => void rotate()}>
              <RefreshCw size={16} />
              개인 키 회전
            </button>
            <button className="button primary" onClick={() => setDrawer(true)}>
              <Plus size={16} />
              {tab === "secrets" ? "Secret" : "API Key"} 추가
            </button>
          </>
        }
      />
      <section className="developer-contracts">
        <div>
          <Braces />
          <span>
            <strong>OpenAPI 3.1</strong>
            <small>Control Plane REST 계약</small>
          </span>
          <a
            className="button ghost"
            href="/api/openapi.json"
            target="_blank"
            rel="noreferrer"
          >
            <ExternalLink size={14} />
            열기
          </a>
        </div>
        <div>
          <ShieldCheck />
          <span>
            <strong>MCP Streamable HTTP</strong>
            <small>POST /mcp · Bearer mcp:read</small>
          </span>
          <code>/mcp</code>
        </div>
      </section>
      {error && <ErrorBanner message={error} onClose={() => setError("")} />}{" "}
      {notice && <SuccessBanner message={notice} />}{" "}
      {token && (
        <div className="one-time-token">
          <ShieldCheck size={20} />
          <div>
            <strong>API Key가 생성되었습니다</strong>
            <p>다시 표시되지 않으니 지금 안전한 곳에 복사하세요.</p>
            <code>{token}</code>
          </div>
          <button onClick={() => void navigator.clipboard.writeText(token)}>
            <Copy size={16} />
            복사
          </button>
        </div>
      )}
      <div className="tabs">
        <button
          className={tab === "secrets" ? "active" : ""}
          onClick={() => setTab("secrets")}
        >
          Personal Secrets <span>{secrets.length}</span>
        </button>
        <button
          className={tab === "api" ? "active" : ""}
          onClick={() => setTab("api")}
        >
          API Keys <span>{keys.length}</span>
        </button>
      </div>
      <section className="panel">
        {tab === "secrets" ? (
          secrets.length === 0 ? (
            <Empty
              icon={<EyeOff />}
              title="저장된 Secret이 없습니다"
              description="Git, MCP, DB 자격증명을 암호화해 에이전트에 참조로 연결하세요."
            />
          ) : (
            <div className="item-list">
              {secrets.map((item) => (
                <div className="list-card" key={item.id}>
                  <div className="list-icon">
                    <KeyRound />
                  </div>
                  <div>
                    <strong>{item.name}</strong>
                    <span>
                      {item.kind} · key v{item.keyVersion}
                    </span>
                  </div>
                  <div className="list-meta">
                    <span>
                      {new Date(item.createdAt).toLocaleDateString("ko-KR")}
                    </span>
                    <button
                      onClick={() => void remove("secret", item.id)}
                      aria-label="삭제"
                    >
                      <Trash2 size={16} />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )
        ) : keys.length === 0 ? (
          <Empty
            icon={<Braces />}
            title="활성 API Key가 없습니다"
            description="자동화와 MCP/API 연동에 사용할 최소 권한 키를 생성하세요."
          />
        ) : (
          <div className="item-list">
            {keys.map((item) => (
              <div className="list-card" key={item.id}>
                <div className="list-icon">
                  <Braces />
                </div>
                <div>
                  <strong>{item.name}</strong>
                  <span>
                    <code>{item.prefix}…</code> · {item.scopes.join(", ")}
                  </span>
                </div>
                <div className="list-meta">
                  <span>
                    {item.lastUsedAt
                      ? "최근 사용 " +
                        new Date(item.lastUsedAt).toLocaleDateString("ko-KR")
                      : "사용 기록 없음"}
                  </span>
                  <button
                    onClick={() => void remove("key", item.id)}
                    aria-label="폐기"
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
      {drawer && (
        <CredentialDrawer
          type={tab}
          close={() => setDrawer(false)}
          done={(newToken) => {
            setDrawer(false);
            setToken(newToken);
            void load();
          }}
          setError={setError}
        />
      )}
    </div>
  );
}

function CredentialDrawer({
  type,
  close,
  done,
  setError,
}: {
  type: "secrets" | "api";
  close: () => void;
  done: (token: string) => void;
  setError: (v: string) => void;
}) {
  const [name, setName] = useState(""),
    [kind, setKind] = useState("api_key"),
    [value, setValue] = useState(""),
    [scope, setScope] = useState("api:read"),
    [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (type === "secrets") {
        await api.post("/api/v1/secrets", { name, kind, value });
        done("");
      } else {
        const result = await api.post<{ token: string }>("/api/v1/api-keys", {
          name,
          scopes: [scope],
        });
        done(result.token);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "저장하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer
      title={type === "secrets" ? "개인 시크릿 추가" : "API 키 생성"}
      subtitle={
        type === "secrets"
          ? "원문은 저장 후 다시 표시되지 않습니다."
          : "필요한 최소 범위만 부여하세요."
      }
      close={close}
      footer={
        <>
          <button className="button ghost" onClick={close}>
            취소
          </button>
          <button
            className="button primary"
            form="credential-form"
            disabled={busy}
          >
            {busy ? "저장 중…" : "안전하게 저장"}
          </button>
        </>
      }
    >
      <form id="credential-form" className="drawer-form" onSubmit={submit}>
        <label>
          <span>
            이름 <b>*</b>
          </span>
          <input
            required
            maxLength={80}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={type === "secrets" ? "Bitbucket PAT" : "CI automation"}
          />
        </label>
        {type === "secrets" ? (
          <>
            <label>
              <span>종류</span>
              <select value={kind} onChange={(e) => setKind(e.target.value)}>
                <option value="api_key">API Key</option>
                <option value="git">Git 자격증명</option>
                <option value="database">Database</option>
                <option value="mcp">MCP Credential</option>
              </select>
            </label>
            <label>
              <span>
                비밀값 <b>*</b>
              </span>
              <textarea
                required
                autoComplete="off"
                rows={5}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="자격증명 원문을 입력하세요."
              />
            </label>
          </>
        ) : (
          <label>
            <span>권한 범위</span>
            <select value={scope} onChange={(e) => setScope(e.target.value)}>
              <option value="api:read">api:read · REST 조회</option>
              <option value="mcp:read">mcp:read · MCP 조회</option>
              <option value="runtime:manage">
                runtime:manage · Runtime 시작/중지
              </option>
              <option value="agent:write">agent:write · Agent 변경</option>
            </select>
          </label>
        )}
        <div className="info-box">
          <ShieldCheck size={17} />
          <div>
            <strong>AES-256-GCM envelope encryption</strong>
            <p>
              개인별 데이터 키로 암호화되며, Agent Definition에는 값 대신 참조만
              저장됩니다.
            </p>
          </div>
        </div>
      </form>
    </Drawer>
  );
}
