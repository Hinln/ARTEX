"use client";

import * as React from "react";

import { Globe2Icon, Loader2Icon, RefreshCwIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { EgressCheck, EgressReport } from "@/lib/types";
import { cn } from "@/lib/utils";

type EgressIpContextValue = {
  enabled: boolean;
  isLoading: boolean;
  report: EgressReport | null;
  error: string | null;
  setEnabled: (enabled: boolean) => Promise<void>;
  probeEgress: () => Promise<EgressReport>;
  dismiss: () => Promise<void>;
};

const EgressIpContext = React.createContext<EgressIpContextValue | null>(null);

export function EgressIpProvider({ children }: { children: React.ReactNode }) {
  const [enabled, setEnabledState] = React.useState(false);
  const [report, setReport] = React.useState<EgressReport | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [isLoading, setIsLoading] = React.useState(false);

  const probeForProxy = React.useCallback(async (globalProxy: string) => {
    setError(null);
    setIsLoading(true);
    try {
      const nextReport = await api.testEgressProxy({ global_proxy: globalProxy });
      setReport(nextReport);
      return nextReport;
    } catch (cause) {
      setReport(null);
      const message = cause instanceof Error ? cause.message : "未知错误";
      setError(message);
      throw cause;
    } finally {
      setIsLoading(false);
    }
  }, []);

  React.useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((settings) => {
        if (cancelled) return;
        const nextEnabled = !!settings.show_egress_ip;
        const nextProxy = settings.global_proxy ?? "";
        setEnabledState(nextEnabled);
        if (nextEnabled) void probeForProxy(nextProxy);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [probeForProxy]);

  React.useEffect(() => {
    if (!enabled) return;
    const timer = window.setInterval(() => {
      void api.settings().then((settings) => probeForProxy(settings.global_proxy ?? "")).catch(() => undefined);
    }, 10000);
    return () => window.clearInterval(timer);
  }, [enabled, probeForProxy]);

  const setEnabled = React.useCallback(
    async (nextEnabled: boolean) => {
      const settings = await api.setSettings({ show_egress_ip: nextEnabled });
      const nextProxy = settings.global_proxy ?? "";
      setEnabledState(nextEnabled);
      if (nextEnabled) {
        await probeForProxy(nextProxy);
      } else {
        setReport(null);
        setError(null);
      }
    },
    [probeForProxy],
  );

  // Refresh from settings first so a proxy saved on the settings page is always
  // the one used for the navbar probe.
  const probeEgress = React.useCallback(async () => {
    const settings = await api.settings();
    const nextProxy = settings.global_proxy ?? "";
    return probeForProxy(nextProxy);
  }, [probeForProxy]);

  const dismiss = React.useCallback(() => setEnabled(false), [setEnabled]);

  const value = React.useMemo(
    () => ({ enabled, isLoading, report, error, setEnabled, probeEgress, dismiss }),
    [dismiss, enabled, error, isLoading, probeEgress, report, setEnabled],
  );

  return <EgressIpContext.Provider value={value}>{children}</EgressIpContext.Provider>;
}

export function useEgressIp() {
  const context = React.useContext(EgressIpContext);
  if (!context) throw new Error("useEgressIp must be used within EgressIpProvider");
  return context;
}

function checkMessage(check: EgressCheck | undefined) {
  if (!check) return "未返回结果";
  if (check.skipped) return "未配置";
  if (check.result.ok) return check.result.exit_ip || "已连通";
  return "探测失败";
}

function IpValue({ label, check }: { label: string; check: EgressCheck | undefined }) {
  const failed = !!check && !check.skipped && !check.result.ok;
  return (
    <span className="min-w-0 truncate" title={`${label}：${checkMessage(check)}`}>
      <span className="text-muted-foreground">{label}</span> {"· "}
      <span className={cn("font-mono", failed && "text-destructive")}>{checkMessage(check)}</span>
    </span>
  );
}

export function EgressIpNavbar() {
  const { enabled, report, error, isLoading, probeEgress, dismiss } = useEgressIp();
  if (!enabled) return null;

  const direct = report?.checks.find((check) => check.key === "direct");
  const global = report?.checks.find((check) => check.key === "global");
  return (
    <div className="flex min-w-0 items-center justify-center gap-2 text-xs" role="status" aria-live="polite">
      <Globe2Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      {isLoading ? (
        <span className="flex items-center gap-1 text-muted-foreground">
          <Loader2Icon className="size-3 animate-spin" aria-hidden="true" /> 正在探测出口 IP…
        </span>
      ) : error ? (
        <span className="truncate text-destructive" title={error}>
          探测失败：{error}
        </span>
      ) : report ? (
        <div className="flex min-w-0 items-center gap-3">
          <IpValue label="直连 IP" check={direct} />
          <IpValue label="全局代理 IP" check={global} />
        </div>
      ) : (
        <span className="text-muted-foreground">出口 IP 暂无结果</span>
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        onClick={() => void probeEgress()}
        disabled={isLoading}
        aria-label="刷新出口 IP"
      >
        <RefreshCwIcon className={cn("size-3.5", isLoading && "animate-spin")} aria-hidden="true" />
      </Button>
      <Button type="button" variant="ghost" size="icon-sm" onClick={() => void dismiss()} aria-label="关闭出口 IP显示">
        <XIcon className="size-3.5" aria-hidden="true" />
      </Button>
    </div>
  );
}
