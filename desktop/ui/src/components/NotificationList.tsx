// The unread notifications, as the Overview's Unread panel and the header's bell list them, and their Mark all as read.
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Notification } from "../api/types";
import { translateNested } from "../lib/daemonText";
import { formatRelative } from "../lib/format";
import { useNow } from "../lib/hooks";
import { actions, useStore } from "../state/store";
import { Button, ListRow, ListRows } from "./ui";

type TFn = ReturnType<typeof useTranslation>["t"];

/**
 * The translated text of a notice, from its event type and parameters; the daemon's text is the fallback. The reason
 * it quotes is translated too when the daemon wrote it.
 */
export function noticeText(t: TFn, n: Notification): { title: string; body: string } {
  const values = translateNested(n.params);
  const params: Record<string, string> = { taskName: "", harness: "", harnessName: "", summary: "", error: "", reason: "", url: "", ...values };
  const key = `notify.${n.eventType}`;
  // The key is built at run time, so the typed overloads, which walk every key of the resources, are left out: past
  // a size, checking them exceeds TypeScript's instantiation depth.
  const translate = t as unknown as (key: string, options: Record<string, string>) => string;
  const title = translate(`${key}.title`, { ...params, defaultValue: n.title });
  const body = translate(`${key}.body`, { ...params, defaultValue: n.body });
  return { title, body };
}

/**
 * The unread notifications, the newest first, each marked read and its execution opened once clicked. `limit` caps
 * them, `empty` stands for none, and `toned` draws each in its severity's tone, its body whole and its time under it,
 * as the bell's popover does; untoned, the body is cut to one line and the time sits on the right.
 */
export function NotificationList({
  onOpenExecution,
  onOpened,
  limit,
  empty,
  toned,
  className,
}: {
  onOpenExecution: (id: string) => void;
  /** Called after a click has marked a notice read and opened its execution. */
  onOpened?: () => void;
  limit?: number;
  empty: ReactNode;
  toned?: boolean;
  className?: string;
}) {
  const { t, i18n } = useTranslation();
  const unread = useStore((s) => s.unread);
  const now = useNow(30000);

  const open = (n: Notification) => {
    void actions.markRead([n.id]);
    if (n.executionId) onOpenExecution(n.executionId);
    onOpened?.();
  };

  return (
    <>
      {unread.length === 0 ? empty : null}
      <ListRows className={className}>
        {unread.slice(0, limit).map((n) => {
          const text = noticeText(t, n);
          const time = formatRelative(n.createdAt, i18n.language, now);
          return (
            <li key={n.id}>
              <ListRow
                title={text.title}
                secondary={text.body}
                wrap={toned}
                meta={toned ? time : undefined}
                trailing={toned ? undefined : time}
                tone={toned ? n.severity : undefined}
                onClick={() => open(n)}
              />
            </li>
          );
        })}
      </ListRows>
    </>
  );
}

/** Marks every unread notification read; nothing while none is unread. */
export function MarkAllRead() {
  const { t } = useTranslation();
  const unread = useStore((s) => s.unread);
  return unread.length ? (
    <Button variant="link" onClick={() => void actions.markRead()}>
      {t("header.markAllRead")}
    </Button>
  ) : null;
}
