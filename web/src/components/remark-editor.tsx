"use client";

import * as React from "react";

import { PencilIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { MAX_TASK_REMARK_RUNES } from "@/lib/types";
import { cn } from "@/lib/utils";

type EditorProps = {
  value?: string;
  label: string;
  onSave: (value: string) => Promise<void>;
};

export function RemarkEditor(props: EditorProps) {
  return <InlineTextEditor {...props} multiline fieldName="备注" maxCharacters={MAX_TASK_REMARK_RUNES} emptyLabel="添加备注" />;
}

export function ScheduleNameEditor(props: EditorProps) {
  return <InlineTextEditor {...props} multiline={false} fieldName="名称" maxCharacters={200} emptyLabel="未命名" />;
}

function InlineTextEditor({
  value = "",
  label,
  onSave,
  multiline,
  fieldName,
  maxCharacters,
  emptyLabel,
}: EditorProps & { multiline: boolean; fieldName: string; maxCharacters: number; emptyLabel: string }) {
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState(value);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const inputRef = React.useRef<HTMLInputElement>(null);
  const textareaRef = React.useRef<HTMLTextAreaElement>(null);
  const cancelOnBlurRef = React.useRef(false);
  const savingRef = React.useRef(false);
  const errorID = React.useId();

  React.useEffect(() => {
    if (!editing) setDraft(value);
  }, [editing, value]);

  function refocus() {
    requestAnimationFrame(() => (multiline ? textareaRef.current : inputRef.current)?.focus());
  }

  async function finishEditing() {
    if (savingRef.current) return;
    if (cancelOnBlurRef.current) {
      cancelOnBlurRef.current = false;
      setDraft(value);
      setError("");
      setEditing(false);
      return;
    }
    const next = draft.trim();
    if (next === value.trim()) {
      setEditing(false);
      return;
    }
    if (!multiline && next.length === 0) {
      setError("名称不能为空");
      refocus();
      return;
    }
    if (Array.from(next).length > maxCharacters) {
      setError(`${fieldName}最多 ${maxCharacters} 个字符`);
      refocus();
      return;
    }
    savingRef.current = true;
    setSaving(true);
    try {
      await onSave(next);
      toast.success(`${fieldName}已保存`);
      setEditing(false);
    } catch (cause) {
      setError(`${fieldName}保存失败：${(cause as Error).message}`);
      refocus();
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  }

  if (editing) {
    const inputProps = {
      value: draft,
      autoFocus: true,
      disabled: saving,
      "aria-busy": saving,
      "aria-label": `${label}${fieldName}`,
      "aria-invalid": error.length > 0,
      "aria-describedby": error ? errorID : undefined,
      title: multiline ? "Enter 保存，Shift+Enter 换行，Esc 取消；留空可清除备注" : "Enter 或失焦保存，Esc 取消",
      onFocus: (event: React.FocusEvent<HTMLInputElement | HTMLTextAreaElement>) => event.currentTarget.select(),
      onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
        setDraft(event.target.value);
        setError("");
      },
      onBlur: () => void finishEditing(),
      onKeyDown: (event: React.KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => {
        if (event.nativeEvent.isComposing) return;
        if (event.key === "Enter" && (!multiline || !event.shiftKey)) {
          event.preventDefault();
          event.currentTarget.blur();
        } else if (event.key === "Escape") {
          event.preventDefault();
          cancelOnBlurRef.current = true;
          event.currentTarget.blur();
        }
      },
    };
    return (
      <div className={cn("flex min-w-0 flex-col gap-1 font-medium", multiline && "w-32 min-w-28 sm:w-48")}>
        {multiline ? (
          <Textarea
            {...inputProps}
            ref={textareaRef}
            rows={1}
            className="max-h-40 min-h-7 resize-none px-2 py-1 [overflow-wrap:anywhere]"
          />
        ) : (
          <Input {...inputProps} ref={inputRef} className="h-7 min-w-28 max-w-48 px-2 font-medium" />
        )}
        {error ? (
          <p id={errorID} role="alert" className="whitespace-normal text-destructive text-xs">
            {error}
          </p>
        ) : null}
      </div>
    );
  }

  const startEditing = () => {
    setDraft(value);
    setError("");
    setEditing(true);
  };
  return (
    <div
      className={cn(
        "group/inline-editor flex min-w-0 items-center gap-1 font-medium",
        multiline ? "w-fit max-w-32 sm:max-w-48" : "max-w-xs",
      )}
    >
      <button
        type="button"
        className="min-w-0 truncate rounded-sm text-left hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={`${label}${fieldName}，点击编辑`}
        title={value.length > 0 ? value : `点击编辑${fieldName}`}
        onClick={startEditing}
      >
        {value.length > 0 ? (
          <span className="block truncate">{value}</span>
        ) : (
          <span className="text-muted-foreground">{emptyLabel}</span>
        )}
      </button>
      <Tooltip delayDuration={200}>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="shrink-0 opacity-100 transition-opacity [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-focus-within:opacity-100 [@media(hover:hover)]:group-hover:opacity-100 [@media(hover:hover)]:group-focus-within/inline-editor:opacity-100 [@media(hover:hover)]:group-hover/inline-editor:opacity-100"
            aria-label={`编辑${label}的${fieldName}`}
            onClick={startEditing}
          >
            <PencilIcon />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6}>
          编辑{fieldName}：输入完成后按 Enter 或点击其他位置保存，按 Esc 取消。
        </TooltipContent>
      </Tooltip>
    </div>
  );
}
