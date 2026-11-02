import { http } from "@/lib/api";

export interface SideModel {
  model: string;
  name: string;
  format: string;
  profile_id: number;
}
export interface SideExchange {
  id: string;
  ordinal: number;
  client_request_id: string;
  question: string;
  answer: string;
  status: "running" | "completed" | "failed" | "cancelled" | "interrupted";
  error?: string;
  model: SideModel;
  snapshot_at: string;
  created_at: string;
  sequence: number;
  context?: {
    phase?: "preparing" | "summarizing_history" | "compressing_snapshot" | "retrying" | "answering";
    recent_exchanges: number;
    history_summarized: boolean;
    snapshot_summarized: boolean;
    estimated_input_tokens?: number;
    input_budget?: number;
    output_tokens?: number;
    overflow_retried?: boolean;
  };
}
export interface SideHistory {
  items: SideExchange[];
  current: SideExchange | null;
  next_cursor: number;
  snapshot: { captured_at: string; model: SideModel; available: boolean; reason: string } | null;
  snapshot_error?: string;
}

async function request<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  return http<T>(path.replace(/^\/api/, ""), { method, body: body === undefined ? undefined : JSON.stringify(body) });
}

function withQuery(path: string, query: string) {
  return `${path}${path.includes("?") ? "&" : "?"}${query}`;
}

function sidePath(parent: string) {
  const mark = parent.indexOf("?");
  if (mark < 0) return `${parent}/side-questions`;
  return `${parent.slice(0, mark)}/side-questions${parent.slice(mark)}`;
}

export const sideAPI = {
  // 历史必须归一化后再交给调用方:items 一旦不是数组,消费端的 setState updater 会抛,
  // 而 React 会把 updater 的异常推迟到 render 阶段重抛 —— 那时调用方的 catch 已经够不着,
  // 整页直接被错误边界接管。
  history: async (parent: string, before = 0) => {
    const data = await request<Partial<SideHistory>>(withQuery(sidePath(parent), `before=${before}`));
    return {
      items: Array.isArray(data?.items) ? data.items : [],
      current: data?.current ?? null,
      next_cursor: Number(data?.next_cursor) || 0,
      snapshot: data?.snapshot ?? null,
      snapshot_error: data?.snapshot_error ?? "",
    } satisfies SideHistory;
  },
  ask: (parent: string, question: string, client_request_id: string) =>
    request<SideExchange>(sidePath(parent), "POST", { question, client_request_id }),
  clear: (parent: string) => request(sidePath(parent), "DELETE"),
  cancel: (id: string) => request(`/api/side-questions/${id}/cancel`, "POST"),
};

export function isBtwCommand(text: string) {
  return /^\/btw(?:\s|$)/.test(text.trim());
}
