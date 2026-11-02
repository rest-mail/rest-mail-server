export function visibleMessages<Message extends { id: number; class?: string }>(messages: Message[], collapsed: boolean, selectedId: number | null): Message[] {
  return messages.filter(message => !collapsed || message.class !== 'newsletter' || message.id === selectedId);
}
