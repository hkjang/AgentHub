import { FormEvent, useCallback, useEffect, useState } from "react";
import { Boxes, Bot, Network, Pencil, Plus, Sparkles, Stethoscope, Trash2 } from "lucide-react";
import { api } from "../api";
import {
  ConfirmDialog,
  Drawer,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusBadge,
} from "../components/UI";
import { runtimeTypeList, runtimeLabel } from "../runtime";

type Kind = "profiles" | "images" | "models" | "apps" | "mcp" | "bundles" | "servers";
type Item = Record<string, unknown> & {
  id: string;
  name: string;
  enabled?: boolean;
  approved?: boolean;
};
const meta: Record<
  Kind,
  { title: string; description: string; endpoint: string; icon: typeof Bot }
> = {
  profiles: {
    title: "런타임 프로파일",
    description:
      "사용자가 선택할 수 있는 CPU, Memory, Storage 및 Idle 정책입니다.",
    endpoint: "runtime-profiles",
    icon: Bot,
  },
  images: {
    title: "런타임 이미지",
    description:
      "승인된 OpenCode, Hermes 및 Custom Runtime 이미지 카탈로그입니다.",
    endpoint: "runtime-images",
    icon: Boxes,
  },
  models: {
    title: "모델 엔드포인트",
    description: "사내 vLLM, Ollama와 OpenAI-compatible 모델 연결입니다.",
    endpoint: "models",
    icon: Sparkles,
  },
  apps: {
    title: "외부 앱",
    description:
      "플랫폼이 실행하지는 않지만 작업을 맡길 수 있는 애플리케이션입니다. 사내에 이미 돌고 있는 Dify 앱을 연결하면, 작업 대기열이 그 앱을 호출하고 결과를 실행 기록에 남깁니다.",
    endpoint: "external-apps",
    icon: Sparkles,
  },
  servers: {
    title: "에이전트 서버",
    description:
      "이 배포가 작업을 맡길 수 있는 서버입니다. 플랫폼이 띄우는 런타임이 아니라 이미 돌고 있는 기계라서, 어디에 있는지 — 개발망인지, 보안망인지 — 가 주소만큼 중요합니다. 등록한 뒤에는 연결 확인으로 실제로 대화를 시작할 수 있는 서버인지 물어보세요.",
    endpoint: "agent-servers",
    icon: Network,
  },
  mcp: {
    title: "MCP 서버",
    description: "Shared, Dedicated, Sidecar MCP의 실행 및 승인 정책입니다.",
    endpoint: "mcp-servers",
    icon: Network,
  },
  bundles: {
    title: "MCP 번들",
    description: "용도별 MCP 조합을 만들어 Agent 생성 단계를 간소화합니다.",
    endpoint: "mcp-bundles",
    icon: Boxes,
  },
};

export function AdminResources({ kind }: { kind: Kind }) {
  const config = meta[kind],
    [items, setItems] = useState<Item[]>(),
    [selected, setSelected] = useState<Item | null>(),
    [error, setError] = useState("");
  const [removing, setRemoving] = useState<Item | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState("");
  // Whether each model endpoint is actually there. An address and a model name
  // are otherwise only checked at the moment a task runs, which is usually at
  // night, on somebody else's agent, as a failure that reads like the agent's.
  const [checks, setChecks] = useState<Record<string, { verdict: string; detail: string; tools?: string[] }>>({});
  const [checking, setChecking] = useState("");
  const check = async (id: string) => {
    setChecking(id);
    // The two registries answer the same question — is this thing actually there
    // — so they share one control and one place to read the answer.
    const path = kind === "models" ? `/api/v1/admin/models/${id}/check`
      : kind === "servers" ? `/api/v1/admin/agent-servers/${id}/check`
      : `/api/v1/admin/mcp-servers/${id}/check`;
    try {
      const result = await api.post<{ verdict?: string; detail?: string; tools?: string[]; health?: string; healthDetail?: string }>(path);
      // An agent server answers with itself — what it is now, including what the
      // check just found — so the whole card is refreshed rather than only the
      // line under it.
      setChecks((current) => ({ ...current, [id]: {
        verdict: result.verdict ?? (result.health === "healthy" ? "ok" : "error"),
        detail: result.detail ?? result.healthDetail ?? "",
        tools: result.tools,
      } }));
      if (kind === "servers") void load();
    } catch (e) {
      setChecks((current) => ({
        ...current,
        [id]: { verdict: "error", detail: e instanceof Error ? e.message : "확인하지 못했습니다." },
      }));
    } finally {
      setChecking("");
    }
  };
  const load = useCallback(() =>
    api
      .get<{ items?: Item[] }>(`/api/v1/admin/${config.endpoint}`)
      .then((v) => setItems(v.items ?? []))
      .catch((e) => { setItems([]); setError(e instanceof Error ? e.message : "목록을 불러오지 못했습니다."); }), [config.endpoint]);
  useEffect(() => {
    setItems(undefined);
    void load();
  }, [load]);
  const remove = async () => {
    if (!removing) return;
    setRemoveBusy(true);
    setRemoveError("");
    try {
      await api.delete(`/api/v1/admin/${config.endpoint}/${removing.id}`);
      setRemoving(null);
      await load();
    } catch (e) {
      setRemoveError(e instanceof Error ? e.message : "삭제하지 못했습니다.");
    } finally {
      setRemoveBusy(false);
    }
  };
  if (!items) return <Loading />;
  const Icon = config.icon;
  return (
    <div className="page">
      <PageHeader
        eyebrow="관리자"
        title={config.title}
        description={config.description}
        actions={
          <button className="button primary" onClick={() => setSelected(null)}>
            <Plus size={16} />새 항목
          </button>
        }
      />
      {error && <ErrorBanner message={error} onClose={() => setError("")} />}{" "}
      {items.length === 0 ? (
        <Empty
          icon={<Icon />}
          title="등록된 항목이 없습니다"
          description="운영 표준에 맞는 첫 항목을 등록하세요."
          action={
            <button
              className="button primary"
              onClick={() => setSelected(null)}
            >
              새 항목
            </button>
          }
        />
      ) : (
        <section className="resource-grid">
          {items.map((item) => (
            <article className="resource-card" key={item.id}>
              <div>
                <div className="list-icon">
                  <Icon />
                </div>
                <StatusBadge
                  status={
                    (item.enabled ?? item.approved) ? "active" : "disabled"
                  }
                />
              </div>
              <h3>{item.name}</h3>
              <p>{summary(kind, item)}</p>
              <dl>
                {facts(kind, item).map(([label, value]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{String(value ?? "—")}</dd>
                  </div>
                ))}
              </dl>
              {checks[String(item.id)] && (
                <p className={`model-check ${checks[String(item.id)].verdict}`}>
                  {checks[String(item.id)].detail}
                  {/* The names an allow or deny list is written against. Until
                      now they came from the vendor's documentation or a guess. */}
                  {(checks[String(item.id)].tools ?? []).length > 0 && (
                    <span className="model-check-tools">
                      {(checks[String(item.id)].tools ?? []).join(", ")}
                    </span>
                  )}
                </p>
              )}
              <div className="card-actions">
                {(kind === "models" || kind === "mcp" || kind === "servers") && (
                  <button
                    title={kind === "models"
                      ? "이 엔드포인트가 실제로 응답하는지, 지정한 모델을 제공하는지 확인합니다"
                      : kind === "servers"
                        ? "이 주소가 실제로 에이전트 서버인지, 대화를 시작할 수 있는지 확인합니다"
                        : "이 서버가 실제로 응답하는지, 어떤 도구를 제공하는지 확인합니다"}
                    disabled={checking === String(item.id)}
                    onClick={() => void check(String(item.id))}
                  >
                    <Stethoscope size={15} />
                    {checking === String(item.id) ? "확인 중…" : "연결 확인"}
                  </button>
                )}
                <button title="수정" onClick={() => setSelected(item)}>
                  <Pencil size={15} />수정
                </button>
                <button
                  className="danger"
                  title="삭제"
                  onClick={() => {
                    setRemoveError("");
                    setRemoving(item);
                  }}
                >
                  <Trash2 size={15} />
                </button>
              </div>
            </article>
          ))}
        </section>
      )}{" "}
      {removing && (
        <ConfirmDialog
          title={`${config.title} 항목을 삭제할까요?`}
          message={
            <>
              <strong>{removing.name}</strong> 항목이 삭제됩니다. 이 리소스를
              사용 중인 Agent가 있으면 삭제가 거부됩니다.
            </>
          }
          busy={removeBusy}
          error={removeError}
          onConfirm={() => void remove()}
          onCancel={() => setRemoving(null)}
        />
      )}
      {selected !== undefined && (
        <ResourceDrawer
          kind={kind}
          item={selected}
          close={() => setSelected(undefined)}
          done={() => {
            setSelected(undefined);
            void load();
          }}
          error={setError}
        />
      )}
    </div>
  );
}

function summary(kind: Kind, item: Item) {
  if (kind === "profiles")
    return String(item.description || "표준 Runtime 자원");
  if (kind === "images") return String(item.image || "");
  if (kind === "models") return String(item.baseUrl || "");
  if (kind === "apps") return String(item.description || item.baseUrl || "");
  if (kind === "servers") return String(item.baseUrl || "");
  return String(item.description || item.endpoint || "MCP 구성");
}
function facts(kind: Kind, item: Item): [string, unknown][] {
  if (kind === "profiles")
    return [
      ["CPU", `${Number(item.cpuMillis) / 1000} Core`],
      ["Memory", `${Number(item.memoryMb) / 1024} GB`],
      ["Storage", `${item.storageGb} GB`],
    ];
  if (kind === "images")
    return [
      ["Runtime", runtimeLabel(String(item.runtimeType ?? ""))],
      ["Version", item.version],
      ["Digest", item.digest || "미등록"],
    ];
  if (kind === "models")
    return [
      ["Provider", item.provider],
      ["Model", item.defaultModel],
      ["Secret", item.secretConfigured ? "설정됨" : "없음"],
      [
        "단가",
        Number(item.inputPricePerMTok) || Number(item.outputPricePerMTok)
          ? `입력 ${item.inputPricePerMTok} / 출력 ${item.outputPricePerMTok} ${item.currency ?? ""}`
          : "미산정",
      ],
      // What the last check found, and when. The check used to be answered once
      // on screen and forgotten, so a key rotated last week looked exactly like
      // an endpoint verified this morning.
      ["연결", item.checkedAt
        ? `${endpointWord(String(item.health))} · ${new Date(String(item.checkedAt)).toLocaleString("ko-KR")}`
        : "아직 확인하지 않음"],
    ];
  if (kind === "apps")
    return [
      ["Provider", item.provider],
      ["종류", item.appKind === "chat" ? "Chat 앱" : "Workflow 앱"],
      ["API 키", item.secretConfigured ? "설정됨" : "없음"],
      ["상태", item.enabled ? "Enabled" : "Disabled"],
    ];
  if (kind === "servers")
    return [
      ["네트워크", item.networkZone || "구역 없음"],
      // Both numbers, because a limit is only meaningful next to what is
      // actually running against it.
      ["동시 실행", item.capacity
        ? `${Number(item.running ?? 0)} / ${item.capacity}`
        : `${Number(item.running ?? 0)} · 제한 없음`],
      // What the last check found, and when. A row that says it works because
      // somebody typed a URL is the claim this console keeps removing.
      ["연결", item.checkedAt
        ? `${healthWord(String(item.health))} · ${new Date(String(item.checkedAt)).toLocaleString("ko-KR")}`
        : "아직 확인하지 않음"],
    ];
  if (kind === "bundles")
    return [
      ["Servers", ((item.serverIds as string[]) || []).length],
      ["Status", item.enabled ? "Enabled" : "Disabled"],
      ["Type", "Bundle"],
    ];
  const authType = String(item.authType ?? "none");
  const auth =
    authType === "none"
      ? "없음"
      : item.perUserCredential
        ? `${authType} · 사용자별`
        : item.credentialConfigured
          ? `${authType} · 등록됨`
          : `${authType} · 미등록`;
  return [
    ["Mode", item.mode],
    ["Risk", item.riskLevel],
    ["인증", auth],
  ];
}

// endpointWord says what a model endpoint's check found, in the words an
// administrator would use for the thing they would go and change.
function endpointWord(health: string) {
  return health === "ok" ? "정상"
    : health === "model_missing" ? "지정한 모델 없음"
    : health === "unauthorised" ? "인증 거절"
    : health === "wrong_path" ? "주소 경로 문제"
    : health === "reachable" ? "응답하지만 모델 목록이 비어 있음"
    : health === "unreachable" ? "연결되지 않음"
    : health === "unconfigured" ? "주소 없음"
    : health === "error" ? "오류 응답"
    : "확인 필요";
}

// healthWord says what a check found in words rather than in a status name.
function healthWord(health: string) {
  return health === "healthy" ? "작업을 맡길 수 있음"
    : health === "unreachable" ? "연결되지 않음"
    : health === "refused" ? "에이전트 서버가 아님"
    : "확인 필요";
}

function ResourceDrawer({
  kind,
  item,
  close,
  done,
  error,
}: {
  kind: Kind;
  item: Item | null;
  close: () => void;
  done: () => void;
  error: (v: string) => void;
}) {
  const defaults =
    kind === "profiles"
      ? {
          name: "",
          description: "",
          cpuMillis: 2000,
          memoryMb: 4096,
          storageGb: 10,
          gpuCount: 0,
          idleTimeoutSeconds: 3600,
          enabled: true,
        }
      : kind === "images"
        ? {
            name: "",
            runtimeType: "opencode",
            image: "",
            version: "",
            digest: "",
            sbomUri: "",
            approved: false,
            deprecated: false,
          }
        : kind === "models"
          ? {
              name: "",
              provider: "openai-compatible",
              baseUrl: "",
              defaultModel: "",
              inputPricePerMTok: 0,
              outputPricePerMTok: 0,
              currency: "KRW",
              secret: "",
              enabled: true,
            }
          : kind === "servers"
            ? {
                name: "",
                baseUrl: "",
                kind: "openhands",
                networkZone: "",
                capacity: 0,
                enabled: true,
              }
          : kind === "bundles"
            ? { name: "", description: "", serverIds: [], enabled: true }
            : {
                name: "",
                description: "",
                mode: "shared",
                transport: "streamable-http",
                endpoint: "",
                image: "",
                port: 8000,
                riskLevel: "low",
                approvalRequired: false,
                enabled: true,
                authType: "none",
                authHeader: "",
                perUserCredential: false,
              };
  // Kept out of `form`: the credential is write-only and must never be part of a
  // resource body that a listing echoes back.
  const [credential, setCredential] = useState("");
  const [form, setForm] = useState<Record<string, unknown>>({
      ...defaults,
      ...item,
    }),
    [busy, setBusy] = useState(false),
    [servers, setServers] = useState<Item[]>([]);
  useEffect(() => {
    if (kind === "bundles")
      void api
        .get<{ items: Item[] }>("/api/v1/admin/mcp-servers")
        .then((v) => setServers(v.items));
  }, [kind]);
  const field = (name: string) => (form[name] ?? "") as string | number;
  const update = (name: string, value: unknown) =>
    setForm((v) => ({ ...v, [name]: value }));
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const saved = await api.post<Item>(`/api/v1/admin/${meta[kind].endpoint}`, form);
      if (kind === "mcp" && credential.trim() && !form.perUserCredential) {
        await api.put(`/api/v1/admin/mcp-servers/${saved.id}/credential`, {
          value: credential,
        });
      }
      done();
    } catch (e) {
      error(e instanceof Error ? e.message : "저장하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer
      title={`${item ? "수정" : "새로 등록"} · ${meta[kind].title}`}
      close={close}
      footer={
        <>
          <button className="button ghost" onClick={close}>
            취소
          </button>
          <button
            className="button primary"
            form="resource-form"
            disabled={busy}
          >
            {busy ? "저장 중…" : "저장"}
          </button>
        </>
      }
    >
      <form id="resource-form" className="drawer-form" onSubmit={submit}>
        <label>
          <span>
            이름 <b>*</b>
          </span>
          <input
            required
            value={field("name")}
            onChange={(e) => update("name", e.target.value)}
          />
        </label>
        {kind === "profiles" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <div className="form-grid">
              <NumberInput
                label="CPU (millicores)"
                name="cpuMillis"
                form={form}
                update={update}
              />
              <NumberInput
                label="Memory (MB)"
                name="memoryMb"
                form={form}
                update={update}
              />
              <NumberInput
                label="Storage (GB)"
                name="storageGb"
                form={form}
                update={update}
              />
              <NumberInput
                label="GPU"
                name="gpuCount"
                form={form}
                update={update}
              />
            </div>
            <NumberInput
              label="Idle Timeout (초)"
              name="idleTimeoutSeconds"
              form={form}
              update={update}
            />
            <Check
              label="사용자 선택 허용"
              name="enabled"
              form={form}
              update={update}
            />
          </>
        )}
        {kind === "mcp" && (
          <>
            <label>
              <span>인증 방식</span>
              <select
                value={String(field("authType") || "none")}
                onChange={(e) => update("authType", e.target.value)}
              >
                <option value="none">없음 (공개 MCP)</option>
                <option value="bearer">Bearer 토큰</option>
                <option value="header">커스텀 헤더</option>
                <option value="basic">Basic 인증</option>
              </select>
            </label>
            {String(field("authType") || "none") === "header" && (
              <label>
                <span>
                  헤더 이름 <b>*</b>
                </span>
                <input
                  required
                  value={String(field("authHeader") || "")}
                  onChange={(e) => update("authHeader", e.target.value)}
                  placeholder="X-Api-Key"
                />
              </label>
            )}
            {String(field("authType") || "none") !== "none" && (
              <>
                <Check
                  label="사용자별 자격증명 사용"
                  name="perUserCredential"
                  form={form}
                  update={update}
                />
                <label>
                  <span>공용 자격증명</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={credential}
                    disabled={Boolean(form.perUserCredential)}
                    onChange={(e) => setCredential(e.target.value)}
                    placeholder={
                      form.perUserCredential
                        ? "사용자별 자격증명 모드에서는 각 사용자가 직접 등록합니다"
                        : item?.credentialConfigured
                          ? "등록됨 · 변경하려면 새 값을 입력하세요"
                          : "미등록"
                    }
                  />
                  <small>
                    값은 암호화되어 저장되며 Runtime에는 Secret으로만 전달됩니다.
                  </small>
                </label>
              </>
            )}
          </>
        )}
        {kind === "images" && (
          <>
            <label>
              <span>Runtime</span>
              <select
                value={field("runtimeType")}
                onChange={(e) => update("runtimeType", e.target.value)}
              >
                {runtimeTypeList().map((value) => (
                  <option key={value} value={value}>
                    {runtimeLabel(value)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              <span>
                Image <b>*</b>
              </span>
              <input
                required
                value={field("image")}
                onChange={(e) => update("image", e.target.value)}
                placeholder="registry.local/agent/opencode"
              />
            </label>
            <label>
              <span>
                Version <b>*</b>
              </span>
              <input
                required
                value={field("version")}
                onChange={(e) => update("version", e.target.value)}
              />
            </label>
            <label>
              <span>Digest</span>
              <input
                value={field("digest")}
                onChange={(e) => update("digest", e.target.value)}
                placeholder="sha256:…"
              />
            </label>
            <label>
              <span>SBOM URI</span>
              <input
                value={field("sbomUri")}
                onChange={(e) => update("sbomUri", e.target.value)}
              />
            </label>
            <Check
              label="운영 승인"
              name="approved"
              form={form}
              update={update}
            />
            <Check
              label="Deprecated"
              name="deprecated"
              form={form}
              update={update}
            />
          </>
        )}
        {kind === "servers" && (
          <>
            <label>
              <span>
                주소 <b>*</b>
              </span>
              <input
                required
                type="url"
                value={field("baseUrl")}
                onChange={(e) => update("baseUrl", e.target.value)}
                placeholder="http://agent-server.dev.internal:8000"
              />
              <small>에이전트 서버 API의 주소입니다. 등록한 뒤 <b>연결 확인</b>으로 실제로 대화를 시작할 수 있는지 확인하세요.</small>
            </label>
            <label>
              <span>네트워크 구역</span>
              <input
                value={field("networkZone")}
                onChange={(e) => update("networkZone", e.target.value)}
                placeholder="예) dev, secure, gpu"
              />
              <small>이 배포가 쓰는 이름을 그대로 적으세요. 목표에서 구역을 고르면 그 안의 서버 중에서 고릅니다 — 보안망 작업이 개발망 기계로 새어 나가지 않게 하는 것이 이 칸의 목적입니다.</small>
            </label>
            <label>
              <span>동시 실행 수</span>
              <input
                type="number"
                min={0}
                value={field("capacity")}
                onChange={(e) => update("capacity", Number(e.target.value))}
              />
              <small>이 서버가 한 번에 들고 있을 대화 수입니다. 0 은 <b>모른다</b>는 뜻이고, 배치는 이를 제한 없음으로 봅니다.</small>
            </label>
          </>
        )}
        {kind === "apps" && (
          <>
            <label>
              <span>
                Base URL <b>*</b>
              </span>
              <input
                required
                type="url"
                value={field("baseUrl")}
                onChange={(e) => update("baseUrl", e.target.value)}
                placeholder="https://dify.internal"
              />
              <small>Dify 주소입니다. /v1 은 붙이지 않아도 됩니다.</small>
            </label>
            <label>
              <span>앱 종류</span>
              <select
                value={field("appKind") || "workflow"}
                onChange={(e) => update("appKind", e.target.value)}
              >
                <option value="workflow">Workflow — 입력 변수를 받아 결과를 돌려줍니다</option>
                <option value="chat">Chat — 질문을 보내고 답변을 받습니다</option>
              </select>
            </label>
            <label>
              <span>API 키 <b>*</b></span>
              <input
                type="password"
                autoComplete="new-password"
                onChange={(e) => update("secret", e.target.value)}
                placeholder="app-..."
              />
              <small>Dify 앱마다 발급되는 키입니다. 저장 후에는 다시 보이지 않으며, 비워 두면 기존 키가 유지됩니다.</small>
            </label>
            <label>
              <span>설명</span>
              <input
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
                placeholder="예) 고객 문의 분류 워크플로"
              />
            </label>
          </>
        )}
        {kind === "models" && (
          <>
            <label>
              <span>Provider</span>
              <input
                required
                value={field("provider")}
                onChange={(e) => update("provider", e.target.value)}
              />
            </label>
            <label>
              <span>
                Base URL <b>*</b>
              </span>
              <input
                required
                type="url"
                value={field("baseUrl")}
                onChange={(e) => update("baseUrl", e.target.value)}
                placeholder="https://vllm.local/v1"
              />
            </label>
            <label>
              <span>
                Default Model <b>*</b>
              </span>
              <input
                required
                value={field("defaultModel")}
                onChange={(e) => update("defaultModel", e.target.value)}
              />
            </label>
            <label>
              <span>API Key</span>
              <input
                type="password"
                value={field("secret")}
                onChange={(e) => update("secret", e.target.value)}
                placeholder={
                  item?.secretConfigured
                    ? "•••••••• · 변경할 때만 입력"
                    : "API Key"
                }
              />
            </label>
            <fieldset>
              <legend>토큰 단가</legend>
              <div className="form-grid">
                <label>
                  <span>입력 (100만 토큰당)</span>
                  <input
                    type="number"
                    min={0}
                    step="0.0001"
                    value={field("inputPricePerMTok")}
                    onChange={(e) => update("inputPricePerMTok", Number(e.target.value))}
                  />
                </label>
                <label>
                  <span>출력 (100만 토큰당)</span>
                  <input
                    type="number"
                    min={0}
                    step="0.0001"
                    value={field("outputPricePerMTok")}
                    onChange={(e) => update("outputPricePerMTok", Number(e.target.value))}
                  />
                </label>
                <label>
                  <span>통화</span>
                  <input
                    maxLength={8}
                    value={field("currency")}
                    onChange={(e) => update("currency", e.target.value)}
                    placeholder="KRW"
                  />
                </label>
              </div>
              <small>단가를 비워 두면 사용량 화면에서 토큰만 집계하고 금액은 &apos;미산정&apos;으로 표시합니다.</small>
            </fieldset>
            <Check label="사용" name="enabled" form={form} update={update} />
          </>
        )}
        {kind === "mcp" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <label>
              <span>Runtime Mode</span>
              <select
                value={field("mode")}
                onChange={(e) => update("mode", e.target.value)}
              >
                <option value="shared">Shared</option>
                <option value="dedicated">Dedicated</option>
                <option value="sidecar">Sidecar</option>
              </select>
            </label>
            {form.mode === "shared" ? (
              <label>
                <span>
                  Endpoint <b>*</b>
                </span>
                <input
                  required
                  type="url"
                  value={field("endpoint")}
                  onChange={(e) => update("endpoint", e.target.value)}
                />
              </label>
            ) : (
              <label>
                <span>
                  Image <b>*</b>
                </span>
                <input
                  required
                  value={field("image")}
                  onChange={(e) => update("image", e.target.value)}
                />
              </label>
            )}
            {form.mode !== "shared" && (
              <NumberInput
                label="Container Port"
                name="port"
                form={form}
                update={update}
              />
            )}
            <label>
              <span>Risk</span>
              <select
                value={field("riskLevel")}
                onChange={(e) => update("riskLevel", e.target.value)}
              >
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
                <option value="critical">Critical</option>
              </select>
            </label>
            <Check
              label="호출 전 승인"
              name="approvalRequired"
              form={form}
              update={update}
            />
            <Check label="사용" name="enabled" form={form} update={update} />
          </>
        )}
        {kind === "bundles" && (
          <>
            <label>
              <span>설명</span>
              <textarea
                value={field("description")}
                onChange={(e) => update("description", e.target.value)}
              />
            </label>
            <fieldset>
              <legend>MCP Servers</legend>
              <div className="bundle-choices">
                {servers.map((server) => {
                  const ids = form.serverIds as string[];
                  return (
                    <label key={server.id}>
                      <input
                        type="checkbox"
                        checked={ids.includes(server.id)}
                        onChange={(e) =>
                          update(
                            "serverIds",
                            e.target.checked
                              ? [...ids, server.id]
                              : ids.filter((id) => id !== server.id),
                          )
                        }
                      />
                      <span>
                        <strong>{server.name}</strong>
                        <small>
                          {String(server.mode)} · {String(server.riskLevel)}
                        </small>
                      </span>
                    </label>
                  );
                })}
              </div>
            </fieldset>
            <Check
              label="사용자 선택 허용"
              name="enabled"
              form={form}
              update={update}
            />
          </>
        )}
      </form>
    </Drawer>
  );
}
function NumberInput({
  label,
  name,
  form,
  update,
}: {
  label: string;
  name: string;
  form: Record<string, unknown>;
  update: (n: string, v: unknown) => void;
}) {
  return (
    <label>
      <span>{label}</span>
      <input
        type="number"
        min="0"
        value={Number(form[name] ?? 0)}
        onChange={(e) => update(name, Number(e.target.value))}
      />
    </label>
  );
}
function Check({
  label,
  name,
  form,
  update,
}: {
  label: string;
  name: string;
  form: Record<string, unknown>;
  update: (n: string, v: unknown) => void;
}) {
  return (
    <label className="toggle-row">
      <span>{label}</span>
      <input
        type="checkbox"
        checked={Boolean(form[name])}
        onChange={(e) => update(name, e.target.checked)}
      />
      <i />
    </label>
  );
}
