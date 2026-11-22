export type MailNotice = { id: number; sender: string; subject: string };

export function createMailNotificationBatch(post: (messages: MailNotice[]) => void, visible: (id: number) => boolean, windowMs = 2000) {
  const pending = new Map<number, MailNotice>();
  const seen = new Set<number>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const clear = () => {
    clearTimeout(timer);
    timer = undefined;
    pending.clear();
    seen.clear();
  };
  return {
    add(message: MailNotice) {
      if (seen.has(message.id)) return;
      seen.add(message.id);
      if (visible(message.id)) return;
      pending.set(message.id, message);
      if (timer !== undefined) return;
      timer = setTimeout(() => {
        timer = undefined;
        const messages = [...pending.values()].filter(message => !visible(message.id));
        pending.clear();
        if (messages.length) post(messages);
      }, windowMs);
    },
    clear,
  };
}
