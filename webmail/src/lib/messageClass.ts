export function visibleMessages<Message extends { id: number; class?: string }>(messages: Message[], collapsed: boolean, selectedId: number | null): Message[] {
  return messages.filter(message => !collapsed || message.class !== 'newsletter' || message.id === selectedId);
}

// The words a message list shows for a class. A person's mail and an unknown class show none.
export function messageClassLabel(messageClass?: string): string | null {
  switch (messageClass) {
    case 'newsletter': return 'list';
    case 'receipt': return 'receipt';
    case 'notification': return 'automated';
    default: return null;
  }
}
